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
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func TestSQLiteTemplateGovernancePersistence(t *testing.T) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:template-governance-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	runTemplateGovernancePersistence(t, store)
}

func TestPostgreSQLTemplateGovernancePersistence(t *testing.T) {
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
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("labcentral_templates_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := admin.ExecContext(context.Background(), `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`) })
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
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	runTemplateGovernancePersistence(t, store)
}

func runTemplateGovernancePersistence(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	var initialUsers int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM labcentral_users`).Scan(&initialUsers); err != nil || initialUsers != 0 {
		t.Fatalf("migration-created users = %d, %v", initialUsers, err)
	}
	if _, err := store.CreateLabTemplate(ctx, controlplane.CreateLabTemplateRequest{
		GovernanceRecordIDs: governanceIDs("local-create"), ID: "template.local", ProjectID: "project.local", Name: "local-lab", CreatedBy: controlplane.LocalAdminID,
		IdempotencyKey: "idem.local-create", RequestDigest: testRequestDigest("local-create"),
	}); err != nil {
		t.Fatalf("create no-auth local Lab Template: %v", err)
	}
	localRevision := testRevisionRequest("revision.local-one", "template.local", "local-bundle", "local-canonical")
	localRevision.PublishedBy = controlplane.LocalAdminID
	if _, err := store.PublishTemplateRevision(ctx, localRevision); err != nil {
		t.Fatalf("publish no-auth local Template Revision: %v", err)
	}
	if err := store.PutUser(ctx, application.UserRecord{ID: "user.author", Username: "author", DisplayName: "Author"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutOrganization(ctx, application.OrganizationRecord{ID: "org.alpha", Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	for _, project := range []application.ProjectRecord{
		{ID: "project.alpha", OrganizationID: "org.alpha", Name: "alpha"},
		{ID: "project.beta", OrganizationID: "org.alpha", Name: "beta"},
	} {
		if err := store.PutProject(ctx, project); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutMembership(ctx, application.MembershipRecord{
		ID: "membership.author", UserID: "user.author", OrganizationID: "org.alpha", ValidFrom: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	created, err := store.CreateLabTemplate(ctx, controlplane.CreateLabTemplateRequest{
		GovernanceRecordIDs: governanceIDs("alpha-create"), ID: "template.alpha", ProjectID: "project.alpha", Name: "first-lab", Description: "First lab", CreatedBy: "user.author",
		IdempotencyKey: "idem.alpha-create", RequestDigest: testRequestDigest("alpha-create"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ResourceVersion != 1 || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() || created.ProjectID != "project.alpha" {
		t.Fatalf("created Lab Template = %#v", created)
	}
	replayedCreate, err := store.CreateLabTemplate(ctx, controlplane.CreateLabTemplateRequest{
		GovernanceRecordIDs: governanceIDs("alpha-create-replay"), ID: "template.alpha-replay", ProjectID: "project.alpha",
		Name: "first-lab", Description: "First lab", CreatedBy: "user.author",
		IdempotencyKey: "idem.alpha-create", RequestDigest: testRequestDigest("alpha-create"),
	})
	if err != nil || replayedCreate != created {
		t.Fatalf("replayed Lab Template create = %#v, %v", replayedCreate, err)
	}
	if _, err := store.GetLabTemplate(ctx, "template.alpha-replay"); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("create replay persisted a second aggregate: %v", err)
	}
	if _, err := store.CreateLabTemplate(ctx, controlplane.CreateLabTemplateRequest{
		GovernanceRecordIDs: governanceIDs("alpha-create-conflict"), ID: "template.alpha-conflict", ProjectID: "project.alpha",
		Name: "changed-lab", CreatedBy: "user.author", IdempotencyKey: "idem.alpha-create", RequestDigest: testRequestDigest("changed-create"),
	}); err == nil {
		t.Fatal("Lab Template create accepted an idempotency key with another request digest")
	}
	if _, err := store.CreateLabTemplate(ctx, controlplane.CreateLabTemplateRequest{
		GovernanceRecordIDs: governanceIDs("alpha-create"), ID: "template.audit-fault", ProjectID: "project.alpha",
		Name: "audit-fault", CreatedBy: "user.author", IdempotencyKey: "idem.audit-fault", RequestDigest: testRequestDigest("audit-fault"),
	}); err == nil {
		t.Fatal("Template creation with duplicate audit/event IDs succeeded")
	}
	if _, err := store.GetLabTemplate(ctx, "template.audit-fault"); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("failed Template governance transaction persisted aggregate: %v", err)
	}
	if _, err := store.CreateLabTemplate(ctx, controlplane.CreateLabTemplateRequest{
		GovernanceRecordIDs: governanceIDs("duplicate-create"), ID: "template.duplicate", ProjectID: "project.alpha", Name: "first-lab", CreatedBy: "user.author",
		IdempotencyKey: "idem.duplicate-create", RequestDigest: testRequestDigest("duplicate-create"),
	}); !errors.Is(err, controlplane.ErrAlreadyExists) {
		t.Fatalf("same-project duplicate name returned %v", err)
	}
	if _, err := store.CreateLabTemplate(ctx, controlplane.CreateLabTemplateRequest{
		GovernanceRecordIDs: governanceIDs("beta-create"), ID: "template.beta", ProjectID: "project.beta", Name: "first-lab", CreatedBy: "user.author",
		IdempotencyKey: "idem.beta-create", RequestDigest: testRequestDigest("beta-create"),
	}); err != nil {
		t.Fatalf("same name in another project: %v", err)
	}
	if _, err := store.CreateLabTemplate(ctx, controlplane.CreateLabTemplateRequest{
		GovernanceRecordIDs: governanceIDs("alpha-second"), ID: "template.alpha-second", ProjectID: "project.alpha", Name: "second-lab", CreatedBy: "user.author",
		IdempotencyKey: "idem.alpha-second", RequestDigest: testRequestDigest("alpha-second"),
	}); err != nil {
		t.Fatalf("create second project Template: %v", err)
	}

	start := make(chan struct{})
	updates := make(chan error, 2)
	for index, name := range []string{"first-lab-a", "first-lab-b"} {
		index := index
		name := name
		go func() {
			<-start
			_, updateErr := store.UpdateLabTemplate(ctx, controlplane.UpdateLabTemplateRequest{
				GovernanceRecordIDs: governanceIDs(fmt.Sprintf("alpha-update-%d", index)), ID: "template.alpha", UpdatedBy: "user.author",
				Name: name, Description: "updated", ExpectedResourceVersion: 1,
				IdempotencyKey: fmt.Sprintf("idem.alpha-update-%d", index), RequestDigest: testRequestDigest(fmt.Sprintf("alpha-update-%d", index)),
			})
			updates <- updateErr
		}()
	}
	close(start)
	updateSuccesses, versionConflicts := 0, 0
	for range 2 {
		switch err := <-updates; {
		case err == nil:
			updateSuccesses++
		case errors.Is(err, controlplane.ErrVersionConflict):
			versionConflicts++
		default:
			t.Fatalf("concurrent Lab Template update: %v", err)
		}
	}
	if updateSuccesses != 1 || versionConflicts != 1 {
		t.Fatalf("concurrent updates successes=%d version-conflicts=%d", updateSuccesses, versionConflicts)
	}
	loaded, err := store.GetLabTemplate(ctx, "template.alpha")
	if err != nil || loaded.ResourceVersion != 2 || loaded.Description != "updated" {
		t.Fatalf("loaded Lab Template = %#v, %v", loaded, err)
	}
	idempotentUpdate := controlplane.UpdateLabTemplateRequest{
		GovernanceRecordIDs: governanceIDs("alpha-update-replayable"), ID: "template.alpha", UpdatedBy: "user.author",
		Name: loaded.Name, Description: "replayable update", ExpectedResourceVersion: loaded.ResourceVersion,
		IdempotencyKey: "idem.alpha-update-replayable", RequestDigest: testRequestDigest("alpha-update-replayable"),
	}
	updated, err := store.UpdateLabTemplate(ctx, idempotentUpdate)
	if err != nil || updated.ResourceVersion != 3 || updated.Description != "replayable update" {
		t.Fatalf("replayable Lab Template update = %#v, %v", updated, err)
	}
	replayedUpdate := idempotentUpdate
	replayedUpdate.GovernanceRecordIDs = governanceIDs("alpha-update-replayed")
	replayedUpdate.ExpectedResourceVersion = 2
	replayed, err := store.UpdateLabTemplate(ctx, replayedUpdate)
	if err != nil || replayed != updated {
		t.Fatalf("replayed Lab Template update = %#v, %v", replayed, err)
	}
	conflictingUpdate := replayedUpdate
	conflictingUpdate.Description = "different body"
	conflictingUpdate.RequestDigest = testRequestDigest("alpha-update-conflict")
	if _, err := store.UpdateLabTemplate(ctx, conflictingUpdate); err == nil {
		t.Fatal("Lab Template update accepted an idempotency key with another request digest")
	}
	firstTemplatePage, err := store.ListLabTemplates(ctx, controlplane.LabTemplateListRequest{ProjectID: "project.alpha", Limit: 1})
	if err != nil || len(firstTemplatePage.Records) != 1 || firstTemplatePage.NextCursor == nil {
		t.Fatalf("first project-scoped Lab Template page = %#v, %v", firstTemplatePage, err)
	}
	secondTemplatePage, err := store.ListLabTemplates(ctx, controlplane.LabTemplateListRequest{ProjectID: "project.alpha", After: firstTemplatePage.NextCursor, Limit: 1})
	if err != nil || len(secondTemplatePage.Records) != 1 || secondTemplatePage.NextCursor != nil || secondTemplatePage.Records[0].ID == firstTemplatePage.Records[0].ID {
		t.Fatalf("second project-scoped Lab Template page = %#v, %v", secondTemplatePage, err)
	}

	concurrentCreates := []controlplane.CreateLabTemplateRequest{
		{GovernanceRecordIDs: governanceIDs("concurrent-create-a"), ID: "template.concurrent-a", ProjectID: "project.alpha", Name: "concurrent-lab", CreatedBy: "user.author", IdempotencyKey: "idem.concurrent-create", RequestDigest: testRequestDigest("concurrent-create")},
		{GovernanceRecordIDs: governanceIDs("concurrent-create-b"), ID: "template.concurrent-b", ProjectID: "project.alpha", Name: "concurrent-lab", CreatedBy: "user.author", IdempotencyKey: "idem.concurrent-create", RequestDigest: testRequestDigest("concurrent-create")},
	}
	concurrentCreateStart := make(chan struct{})
	concurrentCreateResults := make(chan struct {
		record controlplane.LabTemplateRecord
		err    error
	}, len(concurrentCreates))
	for _, request := range concurrentCreates {
		request := request
		go func() {
			<-concurrentCreateStart
			record, createErr := store.CreateLabTemplate(ctx, request)
			concurrentCreateResults <- struct {
				record controlplane.LabTemplateRecord
				err    error
			}{record: record, err: createErr}
		}()
	}
	close(concurrentCreateStart)
	var concurrentCreateID domain.TemplateID
	for range concurrentCreates {
		result := <-concurrentCreateResults
		if result.err != nil {
			t.Fatalf("concurrent idempotent Lab Template create: %v", result.err)
		}
		if concurrentCreateID == "" {
			concurrentCreateID = result.record.ID
		} else if result.record.ID != concurrentCreateID {
			t.Fatalf("concurrent creates returned different Templates: %s and %s", concurrentCreateID, result.record.ID)
		}
	}

	revisionRequests := []controlplane.PublishTemplateRevisionRequest{
		testRevisionRequest("revision.alpha-one", "template.alpha", "bundle-one", "canonical-one"),
		testRevisionRequest("revision.alpha-two", "template.alpha", "bundle-two", "canonical-two"),
	}
	revisionStart := make(chan struct{})
	revisions := make(chan struct {
		record controlplane.TemplateRevisionRecord
		err    error
	}, 2)
	for _, request := range revisionRequests {
		request := request
		go func() {
			<-revisionStart
			record, publishErr := store.PublishTemplateRevision(ctx, request)
			revisions <- struct {
				record controlplane.TemplateRevisionRecord
				err    error
			}{record: record, err: publishErr}
		}()
	}
	close(revisionStart)
	var revisionNumbers []int
	for range 2 {
		result := <-revisions
		if result.err != nil {
			t.Fatalf("concurrent Template Revision publication: %v", result.err)
		}
		if result.record.CreatedAt.IsZero() || result.record.BundleArtifact != domain.ArtifactRef(result.record.BundleDigest.String()) {
			t.Fatalf("published Template Revision = %#v", result.record)
		}
		revisionNumbers = append(revisionNumbers, int(result.record.RevisionNumber))
	}
	sort.Ints(revisionNumbers)
	if fmt.Sprint(revisionNumbers) != "[1 2]" {
		t.Fatalf("revision numbers = %v", revisionNumbers)
	}
	replayedRevisionRequest := revisionRequests[0]
	replayedRevisionRequest.ID = "revision.alpha-replay"
	replayedRevisionRequest.GovernanceRecordIDs = governanceIDs("revision-alpha-replay")
	replayedRevision, err := store.PublishTemplateRevision(ctx, replayedRevisionRequest)
	if err != nil || replayedRevision.ID != revisionRequests[0].ID {
		t.Fatalf("replayed Template Revision publication = %#v, %v", replayedRevision, err)
	}
	if _, err := store.GetTemplateRevision(ctx, replayedRevisionRequest.ID); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("revision replay persisted a second immutable revision: %v", err)
	}
	conflictingRevisionRequest := replayedRevisionRequest
	conflictingRevisionRequest.ID = "revision.alpha-conflict"
	conflictingRevisionRequest.RequestDigest = testRequestDigest("changed-revision")
	if _, err := store.PublishTemplateRevision(ctx, conflictingRevisionRequest); err == nil {
		t.Fatal("Template Revision publication accepted an idempotency key with another request digest")
	}
	firstRevisionPage, err := store.ListTemplateRevisions(ctx, controlplane.TemplateRevisionListRequest{TemplateID: "template.alpha", Limit: 1})
	if err != nil || len(firstRevisionPage.Records) != 1 || firstRevisionPage.NextCursor == nil {
		t.Fatalf("first Template Revision page = %#v, %v", firstRevisionPage, err)
	}
	secondRevisionPage, err := store.ListTemplateRevisions(ctx, controlplane.TemplateRevisionListRequest{TemplateID: "template.alpha", After: firstRevisionPage.NextCursor, Limit: 1})
	listedRevisions := append(append([]controlplane.TemplateRevisionRecord(nil), firstRevisionPage.Records...), secondRevisionPage.Records...)
	if err != nil || secondRevisionPage.NextCursor != nil || len(listedRevisions) != 2 || listedRevisions[0].RevisionNumber != 1 || listedRevisions[1].RevisionNumber != 2 {
		t.Fatalf("Template Revision list = %#v, %v", listedRevisions, err)
	}
	loadedRevision, err := store.GetTemplateRevision(ctx, listedRevisions[0].ID)
	if err != nil || loadedRevision != listedRevisions[0] {
		t.Fatalf("loaded Template Revision = %#v, %v", loadedRevision, err)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_template_revisions SET renderer_version = ? WHERE revision_id = ?`), "tampered", loadedRevision.ID); err == nil {
		t.Fatal("database allowed an immutable Template Revision update")
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`DELETE FROM labcentral_template_revisions WHERE revision_id = ?`), loadedRevision.ID); err == nil {
		t.Fatal("database allowed an immutable Template Revision delete")
	}
	concurrentRevisionA := testRevisionRequest("revision.concurrent-a", "template.alpha", "bundle-concurrent", "canonical-concurrent")
	concurrentRevisionB := concurrentRevisionA
	concurrentRevisionB.ID = "revision.concurrent-b"
	concurrentRevisionB.GovernanceRecordIDs = governanceIDs("revision-concurrent-b")
	concurrentRevisionStart := make(chan struct{})
	concurrentRevisionResults := make(chan struct {
		record controlplane.TemplateRevisionRecord
		err    error
	}, 2)
	for _, request := range []controlplane.PublishTemplateRevisionRequest{concurrentRevisionA, concurrentRevisionB} {
		request := request
		go func() {
			<-concurrentRevisionStart
			record, publishErr := store.PublishTemplateRevision(ctx, request)
			concurrentRevisionResults <- struct {
				record controlplane.TemplateRevisionRecord
				err    error
			}{record: record, err: publishErr}
		}()
	}
	close(concurrentRevisionStart)
	var concurrentRevisionID domain.RevisionID
	for range 2 {
		result := <-concurrentRevisionResults
		if result.err != nil {
			t.Fatalf("concurrent idempotent Template Revision publication: %v", result.err)
		}
		if concurrentRevisionID == "" {
			concurrentRevisionID = result.record.ID
		} else if result.record.ID != concurrentRevisionID {
			t.Fatalf("concurrent publications returned different revisions: %s and %s", concurrentRevisionID, result.record.ID)
		}
	}
	for table, want := range map[string]int{
		"labcentral_audit_records": 11, "labcentral_events": 11, "labcentral_event_outbox_deliveries": 11,
		"labcentral_idempotency_records": 11,
	} {
		var rows int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&rows); err != nil || rows != want {
			t.Fatalf("%s rows=%d want=%d err=%v", table, rows, want, err)
		}
	}

	validInstanceID := "instance.revision-bound"
	if err := createRevisionBoundTestInstance(store, validInstanceID, "project.alpha", loadedRevision.ID); err != nil {
		t.Fatal(err)
	}
	instance, err := store.GetInstance(ctx, domain.InstanceID(validInstanceID))
	if err != nil || instance.RevisionID != loadedRevision.ID {
		t.Fatalf("revision-bound instance = %#v, %v", instance, err)
	}
	if err := createRevisionBoundTestInstance(store, "instance.wrong-project", "project.beta", loadedRevision.ID); err == nil {
		t.Fatal("instance accepted a Template Revision from another project")
	}
}

