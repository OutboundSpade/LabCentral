package localdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/authorization"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func TestSQLiteIdentityRBACAndSessionConformance(t *testing.T) {
	db, err := sql.Open("sqlite", "file:identity-rbac?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	runIdentityConformance(t, store)
}

func TestPostgreSQLIdentityRBACAndSessionConformance(t *testing.T) {
	dsn := os.Getenv("LABCENTRAL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LABCENTRAL_TEST_POSTGRES_DSN is not set")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("LABCENTRAL_TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("labcentral_identity_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := admin.ExecContext(context.Background(), `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.ExecContext(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`) }()
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgreSQL(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	runIdentityConformance(t, store)
}

func runIdentityConformance(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.EnsureAuthenticationMode(ctx, "local"); err != nil {
		t.Fatal(err)
	}
	if err := store.RefuseAuthenticationDowngrade(ctx); err == nil {
		t.Fatal("authenticated installation accepted a no-auth downgrade")
	}
	if err := store.EnsureAuthenticationMode(ctx, "local"); err != nil {
		t.Fatalf("same authentication mode was not idempotent: %v", err)
	}
	if err := store.EnsureAuthenticationMode(ctx, "oidc"); err == nil {
		t.Fatal("mixed authentication mode was accepted")
	}
	for _, user := range []application.UserRecord{
		{ID: "user.alice", Username: "alice", DisplayName: "Alice", PasswordHash: "$argon2id$test"},
		{ID: "user.bob", Username: "bob", DisplayName: "Bob", PasswordHash: "$argon2id$test"},
		{ID: "user.disabled", Username: "disabled", DisplayName: "Disabled", PasswordHash: "$argon2id$test", Disabled: true},
	} {
		if err := store.PutUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	for _, organization := range []application.OrganizationRecord{{ID: "org.alpha", Name: "alpha"}, {ID: "org.beta", Name: "beta"}} {
		if err := store.PutOrganization(ctx, organization); err != nil {
			t.Fatal(err)
		}
	}
	updateStart := make(chan struct{})
	updateResults := make(chan error, 2)
	for _, name := range []string{"beta-one", "beta-two"} {
		name := name
		go func() {
			<-updateStart
			_, updateErr := store.UpdateOrganization(ctx, application.OrganizationRecord{ID: "org.beta", Name: name}, 1)
			updateResults <- updateErr
		}()
	}
	close(updateStart)
	updateSuccesses, updateConflicts := 0, 0
	for range 2 {
		switch err := <-updateResults; {
		case err == nil:
			updateSuccesses++
		case errors.Is(err, application.ErrAuthVersionConflict):
			updateConflicts++
		default:
			t.Fatalf("concurrent organization update: %v", err)
		}
	}
	if updateSuccesses != 1 || updateConflicts != 1 {
		t.Fatalf("concurrent organization updates successes=%d conflicts=%d", updateSuccesses, updateConflicts)
	}
	for _, project := range []application.ProjectRecord{
		{ID: "project.alpha", OrganizationID: "org.alpha", Name: "alpha-class"},
		{ID: "project.beta", OrganizationID: "org.beta", Name: "beta-class"},
	} {
		if err := store.PutProject(ctx, project); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutTeam(ctx, application.TeamRecord{ID: "team.alpha", OrganizationID: "org.alpha", Name: "red-team"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTeam(ctx, application.TeamRecord{ID: "team.beta", OrganizationID: "org.beta", Name: "blue-team"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutProject(ctx, application.ProjectRecord{ID: "project.alpha", OrganizationID: "org.beta", Name: "stolen-project"}); err == nil {
		t.Fatal("cross-organization project identity update was accepted")
	}
	if scope, err := store.ResolveScope(ctx, application.ScopeProject, "project.alpha"); err != nil || scope.OrganizationID != "org.alpha" {
		t.Fatalf("project organization changed: %#v, %v", scope, err)
	}
	if err := store.PutTeam(ctx, application.TeamRecord{ID: "team.alpha", OrganizationID: "org.beta", Name: "stolen-team"}); err == nil {
		t.Fatal("cross-organization team identity update was accepted")
	}
	if scope, err := store.ResolveScope(ctx, application.ScopeTeam, "team.alpha"); err != nil || scope.OrganizationID != "org.alpha" {
		t.Fatalf("team organization changed: %#v, %v", scope, err)
	}
	for _, membership := range []application.MembershipRecord{
		{ID: "membership.alice.org", UserID: "user.alice", OrganizationID: "org.alpha", ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour)},
		{ID: "membership.alice.team", UserID: "user.alice", OrganizationID: "org.alpha", TeamID: "team.alpha", ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour)},
		{ID: "membership.bob.org", UserID: "user.bob", OrganizationID: "org.beta", ValidFrom: now.Add(-time.Hour)},
	} {
		if err := store.PutMembership(ctx, membership); err != nil {
			t.Fatal(err)
		}
	}
	for _, assignment := range []application.RoleAssignmentRecord{
		{ID: "role.alice", SubjectKind: application.SubjectUser, SubjectID: "user.alice", ScopeKind: application.ScopeProject, ScopeID: "project.alpha", Role: application.RoleParticipant, ValidFrom: now.Add(-time.Hour)},
		{ID: "role.alice.instructor", SubjectKind: application.SubjectUser, SubjectID: "user.alice", ScopeKind: application.ScopeProject, ScopeID: "project.alpha", Role: application.RoleInstructor, ValidFrom: now.Add(-time.Hour)},
		{ID: "role.bob", SubjectKind: application.SubjectUser, SubjectID: "user.bob", ScopeKind: application.ScopeProject, ScopeID: "project.beta", Role: application.RoleViewer, ValidFrom: now.Add(-time.Hour)},
		{ID: "role.disabled", SubjectKind: application.SubjectUser, SubjectID: "user.disabled", ScopeKind: application.ScopePlatform, ScopeID: "platform", Role: application.RolePlatformAdministrator, ValidFrom: now.Add(-time.Hour)},
	} {
		if err := store.PutRoleAssignment(ctx, assignment); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutTargetGrant(ctx, application.TargetGrantRecord{
		ID: "grant.alpha", TargetID: "target.alpha", ScopeKind: application.ScopeProject, ScopeID: "project.alpha", ValidFrom: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	evaluator, err := authorization.New(store)
	if err != nil {
		t.Fatal(err)
	}
	assertAuthorized := func(name string, request application.AuthorizationRequest, allowed bool) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			if request.Now.IsZero() {
				request.Now = now
			}
			err := evaluator.Authorize(ctx, request)
			if allowed && err != nil {
				t.Fatalf("authorized request denied: %v", err)
			}
			if !allowed && !errors.Is(err, authorization.ErrDenied) {
				t.Fatalf("denied request returned %v", err)
			}
		})
	}
	base := application.AuthorizationRequest{SubjectID: "user.alice", ScopeKind: application.ScopeProject, ScopeID: "project.alpha"}
	create := base
	create.Action, create.TargetID, create.OwnerKind, create.OwnerID = application.ActionInstanceCreate, "target.alpha", application.OwnerUser, "user.alice"
	assertAuthorized("granted project create", create, true)
	wrongTarget := create
	wrongTarget.TargetID = "target.ungranted"
	assertAuthorized("ungranted target", wrongTarget, false)
	missingTarget := create
	missingTarget.TargetID = ""
	assertAuthorized("missing target", missingTarget, false)
	grantedHealth := base
	grantedHealth.Action, grantedHealth.TargetID = application.ActionProviderTargetHealthRead, "target.alpha"
	assertAuthorized("granted target health", grantedHealth, true)
	ungrantedHealth := grantedHealth
	ungrantedHealth.TargetID = "target.ungranted"
	assertAuthorized("ungranted target health", ungrantedHealth, false)
	crossOrganizationOwner := create
	crossOrganizationOwner.OwnerKind, crossOrganizationOwner.OwnerID = application.OwnerTeam, "team.beta"
	assertAuthorized("cross organization owner", crossOrganizationOwner, false)
	crossTenant := create
	crossTenant.ScopeID = "project.beta"
	assertAuthorized("cross tenant create", crossTenant, false)
	ownedRead := base
	ownedRead.Action, ownedRead.OwnerKind, ownedRead.OwnerID = application.ActionInstanceRead, application.OwnerUser, "user.alice"
	assertAuthorized("owned instance read", ownedRead, true)
	foreignRead := ownedRead
	foreignRead.OwnerID = "user.bob"
	assertAuthorized("instructor reads another participant instance", foreignRead, true)
	expiredMembershipRead := foreignRead
	expiredMembershipRead.Now = now.Add(2 * time.Hour)
	assertAuthorized("expired organization membership", expiredMembershipRead, false)
	teamRead := ownedRead
	teamRead.OwnerKind, teamRead.OwnerID = application.OwnerTeam, "team.alpha"
	assertAuthorized("team instance read", teamRead, true)
	bobCrossRead := foreignRead
	bobCrossRead.SubjectID = "user.bob"
	assertAuthorized("other organization hidden", bobCrossRead, false)
	disabled := create
	disabled.SubjectID = "user.disabled"
	assertAuthorized("disabled platform administrator", disabled, false)
	if err := store.PutUser(ctx, application.UserRecord{ID: "user.admin", Username: "admin", DisplayName: "Admin"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRoleAssignment(ctx, application.RoleAssignmentRecord{
		ID: "role.admin", SubjectKind: application.SubjectUser, SubjectID: "user.admin", ScopeKind: application.ScopePlatform,
		ScopeID: "platform", Role: application.RolePlatformAdministrator, ValidFrom: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	assertAuthorized("unknown action is closed", application.AuthorizationRequest{
		SubjectID: "user.admin", Action: "future.permission", ScopeKind: application.ScopePlatform, ScopeID: "platform",
	}, false)

	createAccessTestInstance(t, store, "instance.alpha-user", "project.alpha", application.OwnerUser, "user.alice", "user.alice")
	createAccessTestInstance(t, store, "instance.alpha-team", "project.alpha", application.OwnerTeam, "team.alpha", "user.alice")
	createAccessTestInstance(t, store, "instance.beta-user", "project.beta", application.OwnerUser, "user.bob", "user.bob")
	appendAuditConflict(t, store, "audit.instance.audit-conflict")
	if err := tryCreateAccessTestInstance(store, "instance.audit-conflict", "project.alpha", application.OwnerUser, "user.alice", "user.alice"); err == nil {
		t.Fatal("operation governance conflict did not abort instance admission")
	}
	if _, err := store.GetInstance(ctx, "instance.audit-conflict"); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("operation governance conflict retained instance: %v", err)
	}
	for _, invalid := range []struct {
		name      string
		projectID domain.ProjectID
		ownerKind application.OwnerKind
		ownerID   string
		creatorID domain.UserID
	}{
		{name: "cross-org user", projectID: "project.alpha", ownerKind: application.OwnerUser, ownerID: "user.bob", creatorID: "user.alice"},
		{name: "cross-org team", projectID: "project.alpha", ownerKind: application.OwnerTeam, ownerID: "team.beta", creatorID: "user.alice"},
		{name: "different project owner", projectID: "project.alpha", ownerKind: application.OwnerProject, ownerID: "project.beta", creatorID: "user.alice"},
		{name: "missing project", projectID: "project.missing", ownerKind: application.OwnerUser, ownerID: "user.alice", creatorID: "user.alice"},
	} {
		t.Run("transaction rejects "+invalid.name, func(t *testing.T) {
			if err := tryCreateAccessTestInstance(store, "instance.invalid-"+string(invalid.projectID)+"-"+string(invalid.ownerKind), invalid.projectID, invalid.ownerKind, invalid.ownerID, invalid.creatorID); err == nil {
				t.Fatal("invalid durable instance scope was accepted")
			}
		})
	}
	assertIDs := func(subject domain.UserID, want ...domain.InstanceID) {
		t.Helper()
		got, err := evaluator.AuthorizedInstanceIDs(ctx, subject, application.ActionInstanceRead)
		if err != nil {
			t.Fatal(err)
		}
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("authorized instance IDs for %s = %v, want %v", subject, got, want)
		}
	}
	assertIDs("user.alice", "instance.alpha-user", "instance.alpha-team")
	assertIDs("user.bob", "instance.beta-user")

	rateKey := strings.Repeat("a", 64)
	for attempt := 1; attempt <= 3; attempt++ {
		allowed, err := store.ConsumeAuthenticationAttempt(ctx, rateKey, now, time.Minute, 2)
		if err != nil || allowed != (attempt <= 2) {
			t.Fatalf("authentication attempt %d allowed=%t err=%v", attempt, allowed, err)
		}
	}
	allowed, err := store.ConsumeAuthenticationAttempt(ctx, rateKey, now.Add(24*time.Hour), time.Minute, 2)
	if err != nil || allowed {
		t.Fatalf("process clock skew reset authentication window allowed=%t err=%v", allowed, err)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_authentication_attempts SET window_started_at_us = 0 WHERE attempt_key = ?`), rateKey); err != nil {
		t.Fatal(err)
	}
	allowed, err = store.ConsumeAuthenticationAttempt(ctx, rateKey, now, time.Minute, 2)
	if err != nil || !allowed {
		t.Fatalf("database-expired authentication window allowed=%t err=%v", allowed, err)
	}
	if err := store.ClearAuthenticationAttempts(ctx, rateKey); err != nil {
		t.Fatal(err)
	}
	allowed, err = store.ConsumeAuthenticationAttempt(ctx, rateKey, now, time.Minute, 2)
	if err != nil || !allowed {
		t.Fatalf("cleared authentication window allowed=%t err=%v", allowed, err)
	}
	concurrentKey := strings.Repeat("b", 64)
	start := make(chan struct{})
	results := make(chan struct {
		allowed bool
		err     error
	}, 2)
	for range 2 {
		go func() {
			<-start
			value, consumeErr := store.ConsumeAuthenticationAttempt(ctx, concurrentKey, now, time.Minute, 1)
			results <- struct {
				allowed bool
				err     error
			}{value, consumeErr}
		}()
	}
	close(start)
	allowedCount := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent authentication attempt: %v", result.err)
		}
		if result.allowed {
			allowedCount++
		}
	}
	if allowedCount != 1 {
		t.Fatalf("concurrent authentication attempts allowed %d, want 1", allowedCount)
	}

	if err := store.LinkExternalIdentity(ctx, application.ExternalIdentityRecord{Issuer: "https://issuer.example", Subject: "subject-alice", UserID: "user.alice"}); err != nil {
		t.Fatal(err)
	}
	external, err := store.UserByExternalIdentity(ctx, "https://issuer.example", "subject-alice")
	if err != nil || external.ID != "user.alice" {
		t.Fatalf("external identity user = %#v, %v", external, err)
	}
	session := application.SessionRecord{
		ID: "session.old", AuditID: "audit.session-old", EventID: "event.session-old", UserID: "user.alice", Kind: application.SessionKindBrowser, TokenHash: "token-old", CSRFHash: "csrf-old", KeyID: "key-1", ExpiresAt: now.Add(time.Hour),
	}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if found, err := store.SessionByTokenHash(ctx, "token-old", now); err != nil || found.ID != session.ID {
		t.Fatalf("session lookup = %#v, %v", found, err)
	}
	replacement := application.SessionRecord{
		ID: "session.new", AuditID: "audit.session-new", EventID: "event.session-new", UserID: "user.alice", Kind: application.SessionKindBrowser, TokenHash: "token-new", CSRFHash: "csrf-new", KeyID: "key-2",
		ExpiresAt: now.Add(2 * time.Hour), RotatedFromID: session.ID,
	}
	if err := store.RotateSession(ctx, session.ID, replacement, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SessionByTokenHash(ctx, "token-old", now); !errors.Is(err, application.ErrAuthRecordNotFound) {
		t.Fatalf("rotated session remained valid: %v", err)
	}
	if err := store.RevokeSession(ctx, replacement.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SessionByTokenHash(ctx, "token-new", now); !errors.Is(err, application.ErrAuthRecordNotFound) {
		t.Fatalf("revoked session remained valid: %v", err)
	}
	appendAuditConflict(t, store, "audit.session-rollback")
	if err := store.CreateSession(ctx, application.SessionRecord{
		ID: "session.rollback", AuditID: "audit.session-rollback", EventID: "event.session-rollback",
		UserID: "user.alice", Kind: application.SessionKindBrowser, TokenHash: "token-rollback", CSRFHash: "csrf-rollback", KeyID: "key-1", ExpiresAt: now.Add(time.Hour),
	}); err == nil {
		t.Fatal("authentication governance conflict did not abort session creation")
	}
	if _, err := store.SessionByTokenHash(ctx, "token-rollback", now); !errors.Is(err, application.ErrAuthRecordNotFound) {
		t.Fatalf("authentication governance conflict retained session: %v", err)
	}

	actorContext, err := application.WithRecordActor(ctx, application.RecordActor{Kind: application.RecordActorUser, ID: "user.admin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateOrganization(actorContext, application.OrganizationRecord{ID: "org.audit", Name: "audit-org"}); err != nil {
		t.Fatal(err)
	}
	targetSnapshot := controlplane.TargetSnapshot{
		GovernanceRecordIDs: controlplane.GovernanceRecordIDs{AuditID: "audit.target-refresh", EventID: "event.target-refresh"},
		ID:                  "target.audit", Actor: application.RecordActor{Kind: application.RecordActorUser, ID: "user.admin"}, ProviderKind: "proxmox", ConfigFingerprint: "sha256:" + strings.Repeat("a", 64),
		ProtocolMajor: 1, ProtocolMinor: 2, Features: []string{"target-discovery-v1"}, DiscoveryJSON: []byte(`{"capabilities":[]}`),
		DiscoveredAt: now, ValidUntil: now.Add(time.Hour),
	}
	if err := store.SaveTargetSnapshot(actorContext, targetSnapshot); err != nil {
		t.Fatal(err)
	}
	failedTargetUpdate := targetSnapshot
	failedTargetUpdate.ProviderKind = "tampered"
	failedTargetUpdate.DiscoveryJSON = []byte(`{"capabilities":["tampered"]}`)
	if err := store.SaveTargetSnapshot(actorContext, failedTargetUpdate); err == nil {
		t.Fatal("target governance conflict did not abort snapshot update")
	}
	loadedTarget, err := store.GetTargetSnapshot(ctx, targetSnapshot.ID)
	if err != nil || loadedTarget.ProviderKind != targetSnapshot.ProviderKind || string(loadedTarget.DiscoveryJSON) != string(targetSnapshot.DiscoveryJSON) {
		t.Fatalf("target governance conflict changed snapshot: %#v, %v", loadedTarget, err)
	}
	for _, category := range []application.AuditCategory{
		application.AuditAuthentication, application.AuditAuthorization, application.AuditTarget,
		application.AuditOperation, application.AuditAdministrator,
	} {
		var count int
		if err := store.db.QueryRowContext(ctx, store.bind(`SELECT COUNT(*) FROM labcentral_audit_records WHERE category = ?`), category).Scan(&count); err != nil || count == 0 {
			t.Fatalf("generalized %s producer count=%d err=%v", category, count, err)
		}
	}
	rows, err := store.db.QueryContext(ctx, `SELECT payload_json FROM labcentral_audit_records`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(payload), "token-old") || strings.Contains(string(payload), "csrf-old") {
			t.Fatal("authentication secret appeared in audit payload")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func appendAuditConflict(t *testing.T, store *Store, id string) {
	t.Helper()
	audit, _, err := application.NewGovernanceRecords(application.GovernanceRecordInput{
		AuditID: id, Category: application.AuditAdministrator, Action: "fault-injection.prepare", Outcome: application.AuditSucceeded,
		EventType: "fault-injection.prepared", Actor: application.RecordActor{Kind: application.RecordActorSystem, ID: "system.test"},
		AggregateType: "fault-injection", AggregateID: id, Payload: struct {
			State string `json:"state"`
		}{"prepared"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendAuditRecord(context.Background(), audit); err != nil {
		t.Fatal(err)
	}
}

func createAccessTestInstance(t *testing.T, store *Store, instanceID string, projectID domain.ProjectID, ownerKind application.OwnerKind, ownerID string, creatorID domain.UserID) {
	t.Helper()
	if err := tryCreateAccessTestInstance(store, instanceID, projectID, ownerKind, ownerID, creatorID); err != nil {
		t.Fatal(err)
	}
}

func tryCreateAccessTestInstance(store *Store, instanceID string, projectID domain.ProjectID, ownerKind application.OwnerKind, ownerID string, creatorID domain.UserID) error {
	plan := []byte(`{"planned":true}`)
	canonical := []byte(`{"apiVersion":"labcentral.io/v1alpha1"}`)
	planSum, canonicalSum, requestSum := sha256.Sum256(plan), sha256.Sum256(canonical), sha256.Sum256([]byte(instanceID))
	_, err := store.CreateInstance(context.Background(), controlplane.CreateInstanceRequest{
		InstanceID:       domain.InstanceID(instanceID),
		ProjectID:        projectID,
		OwnerKind:        ownerKind,
		OwnerID:          ownerID,
		CreatedBy:        creatorID,
		ProviderTargetID: "target.alpha",
		OperationID:      domain.OperationID("operation." + instanceID),
		CanonicalDigest:  domain.SHA256Digest(fmt.Sprintf("%x", canonicalSum)),
		CanonicalJSON:    canonical,
		SourceYAML:       []byte("apiVersion: labcentral.io/v1alpha1\n"),
		Resources: []controlplane.ResourceSeed{{
			ID: "container.test", Kind: domain.ResourceContainer, CanonicalJSON: []byte(`{"kind":"Container"}`),
			ProviderPlan: plan, ProviderPlanDigest: domain.SHA256Digest(fmt.Sprintf("%x", planSum)), OwnershipToken: "ownership." + instanceID,
		}},
		Acknowledged:   true,
		SubjectID:      creatorID,
		IdempotencyKey: "idempotency." + instanceID,
		RequestDigest:  domain.SHA256Digest(fmt.Sprintf("%x", requestSum)),
		Payload:        []byte(`{"schemaVersion":1}`),
		AuditID:        "audit." + instanceID,
		EventID:        "event." + instanceID,
		Quota: application.InstanceQuotaRequest{
			ReservationID: domain.QuotaReservationID("quota." + instanceID),
			Amounts:       []application.QuotaAmount{{Dimension: application.QuotaInstances, Value: 1}},
		},
	})
	return err
}

var _ application.AuthRepository = (*Store)(nil)
