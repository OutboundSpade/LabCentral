package localdb

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/internal/adapter/jobsdb"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"google.golang.org/protobuf/proto"
)

const createAccessSessionRouteAction = "create_access_session"
const revokeAccessSessionRouteAction = "revoke_access_session"
const maximumAccessStateBytes = 64 << 10

var accessValuePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,511}$`)

func accessSessionMigrationStatements(integerType, blobType string) []string {
	return []string{
		`CREATE TABLE labcentral_access_sessions (
			access_session_id TEXT PRIMARY KEY,
			subject_id TEXT NOT NULL,
			organization_id TEXT NOT NULL,
			project_id TEXT NOT NULL,
			instance_id TEXT NOT NULL REFERENCES labcentral_local_instances(instance_id),
			resource_id TEXT NOT NULL,
			method TEXT NOT NULL CHECK (method IN ('ssh', 'vnc')),
			target_id TEXT NOT NULL,
			access_plugin_id TEXT NOT NULL,
			access_plugin_version TEXT NOT NULL,
			access_plugin_instance_id TEXT NOT NULL,
			access_kind TEXT NOT NULL,
			audience TEXT NOT NULL,
			endpoint_schema_name TEXT NOT NULL DEFAULT '',
			endpoint_schema_major ` + integerType + ` NOT NULL DEFAULT 0,
			endpoint_schema_minor ` + integerType + ` NOT NULL DEFAULT 0,
			endpoint_digest TEXT NOT NULL DEFAULT '',
			provider_state_digest TEXT NOT NULL,
			resource_generation ` + integerType + ` NOT NULL CHECK (resource_generation > 0),
			state TEXT NOT NULL CHECK (state IN ('pending', 'ready', 'active', 'expired', 'revoked', 'failed')),
			launch_token_key_id TEXT NOT NULL DEFAULT '',
			launch_token_hash TEXT NOT NULL DEFAULT '',
			launch_expires_at_us ` + integerType + ` NOT NULL DEFAULT 0,
			lease_key_id TEXT NOT NULL DEFAULT '',
			lease_hash TEXT NOT NULL DEFAULT '',
			lease_expires_at_us ` + integerType + ` NOT NULL DEFAULT 0,
			last_authorized_at_us ` + integerType + ` NOT NULL DEFAULT 0,
			expires_at_us ` + integerType + ` NOT NULL CHECK (expires_at_us > 0),
			revoked_at_us ` + integerType + ` NOT NULL DEFAULT 0,
			revoked_by TEXT NOT NULL DEFAULT '',
			revocation_reason TEXT NOT NULL DEFAULT '',
			access_state_schema TEXT NOT NULL DEFAULT '',
			access_state_major ` + integerType + ` NOT NULL DEFAULT 0,
			access_state_minor ` + integerType + ` NOT NULL DEFAULT 0,
			access_state_digest TEXT NOT NULL DEFAULT '',
			access_state ` + blobType + `,
			closure_required ` + integerType + ` NOT NULL DEFAULT 0 CHECK (closure_required IN (0, 1)),
			resource_version ` + integerType + ` NOT NULL CHECK (resource_version > 0),
			created_at_us ` + integerType + ` NOT NULL CHECK (created_at_us > 0),
			updated_at_us ` + integerType + ` NOT NULL CHECK (updated_at_us > 0),
			FOREIGN KEY (instance_id, resource_id) REFERENCES labcentral_local_resources(instance_id, resource_id)
		)`,
		`CREATE INDEX labcentral_access_sessions_subject_idx
			ON labcentral_access_sessions(subject_id, created_at_us, access_session_id)`,
		`CREATE INDEX labcentral_access_sessions_instance_idx
			ON labcentral_access_sessions(instance_id, resource_id, created_at_us, access_session_id)`,
		`CREATE INDEX labcentral_access_sessions_expiry_idx
			ON labcentral_access_sessions(state, expires_at_us, access_session_id)`,
		`CREATE INDEX labcentral_access_sessions_closure_idx
			ON labcentral_access_sessions(closure_required, updated_at_us, access_session_id)`,
	}
}

func (s *Store) BeginAccessSession(ctx context.Context, request controlplane.BeginAccessSessionRequest) (controlplane.AccessSessionRecord, error) {
	if err := validateBeginAccessSession(request); err != nil {
		return controlplane.AccessSessionRecord{}, err
	}
	for attempt := 0; ; attempt++ {
		record, err := s.beginAccessSession(ctx, request)
		if !isRetryableTransactionConflict(err) || attempt == 7 {
			return record, err
		}
		if err := waitTransactionRetry(ctx, attempt); err != nil {
			return controlplane.AccessSessionRecord{}, err
		}
	}
}

func (s *Store) beginAccessSession(ctx context.Context, request controlplane.BeginAccessSessionRequest) (controlplane.AccessSessionRecord, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return controlplane.AccessSessionRecord{}, fmt.Errorf("begin access session: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.lockIdempotency(ctx, tx, request.SubjectID, createAccessSessionRouteAction, request.IdempotencyKey); err != nil {
		return controlplane.AccessSessionRecord{}, err
	}
	if existing, found, err := s.findIdempotency(ctx, tx, request.SubjectID, createAccessSessionRouteAction, request.IdempotencyKey, request.RequestDigest); err != nil {
		return controlplane.AccessSessionRecord{}, err
	} else if found {
		record, err := s.getAccessSessionTx(ctx, tx, domain.AccessSessionID(existing), true)
		if err != nil {
			return controlplane.AccessSessionRecord{}, err
		}
		record.Replay = true
		return record, tx.Commit()
	}
	now, err := s.databaseNowTx(ctx, tx)
	if err != nil {
		return controlplane.AccessSessionRecord{}, err
	}
	instance, stateDigest, err := s.loadAccessResourceTx(ctx, tx, request.InstanceID, request.ResourceID, true)
	if err != nil {
		return controlplane.AccessSessionRecord{}, err
	}
	if instance.Generation != request.ResourceGeneration || stateDigest != request.ProviderStateDigest || !accessResourceUsable(instance) {
		return controlplane.AccessSessionRecord{}, controlplane.ErrAuthorityChanged
	}
	if err := s.requireInstanceOwnerAuthorizationTx(ctx, tx, instance, request.SubjectID, now); err != nil {
		return controlplane.AccessSessionRecord{}, err
	}
	duration := request.RequestedDuration
	if duration == 0 {
		duration = controlplane.DefaultAccessSessionDuration
	}
	expiresAt := now.Add(duration)
	maximum := now.Add(controlplane.MaximumAccessSessionDuration)
	if !instance.ExpiresAt.IsZero() && instance.ExpiresAt.Before(maximum) {
		maximum = instance.ExpiresAt
	}
	if expiresAt.After(maximum) {
		expiresAt = maximum
	}
	if !expiresAt.After(now) {
		return controlplane.AccessSessionRecord{}, fmt.Errorf("access session expiry has elapsed")
	}
	var organizationID domain.OrganizationID
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT organization_id FROM labcentral_projects WHERE project_id = ?`), instance.ProjectID).Scan(&organizationID); err != nil {
		return controlplane.AccessSessionRecord{}, fmt.Errorf("load access session scope: %w", err)
	}
	var createdAt, updatedAt int64
	err = tx.QueryRowContext(ctx, s.bind(`INSERT INTO labcentral_access_sessions
		(access_session_id, subject_id, organization_id, project_id, instance_id, resource_id, method, target_id,
		 access_plugin_id, access_plugin_version, access_plugin_instance_id, access_kind, audience,
		 provider_state_digest, resource_generation, state, expires_at_us, resource_version, created_at_us, updated_at_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, 1, `+s.nowMicros()+`, `+s.nowMicros()+`)
		RETURNING created_at_us, updated_at_us`), request.ID, request.SubjectID, organizationID, instance.ProjectID,
		request.InstanceID, request.ResourceID, request.Method, instance.ProviderTargetID, request.AccessPluginID,
		request.AccessPluginVersion, request.AccessPluginInstance, request.AccessKind, request.Audience,
		request.ProviderStateDigest.Hex(), request.ResourceGeneration, expiresAt.UnixMicro()).Scan(&createdAt, &updatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return controlplane.AccessSessionRecord{}, controlplane.ErrAlreadyExists
		}
		return controlplane.AccessSessionRecord{}, fmt.Errorf("persist pending access session: %w", err)
	}
	record := controlplane.AccessSessionRecord{
		ID: request.ID, SubjectID: request.SubjectID, OrganizationID: organizationID, ProjectID: instance.ProjectID,
		InstanceID: request.InstanceID, ResourceID: request.ResourceID, Method: request.Method, TargetID: instance.ProviderTargetID,
		AccessPluginID: request.AccessPluginID, AccessPluginVersion: request.AccessPluginVersion,
		AccessPluginInstance: request.AccessPluginInstance, AccessKind: request.AccessKind, Audience: request.Audience,
		ProviderStateDigest: request.ProviderStateDigest, ResourceGeneration: request.ResourceGeneration,
		State: domain.AccessSessionPending, ExpiresAt: expiresAt, ResourceVersion: 1,
		CreatedAt: time.UnixMicro(createdAt).UTC(), UpdatedAt: time.UnixMicro(updatedAt).UTC(),
	}
	if err := s.appendAccessGovernanceTx(ctx, tx, record, request.GovernanceRecordIDs, "access.session.create", "access-session.pending", application.AuditAccepted, ""); err != nil {
		return controlplane.AccessSessionRecord{}, err
	}
	if err := s.bindIdempotency(ctx, tx, request.SubjectID, createAccessSessionRouteAction, request.IdempotencyKey, request.RequestDigest, string(request.ID)); err != nil {
		return controlplane.AccessSessionRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return controlplane.AccessSessionRecord{}, fmt.Errorf("commit pending access session: %w", err)
	}
	return record, nil
}

