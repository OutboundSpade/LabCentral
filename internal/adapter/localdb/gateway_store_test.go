package localdb

import (
	"context"
	"errors"
	"fmt"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"sync"
	"testing"
	"time"
)

func TestSQLiteGatewayCodesAndGrants(t *testing.T) {
	gatewayContract(t, newSQLiteAccessContractStore(t))
}
func TestPostgreSQLGatewayCodesAndGrants(t *testing.T) {
	s := newPostgreSQLQuotaTestStore(t, "gateway")
	if e := s.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	gatewayContract(t, s)
}
func gatewayContract(t *testing.T, s *Store) {
	ctx := context.Background()
	if e := s.PutUser(ctx, app.UserRecord{ID: "user.gateway", Username: "gateway-user", DisplayName: "Gateway user"}); e != nil {
		t.Fatal(e)
	}
	client := app.GatewayClient{ID: "gateway.fixture", Name: "Fixture", SecretHash: accessTestHash("client")}
	if e := s.CreateGatewayClient(ctx, client, "user.gateway", governanceIDs("client")); e != nil {
		t.Fatal(e)
	}
	device, e := s.CreateGatewayDevice(ctx, app.GatewayDevice{ID: "device.fixture", ClientID: client.ID, Scope: app.GatewayScope{ProjectID: "project.local", Actions: []string{"templates.read"}}, DeviceHash: accessTestHash("device"), UserCodeHash: accessTestHash("code")})
	if e != nil {
		t.Fatal(e)
	}
	if remaining := time.Until(device.ExpiresAt); remaining > 5*time.Minute || remaining < 4*time.Minute {
		t.Fatal("invalid request lifetime")
	}
	grant := app.GatewayGrant{ID: "grant.fixture", TokenHash: accessTestHash("grant")}
	if _, e = s.PollGatewayDevice(ctx, client.ID, []app.KeyedTokenHash{accessTestHash("device")}, grant, governanceIDs("too-fast")); !errors.Is(e, app.ErrGatewaySlowDown) {
		t.Fatalf("poll rate: %v", e)
	}
	var interval int
	if e = s.db.QueryRowContext(ctx, "SELECT poll_seconds FROM labcentral_gateway_devices WHERE device_id='device.fixture'").Scan(&interval); e != nil || interval != 10 {
		t.Fatalf("slowdown not durable: %d %v", interval, e)
	}
	if e = s.ApproveGatewayDevice(ctx, []app.KeyedTokenHash{accessTestHash("code")}, "user.gateway", true, governanceIDs("approve")); e != nil {
		t.Fatal(e)
	}
	if e = s.ApproveGatewayDevice(ctx, []app.KeyedTokenHash{accessTestHash("code")}, "user.gateway", true, governanceIDs("approve-replay")); !errors.Is(e, app.ErrGatewayConsumed) {
		t.Fatalf("approval replay: %v", e)
	}
	if _, e = s.db.ExecContext(ctx, "UPDATE labcentral_gateway_devices SET next_poll_us=0"); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var grants []app.GatewayGrant
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			g, e := s.PollGatewayDevice(ctx, client.ID, []app.KeyedTokenHash{accessTestHash("device")}, grant, governanceIDs(fmt.Sprintf("poll-%d", i)))
			if e == nil {
				mu.Lock()
				grants = append(grants, g)
				mu.Unlock()
			} else if !errors.Is(e, app.ErrGatewayConsumed) {
				t.Errorf("concurrent poll: %v", e)
			}
		}(i)
	}
	wg.Wait()
	if len(grants) != 1 {
		t.Fatalf("grant winners: %d", len(grants))
	}
	if g := grants[0]; g.SubjectID != "user.gateway" || time.Until(g.ExpiresAt) > 15*time.Minute {
		t.Fatalf("invalid grant: %+v", g)
	}
	if _, e = s.AuthenticateGatewayGrant(ctx, "other", []app.KeyedTokenHash{accessTestHash("grant")}); e == nil {
		t.Fatal("cross-client grant")
	}
	if _, e = s.AuthenticateGatewayGrant(ctx, client.ID, []app.KeyedTokenHash{accessTestHash("grant")}); e != nil {
		t.Fatal(e)
	}
	if e = s.RevokeGatewayGrant(ctx, grant.ID, "user.other", governanceIDs("cross-revoke")); e == nil {
		t.Fatal("cross-user revocation")
	}
	if e = s.RevokeGatewayGrant(ctx, grant.ID, "user.gateway", governanceIDs("revoke")); e != nil {
		t.Fatal(e)
	}
	if _, e = s.AuthenticateGatewayGrant(ctx, client.ID, []app.KeyedTokenHash{accessTestHash("grant")}); e == nil {
		t.Fatal("revoked grant usable")
	}
	if _, e = s.db.ExecContext(ctx, "UPDATE labcentral_gateway_devices SET expires_at_us=1"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.InspectGatewayDevice(ctx, []app.KeyedTokenHash{accessTestHash("code")}, "user.gateway"); !errors.Is(e, app.ErrGatewayExpired) {
		t.Fatalf("expired code: %v", e)
	}
	// Unknown code attempts are committed, not rolled back with the denial.
	for i := 0; i < 22; i++ {
		_, e = s.InspectGatewayDevice(ctx, []app.KeyedTokenHash{accessTestHash("wrong")}, "user.gateway")
	}
	if !errors.Is(e, app.ErrGatewaySlowDown) {
		t.Fatalf("code brute force not limited: %v", e)
	}
}

