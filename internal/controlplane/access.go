package controlplane

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

const maximumAccessEnvelopeBytes = 64 << 10

type AccessSessionCommand struct {
	SubjectID      domain.UserID
	InstanceID     domain.InstanceID
	ResourceID     domain.LogicalResourceID
	Method         string
	Duration       time.Duration
	IdempotencyKey string
}

type AccessSessionLaunch struct {
	Session     AccessSessionRecord
	LaunchURL   string
	LaunchToken string
}

func (s *Service) CreateAccessSession(ctx context.Context, command AccessSessionCommand) (AccessSessionLaunch, error) {
	if s.access == nil || s.authorizer == nil || s.tokenHasher == nil {
		return AccessSessionLaunch{}, ErrAccessUnavailable
	}
	if _, err := domain.NewUserID(string(command.SubjectID)); err != nil {
		return AccessSessionLaunch{}, err
	}
	if _, err := domain.NewInstanceID(string(command.InstanceID)); err != nil {
		return AccessSessionLaunch{}, err
	}
	if _, err := domain.NewLogicalResourceID(string(command.ResourceID)); err != nil {
		return AccessSessionLaunch{}, err
	}
	if command.Duration < 0 || command.Duration > MaximumAccessSessionDuration || len(command.IdempotencyKey) == 0 || len(command.IdempotencyKey) > 256 {
		return AccessSessionLaunch{}, fmt.Errorf("access session request is invalid")
	}
	binding, err := s.access.Select(command.Method)
	if err != nil {
		return AccessSessionLaunch{}, err
	}
	access, err := s.authorizer.AuthorizeInstance(ctx, command.SubjectID, application.ActionAccessSessionCreate, command.InstanceID)
	if err != nil {
		return AccessSessionLaunch{}, err
	}
	instance, resources, err := s.GetInstance(ctx, command.InstanceID)
	if err != nil {
		return AccessSessionLaunch{}, err
	}
	if access.ProjectID != instance.ProjectID || access.TargetID != instance.ProviderTargetID {
		return AccessSessionLaunch{}, ErrAuthorityChanged
	}
	resource, err := accessResource(resources, command.ResourceID)
	if err != nil {
		return AccessSessionLaunch{}, err
	}
	stateSession, err := s.targets.Acquire(ctx, instance.ProviderTargetID, application.InvocationScope{}, false)
	if err != nil {
		return AccessSessionLaunch{}, err
	}
	stateDigestBytes, err := stateSession.Provider.StateDigest(resource.ProviderState)
	stateSession.Close()
	if err != nil || len(stateDigestBytes) != sha256.Size {
		return AccessSessionLaunch{}, fmt.Errorf("resource provider state is unavailable")
	}
	providerDigest, _ := domain.ParseSHA256Digest(hex.EncodeToString(stateDigestBytes))
	requestDigest := digestBytes("labcentral.access-session.create/v1", []byte(command.SubjectID), []byte(command.InstanceID),
		[]byte(command.ResourceID), []byte(command.Method), []byte(command.Duration.String()), []byte(binding.PluginInstance))
	sessionIDValue, err := s.ids.New("access")
	if err != nil {
		return AccessSessionLaunch{}, err
	}
	recordIDs, err := s.newGovernanceRecordIDs()
	if err != nil {
		return AccessSessionLaunch{}, err
	}
	pending, err := s.store.BeginAccessSession(ctx, BeginAccessSessionRequest{
		GovernanceRecordIDs: recordIDs, ID: domain.AccessSessionID(sessionIDValue), SubjectID: command.SubjectID,
		InstanceID: command.InstanceID, ResourceID: command.ResourceID, Method: command.Method,
		AccessPluginID: binding.PluginID, AccessPluginVersion: binding.PluginVersion,
		AccessPluginInstance: binding.PluginInstance, AccessKind: binding.AccessKind, Audience: binding.Audience,
		ProviderStateDigest: providerDigest, ResourceGeneration: instance.Generation, RequestedDuration: command.Duration,
		IdempotencyKey: command.IdempotencyKey, RequestDigest: requestDigest,
	})
	if err != nil {
		return AccessSessionLaunch{}, err
	}
	if pending.State == domain.AccessSessionRevoked || pending.State == domain.AccessSessionExpired || pending.State == domain.AccessSessionFailed || pending.State == domain.AccessSessionActive {
		return AccessSessionLaunch{}, fmt.Errorf("access session cannot issue new launch material")
	}

	invocationValue, err := s.ids.New("invocation")
	if err != nil {
		return AccessSessionLaunch{}, err
	}
	targetSession, err := s.targets.AcquireForSubject(ctx, pending.TargetID, command.SubjectID, application.InvocationScope{
		InvocationID: domain.InvocationID(invocationValue), InstanceID: pending.InstanceID, ResourceID: pending.ResourceID,
		ExpiresAt: time.Now().UTC().Add(20 * time.Second),
	}, true)
	if err != nil {
		return AccessSessionLaunch{}, s.failAccessSession(ctx, pending, "provider_unavailable", ErrAccessUnavailable)
	}
	defer targetSession.Close()
	discoveryContext, cancelDiscovery := context.WithTimeout(ctx, 12*time.Second)
	endpointProvider, ok := targetSession.Provider.(application.ProviderAccess)
	if !ok {
		cancelDiscovery()
		return AccessSessionLaunch{}, s.failAccessSession(ctx, pending, "provider_unavailable", ErrAccessUnsupported)
	}
	discovered, err := endpointProvider.DiscoverEndpoint(discoveryContext, application.ProviderCall{
		RequestID: "access-discover", InvocationID: invocationValue, InstanceID: string(pending.InstanceID), ResourceID: string(pending.ResourceID), TargetID: string(pending.TargetID), IdempotencyKey: command.IdempotencyKey, Generation: uint64(pending.ResourceGeneration),
	}, targetSession.Context, resource.ProviderState, pending.Method)
	cancelDiscovery()
	if err != nil || discovered.Error != nil {
		return AccessSessionLaunch{}, s.failAccessSession(ctx, pending, "provider_unavailable", ErrAccessUnavailable)
	}
	endpointBytes := discovered.Opaque
	defer clear(endpointBytes)
	endpointDigest, err := validateDiscoveredAccessEndpoint(discovered, pending, providerDigest, endpointBytes)
	if err != nil || !s.access.Accepts(binding, discovered.Schema) {
		return AccessSessionLaunch{}, s.failAccessSession(ctx, pending, "protocol_invalid", fmt.Errorf("provider access endpoint is incompatible"))
	}
	if _, err := s.authorizer.AuthorizeInstance(ctx, command.SubjectID, application.ActionAccessSessionCreate, command.InstanceID); err != nil {
		return AccessSessionLaunch{}, s.failAccessSession(ctx, pending, "authorization_denied", err)
	}
	expiresAt := earliestAccessExpiry(pending.ExpiresAt, discovered.ValidUntil)
	createContext, cancelCreate := context.WithTimeout(ctx, 12*time.Second)
	created, err := binding.Access.Create(createContext, application.AccessCreate{
		Call: application.ProviderCall{
			RequestID: "access-create", InvocationID: invocationValue, InstanceID: string(pending.InstanceID),
			ResourceID: string(pending.ResourceID), TargetID: string(pending.TargetID),
			IdempotencyKey: command.IdempotencyKey, Generation: uint64(pending.ResourceGeneration),
		},
		SessionID: string(pending.ID), SubjectID: string(command.SubjectID), Method: pending.Method,
		ExpiresAt: expiresAt, Endpoint: endpointBytes,
		Schema: discovered.Schema, EndpointDigest: endpointDigest.String(),
		StateDigest: providerDigest.String(), Audience: binding.Audience,
	})
	cancelCreate()
	if err != nil || created.Error != nil {
		return AccessSessionLaunch{}, s.failAccessSession(ctx, pending, "access_plugin_unavailable", ErrAccessUnavailable)
	}
	accessState := created.State
	defer clear(accessState)
	stateDigest, err := validateCreatedAccess(created, binding, pending.ID, expiresAt)
	if err != nil {
		return AccessSessionLaunch{}, s.failAccessSession(ctx, pending, "protocol_invalid", err)
	}
	expiresAt = earliestAccessExpiry(expiresAt, created.ValidUntil)
	now := time.Now().UTC()
	launchExpires := earliestAccessExpiry(expiresAt, now.Add(DefaultAccessLaunchTTL))
	finalIDs, err := s.newGovernanceRecordIDs()
	if err != nil {
		return AccessSessionLaunch{}, err
	}
	final, err := s.store.FinalizeAccessSession(ctx, FinalizeAccessSessionRequest{
		GovernanceRecordIDs: finalIDs, ID: pending.ID, SubjectID: command.SubjectID,
		EndpointSchemaName: discovered.Schema.Name, EndpointSchemaMajor: discovered.Schema.Major,
		EndpointSchemaMinor: discovered.Schema.Minor, EndpointDigest: endpointDigest,
		ProviderStateDigest: providerDigest, LaunchToken: s.tokenHasher.HashCurrent("access-launch", created.Token),
		LaunchExpiresAt: launchExpires, ExpiresAt: expiresAt, AccessStateSchema: created.Schema.Name,
		AccessStateMajor: created.Schema.Major, AccessStateMinor: created.Schema.Minor,
		AccessStateDigest: stateDigest, AccessState: append([]byte(nil), accessState...),
	})
	if err != nil {
		return AccessSessionLaunch{}, s.failCreatedAccessSession(ctx, pending, created, stateDigest, "authorization_denied", err)
	}
	return AccessSessionLaunch{Session: final, LaunchURL: created.URL, LaunchToken: created.Token}, nil
}

