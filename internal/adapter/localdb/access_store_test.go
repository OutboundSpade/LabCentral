package localdb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"google.golang.org/protobuf/proto"
)

func TestSQLiteAccessSessionConformance(t *testing.T) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:access-session-%d?mode=memory&cache=shared", time.Now().UnixNano()))
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
	runAccessSessionConformance(t, store)
}

func TestPostgreSQLAccessSessionConformance(t *testing.T) {
	store := newPostgreSQLQuotaTestStore(t, "access_session")
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	runAccessSessionConformance(t, store)
}

func TestAccessContract004ConcurrentExchangeHasOneWinner(t *testing.T) {
	runAccessSessionConformance(t, newSQLiteAccessContractStore(t))
}

func TestAccessContract005AudienceAndPluginBinding(t *testing.T) {
	runAccessSessionConformance(t, newSQLiteAccessContractStore(t))
}

func TestAccessContract006ExchangeReauthorizes(t *testing.T) {
	mutations := map[string]func(*testing.T, *Store){
		"user-disabled": func(t *testing.T, store *Store) {
			if err := store.PutUser(context.Background(), application.UserRecord{ID: "user.quota", Username: "quota", DisplayName: "Quota User", Disabled: true}); err != nil {
				t.Fatal(err)
			}
		},
		"membership-ended": func(t *testing.T, store *Store) {
			if _, err := store.db.ExecContext(context.Background(), store.bind(`UPDATE labcentral_role_assignments SET role = 'instructor', scope_kind = 'project', scope_id = 'project.alpha' WHERE assignment_id = 'role.quota'`)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(context.Background(), store.bind(`UPDATE labcentral_memberships SET valid_until_us = 1 WHERE membership_id = 'membership.quota'`)); err != nil {
				t.Fatal(err)
			}
		},
		"role-revoked": func(t *testing.T, store *Store) {
			if _, err := store.db.ExecContext(context.Background(), store.bind(`UPDATE labcentral_role_assignments SET valid_until_us = 1 WHERE assignment_id = 'role.quota'`)); err != nil {
				t.Fatal(err)
			}
		},
		"target-grant-revoked": func(t *testing.T, store *Store) {
			if _, err := store.db.ExecContext(context.Background(), store.bind(`UPDATE labcentral_provider_target_grants SET valid_until_us = 1 WHERE grant_id = 'grant.quota'`)); err != nil {
				t.Fatal(err)
			}
		},
		"owner-changed": func(t *testing.T, store *Store) {
			if _, err := store.db.ExecContext(context.Background(), store.bind(`UPDATE labcentral_role_assignments SET role = 'participant', scope_kind = 'project', scope_id = 'project.alpha' WHERE assignment_id = 'role.quota'`)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(context.Background(), store.bind(`UPDATE labcentral_local_instances SET owner_id = 'user.other' WHERE instance_id = 'instance.reauthorize'`)); err != nil {
				t.Fatal(err)
			}
		},
		"instance-destroying": func(t *testing.T, store *Store) {
			if _, err := store.db.ExecContext(context.Background(), store.bind(`UPDATE labcentral_local_instances SET phase = ? WHERE instance_id = 'instance.reauthorize'`), domain.InstanceDestroying); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			store := newSQLiteAccessContractStore(t)
			setupQuotaTestTenancy(t, store)
			instance, providerDigest := createReadyAccessTestInstance(t, store, "instance.reauthorize")
			ready := finalizeAccessTestSession(t, store,
				beginAccessTestSession(t, store, instance, providerDigest, "access.reauthorize", "reauthorize"),
				providerDigest, accessTestHash("reauthorize-launch"), "reauthorize")
			mutate(t, store)
			denied, allowed, err := store.ExchangeAccessToken(context.Background(), application.AccessExchangeRequest{
				GovernanceRecordIDs: governanceIDs("access-exchange-reauthorize-" + name), ID: ready.ID,
				Identity: accessTestIdentity(), TokenHashes: []application.KeyedTokenHash{accessTestHash("reauthorize-launch")},
				NewLease: accessTestHash("reauthorize-lease"), LeaseDuration: application.DefaultAccessLeaseDuration,
			})
			if err != nil || allowed || denied.State != domain.AccessSessionFailed || denied.LaunchTokenHash != "" {
				t.Fatalf("reauthorization after %s = state %q allowed=%v token=%q err=%v", name, denied.State, allowed, denied.LaunchTokenHash, err)
			}
		})
	}
}

func TestAccessContract007ActiveLeaseReauthorization(t *testing.T) {
	runAccessSessionConformance(t, newSQLiteAccessContractStore(t))
}

func TestAccessContract009RevocationIsCoreFirst(t *testing.T) {
	runAccessSessionConformance(t, newSQLiteAccessContractStore(t))
}

func TestAccessContract011LostCreateResponseRecovery(t *testing.T) {
	store := newSQLiteAccessContractStore(t)
	setupQuotaTestTenancy(t, store)
	instance, providerDigest := createReadyAccessTestInstance(t, store, "instance.lost-response")
	request := accessBeginTestRequest(instance, providerDigest, "access.lost-response", "lost-response")
	pending, err := store.BeginAccessSession(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	retry := request
	retry.ID = "access.substituted"
	replayed, err := store.BeginAccessSession(context.Background(), retry)
	if err != nil || !replayed.Replay || replayed.ID != pending.ID {
		t.Fatalf("lost-response Begin replay = %#v, %v", replayed, err)
	}
	first := finalizeAccessTestSession(t, store, pending, providerDigest, accessTestHash("superseded-launch"), "lost-first")
	latest := finalizeAccessTestSession(t, store, first, providerDigest, accessTestHash("latest-launch"), "lost-second")
	if _, allowed, err := store.ExchangeAccessToken(context.Background(), application.AccessExchangeRequest{
		GovernanceRecordIDs: governanceIDs("access-lost-old-token"), ID: latest.ID, Identity: accessTestIdentity(),
		TokenHashes: []application.KeyedTokenHash{accessTestHash("superseded-launch")}, NewLease: accessTestHash("unused-lease"), LeaseDuration: application.DefaultAccessLeaseDuration,
	}); err != nil || allowed {
		t.Fatalf("superseded launch allowed=%v err=%v", allowed, err)
	}
	if _, allowed, err := store.ExchangeAccessToken(context.Background(), application.AccessExchangeRequest{
		GovernanceRecordIDs: governanceIDs("access-lost-latest-token"), ID: latest.ID, Identity: accessTestIdentity(),
		TokenHashes: []application.KeyedTokenHash{accessTestHash("latest-launch")}, NewLease: accessTestHash("latest-lease"), LeaseDuration: application.DefaultAccessLeaseDuration,
	}); err != nil || !allowed {
		t.Fatalf("latest launch allowed=%v err=%v", allowed, err)
	}
}

func TestAccessContract012RestartPreservesDenial(t *testing.T) {
	store := newSQLiteAccessContractStore(t)
	setupQuotaTestTenancy(t, store)
	instance, providerDigest := createReadyAccessTestInstance(t, store, "instance.restart")
	active := finalizeAccessTestSession(t, store,
		beginAccessTestSession(t, store, instance, providerDigest, "access.restart-active", "restart-active"),
		providerDigest, accessTestHash("restart-consumed"), "restart-active")
	if _, allowed, err := store.ExchangeAccessToken(context.Background(), application.AccessExchangeRequest{
		GovernanceRecordIDs: governanceIDs("access-restart-consume"), ID: active.ID, Identity: accessTestIdentity(),
		TokenHashes: []application.KeyedTokenHash{accessTestHash("restart-consumed")}, NewLease: accessTestHash("restart-lease"), LeaseDuration: application.DefaultAccessLeaseDuration,
	}); err != nil || !allowed {
		t.Fatalf("initial exchange allowed=%v err=%v", allowed, err)
	}
	revoked := finalizeAccessTestSession(t, store,
		beginAccessTestSession(t, store, instance, providerDigest, "access.restart-revoked", "restart-revoked"),
		providerDigest, accessTestHash("restart-revoked"), "restart-revoked")
	if _, err := store.RevokeAccessSession(context.Background(), application.RevokeAccessSessionRequest{
		GovernanceRecordIDs: governanceIDs("access-restart-revoke"), ID: revoked.ID, ActorID: "user.quota", Reason: "revoked",
		ExpectedResourceVersion: revoked.ResourceVersion, IdempotencyKey: "idem.restart-revoke", RequestDigest: testRequestDigest("restart-revoke"),
	}); err != nil {
		t.Fatal(err)
	}
	expired := finalizeAccessTestSession(t, store,
		beginAccessTestSession(t, store, instance, providerDigest, "access.restart-expired", "restart-expired"),
		providerDigest, accessTestHash("restart-expired"), "restart-expired")
	if _, err := store.db.ExecContext(context.Background(), store.bind(`UPDATE labcentral_access_sessions SET launch_expires_at_us = 1 WHERE access_session_id = ?`), expired.ID); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(store.db)
	if err != nil {
		t.Fatal(err)
	}
	for name, candidate := range map[string]struct {
		id    domain.AccessSessionID
		token string
	}{
		"consumed": {active.ID, "restart-consumed"}, "revoked": {revoked.ID, "restart-revoked"}, "expired": {expired.ID, "restart-expired"},
	} {
		t.Run(name, func(t *testing.T) {
			record, allowed, err := restarted.ExchangeAccessToken(context.Background(), application.AccessExchangeRequest{
				GovernanceRecordIDs: governanceIDs("access-restart-deny-" + name), ID: candidate.id, Identity: accessTestIdentity(),
				TokenHashes: []application.KeyedTokenHash{accessTestHash(candidate.token)}, NewLease: accessTestHash("restart-unused-" + name), LeaseDuration: application.DefaultAccessLeaseDuration,
			})
			if err != nil || allowed {
				t.Fatalf("post-restart %s state=%q allowed=%v err=%v", name, record.State, allowed, err)
			}
		})
	}
}

func TestAccessContract014SensitiveCanariesAbsent(t *testing.T) {
	runAccessSessionConformance(t, newSQLiteAccessContractStore(t))
}

func newSQLiteAccessContractStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:access-contract-%d?mode=memory&cache=shared", time.Now().UnixNano()))
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
	return store
}

func accessTestIdentity() application.AccessCallbackIdentity {
	return application.AccessCallbackIdentity{
		PluginID: "access.novnc", PluginInstanceID: "plugin.access-1", Audience: "https://access.example.test",
		ProtocolMajor: 1, ProtocolMinor: application.AccessProtocolMinor,
	}
}

func runAccessSessionConformance(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	setupQuotaTestTenancy(t, store)
	instance, providerDigest := createReadyAccessTestInstance(t, store, "instance.access")
	identity := accessTestIdentity()
	pending := beginAccessTestSession(t, store, instance, providerDigest, "access.one", "one")
	launchHash := accessTestHash("launch-token")
	ready := finalizeAccessTestSession(t, store, pending, providerDigest, launchHash, "one")
	if ready.State != domain.AccessSessionReady || ready.LaunchTokenHash != launchHash.Digest {
		t.Fatalf("ready access session = %#v", ready)
	}

	start := make(chan struct{})
	type result struct {
		record  application.AccessSessionRecord
		allowed bool
		err     error
	}
	results := make(chan result, 8)
	var workers sync.WaitGroup
	for index := range 8 {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			record, allowed, err := store.ExchangeAccessToken(ctx, application.AccessExchangeRequest{
				GovernanceRecordIDs: governanceIDs(fmt.Sprintf("access-exchange-%d", index)), ID: ready.ID, Identity: identity,
				TokenHashes: []application.KeyedTokenHash{launchHash}, NewLease: accessTestHash("lease-one"),
				LeaseDuration: application.DefaultAccessLeaseDuration,
			})
			results <- result{record: record, allowed: allowed, err: err}
		}(index)
	}
	close(start)
	workers.Wait()
	close(results)
	winners := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent exchange: %v", result.err)
		}
		if result.allowed {
			winners++
			if result.record.State != domain.AccessSessionActive {
				t.Fatalf("winner state = %q", result.record.State)
			}
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent exchange winners = %d, want 1", winners)
	}

	if _, allowed, err := store.RecheckAccessLease(ctx, application.AccessLeaseRecheckRequest{
		GovernanceRecordIDs: governanceIDs("access-recheck-wrong-audience"), ID: ready.ID,
		Identity:    application.AccessCallbackIdentity{PluginID: identity.PluginID, PluginInstanceID: identity.PluginInstanceID, Audience: "https://other.example.test", ProtocolMajor: 1, ProtocolMinor: application.AccessProtocolMinor},
		LeaseHashes: []application.KeyedTokenHash{accessTestHash("lease-one")}, NewLease: accessTestHash("lease-never"), LeaseDuration: application.DefaultAccessLeaseDuration,
	}); err != nil || allowed {
		t.Fatalf("audience mismatch allowed=%v err=%v", allowed, err)
	}
	if _, allowed, err := store.RecheckAccessLease(ctx, application.AccessLeaseRecheckRequest{
		GovernanceRecordIDs: governanceIDs("access-recheck-ok"), ID: ready.ID, Identity: identity,
		LeaseHashes: []application.KeyedTokenHash{accessTestHash("lease-one")}, NewLease: accessTestHash("lease-two"), LeaseDuration: application.DefaultAccessLeaseDuration,
	}); err != nil || !allowed {
		t.Fatalf("valid recheck allowed=%v err=%v", allowed, err)
	}
	if err := store.PutUser(ctx, application.UserRecord{ID: "user.quota", Username: "quota", DisplayName: "Quota User", Disabled: true}); err != nil {
		t.Fatal(err)
	}
	denied, allowed, err := store.RecheckAccessLease(ctx, application.AccessLeaseRecheckRequest{
		GovernanceRecordIDs: governanceIDs("access-recheck-disabled"), ID: ready.ID, Identity: identity,
		LeaseHashes: []application.KeyedTokenHash{accessTestHash("lease-two")}, NewLease: accessTestHash("lease-three"), LeaseDuration: application.DefaultAccessLeaseDuration,
	})
	if err != nil || allowed || denied.State != domain.AccessSessionFailed || !denied.ClosureRequired {
		t.Fatalf("disabled recheck = state %q allowed=%v closure=%v err=%v", denied.State, allowed, denied.ClosureRequired, err)
	}
	wrongIdentity := identity
	wrongIdentity.PluginInstanceID = "plugin.access-other"
	if _, ended, err := store.EndAccessLease(ctx, application.EndAccessLeaseRequest{
		GovernanceRecordIDs: governanceIDs("access-end-wrong-plugin"), ID: ready.ID, Identity: wrongIdentity,
		LeaseHashes: []application.KeyedTokenHash{accessTestHash("lease-two")}, Reason: "authorization_denied",
	}); err != nil || ended {
		t.Fatalf("wrong-plugin terminal acknowledgement ended=%v err=%v", ended, err)
	}
	endedRecord, ended, err := store.EndAccessLease(ctx, application.EndAccessLeaseRequest{
		GovernanceRecordIDs: governanceIDs("access-end-terminal"), ID: ready.ID, Identity: identity,
		LeaseHashes: []application.KeyedTokenHash{accessTestHash("lease-two")}, Reason: "authorization_denied",
	})
	if err != nil || !ended || endedRecord.ClosureRequired {
		t.Fatalf("terminal closure acknowledgement = %#v ended=%v err=%v", endedRecord, ended, err)
	}

	if err := store.PutUser(ctx, application.UserRecord{ID: "user.quota", Username: "quota", DisplayName: "Quota User"}); err != nil {
		t.Fatal(err)
	}
	second := beginAccessTestSession(t, store, instance, providerDigest, "access.two", "two")
	second = finalizeAccessTestSession(t, store, second, providerDigest, accessTestHash("second-launch-secret-canary"), "two")
	revoked, err := store.RevokeAccessSession(ctx, application.RevokeAccessSessionRequest{
		GovernanceRecordIDs: governanceIDs("access-revoke"), ID: second.ID, ActorID: "user.quota", Reason: "revoked",
		ExpectedResourceVersion: second.ResourceVersion, IdempotencyKey: "idem.access-revoke", RequestDigest: testRequestDigest("access-revoke"),
	})
	if err != nil || revoked.State != domain.AccessSessionRevoked || !revoked.ClosureRequired || revoked.LaunchTokenHash != "" {
		t.Fatalf("revoked access session = %#v, %v", revoked, err)
	}
	if _, allowed, err := store.ExchangeAccessToken(ctx, application.AccessExchangeRequest{
		GovernanceRecordIDs: governanceIDs("access-exchange-revoked"), ID: second.ID, Identity: identity,
		TokenHashes: []application.KeyedTokenHash{accessTestHash("second-launch-secret-canary")}, NewLease: accessTestHash("unused-lease"), LeaseDuration: application.DefaultAccessLeaseDuration,
	}); err != nil || allowed {
		t.Fatalf("revoked exchange allowed=%v err=%v", allowed, err)
	}
	if err := store.MarkAccessClosureComplete(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	closed, err := store.GetAccessSession(ctx, second.ID)
	if err != nil || closed.ClosureRequired {
		t.Fatalf("closure complete = %#v, %v", closed, err)
	}

	expiring := finalizeAccessTestSession(t, store,
		beginAccessTestSession(t, store, instance, providerDigest, "access.expiring", "expiring"),
		providerDigest, accessTestHash("expiring-launch"), "expiring")
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_access_sessions SET launch_expires_at_us = 1 WHERE access_session_id = ?`), expiring.ID); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.ListAccessSessionsForReconciliation(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range candidates {
		found = found || candidate.ID == expiring.ID
	}
	if !found {
		t.Fatal("expired launch was absent from access reconciliation candidates")
	}
	expired, changed, err := store.ExpireAccessSession(ctx, expiring.ID, governanceIDs("access-expire"))
	if err != nil || !changed || expired.State != domain.AccessSessionExpired || !expired.ClosureRequired || expired.LaunchTokenHash != "" {
		t.Fatalf("expired access session = %#v changed=%v err=%v", expired, changed, err)
	}

	assertAccessGovernanceRedacted(t, store, "second-launch-secret-canary", "endpoint-secret-canary", "opaque-state-canary")
}

func createReadyAccessTestInstance(t *testing.T, store *Store, id string) (application.InstanceRecord, domain.SHA256Digest) {
	t.Helper()
	ctx := context.Background()
	request := quotaTestCreateRequest(id, "project.alpha", 3600)
	request.Resources[0].ID = "vm.test"
	request.Resources[0].Kind = domain.ResourceVM
	request.Resources[0].CanonicalJSON = []byte(`{"id":"vm.test","kind":"VirtualMachine"}`)
	request.Quota.Amounts = append(request.Quota.Amounts, application.QuotaAmount{Dimension: application.QuotaVMs, Value: 1})
	created, err := store.CreateInstance(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	digestBytes := sha256.Sum256([]byte("provider-state-" + id))
	state := &pluginv1.ProviderState{
		Schema:   &pluginv1.SchemaVersion{Name: "proxmox.resource-state", Major: 1, Minor: 0},
		PluginId: "provider.proxmox", PluginVersion: "1.0.0", Opaque: []byte(`{"vmid":910001}`),
		OwnershipIds: []string{"ownership." + id}, Digest: digestBytes[:],
	}
	encoded, err := proto.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_jobs SET state = ? WHERE operation_id = ?`), domain.OperationSucceeded, created.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_local_instances SET phase = ? WHERE instance_id = ?`), domain.InstanceReady, request.InstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`UPDATE labcentral_local_resources SET phase = ?, provider_state = ? WHERE instance_id = ? AND resource_id = ?`), domain.ResourceReady, encoded, request.InstanceID, "vm.test"); err != nil {
		t.Fatal(err)
	}
	instance, err := store.GetInstance(ctx, request.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	return instance, domain.SHA256Digest(fmt.Sprintf("%x", digestBytes))
}

func beginAccessTestSession(t *testing.T, store *Store, instance application.InstanceRecord, providerDigest domain.SHA256Digest, id, suffix string) application.AccessSessionRecord {
	t.Helper()
	record, err := store.BeginAccessSession(context.Background(), accessBeginTestRequest(instance, providerDigest, id, suffix))
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func accessBeginTestRequest(instance application.InstanceRecord, providerDigest domain.SHA256Digest, id, suffix string) application.BeginAccessSessionRequest {
	return application.BeginAccessSessionRequest{
		GovernanceRecordIDs: governanceIDs("access-begin-" + suffix), ID: domain.AccessSessionID(id), SubjectID: "user.quota",
		InstanceID: instance.ID, ResourceID: "vm.test", Method: "vnc", AccessPluginID: "access.novnc", AccessPluginVersion: "1.0.0",
		AccessPluginInstance: "plugin.access-1", AccessKind: "browser-vnc", Audience: "https://access.example.test",
		ProviderStateDigest: providerDigest, ResourceGeneration: instance.Generation, RequestedDuration: 20 * time.Minute,
		IdempotencyKey: "idem.access-begin-" + suffix, RequestDigest: testRequestDigest("access-begin-" + suffix),
	}
}

func finalizeAccessTestSession(t *testing.T, store *Store, pending application.AccessSessionRecord, providerDigest domain.SHA256Digest, launch application.KeyedTokenHash, suffix string) application.AccessSessionRecord {
	t.Helper()
	endpointDigest := sha256.Sum256([]byte("endpoint-secret-canary"))
	stateDigest := sha256.Sum256([]byte("opaque-state-canary"))
	record, err := store.FinalizeAccessSession(context.Background(), application.FinalizeAccessSessionRequest{
		GovernanceRecordIDs: governanceIDs("access-finalize-" + suffix), ID: pending.ID, SubjectID: pending.SubjectID,
		EndpointSchemaName: "proxmox.vnc-endpoint", EndpointSchemaMajor: 1, EndpointDigest: domain.SHA256Digest(fmt.Sprintf("%x", endpointDigest)),
		ProviderStateDigest: providerDigest, LaunchToken: launch, LaunchExpiresAt: time.Now().UTC().Add(2 * time.Minute),
		ExpiresAt: pending.ExpiresAt.Add(-time.Second), AccessStateSchema: "novnc.access-state", AccessStateMajor: 1,
		AccessStateDigest: domain.SHA256Digest(fmt.Sprintf("%x", stateDigest)), AccessState: []byte("opaque-state-canary"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func accessTestHash(value string) application.KeyedTokenHash {
	digest := sha256.Sum256([]byte(value))
	return application.KeyedTokenHash{KeyID: "key.test", Digest: fmt.Sprintf("%x", digest)}
}

func assertAccessGovernanceRedacted(t *testing.T, store *Store, canaries ...string) {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(context.Background(), store.bind(`SELECT COUNT(*) FROM labcentral_audit_records WHERE category = ?`), application.AuditAccess).Scan(&count); err != nil || count == 0 {
		t.Fatalf("access governance producer count=%d err=%v", count, err)
	}
	rows, err := store.db.QueryContext(context.Background(), `SELECT payload_json FROM labcentral_audit_records
		WHERE aggregate_type = 'access-session' UNION ALL SELECT payload_json FROM labcentral_events WHERE aggregate_type = 'access-session'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		for _, canary := range canaries {
			if bytes.Contains(payload, []byte(canary)) {
				t.Fatalf("access governance payload leaked canary %q", canary)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