func testRevisionRequest(id domain.RevisionID, templateID domain.TemplateID, bundle, canonical string) controlplane.PublishTemplateRevisionRequest {
	bundleSum := sha256.Sum256([]byte(bundle))
	canonicalSum := sha256.Sum256([]byte(canonical))
	bundleDigest := domain.SHA256Digest(fmt.Sprintf("%x", bundleSum))
	return controlplane.PublishTemplateRevisionRequest{
		GovernanceRecordIDs: governanceIDs(string(id)), ID: id, TemplateID: templateID, BundleArtifact: domain.ArtifactRef(bundleDigest.String()), BundleDigest: bundleDigest,
		BundleSizeBytes: uint64(len(bundle)), AuthorAPIVersion: "labcentral.io/v1alpha1", RendererVersion: "renderer/v1",
		CanonicalVersion: "rfc8785/v1", CanonicalDigest: domain.SHA256Digest(fmt.Sprintf("%x", canonicalSum)), PublishedBy: "user.author",
		IdempotencyKey: "idem." + string(id), RequestDigest: testRequestDigest("publish-" + string(id)),
	}
}

func testRequestDigest(value string) domain.SHA256Digest {
	digest := sha256.Sum256([]byte(value))
	return domain.SHA256Digest(fmt.Sprintf("%x", digest))
}

