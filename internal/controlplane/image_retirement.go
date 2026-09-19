package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"time"
)

func (s *Service) requestImageRetirement(ctx context.Context, actor domain.UserID, project domain.ProjectID, id string) (bool, error) {
	b, err := s.store.(app.BuildStore).GetImageBinding(ctx, id)
	if err != nil {
		return false, err
	}
	session, err := s.targets.AcquireForSubject(ctx, b.TargetID, actor, app.InvocationScope{}, false)
	if err != nil {
		return false, err
	}
	defer session.Close()
	// Legacy providers retain the existing policy-only retirement behavior.
	if !session.Supports(0, "image-retirement-v1") {
		return false, nil
	}
	if session.Context.ConfigFingerprint != b.Binding.TargetConfigurationDigest || providerIdentity(session.Provider) != b.ProviderDigest {
		return false, fmt.Errorf("original image provider and target required for retirement")
	}
	key, err := s.ids.New("retirement")
	if err != nil {
		return false, err
	}
	_, err = s.newExtensionWork(ctx, actor, project, app.ExtensionWorkRequest{Version: 2, Kind: domain.WorkImageImport, PluginInstance: "core-import", PluginDigest: b.ProviderDigest, BindingID: string(b.TargetID), ConfigurationDigest: digestBytes("extension.configuration/v1", session.Context.Config), DedupKey: digestBytes("image.retirement/v1", []byte(id)).Hex(), Import: &app.ImportWork{RetireBindingID: id, TargetID: b.TargetID, TargetFingerprint: b.Binding.TargetConfigurationDigest}}, key)
	return true, err
}
func (s *Service) executeImageRetirement(ctx context.Context, c app.ExtensionWorkClaim) error {
	r := c.Work.Request.Import
	if c.Work.Request.Version != 2 || r == nil || r.RetireBindingID == "" {
		return fmt.Errorf("versioned retirement work required")
	}
	current, err := s.store.(app.BuildStore).GetExtensionWork(ctx, c.Work.ID)
	if err != nil {
		return err
	}
	if current.CancelRequested {
		return s.finishWork(ctx, c, domain.OperationCanceled, nil, "")
	}
	store, ok := s.store.(app.ImageRetirementStore)
	if !ok {
		return ErrAccessUnavailable
	}
	if err := s.operationsAuthorizer.Authorize(ctx, app.AuthorizationRequest{SubjectID: c.Work.ActorID, Action: app.ActionProviderTargetGrant, ScopeKind: app.ScopePlatform, ScopeID: "platform", TargetID: r.TargetID}); err != nil {
		return s.finishWork(ctx, c, domain.OperationCanceled, nil, "authority_changed")
	}
	ready, err := store.CanRetireImage(ctx, c, r.RetireBindingID)
	if err != nil {
		return err
	}
	if !ready {
		return s.yieldWorkProgress(ctx, c, struct {
			BindingID string `json:"bindingId"`
			Stage     string `json:"stage"`
		}{r.RetireBindingID, "waiting-for-labs"})
	}
	b, err := s.store.(app.BuildStore).GetImageBinding(ctx, r.RetireBindingID)
	if err != nil {
		return err
	}
	session, err := s.targets.AcquireForSubject(ctx, r.TargetID, c.Work.ActorID, app.InvocationScope{WorkSubject: &domain.WorkSubject{Kind: c.Work.Request.Kind, ID: string(c.Work.ID)}, InvocationID: c.Work.InvocationID, ExpiresAt: time.Now().Add(15 * time.Minute)}, true)
	if err != nil {
		return err
	}
	defer session.Close()
	provider, ok := session.Provider.(app.ImageRetirer)
	if !ok || !session.Supports(0, "image-retirement-v1") || providerIdentity(session.Provider) != c.Work.Request.PluginDigest || session.Context.ConfigFingerprint != r.TargetFingerprint {
		return ErrAccessUnavailable
	}
	call := workCall(c)
	call.TargetID = string(r.TargetID)
	state, err := provider.RetireImage(ctx, app.ImportRequest{Call: call, Target: session.Context}, b.Binding)
	if err != nil {
		return err
	}
	if state != "retired" {
		return s.store.(app.BuildStore).YieldExtensionWork(ctx, c)
	}
	if err = store.ReleaseImageImportClaims(ctx, c, r.RetireBindingID); err != nil {
		return err
	}
	return s.finishWork(ctx, c, domain.OperationSucceeded, struct {
		BindingID string `json:"bindingId"`
		State     string `json:"state"`
	}{b.ID, "retired"}, "")
}

func (s *Service) yieldWorkProgress(ctx context.Context, c app.ExtensionWorkClaim, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if progress, ok := s.store.(app.ExtensionWorkProgressStore); ok {
		return progress.YieldExtensionWorkProgress(ctx, c, raw)
	}
	return s.store.(app.BuildStore).YieldExtensionWork(ctx, c)
}
