package authorization

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
)

type unavailableAuditWriter struct{}

var errAuditUnavailable = errors.New("audit writer unavailable")

func (unavailableAuditWriter) AppendGovernanceRecords(context.Context, application.AppendAuditRecord, application.AppendEventRecord) (application.AuditRecord, application.EventRecord, error) {
	return application.AuditRecord{}, application.EventRecord{}, errAuditUnavailable
}

func TestRoutineReadsDoNotDependOnAuditWriter(t *testing.T) {
	f := newAuthorizationFixture(t)
	f.addUserRole(roleFixture{role: application.RolePlatformAdministrator, subjectID: "user.platform-admin", scopeKind: application.ScopePlatform, scopeID: "platform"})
	f.evaluator.recorder = unavailableAuditWriter{}
	for _, action := range []application.PermissionAction{application.ActionInstanceRead, application.ActionTemplateRead, application.ActionProviderTargetHealthRead, application.ActionAuditRead, application.ActionAccessSessionRead} {
		err := f.evaluator.Authorize(context.Background(), f.request("user.platform-admin", action))
		if err != nil {
			t.Fatalf("routine read %s waited for audit writer: %v", action, err)
		}
	}
	for _, action := range []application.PermissionAction{application.ActionInstanceCreate, application.ActionInstanceDestroy, application.ActionAccessSessionCreate, application.ActionMembershipManage} {
		err := f.evaluator.Authorize(context.Background(), f.request("user.platform-admin", action))
		if !errors.Is(err, errAuditUnavailable) {
			t.Fatalf("sensitive action %s bypassed audit writer: %v", action, err)
		}
	}
	for _, decision := range []error{ErrDenied, errors.New("repository failure")} {
		err := f.evaluator.recordDecision(context.Background(), application.AuthorizationRequest{Action: application.ActionInstanceRead}, application.RecordScope{}, decision)
		if !errors.Is(err, errAuditUnavailable) {
			t.Fatalf("denied/failed read bypassed audit writer: %v", err)
		}
	}
}

func TestProjectCapabilityHintsAreReadOnlyButExecutionDenialsRemainAudited(t *testing.T) {
	f := newAuthorizationFixture(t)
	f.evaluator.now = func() time.Time { return f.now }
	f.addUserRole(roleFixture{role: application.RoleParticipant, subjectID: "user.participant", scopeKind: application.ScopeProject, scopeID: "project.alpha"})
	f.evaluator.recorder = unavailableAuditWriter{}
	actions, err := f.evaluator.ProjectCapabilities(context.Background(), "user.participant", "project.alpha")
	if err != nil || len(actions) != 1 || actions[0] != application.ActionTemplateRead {
		t.Fatalf("capability hint wrote an audit or exceeded role: %v %v", actions, err)
	}
	if err = f.evaluator.Authorize(context.Background(), f.request("user.participant", application.ActionQuotaManage)); !errors.Is(err, errAuditUnavailable) {
		t.Fatal("execution denial bypassed audit", err)
	}
	if _, err = f.evaluator.ProjectCapabilities(context.Background(), "user.participant", "project.sibling"); !errors.Is(err, errAuditUnavailable) {
		t.Fatal("unauthorized project read bypassed audit", err)
	}
}