func TestSQLiteGatewayRevocationClosesDelegatedAccess(t *testing.T) {
	for _, deletion := range []bool{false, true} {
		t.Run(fmt.Sprint(deletion), func(t *testing.T) { gatewayAccessContract(t, newSQLiteAccessContractStore(t), deletion) })
	}
}
func TestPostgreSQLGatewayRevocationClosesDelegatedAccess(t *testing.T) {
	s := newPostgreSQLQuotaTestStore(t, "gateway_access")
	if e := s.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	gatewayAccessContract(t, s, true)
}
func gatewayAccessContract(t *testing.T, s *Store, deletion bool) {
	ctx := context.Background()
	setupQuotaTestTenancy(t, s)
	c := app.GatewayClient{ID: "gateway.access", Name: "Access gateway", SecretHash: accessTestHash("client")}
	if e := s.CreateGatewayClient(ctx, c, "user.quota", governanceIDs("ga-client")); e != nil {
		t.Fatal(e)
	}
	d, e := s.CreateGatewayDevice(ctx, app.GatewayDevice{ID: "device.access", ClientID: c.ID, Scope: app.GatewayScope{ProjectID: "project.alpha", Actions: []string{"access.create"}}, DeviceHash: accessTestHash("device-access"), UserCodeHash: accessTestHash("code-access")})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.ApproveGatewayDevice(ctx, []app.KeyedTokenHash{accessTestHash("code-access")}, "user.quota", true, governanceIDs("ga-approve")); e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.ExecContext(ctx, s.bind("UPDATE labcentral_gateway_devices SET next_poll_us=0 WHERE device_id=?"), d.ID); e != nil {
		t.Fatal(e)
	}
	grant, e := s.PollGatewayDevice(ctx, c.ID, []app.KeyedTokenHash{accessTestHash("device-access")}, app.GatewayGrant{ID: "grant.access", TokenHash: accessTestHash("ga-token")}, governanceIDs("ga-issue"))
	if e != nil {
		t.Fatal(e)
	}
	instance, digest := createReadyAccessTestInstance(t, s, "instance.gateway")
	request := accessBeginTestRequest(instance, digest, "access.gateway", "gateway")
	request.GatewayGrantID = grant.ID
	request.RequestedDuration = 30 * time.Minute
	pending, e := s.BeginAccessSession(ctx, request)
	if e != nil {
		t.Fatal(e)
	}
	if pending.ExpiresAt.After(grant.ExpiresAt) {
		t.Fatal("access outlives grant")
	}
	ready := finalizeAccessTestSession(t, s, pending, digest, accessTestHash("ga-launch"), "gateway")
	if _, allowed, e := s.ExchangeAccessToken(ctx, app.AccessExchangeRequest{GovernanceRecordIDs: governanceIDs("ga-exchange"), ID: ready.ID, Identity: accessTestIdentity(), TokenHashes: []app.KeyedTokenHash{accessTestHash("ga-launch")}, NewLease: accessTestHash("ga-lease"), LeaseDuration: app.DefaultAccessLeaseDuration}); e != nil || !allowed {
		t.Fatalf("exchange %v %v", allowed, e)
	}
	if deletion {
		e = s.DeleteGatewayClient(ctx, c.ID, "user.quota", governanceIDs("ga-delete"))
	} else {
		e = s.RevokeGatewayGrant(ctx, grant.ID, "user.quota", governanceIDs("ga-revoke"))
	}
	if e != nil {
		t.Fatal(e)
	}
	denied, allowed, e := s.RecheckAccessLease(ctx, app.AccessLeaseRecheckRequest{GovernanceRecordIDs: governanceIDs("ga-recheck"), ID: ready.ID, Identity: accessTestIdentity(), LeaseHashes: []app.KeyedTokenHash{accessTestHash("ga-lease")}, NewLease: accessTestHash("ga-new-lease"), LeaseDuration: app.DefaultAccessLeaseDuration})
	if e != nil || allowed || !denied.ClosureRequired {
		t.Fatalf("revoked gateway lease allowed=%v closure=%v err=%v", allowed, denied.ClosureRequired, e)
	}
}

