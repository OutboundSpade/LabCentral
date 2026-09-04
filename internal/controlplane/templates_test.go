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
	service, err := controlplane.NewService(testRenderer(t), store, provider, &sequenceIDs{})
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
		"labcentral_audit_records": 4, "labcentral_events": 4, "labcentral_event_outbox_deliveries": 4,
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