func (s *Service) GetAccessSession(ctx context.Context, subject domain.UserID, id domain.AccessSessionID) (AccessSessionRecord, error) {
	record, err := s.store.GetAccessSession(ctx, id)
	if err != nil {
		return record, err
	}
	if s.authorizer == nil {
		return AccessSessionRecord{}, ErrAccessUnavailable
	}
	if _, err := s.authorizer.AuthorizeInstance(ctx, subject, application.ActionAccessSessionCreate, record.InstanceID); err != nil {
		return AccessSessionRecord{}, err
	}
	return record, nil
}

func (s *Service) RevokeAccessSession(ctx context.Context, subject domain.UserID, id domain.AccessSessionID, reason, idempotencyKey string, expected domain.ResourceVersion) (AccessSessionRecord, error) {
	if s.access == nil || s.authorizer == nil {
		return AccessSessionRecord{}, ErrAccessUnavailable
	}
	record, err := s.store.GetAccessSession(ctx, id)
	if err != nil {
		return record, err
	}
	if _, err := s.authorizer.AuthorizeInstance(ctx, subject, application.ActionAccessSessionRevoke, record.InstanceID); err != nil {
		return AccessSessionRecord{}, err
	}
	ids, err := s.newGovernanceRecordIDs()
	if err != nil {
		return record, err
	}
	digest := digestBytes("labcentral.access-session.revoke/v1", []byte(subject), []byte(id), []byte(reason), []byte(fmt.Sprint(expected)))
	record, err = s.store.RevokeAccessSession(ctx, RevokeAccessSessionRequest{
		GovernanceRecordIDs: ids, ID: id, ActorID: subject, Reason: reason, ExpectedResourceVersion: expected,
		IdempotencyKey: idempotencyKey, RequestDigest: digest,
	})
	if err != nil || !record.ClosureRequired {
		return record, err
	}
	binding, err := s.access.Get(record.AccessPluginInstance)
	if err != nil {
		return record, nil
	}
	revokeContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	response, callErr := binding.Access.Revoke(revokeContext, application.AccessRevoke{
		Call:      application.ProviderCall{RequestID: "access-revoke", InstanceID: string(record.InstanceID), ResourceID: string(record.ResourceID), TargetID: string(record.TargetID)},
		SessionID: string(record.ID), State: append([]byte(nil), record.AccessState...),
	})
	cancel()
	if callErr == nil && response.Error == nil && response.Closed {
		if err := s.store.MarkAccessClosureComplete(ctx, record.ID); err == nil {
			record.ClosureRequired = false
			record.ResourceVersion++
		}
	}
	return record, nil
}