func governanceIDs(suffix string) controlplane.GovernanceRecordIDs {
	return controlplane.GovernanceRecordIDs{AuditID: "audit." + suffix, EventID: "event." + suffix}
}

func createRevisionBoundTestInstance(store *Store, instanceID string, projectID domain.ProjectID, revisionID domain.RevisionID) error {
	plan := []byte(`{"planned":true}`)
	canonical := []byte(`{"apiVersion":"labcentral.io/v1alpha1"}`)
	planSum, canonicalSum, requestSum := sha256.Sum256(plan), sha256.Sum256(canonical), sha256.Sum256([]byte(instanceID))
	_, err := store.CreateInstance(context.Background(), controlplane.CreateInstanceRequest{
		InstanceID: domain.InstanceID(instanceID), RevisionID: revisionID, ProjectID: projectID,
		OwnerKind: application.OwnerUser, OwnerID: "user.author", CreatedBy: "user.author", ProviderTargetID: "target.alpha",
		OperationID: domain.OperationID("operation." + instanceID), CanonicalDigest: domain.SHA256Digest(fmt.Sprintf("%x", canonicalSum)),
		CanonicalJSON: canonical, SourceYAML: []byte("apiVersion: labcentral.io/v1alpha1\n"),
		Resources: []controlplane.ResourceSeed{{
			ID: "container.test", Kind: domain.ResourceContainer, CanonicalJSON: []byte(`{"kind":"Container"}`),
			ProviderPlan: plan, ProviderPlanDigest: domain.SHA256Digest(fmt.Sprintf("%x", planSum)), OwnershipToken: "ownership." + instanceID,
		}},
		Acknowledged: true, SubjectID: "user.author", IdempotencyKey: "idempotency." + instanceID,
		RequestDigest: domain.SHA256Digest(fmt.Sprintf("%x", requestSum)), Payload: []byte(`{"schemaVersion":1}`),
		AuditID: "audit." + instanceID, EventID: "event." + instanceID,
	})
	return err
}
