package authorization

import (
	"context"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"time"
)

// NewLocal uses the ordinary evaluator with a synthetic administrator snapshot.
// Only explicitly selected loopback development mode installs this adapter.
func NewLocal(repository application.AuthRepository, targets ...domain.ProviderTargetID) (*Evaluator, error) {
	return New(localRepository{AuthRepository: repository, targets: targets})
}

type localRepository struct {
	application.AuthRepository
	targets []domain.ProviderTargetID
}

func (r localRepository) AuthorizationSnapshot(ctx context.Context, id domain.UserID, now time.Time) (application.AuthorizationSnapshot, error) {
	if id != application.LocalAdminID {
		return application.AuthorizationSnapshot{}, ErrDenied
	}
	grants := []application.TargetGrantRecord{}
	for _, id := range r.targets {
		grants = append(grants, application.TargetGrantRecord{TargetID: id, ScopeKind: application.ScopePlatform, ScopeID: "platform"})
	}
	return application.AuthorizationSnapshot{Grants: grants, User: application.UserRecord{ID: id}, Assignments: []application.RoleAssignmentRecord{{Role: application.RolePlatformAdministrator, ScopeKind: application.ScopePlatform, ScopeID: "platform", SubjectKind: application.SubjectUser, SubjectID: string(id)}}}, nil
}

func (r localRepository) OwnerBelongsToScope(ctx context.Context, kind application.OwnerKind, id string, h application.ScopeHierarchy, now time.Time) (bool, error) {
	if kind == application.OwnerUser && id == string(application.LocalAdminID) && h.ProjectID == "project.local" {
		return true, nil
	}
	return r.AuthRepository.OwnerBelongsToScope(ctx, kind, id, h, now)
}