func (s *Store) FinalizeAccessSession(ctx context.Context, request controlplane.FinalizeAccessSessionRequest) (controlplane.AccessSessionRecord, error) {
	if err := validateFinalizeAccessSession(request); err != nil {
		return controlplane.AccessSessionRecord{}, err
	}
	return s.mutateAccessSession(ctx, func(tx *sql.Tx) (controlplane.AccessSessionRecord, error) {
		record, err := s.getAccessSessionTx(ctx, tx, request.ID, true)
		if err != nil {
			return record, err
		}
		if record.SubjectID != request.SubjectID || (record.State != domain.AccessSessionPending && record.State != domain.AccessSessionReady) || record.ProviderStateDigest != request.ProviderStateDigest {
			return record, controlplane.ErrAuthorityChanged
		}
		now, err := s.databaseNowTx(ctx, tx)
		if err != nil {
			return record, err
		}
		instance, stateDigest, err := s.loadAccessResourceTx(ctx, tx, record.InstanceID, record.ResourceID, true)
		if err != nil || stateDigest != record.ProviderStateDigest || instance.Generation != record.ResourceGeneration || !accessResourceUsable(instance) {
			return record, controlplane.ErrAuthorityChanged
		}
		if err := s.requireInstanceOwnerAuthorizationTx(ctx, tx, instance, record.SubjectID, now); err != nil {
			return record, err
		}
		if !request.ExpiresAt.After(now) || request.ExpiresAt.After(record.ExpiresAt) || !request.LaunchExpiresAt.After(now) || request.LaunchExpiresAt.After(request.ExpiresAt) || request.LaunchExpiresAt.After(now.Add(controlplane.DefaultAccessLaunchTTL)) {
			return record, fmt.Errorf("access session validity is invalid")
		}
		result, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_access_sessions SET
			endpoint_schema_name = ?, endpoint_schema_major = ?, endpoint_schema_minor = ?, endpoint_digest = ?,
			launch_token_key_id = ?, launch_token_hash = ?, launch_expires_at_us = ?, expires_at_us = ?,
			lease_key_id = '', lease_hash = '', lease_expires_at_us = 0, last_authorized_at_us = 0,
			access_state_schema = ?, access_state_major = ?, access_state_minor = ?, access_state_digest = ?, access_state = ?,
			state = 'ready', closure_required = 0, resource_version = resource_version + 1, updated_at_us = `+s.nowMicros()+`
			WHERE access_session_id = ? AND resource_version = ?`),
			request.EndpointSchemaName, request.EndpointSchemaMajor, request.EndpointSchemaMinor, request.EndpointDigest.Hex(),
			request.LaunchToken.KeyID, request.LaunchToken.Digest, request.LaunchExpiresAt.UnixMicro(), request.ExpiresAt.UnixMicro(),
			request.AccessStateSchema, request.AccessStateMajor, request.AccessStateMinor, request.AccessStateDigest.Hex(), nullableBytes(request.AccessState),
			request.ID, record.ResourceVersion)
		if err != nil {
			return record, fmt.Errorf("finalize access session: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return record, controlplane.ErrVersionConflict
		}
		record, err = s.getAccessSessionTx(ctx, tx, request.ID, false)
		if err != nil {
			return record, err
		}
		if err := s.appendAccessGovernanceTx(ctx, tx, record, request.GovernanceRecordIDs, "access.session.ready", "access-session.ready", application.AuditSucceeded, ""); err != nil {
			return record, err
		}
		return record, nil
	})
}

func (s *Store) FailAccessSession(ctx context.Context, request controlplane.FailAccessSessionRequest) (controlplane.AccessSessionRecord, error) {
	if _, err := domain.NewAccessSessionID(string(request.ID)); err != nil {
		return controlplane.AccessSessionRecord{}, err
	}
	if _, err := domain.NewUserID(string(request.ActorID)); err != nil {
		return controlplane.AccessSessionRecord{}, err
	}
	if err := validateGovernanceRecordIDs(request.GovernanceRecordIDs); err != nil {
		return controlplane.AccessSessionRecord{}, err
	}
	if request.ClosureRequired && (!accessValuePattern.MatchString(request.AccessStateSchema) || request.AccessStateMajor == 0 || request.AccessStateDigest == "" || len(request.AccessState) == 0 || len(request.AccessState) > maximumAccessStateBytes) {
		return controlplane.AccessSessionRecord{}, fmt.Errorf("failed access cleanup state is invalid")
	}
	reason := safeAccessReason(request.Reason)
	return s.mutateAccessSession(ctx, func(tx *sql.Tx) (controlplane.AccessSessionRecord, error) {
		record, err := s.getAccessSessionTx(ctx, tx, request.ID, true)
		if err != nil {
			return record, err
		}
		if record.State == domain.AccessSessionRevoked || record.State == domain.AccessSessionExpired || record.State == domain.AccessSessionFailed {
			return record, nil
		}
		closure := request.ClosureRequired || record.ClosureRequired || record.State == domain.AccessSessionReady || record.State == domain.AccessSessionActive
		stateSchema, stateMajor, stateMinor, stateDigest, state := record.AccessStateSchema, record.AccessStateMajor, record.AccessStateMinor, record.AccessStateDigest, record.AccessState
		if request.ClosureRequired {
			stateSchema, stateMajor, stateMinor, stateDigest, state = request.AccessStateSchema, request.AccessStateMajor, request.AccessStateMinor, request.AccessStateDigest, request.AccessState
		}
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_access_sessions SET state = 'failed',
			launch_token_key_id = '', launch_token_hash = '', launch_expires_at_us = 0,
			lease_key_id = '', lease_hash = '', lease_expires_at_us = 0, closure_required = ?,
			access_state_schema = ?, access_state_major = ?, access_state_minor = ?, access_state_digest = ?, access_state = ?,
			resource_version = resource_version + 1, updated_at_us = `+s.nowMicros()+` WHERE access_session_id = ?`),
			quotaBoolean(closure), stateSchema, stateMajor, stateMinor, stateDigest, nullableBytes(state), request.ID); err != nil {
			return record, fmt.Errorf("fail access session: %w", err)
		}
		record, err = s.getAccessSessionTx(ctx, tx, request.ID, false)
		if err == nil {
			err = s.appendAccessGovernanceTx(ctx, tx, record, request.GovernanceRecordIDs, "access.session.failed", "access-session.failed", application.AuditFailed, reason)
		}
		return record, err
	})
}

