package localdb

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
)

func TestSQLiteAuditEventStore(t *testing.T) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:audit-events-%d?mode=memory&cache=shared", time.Now().UnixNano()))
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
	runAuditEventStoreConformance(t, store)
}

func TestPostgreSQLAuditEventStore(t *testing.T) {
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
	schema := fmt.Sprintf("labcentral_audit_%d_%d", os.Getpid(), time.Now().UnixNano())
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
	runAuditEventStoreConformance(t, store)
}

func runAuditEventStoreConformance(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	before := time.Now().UTC().Add(-time.Second)
	payload, err := application.NewRedactedAuditPayload([]byte(`{"changedFields":["name"],"requestDigest":"sha256:abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	base := application.AppendAuditRecord{
		ID: "audit.one", SchemaVersion: "audit.labcentral.io/v1", Category: application.AuditTemplate,
		Action: "template.create", Outcome: application.AuditSucceeded,
		Scope:         application.RecordScope{OrganizationID: "org.alpha", ProjectID: "project.alpha"},
		Actor:         application.RecordActor{Kind: application.RecordActorUser, ID: "user.author"},
		AggregateType: "lab-template", AggregateID: "template.alpha", RedactedPayload: payload,
	}
	first, err := store.AppendAuditRecord(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	base.ID, base.Action = "audit.two", "template.update"
	second, err := store.AppendAuditRecord(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if first.StreamPosition != 1 || second.StreamPosition != 2 || first.OccurredAt.Before(before) || second.OccurredAt.Before(first.OccurredAt) {
		t.Fatalf("audit stream positions/timestamps = %#v %#v", first, second)
	}
	loaded, err := store.GetAuditRecord(ctx, first.ID)
	if err != nil || loaded.StreamPosition != first.StreamPosition || !bytes.Equal(loaded.RedactedPayload.Bytes(), payload.Bytes()) {
		t.Fatalf("loaded audit = %#v, %v", loaded, err)
	}
	page, err := store.ListAuditRecords(ctx, application.AuditRecordFilter{
		Scope: application.RecordScope{OrganizationID: "org.alpha", ProjectID: "project.alpha"}, Limit: 1,
	})
	if err != nil || len(page.Records) != 1 || page.NextCursor == nil {
		t.Fatalf("first audit page = %#v, %v", page, err)
	}
	next, err := store.ListAuditRecords(ctx, application.AuditRecordFilter{
		Scope: application.RecordScope{OrganizationID: "org.alpha", ProjectID: "project.alpha"}, After: page.NextCursor, Limit: 1,
	})
	if err != nil || len(next.Records) != 1 || next.Records[0].ID != second.ID || next.NextCursor != nil {
		t.Fatalf("second audit page = %#v, %v", next, err)
	}

	eventPayload, err := application.NewSafeEventPayload([]byte(`{"revisionId":"revision.alpha"}`))
	if err != nil {
		t.Fatal(err)
	}
	const events = 8
	start := make(chan struct{})
	results := make(chan application.EventRecord, events)
	errorsSeen := make(chan error, events)
	var wait sync.WaitGroup
	for index := 0; index < events; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			record, appendErr := store.AppendEventRecord(ctx, application.AppendEventRecord{
				ID: fmt.Sprintf("event.%02d", index), SchemaVersion: "event.labcentral.io/v1", Type: "template.revision.published",
				Scope:         application.RecordScope{OrganizationID: "org.alpha", ProjectID: "project.alpha"},
				Actor:         application.RecordActor{Kind: application.RecordActorUser, ID: "user.author"},
				AggregateType: "lab-template", AggregateID: "template.alpha", SafePayload: eventPayload,
			})
			if appendErr != nil {
				errorsSeen <- appendErr
				return
			}
			results <- record
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsSeen)
	for appendErr := range errorsSeen {
		t.Fatalf("concurrent event append: %v", appendErr)
	}
	sequences := make([]int, 0, events)
	positions := make([]int, 0, events)
	for record := range results {
		sequences = append(sequences, int(record.AggregateSequence))
		positions = append(positions, int(record.StreamPosition))
	}
	var outboxRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM labcentral_event_outbox_deliveries`).Scan(&outboxRows); err != nil || outboxRows != events {
		t.Fatalf("event outbox rows=%d err=%v", outboxRows, err)
	}
	sort.Ints(sequences)
	sort.Ints(positions)
	for index := 0; index < events; index++ {
		if sequences[index] != index+1 || positions[index] != index+1 {
			t.Fatalf("event sequences=%v positions=%v", sequences, positions)
		}
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_audit_records SET action = ? WHERE audit_id = ?`), "tamper", first.ID); err == nil {
		t.Fatal("database allowed immutable audit update")
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_events SET event_type = ? WHERE event_id = ?`), "tamper", "event.00"); err == nil {
		t.Fatal("database allowed immutable event update")
	}

	var exported bytes.Buffer
	export, err := store.ExportAuditRecords(ctx, application.AuditExportRequest{
		Filter:     application.AuditRecordFilter{Scope: application.RecordScope{OrganizationID: "org.alpha", ProjectID: "project.alpha"}, Limit: 1},
		MaxRecords: 10, MaxBytes: 1 << 20,
	}, &exported)
	if err != nil || export.Records != 2 || export.Truncated || strings.Count(exported.String(), "\n") != 2 {
		t.Fatalf("audit export = %#v %q, %v", export, exported.String(), err)
	}
	deleted, err := store.ApplyAuditRetention(ctx, application.RetentionRequest{OccurredBefore: time.Now().UTC().Add(time.Minute), BatchSize: 1})
	if err != nil || deleted != 1 {
		t.Fatalf("bounded audit retention deleted=%d err=%v", deleted, err)
	}
	retention := application.RetentionRequest{OccurredBefore: time.Now().UTC().Add(time.Minute), BatchSize: 3}
	deleted, err = store.ApplyEventRetention(ctx, retention)
	if err != nil || deleted != 0 {
		t.Fatalf("event retention removed undelivered events: deleted=%d err=%v", deleted, err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM labcentral_event_outbox_deliveries WHERE delivered_at_us = 0`).Scan(&outboxRows); err != nil || outboxRows != events {
		t.Fatalf("undelivered event outbox rows=%d err=%v", outboxRows, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE labcentral_event_outbox_deliveries
		SET delivered_at_us = available_at_us
		WHERE event_id IN (
			SELECT event_id FROM labcentral_event_outbox_deliveries ORDER BY event_id LIMIT 3
		)`); err != nil {
		t.Fatal(err)
	}
	deleted, err = store.ApplyEventRetention(ctx, retention)
	if err != nil || deleted != 3 {
		t.Fatalf("bounded delivered-event retention deleted=%d err=%v", deleted, err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM labcentral_event_outbox_deliveries WHERE delivered_at_us = 0`).Scan(&outboxRows); err != nil || outboxRows != events-3 {
		t.Fatalf("retention changed undelivered outbox rows=%d err=%v", outboxRows, err)
	}

	audit, event, err := application.NewGovernanceRecords(application.GovernanceRecordInput{
		AuditID: "audit.atomic-pair", EventID: "event.07", Category: application.AuditAdministrator,
		Action: "governance.atomic-test", Outcome: application.AuditSucceeded, EventType: "governance.atomic-test",
		Actor:         application.RecordActor{Kind: application.RecordActorSystem, ID: "system.test"},
		AggregateType: "governance-test", AggregateID: "pair", Payload: struct {
			Result string `json:"result"`
		}{"rollback"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AppendGovernanceRecords(ctx, audit, event); err == nil {
		t.Fatal("atomic governance pair accepted a duplicate event")
	}
	if _, err := store.GetAuditRecord(ctx, audit.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("audit half of failed governance pair was retained: %v", err)
	}
	event.ID = "event.atomic-pair"
	if _, _, err := store.AppendGovernanceRecords(ctx, audit, event); err != nil {
		t.Fatalf("append atomic governance pair: %v", err)
	}
}

func TestAuditPayloadRejectsSecretKeysAndMalformedJSON(t *testing.T) {
	for _, raw := range []string{
		`{"password":"canary"}`,
		`{"accessToken":"canary"}`,
		`{"name":"first","name":"second"}`,
		`[]`,
		`{"nested":{"privateKey":"canary"}}`,
	} {
		if _, err := application.NewRedactedAuditPayload([]byte(raw)); err == nil {
			t.Fatalf("unsafe audit payload was accepted: %s", raw)
		}
	}
	if _, err := application.NewRedactedAuditPayload([]byte(`{"tokenHash":"sha256:abc","credentialRef":"secretref://target"}`)); err != nil {
		t.Fatalf("safe references and hashes were rejected: %v", err)
	}
}

func TestAuditEventMissingRecordsUseSQLNotFound(t *testing.T) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:audit-not-found-%d?mode=memory&cache=shared", time.Now().UnixNano()))
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
	if _, err := store.GetAuditRecord(context.Background(), "audit.missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing audit error = %v", err)
	}
}