func (s *Service) ExchangeAccessToken(ctx context.Context, identity application.AccessCallbackIdentity, sessionID, token string) (application.AccessAuthorizationDecision, error) {
	id, err := domain.NewAccessSessionID(sessionID)
	if err != nil || len(token) < 43 || len(token) > 256 || s.tokenHasher == nil {
		return deniedAccessDecision(), nil
	}
	lease, err := randomAccessSecret()
	if err != nil {
		return application.AccessAuthorizationDecision{}, err
	}
	ids, err := s.newGovernanceRecordIDs()
	if err != nil {
		return application.AccessAuthorizationDecision{}, err
	}
	record, allowed, err := s.store.ExchangeAccessToken(ctx, AccessExchangeRequest{
		GovernanceRecordIDs: ids, ID: id, Identity: identity,
		TokenHashes: s.tokenHasher.HashCandidates("access-launch", token), NewLease: s.tokenHasher.HashCurrent("access-lease", lease),
		LeaseDuration: DefaultAccessLeaseDuration,
	})
	if err != nil {
		return application.AccessAuthorizationDecision{}, err
	}
	if !allowed {
		return deniedAccessDecision(), nil
	}
	return application.AccessAuthorizationDecision{Granted: true, Lease: lease, ValidUntil: record.LeaseExpiresAt, RecheckAfter: DefaultAccessLeaseDuration / 2}, nil
}

