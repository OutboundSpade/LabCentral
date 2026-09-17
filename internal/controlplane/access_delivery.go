package controlplane

import (
	"context"
	"fmt"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"regexp"
	"time"
)

var downloadFilename = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,100}$`)

func (s *Service) DownloadAccess(ctx context.Context, subject domain.UserID, id domain.AccessSessionID, token string) (application.AccessDownloaded, error) {
	if !s.AccessAvailable() || len(token) < 32 || len(token) > 256 {
		return application.AccessDownloaded{}, ErrAccessUnavailable
	}
	record, err := s.GetAccessSession(ctx, subject, id)
	if err != nil {
		return application.AccessDownloaded{}, err
	}
	binding, err := s.access.Get(record.AccessPluginInstance)
	if err != nil {
		return application.AccessDownloaded{}, err
	}
	permitted := false
	maximumOffline := time.Duration(0)
	for _, d := range binding.Descriptors {
		if d.Method == record.Method && d.Presentation == "credential-download" {
			permitted = true
			maximumOffline = time.Duration(d.MaximumOfflineSeconds) * time.Second
		}
	}
	adapter, ok := binding.Access.(application.AccessDelivery)
	store, storeOK := s.store.(application.AccessDeliveryStore)
	if !permitted || !ok || !storeOK {
		return application.AccessDownloaded{}, ErrAccessUnsupported
	}
	ids, err := s.newGovernanceRecordIDs()
	if err != nil {
		return application.AccessDownloaded{}, err
	}
	record, err = store.ClaimAccessDelivery(ctx, id, subject, s.tokenHasher.HashCandidates("access-launch", token), ids)
	if err != nil {
		return application.AccessDownloaded{}, err
	}
	request := application.AccessRevoke{SessionID: string(id), State: record.AccessState, Call: application.ProviderCall{InstanceID: string(record.InstanceID), ResourceID: string(record.ResourceID), TargetID: string(record.TargetID)}}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := adapter.Download(callCtx, application.AccessDownload{AccessRevoke: request, DeliveryID: string(id), Token: token})
	if err != nil {
		return application.AccessDownloaded{}, s.failAccessSession(ctx, record, "delivery_failed", ErrAccessUnavailable)
	}
	if len(result.Content) == 0 || len(result.Content) > 64<<10 || !downloadFilename.MatchString(result.Filename) || (result.MediaType != "application/octet-stream" && result.MediaType != "text/plain") {
		clear(result.Content)
		return application.AccessDownloaded{}, s.failAccessSession(ctx, record, "protocol_invalid", fmt.Errorf("invalid access download"))
	}
	current, err := s.GetAccessSession(ctx, subject, id)
	if err != nil || current.State != domain.AccessSessionActive || !current.LeaseExpiresAt.After(time.Now()) {
		clear(result.Content)
		return application.AccessDownloaded{}, s.failAccessSession(ctx, record, "authorization_denied", ErrAuthorityChanged)
	}
	status, err := adapter.Inspect(callCtx, request)
	if err != nil || status.Closed || !status.EnforcedUntil.After(time.Now()) || status.EnforcedUntil.After(current.LastAuthorizedAt.Add(maximumOffline)) || status.EnforcedUntil.After(current.ExpiresAt) {
		clear(result.Content)
		return application.AccessDownloaded{}, s.failAccessSession(ctx, record, "protocol_invalid", ErrAccessUnavailable)
	}
	return result, nil
}
