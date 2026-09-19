package controlplane

import (
	"context"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"testing"
)

type workVisibilityPolicy struct {
	app.Authorizer
	publish, read, admin bool
}

func (p workVisibilityPolicy) Authorize(_ context.Context, r app.AuthorizationRequest) error {
	if r.ScopeKind == app.ScopeProject && r.ScopeID == "project.visible" {
		if r.Action == app.ActionTemplateRevisionPublish && p.publish || r.Action == app.ActionTemplateRead && p.read {
			return nil
		}
	}
	if r.ScopeKind == app.ScopePlatform && r.ScopeID == "platform" && r.Action == app.ActionProviderTargetGrant && p.admin {
		return nil
	}
	return ErrAuthorityChanged
}

type workVisibilityStore struct {
	Store
	app.ExtensionWorkStore
	project domain.ProjectID
}

func (s *workVisibilityStore) ListExtensionWork(_ context.Context, p domain.ProjectID) ([]app.ExtensionWork, error) {
	s.project = p
	return []app.ExtensionWork{{ProjectID: p}}, nil
}
func TestImageWorkVisibilityRequiresProjectAccessAndAdministrativeAuthority(t *testing.T) {
	for _, tc := range []struct {
		name    string
		policy  workVisibilityPolicy
		project domain.ProjectID
		allowed bool
	}{
		{"author", workVisibilityPolicy{publish: true}, "project.visible", true},
		{"image administrator with project access", workVisibilityPolicy{read: true, admin: true}, "project.visible", true},
		{"participant", workVisibilityPolicy{read: true}, "project.visible", false},
		{"administrator without project access", workVisibilityPolicy{admin: true}, "project.visible", false},
		{"other project", workVisibilityPolicy{read: true, admin: true}, "project.other", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &workVisibilityStore{}
			s := &Service{store: store, operationsAuthorizer: tc.policy}
			rows, err := s.ListExtensionWork(context.Background(), "user.test", tc.project)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v error=%v", tc.allowed, err)
			}
			if tc.allowed {
				if len(rows) != 1 || store.project != tc.project {
					t.Fatal("wrong project result")
				}
			} else if store.project != "" {
				t.Fatal("unauthorized store access")
			}
		})
	}
}