func (s *Store) GetAccessSession(ctx context.Context, id domain.AccessSessionID) (controlplane.AccessSessionRecord, error) {
	if _, err := domain.NewAccessSessionID(string(id)); err != nil {
		return controlplane.AccessSessionRecord{}, err
	}
	return s.getAccessSession(ctx, s.db, id, false)
}

func (s *Store) ExchangeAccessToken(ctx context.Context, request controlplane.AccessExchangeRequest) (controlplane.AccessSessionRecord, bool, error) {
	if err := validateAccessExchange(request); err != nil {
		return controlplane.AccessSessionRecord{}, false, err
	}
	authorized := false
	record, err := s.mutateAccessSession(ctx, func(tx *sql.Tx) (controlplane.AccessSessionRecord, error) {
		current, err := s.getAccessSessionTx(ctx, tx, request.ID, true)
		if err != nil {
			return current, err
		}
		if !accessCallbackMatches(current, request.Identity) || current.State != domain.AccessSessionReady ||
			!keyedHashMatches(current.LaunchTokenKeyID, current.LaunchTokenHash, request.TokenHashes) {
			return current, nil
		}
		now, err := s.databaseNowTx(ctx, tx)
		if err != nil {
			return current, err
		}
		allowed, err := s.accessCurrentlyAuthorizedTx(ctx, tx, current, now)
		if err != nil {
			return current, err
		}
		if !allowed || !current.LaunchExpiresAt.After(now) || !current.ExpiresAt.After(now) {
			state, reason := domain.AccessSessionFailed, "authorization_denied"
			if !current.LaunchExpiresAt.After(now) || !current.ExpiresAt.After(now) {
				state, reason = domain.AccessSessionExpired, "expired"
			}
			if err := s.denyAccessTx(ctx, tx, &current, state, false); err != nil {
				return current, err
			}
			if err := s.appendAccessGovernanceTx(ctx, tx, current, request.GovernanceRecordIDs, "access.token.exchange", "access-session.denied", application.AuditDenied, reason); err != nil {
				return current, err
			}
			return current, nil
		}
		leaseExpires := now.Add(request.LeaseDuration)
		if leaseExpires.After(current.ExpiresAt) {
			leaseExpires = current.ExpiresAt
		}
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_access_sessions SET state = 'active',
			launch_token_key_id = '', launch_token_hash = '', launch_expires_at_us = 0,
			lease_key_id = ?, lease_hash = ?, lease_expires_at_us = ?, last_authorized_at_us = ?,
			resource_version = resource_version + 1, updated_at_us = `+s.nowMicros()+` WHERE access_session_id = ?`),
			request.NewLease.KeyID, request.NewLease.Digest, leaseExpires.UnixMicro(), now.UnixMicro(), current.ID); err != nil {
			return current, fmt.Errorf("activate access session: %w", err)
		}
		current, err = s.getAccessSessionTx(ctx, tx, current.ID, false)
		if err != nil {
			return current, err
		}
		if err := s.appendAccessGovernanceTx(ctx, tx, current, request.GovernanceRecordIDs, "access.token.exchange", "access-session.active", application.AuditSucceeded, ""); err != nil {
			return current, err
		}
		authorized = true
		return current, nil
	})
	return record, authorized, err
}

func (s *Store) RecheckAccessLease(ctx context.Context, request controlplane.AccessLeaseRecheckRequest) (controlplane.AccessSessionRecord, bool, error) {
	if err := validateAccessRecheck(request); err != nil {
		return controlplane.AccessSessionRecord{}, false, err
	}
	authorized := false
	record, err := s.mutateAccessSession(ctx, func(tx *sql.Tx) (controlplane.AccessSessionRecord, error) {
		current, err := s.getAccessSessionTx(ctx, tx, request.ID, true)
		if err != nil {
			return current, err
		}
		if !accessCallbackMatches(current, request.Identity) || current.State != domain.AccessSessionActive ||
			!keyedHashMatches(current.LeaseKeyID, current.LeaseHash, request.LeaseHashes) {
			return current, nil
		}
		now, err := s.databaseNowTx(ctx, tx)
		if err != nil {
			return current, err
		}
		allowed, err := s.accessCurrentlyAuthorizedTx(ctx, tx, current, now)
		if err != nil {
			return current, err
		}
		if !allowed || !current.LeaseExpiresAt.After(now) || !current.ExpiresAt.After(now) {
			state, reason := domain.AccessSessionFailed, "authorization_denied"
			if !current.LeaseExpiresAt.After(now) || !current.ExpiresAt.After(now) {
				state, reason = domain.AccessSessionExpired, "expired"
			}
			if err := s.denyAccessTx(ctx, tx, &current, state, true); err != nil {
				return current, err
			}
			if err := s.appendAccessGovernanceTx(ctx, tx, current, request.GovernanceRecordIDs, "access.lease.recheck", "access-session.denied", application.AuditDenied, reason); err != nil {
				return current, err
			}
			return current, nil
		}
		leaseExpires := now.Add(request.LeaseDuration)
		if leaseExpires.After(current.ExpiresAt) {
			leaseExpires = current.ExpiresAt
		}
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_access_sessions SET lease_key_id = ?, lease_hash = ?,
			lease_expires_at_us = ?, last_authorized_at_us = ?, resource_version = resource_version + 1,
			updated_at_us = `+s.nowMicros()+` WHERE access_session_id = ?`), request.NewLease.KeyID, request.NewLease.Digest,
			leaseExpires.UnixMicro(), now.UnixMicro(), current.ID); err != nil {
			return current, fmt.Errorf("renew access authorization lease: %w", err)
		}
		current, err = s.getAccessSessionTx(ctx, tx, current.ID, false)
		authorized = err == nil
		return current, err
	})
	return record, authorized, err
}

