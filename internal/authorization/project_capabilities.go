package authorization

import (
	"context"
	"errors"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

// ProjectCapabilities describes available UI actions. Missing permissions are
// hints, not failed attempts to execute an action, and must not write audits.
func (e *Evaluator) ProjectCapabilities(ctx context.Context, subject domain.UserID, project domain.ProjectID) ([]app.PermissionAction, error) {
	r := app.AuthorizationRequest{SubjectID: subject, ScopeKind: app.ScopeProject, ScopeID: string(project), Action: app.ActionTemplateRead}
	if err := e.Authorize(ctx, r); err != nil {
		return nil, err
	}
	result := []app.PermissionAction{app.ActionTemplateRead}
	for _, action := range []app.PermissionAction{app.ActionTemplateCreate, app.ActionQuotaManage, app.ActionProjectManage, app.ActionTeamManage} {
		r.Action = action
		if _, err := e.evaluate(ctx, r); err == nil {
			result = append(result, action)
		} else if !errors.Is(err, ErrDenied) {
			return nil, err
		}
	}
	snapshot, err := e.repository.AuthorizationSnapshot(ctx, subject, e.now().UTC())
	if err != nil {
		return nil, err
	}
	r.Action = app.ActionInstanceBatchCreate
	for _, grant := range snapshot.Grants {
		r.TargetID = grant.TargetID
		if _, err := e.evaluate(ctx, r); err == nil {
			result = append(result, r.Action)
			break
		} else if !errors.Is(err, ErrDenied) {
			return nil, err
		}
	}
	return result, nil
}
