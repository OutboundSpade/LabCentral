package controlplane_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"

	artifactfs "github.com/OutboundSpade/LabCentral/internal/adapter/filesystem"
	"github.com/OutboundSpade/LabCentral/internal/adapter/localdb"
	"github.com/OutboundSpade/LabCentral/internal/adapter/providergrpc"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func TestTemplatePublicationUsesVerifiedImmutableArtifactAndAtomicRecords(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:template-service?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	store, err := localdb.New(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	artifactRoot := t.TempDir()
	if err := os.Chmod(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	artifacts, err := artifactfs.New(artifactRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = artifacts.Close() })
	provider, _, closeProvider := testProvider(t)
	t.Cleanup(closeProvider)
	service, err := controlplane.NewService(testRenderer(t), store, providergrpc.New(provider), &sequenceIDs{})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetArtifactStore(artifacts); err != nil {
		t.Fatal(err)
	}

	template, err := service.CreateLabTemplate(ctx, controlplane.LocalAdminID, "project.local", "intro-lab", "Reusable classroom lab", "template-create-1")
	if err != nil {
		t.Fatal(err)
	}
	if template.ResourceVersion != 1 || template.ProjectID != "project.local" {
		t.Fatalf("created Template = %#v", template)
	}
	source, err := os.ReadFile("../../examples/docker-laptop/lab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	revision, err := service.PublishTemplateRevision(ctx, controlplane.LocalAdminID, template.ID, source, "template-publish-1")
	if err != nil {
		t.Fatal(err)
	}
	if revision.RevisionNumber != 1 || revision.BundleArtifact != domain.ArtifactRef(revision.BundleDigest.String()) || revision.CanonicalDigest == "" {
		t.Fatalf("published revision = %#v", revision)
	}
	verified, opened, err := service.OpenTemplateRevision(ctx, controlplane.LocalAdminID, revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if verified != revision || !bytes.Equal(opened, source) {
		t.Fatal("opened Template Revision did not match its published content and metadata")
	}
	created, err := service.CreateInstanceRevisionTargetFor(
		ctx, controlplane.LocalAdminID, "project.local", "user", string(controlplane.LocalAdminID),
		revision.ID, opened, true, "template-instance-create", controlplane.LocalDockerTarget, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := store.GetInstance(ctx, created.InstanceID)
	if err != nil || instance.RevisionID != revision.ID {
		t.Fatalf("instance did not persist immutable revision binding: %#v, %v", instance, err)
	}
	batch, err := service.CreateInstanceBatchRevisionTargetForLifetime(
		ctx, controlplane.LocalAdminID, "project.local",
		[]controlplane.InstanceOwner{{Kind: application.OwnerUser, ID: string(controlplane.LocalAdminID)}},
		revision.ID, opened, application.InstanceLifetimeRequest{}, true, "template-batch-create",
		controlplane.LocalDockerTarget, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Items) != 1 || batch.Items[0].OwnerID != string(controlplane.LocalAdminID) {
		t.Fatalf("created instance batch = %#v", batch)
	}
	replayedBatch, err := service.CreateInstanceBatchRevisionTargetForLifetime(
		ctx, controlplane.LocalAdminID, "project.local",
		[]controlplane.InstanceOwner{{Kind: application.OwnerUser, ID: string(controlplane.LocalAdminID)}},
		revision.ID, opened, application.InstanceLifetimeRequest{}, true, "template-batch-create",
		controlplane.LocalDockerTarget, nil,
	)
	if err != nil || !replayedBatch.Replay || replayedBatch.ID != batch.ID || replayedBatch.Items[0].InstanceID != batch.Items[0].InstanceID {
		t.Fatalf("replayed instance batch = %#v, %v", replayedBatch, err)
	}
	updated, err := service.UpdateLabTemplate(ctx, controlplane.LocalAdminID, template.ID, "intro-lab-v2", "Metadata can evolve", template.ResourceVersion, "update-template-1")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ResourceVersion != 2 {
		t.Fatalf("updated Template resource version = %d", updated.ResourceVersion)
	}
	if _, err := service.UpdateLabTemplate(ctx, controlplane.LocalAdminID, template.ID, "stale-update", "", template.ResourceVersion, "update-template-2"); !errors.Is(err, controlplane.ErrVersionConflict) {
		t.Fatalf("stale Template update error = %v", err)
	}
	verifiedAfterUpdate, openedAfterUpdate, err := service.OpenTemplateRevision(ctx, controlplane.LocalAdminID, revision.ID)
	if err != nil || verifiedAfterUpdate != revision || !bytes.Equal(openedAfterUpdate, source) {
		t.Fatalf("immutable revision changed after metadata update: %#v %v", verifiedAfterUpdate, err)
	}

	for table, want := range map[string]int{
		// Three Template mutations, two independently queued operations, and
		// the classroom-batch acceptance each emit one generalized pair.
		"labcentral_audit_records": 6, "labcentral_events": 6, "labcentral_event_outbox_deliveries": 6,
	} {
		var count int
		if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count); err != nil || count != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, count, want, err)
		}
	}
}

func TestTemplatePublicationRequiresArtifactStore(t *testing.T) {
	service := new(controlplane.Service)
	if _, err := service.PublishTemplateRevision(context.Background(), controlplane.LocalAdminID, "template.alpha", []byte("source"), "publish-key-1"); err == nil {
		t.Fatal("Template publication succeeded without a configured artifact store")
	}
}

func TestParameterizedRevisionInputIdentityAndSemantics(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:parameter-service?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	store, err := localdb.New(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	artifactRoot := t.TempDir()
	if err := os.Chmod(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	artifacts, err := artifactfs.New(artifactRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = artifacts.Close() })
	provider, _, closeProvider := testProvider(t)
	t.Cleanup(closeProvider)
	service, err := controlplane.NewService(testRenderer(t), store, providergrpc.New(provider), &sequenceIDs{})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetArtifactStore(artifacts); err != nil {
		t.Fatal(err)
	}

	template, err := service.CreateLabTemplate(ctx, controlplane.LocalAdminID, "project.local", "parameter-lab", "", "parameter-template")
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../examples/docker-laptop/lab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	source = bytes.Replace(source, []byte("spec:\n"), []byte("spec:\n  parameters:\n    - name: class_name\n      type: string\n      required: true\n"), 1)
	revision, err := service.PublishTemplateRevision(ctx, controlplane.LocalAdminID, template.ID, source, "parameter-publish")
	if err != nil {
		t.Fatal(err)
	}
	_, opened, err := service.OpenTemplateRevision(ctx, controlplane.LocalAdminID, revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	create := func(value, key string) (controlplane.EnqueueResult, error) {
		return service.CreateInstanceRevisionTargetForLifetime(ctx, controlplane.LocalAdminID, "project.local", application.OwnerUser, string(controlplane.LocalAdminID), revision.ID, opened, application.InstanceLifetimeRequest{}, true, key, controlplane.LocalDockerTarget, nil, controlplane.RenderOptions{Inputs: domain.TypedInputs{{Name: "class_name", Kind: domain.InputString, String: value}}})
	}
	first, err := create("alpha", "parameter-create")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := create("alpha", "parameter-create")
	if err != nil || !replay.Replay || first.InstanceID != replay.InstanceID {
		t.Fatal("identical input did not replay", err)
	}
	if _, err := create("beta", "parameter-create"); err == nil {
		t.Fatal("different inputs reused idempotency key")
	}
	second, err := create("beta", "parameter-create-two")
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.GetInstance(ctx, first.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.GetInstance(ctx, second.InstanceID)
	if err != nil || a.CanonicalDigest == b.CanonicalDigest {
		t.Fatal("input identity was not preserved", err)
	}
	future, err := controlplane.NewService(testRenderer(t), &templateSemanticsStore{Store: store}, providergrpc.New(provider), &sequenceIDs{})
	if err != nil {
		t.Fatal(err)
	}
	if err := future.SetArtifactStore(artifacts); err != nil {
		t.Fatal(err)
	}
	if _, _, err := future.OpenTemplateRevision(ctx, controlplane.LocalAdminID, revision.ID); err != nil {
		t.Fatal("source reading invoked unsupported renderer", err)
	}
	if _, err := future.CreateInstanceRevisionTargetForLifetime(ctx, controlplane.LocalAdminID, "project.local", application.OwnerUser, string(controlplane.LocalAdminID), revision.ID, opened, application.InstanceLifetimeRequest{}, true, "future-create", controlplane.LocalDockerTarget, nil, controlplane.RenderOptions{Inputs: domain.TypedInputs{{Name: "class_name", Kind: domain.InputString, String: "alpha"}}}); err == nil {
		t.Fatal("revision silently used current semantics")
	}

}

type templateSemanticsStore struct{ application.Store }

func (s *templateSemanticsStore) GetTemplateRevision(ctx context.Context, id domain.RevisionID) (application.TemplateRevisionRecord, error) {
	record, err := s.Store.GetTemplateRevision(ctx, id)
	record.RendererVersion = "future-semantics"
	return record, err
}