func (s *Store) EndAccessLease(ctx context.Context, request controlplane.EndAccessLeaseRequest) (controlplane.AccessSessionRecord, bool, error) {
	if err := validateEndAccessLease(request); err != nil {
		return controlplane.AccessSessionRecord{}, false, err
	}
	ended := false
	record, err := s.mutateAccessSession(ctx, func(tx *sql.Tx) (controlplane.AccessSessionRecord, error) {
		current, err := s.getAccessSessionTx(ctx, tx, request.ID, true)
		if err != nil {
			return current, err
		}
		if current.State == domain.AccessSessionExpired || current.State == domain.AccessSessionRevoked || current.State == domain.AccessSessionFailed {
			if !accessCallbackMatches(current, request.Identity) {
				return current, nil
			}
			if current.ClosureRequired {
				if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_access_sessions SET closure_required = 0,
					resource_version = resource_version + 1, updated_at_us = `+s.nowMicros()+`
					WHERE access_session_id = ? AND closure_required = 1`), current.ID); err != nil {
					return current, fmt.Errorf("acknowledge terminal access closure: %w", err)
				}
				current, err = s.getAccessSessionTx(ctx, tx, current.ID, false)
				if err != nil {
					return current, err
				}
			}
			ended = true
			return current, nil
		}
		if !accessCallbackMatches(current, request.Identity) || current.State != domain.AccessSessionActive ||
			!keyedHashMatches(current.LeaseKeyID, current.LeaseHash, request.LeaseHashes) {
			return current, nil
		}
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_access_sessions SET state = 'expired',
			lease_key_id = '', lease_hash = '', lease_expires_at_us = 0, closure_required = 0,
			resource_version = resource_version + 1, updated_at_us = `+s.nowMicros()+` WHERE access_session_id = ?`), current.ID); err != nil {
			return current, fmt.Errorf("end access authorization lease: %w", err)
		}
		current, err = s.getAccessSessionTx(ctx, tx, current.ID, false)
		if err != nil {
			return current, err
		}
		if err := s.appendAccessGovernanceTx(ctx, tx, current, request.GovernanceRecordIDs, "access.lease.end", "access-session.ended", application.AuditSucceeded, request.Reason); err != nil {
			return current, err
		}
		ended = true
		return current, nil
	})
	return record, ended, err
}

func (s *Store) RevokeAccessSession(ctx context.Context, request controlplane.RevokeAccessSessionRequest) (controlplane.AccessSessionRecord, error) {
	if err := validateRevokeAccessSession(request); err != nil {
		return controlplane.AccessSessionRecord{}, err
	}
	for attempt := 0; ; attempt++ {
		record, err := s.revokeAccessSession(ctx, request)
		if !isRetryableTransactionConflict(err) || attempt == 7 {
			return record, err
		}
		if err := waitTransactionRetry(ctx, attempt); err != nil {
			return controlplane.AccessSessionRecord{}, err
		}
	}
}