func (s *Service) RecheckAccessLease(ctx context.Context, identity application.AccessCallbackIdentity, sessionID, lease string) (application.AccessAuthorizationDecision, error) {
	id, err := domain.NewAccessSessionID(sessionID)
	if err != nil || len(lease) < 43 || len(lease) > 256 || s.tokenHasher == nil {
		return deniedAccessDecision(), nil
	}
	next, err := randomAccessSecret()
	if err != nil {
		return application.AccessAuthorizationDecision{}, err
	}
	ids, err := s.newGovernanceRecordIDs()
	if err != nil {
		return application.AccessAuthorizationDecision{}, err
	}
	record, allowed, err := s.store.RecheckAccessLease(ctx, AccessLeaseRecheckRequest{
		GovernanceRecordIDs: ids, ID: id, Identity: identity,
		LeaseHashes: s.tokenHasher.HashCandidates("access-lease", lease), NewLease: s.tokenHasher.HashCurrent("access-lease", next),
		LeaseDuration: DefaultAccessLeaseDuration,
	})
	if err != nil {
		return application.AccessAuthorizationDecision{}, err
	}
	if !allowed {
		return deniedAccessDecision(), nil
	}
	return application.AccessAuthorizationDecision{Granted: true, Lease: next, ValidUntil: record.LeaseExpiresAt, RecheckAfter: DefaultAccessLeaseDuration / 2}, nil
}

func (s *Service) EndAccessLease(ctx context.Context, identity application.AccessCallbackIdentity, sessionID, lease, reason string) (bool, error) {
	id, err := domain.NewAccessSessionID(sessionID)
	if err != nil || len(lease) < 43 || len(lease) > 256 || s.tokenHasher == nil {
		return false, nil
	}
	ids, err := s.newGovernanceRecordIDs()
	if err != nil {
		return false, err
	}
	_, ended, err := s.store.EndAccessLease(ctx, EndAccessLeaseRequest{
		GovernanceRecordIDs: ids, ID: id, Identity: identity,
		LeaseHashes: s.tokenHasher.HashCandidates("access-lease", lease), Reason: reason,
	})
	return ended, err
}

func (s *Service) RunAccessReconciliation(ctx context.Context, limit uint32) (AccessReconciliationResult, error) {
	if !s.AccessAvailable() || limit == 0 || limit > 1000 {
		return AccessReconciliationResult{}, fmt.Errorf("access reconciliation is unavailable or invalid")
	}
	candidates, err := s.store.ListAccessSessionsForReconciliation(ctx, limit)
	if err != nil {
		return AccessReconciliationResult{}, err
	}
	result := AccessReconciliationResult{}
	for _, candidate := range candidates {
		if candidate.State == domain.AccessSessionPending || candidate.State == domain.AccessSessionReady || candidate.State == domain.AccessSessionActive {
			ids, err := s.newGovernanceRecordIDs()
			if err != nil {
				return result, err
			}
			updated, expired, err := s.store.ExpireAccessSession(ctx, candidate.ID, ids)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					continue
				}
				return result, err
			}
			candidate = updated
			if expired {
				result.Expired++
			}
		}
		if !candidate.ClosureRequired {
			continue
		}
		binding, err := s.access.Get(candidate.AccessPluginInstance)
		if err != nil {
			continue
		}
		result.ClosuresRetried++
		callContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		response, callErr := binding.Access.Revoke(callContext, application.AccessRevoke{
			Call: application.ProviderCall{
				RequestID: "access-reconcile", InstanceID: string(candidate.InstanceID), ResourceID: string(candidate.ResourceID), TargetID: string(candidate.TargetID),
			},
			SessionID: string(candidate.ID), State: append([]byte(nil), candidate.AccessState...),
		})
		cancel()
		if callErr == nil && response.Error == nil && response.Closed {
			if err := s.store.MarkAccessClosureComplete(ctx, candidate.ID); err != nil {
				return result, err
			}
			result.ClosuresClosed++
		}
	}
	return result, nil
}

func (s *Service) failAccessSession(ctx context.Context, pending AccessSessionRecord, reason string, result error) error {
	ids, idErr := s.newGovernanceRecordIDs()
	if idErr != nil {
		return result
	}
	_, _ = s.store.FailAccessSession(ctx, FailAccessSessionRequest{
		GovernanceRecordIDs: ids, ID: pending.ID, ActorID: pending.SubjectID, Reason: reason,
	})
	return result
}