func TestSQLiteGatewayDeleteRacesIssuance(t *testing.T) {
	gatewayDeletionContract(t, newSQLiteAccessContractStore(t))
}
func TestPostgreSQLGatewayDeleteRacesIssuance(t *testing.T) {
	s := newPostgreSQLQuotaTestStore(t, "gateway_delete")
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	gatewayDeletionContract(t, s)
}
func gatewayDeletionContract(t *testing.T, s *Store) {
	ctx := context.Background()
	setupQuotaTestTenancy(t, s)
	c := app.GatewayClient{ID: "gateway.delete", Name: "Delete", SecretHash: accessTestHash("delete")}
	if err := s.CreateGatewayClient(ctx, c, "user.quota", governanceIDs("delete-create")); err != nil {
		t.Fatal(err)
	}
	d := app.GatewayDevice{ID: "device.delete", ClientID: c.ID, Scope: app.GatewayScope{ProjectID: "project.alpha", Actions: []string{"templates.read"}}, DeviceHash: accessTestHash("delete-device"), UserCodeHash: accessTestHash("delete-code")}
	if _, err := s.CreateGatewayDevice(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveGatewayDevice(ctx, []app.KeyedTokenHash{d.UserCodeHash}, "user.quota", true, governanceIDs("delete-approve")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE labcentral_gateway_devices SET next_poll_us=0"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := s.PollGatewayDevice(ctx, c.ID, []app.KeyedTokenHash{d.DeviceHash}, app.GatewayGrant{ID: "grant.delete", TokenHash: accessTestHash("delete-grant")}, governanceIDs("delete-poll"))
		if err != nil && !errors.Is(err, app.ErrGatewayExpired) {
			t.Errorf("poll/delete race: %v", err)
		}
	}()
	if err := s.DeleteGatewayClient(ctx, c.ID, "user.quota", governanceIDs("delete")); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if err := s.DeleteGatewayClient(ctx, c.ID, "user.quota", governanceIDs("delete-replay")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GatewayClient(ctx, c.ID); !errors.Is(err, app.ErrGatewayInvalid) {
		t.Fatal("deleted client authenticated", err)
	}
	if _, err := s.CreateGatewayDevice(ctx, d); !errors.Is(err, app.ErrGatewayInvalid) {
		t.Fatal("deleted client issued a device request", err)
	}
	if _, err := s.AuthenticateGatewayGrant(ctx, c.ID, []app.KeyedTokenHash{accessTestHash("delete-grant")}); err == nil {
		t.Fatal("grant survived deletion")
	}
	if err := s.ApproveGatewayDevice(ctx, []app.KeyedTokenHash{d.UserCodeHash}, "user.quota", true, governanceIDs("delete-late-approval")); err == nil {
		t.Fatal("approval survived deletion")
	}
	for _, table := range []string{"labcentral_gateway_clients", "labcentral_gateway_devices", "labcentral_gateway_grants"} {
		var count int
		if err := s.db.QueryRowContext(ctx, s.bind("SELECT COUNT(*) FROM "+table+" WHERE client_id=?"), c.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("credentials retained in %s: %d %v", table, count, err)
		}
	}
}