func (s *Store) revokeAccessSession(ctx context.Context, request controlplane.RevokeAccessSessionRequest) (controlplane.AccessSessionRecord, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return controlplane.AccessSessionRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.lockIdempotency(ctx, tx, request.ActorID, revokeAccessSessionRouteAction, request.IdempotencyKey); err != nil {
		return controlplane.AccessSessionRecord{}, err
	}
	if existing, found, err := s.findIdempotency(ctx, tx, request.ActorID, revokeAccessSessionRouteAction, request.IdempotencyKey, request.RequestDigest); err != nil {
		return controlplane.AccessSessionRecord{}, err
	} else if found {
		record, err := s.getAccessSessionTx(ctx, tx, domain.AccessSessionID(existing), true)
		record.Replay = err == nil
		if err != nil {
			return record, err
		}
		return record, tx.Commit()
	}
	record, err := s.getAccessSessionTx(ctx, tx, request.ID, true)
	if err != nil {
		return record, err
	}
	if record.ResourceVersion != request.ExpectedResourceVersion {
		return record, controlplane.ErrVersionConflict
	}
	now, err := s.databaseNowTx(ctx, tx)
	if err != nil {
		return record, err
	}
	instanceQuery := instanceRecordSelect + ` WHERE instance_id = ?`
	if s.dialect == jobsdb.DialectPostgreSQL {
		instanceQuery += ` FOR UPDATE`
	}
	instance, err := scanInstance(tx.QueryRowContext(ctx, s.bind(instanceQuery), record.InstanceID))
	if err != nil {
		return record, err
	}
	if err := s.requireInstanceOwnerAuthorizationTx(ctx, tx, instance, request.ActorID, now); err != nil {
		return record, err
	}
	closureRequired := record.State == domain.AccessSessionReady || record.State == domain.AccessSessionActive
	if record.State != domain.AccessSessionRevoked {
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_access_sessions SET state = 'revoked',
			launch_token_key_id = '', launch_token_hash = '', launch_expires_at_us = 0,
			lease_key_id = '', lease_hash = '', lease_expires_at_us = 0,
			revoked_at_us = ?, revoked_by = ?, revocation_reason = ?, closure_required = ?,
			resource_version = resource_version + 1, updated_at_us = `+s.nowMicros()+` WHERE access_session_id = ?`),
			now.UnixMicro(), request.ActorID, safeAccessReason(request.Reason), quotaBoolean(closureRequired), request.ID); err != nil {
			return record, fmt.Errorf("revoke access session: %w", err)
		}
		record, err = s.getAccessSessionTx(ctx, tx, request.ID, false)
		if err != nil {
			return record, err
		}
		if err := s.appendAccessGovernanceTx(ctx, tx, record, request.GovernanceRecordIDs, "access.session.revoke", "access-session.revoked", application.AuditSucceeded, request.Reason); err != nil {
			return record, err
		}
	}
	if err := s.bindIdempotency(ctx, tx, request.ActorID, revokeAccessSessionRouteAction, request.IdempotencyKey, request.RequestDigest, string(request.ID)); err != nil {
		return record, err
	}
	if err := tx.Commit(); err != nil {
		return record, err
	}
	return record, nil
}

func (s *Store) MarkAccessClosureComplete(ctx context.Context, id domain.AccessSessionID) error {
	if _, err := domain.NewAccessSessionID(string(id)); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.bind(`UPDATE labcentral_access_sessions SET closure_required = 0,
		resource_version = resource_version + 1, updated_at_us = `+s.nowMicros()+`
		WHERE access_session_id = ? AND closure_required = 1`), id)
	return err
}

func (s *Store) ListAccessSessionsForReconciliation(ctx context.Context, limit uint32) ([]controlplane.AccessSessionRecord, error) {
	if limit == 0 || limit > 1000 {
		return nil, fmt.Errorf("access reconciliation limit must be 1-1000")
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT access_session_id FROM labcentral_access_sessions
		WHERE closure_required = 1 OR
			(state IN ('pending', 'ready', 'active') AND expires_at_us <= `+s.nowMicros()+`) OR
			(state = 'ready' AND launch_expires_at_us > 0 AND launch_expires_at_us <= `+s.nowMicros()+`)
		ORDER BY updated_at_us, access_session_id LIMIT ?`), limit)
	if err != nil {
		return nil, fmt.Errorf("list access sessions for reconciliation: %w", err)
	}
	defer rows.Close()
	ids := make([]domain.AccessSessionID, 0)
	for rows.Next() {
		var id domain.AccessSessionID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	records := make([]controlplane.AccessSessionRecord, 0, len(ids))
	for _, id := range ids {
		record, err := s.GetAccessSession(ctx, id)
		if errors.Is(err, controlplane.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func (s *Store) ExpireAccessSession(ctx context.Context, id domain.AccessSessionID, ids controlplane.GovernanceRecordIDs) (controlplane.AccessSessionRecord, bool, error) {
	if _, err := domain.NewAccessSessionID(string(id)); err != nil {
		return controlplane.AccessSessionRecord{}, false, err
	}
	if err := validateGovernanceRecordIDs(ids); err != nil {
		return controlplane.AccessSessionRecord{}, false, err
	}
	changed := false
	record, err := s.mutateAccessSession(ctx, func(tx *sql.Tx) (controlplane.AccessSessionRecord, error) {
		current, err := s.getAccessSessionTx(ctx, tx, id, true)
		if err != nil {
			return current, err
		}
		if current.State == domain.AccessSessionExpired || current.State == domain.AccessSessionRevoked || current.State == domain.AccessSessionFailed {
			return current, nil
		}
		now, err := s.databaseNowTx(ctx, tx)
		if err != nil {
			return current, err
		}
		due := !current.ExpiresAt.After(now)
		if current.State == domain.AccessSessionReady && !current.LaunchExpiresAt.IsZero() && !current.LaunchExpiresAt.After(now) {
			due = true
		}
		if !due {
			return current, nil
		}
		closure := current.ClosureRequired || current.State == domain.AccessSessionReady || current.State == domain.AccessSessionActive
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_access_sessions SET state = 'expired',
			launch_token_key_id = '', launch_token_hash = '', launch_expires_at_us = 0,
			lease_key_id = '', lease_hash = '', lease_expires_at_us = 0, closure_required = ?,
			resource_version = resource_version + 1, updated_at_us = `+s.nowMicros()+`
			WHERE access_session_id = ?`), quotaBoolean(closure), current.ID); err != nil {
			return current, fmt.Errorf("expire access session: %w", err)
		}
		current, err = s.getAccessSessionTx(ctx, tx, current.ID, false)
		if err != nil {
			return current, err
		}
		if err := s.appendAccessGovernanceTx(ctx, tx, current, ids, "access.session.expire", "access-session.expired", application.AuditSucceeded, "expired"); err != nil {
			return current, err
		}
		changed = true
		return current, nil
	})
	return record, changed, err
}

func validateAccessExchange(request controlplane.AccessExchangeRequest) error {
	if err := validateAccessCallbackRequest(request.ID, request.Identity, request.TokenHashes, request.NewLease, request.LeaseDuration); err != nil {
		return err
	}
	return validateGovernanceRecordIDs(request.GovernanceRecordIDs)
}

func validateAccessRecheck(request controlplane.AccessLeaseRecheckRequest) error {
	if err := validateAccessCallbackRequest(request.ID, request.Identity, request.LeaseHashes, request.NewLease, request.LeaseDuration); err != nil {
		return err
	}
	return validateGovernanceRecordIDs(request.GovernanceRecordIDs)
}

func validateAccessCallbackRequest(id domain.AccessSessionID, identity application.AccessCallbackIdentity, hashes []application.KeyedTokenHash, next application.KeyedTokenHash, duration time.Duration) error {
	if _, err := domain.NewAccessSessionID(string(id)); err != nil {
		return err
	}
	if !accessValuePattern.MatchString(identity.PluginID) || !accessValuePattern.MatchString(identity.PluginInstanceID) ||
		!accessValuePattern.MatchString(identity.Audience) || identity.ProtocolMajor != 1 || identity.ProtocolMinor < controlplane.AccessProtocolMinor ||
		len(hashes) == 0 || len(hashes) > 3 || !accessValuePattern.MatchString(next.KeyID) || len(next.Digest) != sha256.Size*2 ||
		duration <= 0 || duration > controlplane.DefaultAccessLeaseDuration {
		return fmt.Errorf("access callback request is invalid")
	}
	for _, hash := range hashes {
		if !accessValuePattern.MatchString(hash.KeyID) || len(hash.Digest) != sha256.Size*2 {
			return fmt.Errorf("access callback token hash is invalid")
		}
	}
	return nil
}

func validateEndAccessLease(request controlplane.EndAccessLeaseRequest) error {
	if _, err := domain.NewAccessSessionID(string(request.ID)); err != nil {
		return err
	}
	if !accessValuePattern.MatchString(request.Identity.PluginID) || !accessValuePattern.MatchString(request.Identity.PluginInstanceID) ||
		!accessValuePattern.MatchString(request.Identity.Audience) || request.Identity.ProtocolMajor != 1 || request.Identity.ProtocolMinor < controlplane.AccessProtocolMinor ||
		len(request.LeaseHashes) == 0 || len(request.LeaseHashes) > 3 {
		return fmt.Errorf("access lease end request is invalid")
	}
	for _, hash := range request.LeaseHashes {
		if !accessValuePattern.MatchString(hash.KeyID) || len(hash.Digest) != sha256.Size*2 {
			return fmt.Errorf("access lease hash is invalid")
		}
	}
	return validateGovernanceRecordIDs(request.GovernanceRecordIDs)
}

func validateRevokeAccessSession(request controlplane.RevokeAccessSessionRequest) error {
	if err := validateGovernanceRecordIDs(request.GovernanceRecordIDs); err != nil {
		return err
	}
	if _, err := domain.NewAccessSessionID(string(request.ID)); err != nil {
		return err
	}
	if _, err := domain.NewUserID(string(request.ActorID)); err != nil {
		return err
	}
	if request.ExpectedResourceVersion == 0 {
		return fmt.Errorf("access session resource version is required")
	}
	return validateTemplateIdempotency(request.IdempotencyKey, request.RequestDigest)
}

func accessCallbackMatches(record controlplane.AccessSessionRecord, identity application.AccessCallbackIdentity) bool {
	return record.AccessPluginID == identity.PluginID && record.AccessPluginInstance == identity.PluginInstanceID &&
		record.Audience == identity.Audience && identity.ProtocolMajor == 1 && identity.ProtocolMinor >= controlplane.AccessProtocolMinor
}

func (s *Store) accessCurrentlyAuthorizedTx(ctx context.Context, tx *sql.Tx, record controlplane.AccessSessionRecord, now time.Time) (bool, error) {
	instance, digest, err := s.loadAccessResourceTx(ctx, tx, record.InstanceID, record.ResourceID, true)
	if errors.Is(err, controlplane.ErrAuthorityChanged) || errors.Is(err, controlplane.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if digest != record.ProviderStateDigest || instance.Generation != record.ResourceGeneration || instance.ProviderTargetID != record.TargetID || !accessResourceUsable(instance) {
		return false, nil
	}
	if err := s.requireInstanceOwnerAuthorizationTx(ctx, tx, instance, record.SubjectID, now); err != nil {
		if errors.Is(err, controlplane.ErrAuthorityChanged) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *Store) denyAccessTx(ctx context.Context, tx *sql.Tx, record *controlplane.AccessSessionRecord, state domain.AccessSessionState, closure bool) error {
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_access_sessions SET state = ?,
		launch_token_key_id = '', launch_token_hash = '', launch_expires_at_us = 0,
		lease_key_id = '', lease_hash = '', lease_expires_at_us = 0, closure_required = ?,
		resource_version = resource_version + 1, updated_at_us = `+s.nowMicros()+` WHERE access_session_id = ?`),
		state, quotaBoolean(closure), record.ID); err != nil {
		return err
	}
	updated, err := s.getAccessSessionTx(ctx, tx, record.ID, false)
	if err == nil {
		*record = updated
	}
	return err
}

func (s *Store) mutateAccessSession(ctx context.Context, mutate func(*sql.Tx) (controlplane.AccessSessionRecord, error)) (controlplane.AccessSessionRecord, error) {
	for attempt := 0; ; attempt++ {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return controlplane.AccessSessionRecord{}, err
		}
		record, mutateErr := mutate(tx)
		if mutateErr == nil {
			mutateErr = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if !isRetryableTransactionConflict(mutateErr) || attempt == 7 {
			return record, mutateErr
		}
		if err := waitTransactionRetry(ctx, attempt); err != nil {
			return controlplane.AccessSessionRecord{}, err
		}
	}
}

func validateBeginAccessSession(request controlplane.BeginAccessSessionRequest) error {
	if err := validateGovernanceRecordIDs(request.GovernanceRecordIDs); err != nil {
		return err
	}
	if _, err := domain.NewAccessSessionID(string(request.ID)); err != nil {
		return err
	}
	if _, err := domain.NewUserID(string(request.SubjectID)); err != nil {
		return err
	}
	if _, err := domain.NewInstanceID(string(request.InstanceID)); err != nil {
		return err
	}
	if _, err := domain.NewLogicalResourceID(string(request.ResourceID)); err != nil {
		return err
	}
	if request.Method != "vnc" && request.Method != "ssh" {
		return fmt.Errorf("unsupported access method")
	}
	for _, value := range []string{request.AccessPluginID, request.AccessPluginVersion, request.AccessPluginInstance, request.AccessKind, request.Audience} {
		if !accessValuePattern.MatchString(value) {
			return fmt.Errorf("access plugin binding is invalid")
		}
	}
	if request.ProviderStateDigest == "" || request.ResourceGeneration == 0 {
		return fmt.Errorf("access resource binding is invalid")
	}
	if request.RequestedDuration < 0 || request.RequestedDuration > controlplane.MaximumAccessSessionDuration {
		return fmt.Errorf("access session duration is invalid")
	}
	return validateTemplateIdempotency(request.IdempotencyKey, request.RequestDigest)
}

func validateFinalizeAccessSession(request controlplane.FinalizeAccessSessionRequest) error {
	if err := validateGovernanceRecordIDs(request.GovernanceRecordIDs); err != nil {
		return err
	}
	if _, err := domain.NewAccessSessionID(string(request.ID)); err != nil {
		return err
	}
	if _, err := domain.NewUserID(string(request.SubjectID)); err != nil {
		return err
	}
	if !accessValuePattern.MatchString(request.EndpointSchemaName) || request.EndpointSchemaMajor == 0 || request.EndpointDigest == "" || request.ProviderStateDigest == "" ||
		!accessValuePattern.MatchString(request.LaunchToken.KeyID) || len(request.LaunchToken.Digest) != sha256.Size*2 ||
		!accessValuePattern.MatchString(request.AccessStateSchema) || request.AccessStateMajor == 0 || request.AccessStateDigest == "" || len(request.AccessState) > maximumAccessStateBytes {
		return fmt.Errorf("access session finalization is invalid")
	}
	return nil
}

func accessResourceUsable(instance controlplane.InstanceRecord) bool {
	return (instance.DesiredLifecycle == domain.LifecycleRunning || instance.DesiredLifecycle == domain.LifecyclePaused) &&
		(instance.Phase == domain.InstanceReady || instance.Phase == domain.InstancePaused)
}

func safeAccessReason(reason string) string {
	if reason == "" {
		return ""
	}
	switch reason {
	case "provider_unavailable", "access_plugin_unavailable", "protocol_invalid", "authorization_denied", "expired", "revoked", "client_closed", "upstream_closed", "core_unreachable":
		return reason
	default:
		return "access_failed"
	}
}

func (s *Store) loadAccessResourceTx(ctx context.Context, tx *sql.Tx, instanceID domain.InstanceID, resourceID domain.LogicalResourceID, lock bool) (controlplane.InstanceRecord, domain.SHA256Digest, error) {
	instanceQuery := instanceRecordSelect + ` WHERE instance_id = ?`
	if lock && s.dialect == jobsdb.DialectPostgreSQL {
		instanceQuery += ` FOR UPDATE`
	}
	instance, err := scanInstance(tx.QueryRowContext(ctx, s.bind(instanceQuery), instanceID))
	if err != nil {
		return controlplane.InstanceRecord{}, "", err
	}
	resourceQuery := `SELECT kind, phase, provider_state FROM labcentral_local_resources WHERE instance_id = ? AND resource_id = ?`
	if lock && s.dialect == jobsdb.DialectPostgreSQL {
		resourceQuery += ` FOR UPDATE`
	}
	var kind, phase string
	var providerState []byte
	if err := tx.QueryRowContext(ctx, s.bind(resourceQuery), instanceID, resourceID).Scan(&kind, &phase, &providerState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return controlplane.InstanceRecord{}, "", controlplane.ErrNotFound
		}
		return controlplane.InstanceRecord{}, "", err
	}
	if kind != string(domain.ResourceVM) || (phase != string(domain.ResourceReady) && phase != string(domain.ResourcePaused)) || len(providerState) == 0 {
		return controlplane.InstanceRecord{}, "", controlplane.ErrAuthorityChanged
	}
	state := &pluginv1.ProviderState{}
	if err := proto.Unmarshal(providerState, state); err != nil || len(state.GetDigest()) != sha256.Size {
		return controlplane.InstanceRecord{}, "", controlplane.ErrAuthorityChanged
	}
	parsed, _ := domain.ParseSHA256Digest(hex.EncodeToString(state.GetDigest()))
	return instance, parsed, nil
}

type accessSessionQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

const accessSessionSelect = `SELECT access_session_id, subject_id, organization_id, project_id, instance_id, resource_id,
	method, target_id, access_plugin_id, access_plugin_version, access_plugin_instance_id, access_kind, audience,
	endpoint_schema_name, endpoint_schema_major, endpoint_schema_minor, endpoint_digest,
	provider_state_digest, resource_generation, state, launch_token_key_id, launch_token_hash, launch_expires_at_us,
	lease_key_id, lease_hash, lease_expires_at_us, last_authorized_at_us, expires_at_us,
	revoked_at_us, revoked_by, revocation_reason, access_state_schema, access_state_major, access_state_minor,
	access_state_digest, access_state, closure_required, resource_version, created_at_us, updated_at_us
	FROM labcentral_access_sessions`

func (s *Store) getAccessSessionTx(ctx context.Context, tx *sql.Tx, id domain.AccessSessionID, lock bool) (controlplane.AccessSessionRecord, error) {
	return s.getAccessSession(ctx, tx, id, lock)
}

func (s *Store) getAccessSession(ctx context.Context, queryer accessSessionQueryer, id domain.AccessSessionID, lock bool) (controlplane.AccessSessionRecord, error) {
	query := accessSessionSelect + ` WHERE access_session_id = ?`
	if lock && s.dialect == jobsdb.DialectPostgreSQL {
		query += ` FOR UPDATE`
	}
	var record controlplane.AccessSessionRecord
	var endpointDigest, providerDigest, accessStateDigest, state string
	var endpointMajor, endpointMinor, generation, launchExpires, leaseExpires, lastAuthorized, expires, revokedAt int64
	var accessStateMajor, accessStateMinor, closure, version, createdAt, updatedAt int64
	err := queryer.QueryRowContext(ctx, s.bind(query), id).Scan(
		&record.ID, &record.SubjectID, &record.OrganizationID, &record.ProjectID, &record.InstanceID, &record.ResourceID,
		&record.Method, &record.TargetID, &record.AccessPluginID, &record.AccessPluginVersion, &record.AccessPluginInstance,
		&record.AccessKind, &record.Audience, &record.EndpointSchemaName, &endpointMajor, &endpointMinor, &endpointDigest,
		&providerDigest, &generation, &state, &record.LaunchTokenKeyID, &record.LaunchTokenHash, &launchExpires,
		&record.LeaseKeyID, &record.LeaseHash, &leaseExpires, &lastAuthorized, &expires, &revokedAt, &record.RevokedBy,
		&record.RevocationReason, &record.AccessStateSchema, &accessStateMajor, &accessStateMinor, &accessStateDigest,
		&record.AccessState, &closure, &version, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return record, controlplane.ErrNotFound
	}
	if err != nil {
		return record, err
	}
	record.State = domain.AccessSessionState(state)
	record.EndpointSchemaMajor, record.EndpointSchemaMinor = uint32(endpointMajor), uint32(endpointMinor)
	record.ResourceGeneration = domain.Generation(generation)
	record.AccessStateMajor, record.AccessStateMinor = uint32(accessStateMajor), uint32(accessStateMinor)
	record.ClosureRequired = closure != 0
	record.ResourceVersion = domain.ResourceVersion(version)
	record.LaunchExpiresAt = accessTime(launchExpires)
	record.LeaseExpiresAt = accessTime(leaseExpires)
	record.LastAuthorizedAt = accessTime(lastAuthorized)
	record.ExpiresAt = accessTime(expires)
	record.RevokedAt = accessTime(revokedAt)
	record.CreatedAt = accessTime(createdAt)
	record.UpdatedAt = accessTime(updatedAt)
	var parseErr error
	if endpointDigest != "" {
		record.EndpointDigest, parseErr = domain.ParseSHA256Digest(endpointDigest)
	}
	if parseErr == nil {
		record.ProviderStateDigest, parseErr = domain.ParseSHA256Digest(providerDigest)
	}
	if parseErr == nil && accessStateDigest != "" {
		record.AccessStateDigest, parseErr = domain.ParseSHA256Digest(accessStateDigest)
	}
	if parseErr != nil || record.ExpiresAt.IsZero() || record.ResourceVersion == 0 {
		return controlplane.AccessSessionRecord{}, fmt.Errorf("stored access session is invalid")
	}
	return record, nil
}

func accessTime(micros int64) time.Time {
	if micros <= 0 {
		return time.Time{}
	}
	return time.UnixMicro(micros).UTC()
}

type accessGovernancePayload struct {
	SessionID domain.AccessSessionID    `json:"accessSessionId"`
	Instance  domain.InstanceID         `json:"instanceId"`
	Resource  domain.LogicalResourceID  `json:"resourceId"`
	Method    string                    `json:"method"`
	State     domain.AccessSessionState `json:"state"`
	Reason    string                    `json:"reason,omitempty"`
}

func (s *Store) appendAccessGovernanceTx(ctx context.Context, tx *sql.Tx, record controlplane.AccessSessionRecord, ids controlplane.GovernanceRecordIDs, action, eventType string, outcome application.AuditOutcome, reason string) error {
	if err := validateGovernanceRecordIDs(ids); err != nil {
		return err
	}
	payload, err := json.Marshal(accessGovernancePayload{SessionID: record.ID, Instance: record.InstanceID, Resource: record.ResourceID, Method: record.Method, State: record.State, Reason: safeAccessReason(reason)})
	if err != nil {
		return err
	}
	redacted, err := application.NewRedactedAuditPayload(payload)
	if err != nil {
		return err
	}
	safe, err := application.NewSafeEventPayload(payload)
	if err != nil {
		return err
	}
	scope := application.RecordScope{OrganizationID: record.OrganizationID, ProjectID: record.ProjectID}
	actor := application.RecordActor{Kind: application.RecordActorUser, ID: string(record.SubjectID)}
	if record.RevokedBy != "" {
		actor.ID = string(record.RevokedBy)
	}
	if _, err := s.appendAuditRecordTx(ctx, tx, application.AppendAuditRecord{
		ID: ids.AuditID, SchemaVersion: "audit.labcentral.io/v1", Category: application.AuditAccess,
		Action: action, Outcome: outcome, Scope: scope, Actor: actor, AggregateType: "access-session",
		AggregateID: string(record.ID), RedactedPayload: redacted,
	}); err != nil {
		return fmt.Errorf("append access audit: %w", err)
	}
	if _, err := s.appendEventRecordTx(ctx, tx, application.AppendEventRecord{
		ID: ids.EventID, SchemaVersion: "event.labcentral.io/v1", Type: eventType, Scope: scope, Actor: actor,
		AggregateType: "access-session", AggregateID: string(record.ID), SafePayload: safe,
	}); err != nil {
		return fmt.Errorf("append access event: %w", err)
	}
	return nil
}

func keyedHashMatches(keyID, digest string, candidates []application.KeyedTokenHash) bool {
	matched := 0
	for _, candidate := range candidates {
		keyMatch := subtle.ConstantTimeCompare([]byte(keyID), []byte(candidate.KeyID))
		digestMatch := subtle.ConstantTimeCompare([]byte(digest), []byte(candidate.Digest))
		matched |= keyMatch & digestMatch
	}
	return matched == 1
}
