package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func (s *Service) projectGroupStore(ctx context.Context, actor domain.UserID, project domain.ProjectID, action app.PermissionAction) (app.ProjectGroupStore, error) {
	if _, err := domain.NewProjectID(string(project)); err != nil {
		return nil, err
	}
	if err := s.authorizeExtension(ctx, actor, project, action); err != nil {
		return nil, err
	}
	store, ok := s.store.(app.ProjectGroupStore)
	if !ok {
		return nil, ErrAccessUnavailable
	}
	return store, nil
}
func (s *Service) ListProjectGroups(ctx context.Context, actor domain.UserID, project domain.ProjectID, after string, limit uint32) (app.ProjectGroupPage, error) {
	store, err := s.projectGroupStore(ctx, actor, project, app.ActionTemplateRead)
	if err != nil {
		return app.ProjectGroupPage{}, err
	}
	return store.ListProjectGroups(ctx, project, actor, after, limit)
}
func (s *Service) CreateProjectGroups(ctx context.Context, actor domain.UserID, project domain.ProjectID, names []string, open bool, key string) ([]app.ProjectGroup, error) {
	store, err := s.projectGroupStore(ctx, actor, project, app.ActionTeamManage)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 || len(names) > app.MaximumGroupBatch {
		return nil, fmt.Errorf("group batch must contain 1-100 names")
	}
	r := app.CreateProjectGroups{ActorID: actor, ProjectID: project, EnrollmentOpen: open, IdempotencyKey: key}
	seen := map[string]bool{}
	for _, name := range names {
		if !app.ValidWorkspaceName(name) || seen[name] {
			return nil, fmt.Errorf("invalid group name")
		}
		seen[name] = true
		id, err := s.ids.New("team")
		if err != nil {
			return nil, err
		}
		r.Groups = append(r.Groups, app.ProjectGroupSeed{ID: domain.TeamID(id), Name: name})
	}
	raw, _ := json.Marshal(struct {
		Project domain.ProjectID
		Names   []string
		Open    bool
	}{project, names, open})
	r.RequestDigest = digestBytes("projectGroup.create/v1", raw)
	return store.CreateProjectGroups(ctx, r)
}
func (s *Service) ChangeProjectGroupEnrollment(ctx context.Context, actor domain.UserID, project domain.ProjectID, group domain.TeamID, open bool, version domain.ResourceVersion, key string) (app.ProjectGroup, error) {
	store, err := s.projectGroupStore(ctx, actor, project, app.ActionTeamManage)
	if err != nil {
		return app.ProjectGroup{}, err
	}
	raw, _ := json.Marshal(struct {
		Project domain.ProjectID
		Group   domain.TeamID
		Open    bool
		Version domain.ResourceVersion
	}{project, group, open, version})
	return store.ChangeProjectGroupEnrollment(ctx, app.ChangeProjectGroupEnrollment{ActorID: actor, ProjectID: project, GroupID: group, Open: open, ExpectedVersion: version, IdempotencyKey: key, RequestDigest: digestBytes("projectGroup.enrollment/v1", raw)})
}
func (s *Service) ChangeProjectGroupMembership(ctx context.Context, actor domain.UserID, project domain.ProjectID, group domain.TeamID, join bool, key string) (app.ProjectGroup, error) {
	store, err := s.projectGroupStore(ctx, actor, project, app.ActionTemplateRead)
	if err != nil {
		return app.ProjectGroup{}, err
	}
	raw, _ := json.Marshal(struct {
		Project domain.ProjectID
		Group   domain.TeamID
		Join    bool
	}{project, group, join})
	return store.ChangeProjectGroupMembership(ctx, app.ChangeProjectGroupMembership{ActorID: actor, ProjectID: project, GroupID: group, Join: join, IdempotencyKey: key, RequestDigest: digestBytes("projectGroup.member/v1", raw)})
}

func (s *Service) ListProjectGroupMembers(ctx context.Context, actor domain.UserID, project domain.ProjectID, group domain.TeamID, after string, limit uint32) (app.ProjectGroupMemberPage, error) {
	if _, err := s.projectGroupStore(ctx, actor, project, app.ActionTemplateRead); err != nil {
		return app.ProjectGroupMemberPage{}, err
	}
	store, ok := s.store.(app.ProjectGroupMemberStore)
	if !ok {
		return app.ProjectGroupMemberPage{}, ErrAccessUnavailable
	}
	return store.ListProjectGroupMembers(ctx, project, group, actor, after, limit)
}

func (s *Service) DeleteProjectGroup(ctx context.Context, actor domain.UserID, project domain.ProjectID, group domain.TeamID, version domain.ResourceVersion, key string) error {
	store, err := s.projectGroupStore(ctx, actor, project, app.ActionTeamManage)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(struct {
		Project domain.ProjectID
		Group   domain.TeamID
		Version domain.ResourceVersion
	}{project, group, version})
	return store.DeleteProjectGroup(ctx, app.DeleteProjectGroup{ProjectID: project, GroupID: group, ActorID: actor, ExpectedVersion: version, IdempotencyKey: key, RequestDigest: digestBytes("projectGroup.delete/v1", raw)})
}

func (s *Service) InstancePresentation(ctx context.Context, id domain.InstanceID) app.InstancePresentation {
	if store, ok := s.store.(interface {
		InstancePresentation(context.Context, domain.InstanceID) (app.InstancePresentation, error)
	}); ok {
		if out, err := store.InstancePresentation(ctx, id); err == nil {
			return out
		}
	}
	return app.InstancePresentation{}
}