func (s *Service) failCreatedAccessSession(ctx context.Context, pending AccessSessionRecord, created application.AccessCreated, stateDigest domain.SHA256Digest, reason string, result error) error {
	ids, idErr := s.newGovernanceRecordIDs()
	if idErr != nil {
		return result
	}
	_, _ = s.store.FailAccessSession(ctx, FailAccessSessionRequest{
		GovernanceRecordIDs: ids, ID: pending.ID, ActorID: pending.SubjectID, Reason: reason,
		AccessStateSchema: created.Schema.Name, AccessStateMajor: created.Schema.Major,
		AccessStateMinor: created.Schema.Minor, AccessStateDigest: stateDigest,
		AccessState: append([]byte(nil), created.State...), ClosureRequired: true,
	})
	return result
}

func accessResource(resources []ResourceRecord, id domain.LogicalResourceID) (ResourceRecord, error) {
	for _, resource := range resources {
		if resource.ID == id && resource.UsesVirtualMachine() && (resource.Phase == domain.ResourceReady || resource.Phase == domain.ResourcePaused) {
			return resource, nil
		}
	}
	return ResourceRecord{}, ErrNotFound
}

func validateDiscoveredAccessEndpoint(response application.ProviderEndpoint, session AccessSessionRecord, expectedState domain.SHA256Digest, opaque []byte) (domain.SHA256Digest, error) {
	if response.Schema.Major == 0 || len(opaque) == 0 || len(opaque) > maximumAccessEnvelopeBytes ||
		response.TargetID != string(session.TargetID) || response.InstanceID != string(session.InstanceID) ||
		response.ResourceID != string(session.ResourceID) || response.Method != session.Method ||
		!response.ValidUntil.After(time.Now().UTC()) {
		return "", fmt.Errorf("provider access endpoint binding is invalid")
	}
	stateDigest, err := domain.ParseSHA256Digest(response.StateDigest)
	if err != nil || stateDigest != expectedState {
		return "", fmt.Errorf("provider access endpoint state binding is invalid")
	}
	claimed, err := domain.ParseSHA256Digest(response.Digest)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(opaque)
	actual, _ := domain.ParseSHA256Digest(hex.EncodeToString(digest[:]))
	if claimed != actual {
		return "", fmt.Errorf("provider access endpoint digest mismatch")
	}
	return actual, nil
}

func validateCreatedAccess(response application.AccessCreated, binding AccessBinding, sessionID domain.AccessSessionID, requestedExpiry time.Time) (domain.SHA256Digest, error) {
	if response.Schema.Major == 0 ||
		len(response.State) == 0 || len(response.State) > maximumAccessEnvelopeBytes ||
		len(response.Token) < 43 || len(response.Token) > 256 ||
		!response.ValidUntil.After(time.Now().UTC()) ||
		response.ValidUntil.After(requestedExpiry) {
		return "", fmt.Errorf("access plugin response is invalid")
	}
	if err := validateAccessLaunchURL(response.URL, binding.Audience, string(sessionID), response.Token); err != nil {
		return "", err
	}
	claimed, err := domain.ParseSHA256Digest(response.Digest)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(response.State)
	actual, _ := domain.ParseSHA256Digest(hex.EncodeToString(digest[:]))
	if claimed != actual {
		return "", fmt.Errorf("access plugin state digest mismatch")
	}
	return actual, nil
}

func validateAccessLaunchURL(value, audience, sessionID, token string) error {
	if value == "" || len(value) > 2048 || strings.Contains(value, token) {
		return fmt.Errorf("access launch URL is invalid")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.Contains(parsed.EscapedPath(), url.PathEscape(sessionID)) {
		return fmt.Errorf("access launch URL is invalid")
	}
	if parsed.IsAbs() {
		origin := parsed.Scheme + "://" + parsed.Host
		if parsed.Scheme != "https" || origin != audience {
			return fmt.Errorf("access launch URL audience is invalid")
		}
	} else if !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") {
		return fmt.Errorf("access launch URL must be relative or use the configured HTTPS audience")
	}
	return nil
}

func earliestAccessExpiry(values ...time.Time) time.Time {
	var result time.Time
	for _, value := range values {
		value = value.UTC()
		if value.IsZero() {
			continue
		}
		if result.IsZero() || value.Before(result) {
			result = value
		}
	}
	return result
}

func randomAccessSecret() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate access authorization material: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func deniedAccessDecision() application.AccessAuthorizationDecision {
	return application.AccessAuthorizationDecision{Granted: false, Code: "access_denied", SafeMessage: "Access authorization was denied"}
}

var _ application.AccessAuthorizationHandler = (*Service)(nil)
