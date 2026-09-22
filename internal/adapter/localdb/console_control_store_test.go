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

func TestSQLiteConsoleControl(t *testing.T) {
	consoleControlContract(t, newSQLiteAccessContractStore(t))
}
func TestPostgreSQLConsoleControl(t *testing.T) {
	s := newPostgreSQLQuotaTestStore(t, "console_control")
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	consoleControlContract(t, s)
}
func consoleControlContract(t *testing.T, s *Store) {
	ctx := context.Background()
	setupQuotaTestTenancy(t, s)
	instance, digest := createReadyAccessTestInstance(t, s, "instance.console")
	sessions := make([]app.AccessSessionRecord, 3)
	tokens := make([]app.KeyedTokenHash, 3)
	sequence := make([]int, 3)
	for i := range sessions {
		name := fmt.Sprint("console-", i)
		r := accessBeginTestRequest(instance, digest, "access."+name, name)
		r.SharedConsole = true
		pending, err := s.BeginAccessSession(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		token := accessTestHash(name + "-launch")
		ready := finalizeAccessTestSession(t, s, pending, digest, token, name)
		tokens[i] = accessTestHash(name + "-lease")
		active, ok, err := s.ExchangeAccessToken(ctx, app.AccessExchangeRequest{GovernanceRecordIDs: governanceIDs(name + "-exchange"), ID: ready.ID, Identity: accessTestIdentity(), TokenHashes: []app.KeyedTokenHash{token}, NewLease: tokens[i], LeaseDuration: app.DefaultAccessLeaseDuration})
		if err != nil || !ok || !active.SharedConsole || active.InputLease != nil {
			t.Fatal("initial connection must watch", err)
		}
		sessions[i] = active
		// A simultaneous viewer must never be displaced by automatic acquisition.
		autoKey := fmt.Sprintf("auto-initial-%d", i)
		automatic, e := s.ChangeConsoleControl(ctx, app.ChangeConsoleControl{ActorID: "user.quota", SessionID: active.ID, Take: true, OnlyIfAlone: true, IdempotencyKey: autoKey, RequestDigest: testRequestDigest(autoKey)})
		if e != nil {
			t.Fatal(e)
		}
		if i == 0 {
			if automatic.RequestedSessionID != active.ID {
				t.Fatal("sole console did not request control")
			}
			_, e = s.ChangeConsoleControl(ctx, app.ChangeConsoleControl{ActorID: "user.quota", SessionID: active.ID, Take: false, IdempotencyKey: "auto-release", RequestDigest: testRequestDigest("auto-release")})
			if e != nil {
				t.Fatal(e)
			}
		} else if automatic.RequestedSessionID != "" {
			t.Fatal("automatic request displaced another viewer")
		}

	}
	renew := func(i int) app.AccessSessionRecord {
		t.Helper()
		sequence[i]++
		name := fmt.Sprintf("console-%d-renew-%d", i, sequence[i])
		next := accessTestHash(name)
		r, ok, err := s.RecheckAccessLease(ctx, app.AccessLeaseRecheckRequest{GovernanceRecordIDs: governanceIDs(name), ID: sessions[i].ID, Identity: accessTestIdentity(), LeaseHashes: []app.KeyedTokenHash{tokens[i]}, NewLease: next, LeaseDuration: app.DefaultAccessLeaseDuration})
		if err != nil || !ok {
			t.Fatal("renew", ok, err)
		}
		if !r.LeaseRetained {
			tokens[i] = next
		}
		return r
	}
	// A watching client retains its original bounded lease without a write,
	// even while the SQLite writer connection is occupied by unrelated work.
	if s.readers != nil {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE labcentral_users SET display_name='pending' WHERE user_id='user.quota'`); err != nil {
			t.Fatal(err)
		}
		oldCtx := ctx
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
		r := renew(2)
		cancel()
		ctx = oldCtx
		if err = tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if !r.LeaseRetained {
			t.Fatal("viewer waited for writer")
		}
	}
	r := renew(2)
	if !r.LeaseRetained || r.InputLease != nil || r.ResourceVersion != sessions[2].ResourceVersion || r.LeaseHash != sessions[2].LeaseHash || !r.LeaseExpiresAt.Equal(sessions[2].LeaseExpiresAt) {
		t.Fatal("spectator recheck mutated or extended its lease")
	}
	if _, err := s.db.ExecContext(ctx, s.bind(`UPDATE labcentral_access_sessions SET lease_expires_at_us=? WHERE access_session_id=?`), time.Now().Add(6*time.Second).UnixMicro(), sessions[2].ID); err != nil {
		t.Fatal(err)
	}
	if r = renew(2); r.LeaseRetained || r.LeaseHash == sessions[2].LeaseHash {
		t.Fatal("due lease failed to rotate")
	}
	change := func(i int, take bool, key string) (app.ConsolePresence, error) {
		return s.ChangeConsoleControl(ctx, app.ChangeConsoleControl{ActorID: "user.quota", SessionID: sessions[i].ID, Take: take, IdempotencyKey: key, RequestDigest: testRequestDigest(key)})
	}
	expireInput := func() {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, `UPDATE labcentral_console_control SET lease_until_us=1`); err != nil {
			t.Fatal(err)
		}
	}
	presence, err := s.ConsolePresence(ctx, "user.quota", sessions[0].ID)
	if err != nil || len(presence.Participants) != 3 || presence.DriverSessionID != "" {
		t.Fatal("shared presence", presence, err)
	}
	if _, err = s.ConsolePresence(ctx, "user.other", sessions[0].ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatal("foreign presence", err)
	}
	if _, err = change(0, true, "take-first-driver"); err != nil {
		t.Fatal(err)
	}
	first := renew(0)
	if first.LeaseRetained || first.InputLease == nil || first.InputLease.Fence != 1 || first.InputLease.ValidFor > app.ConsoleInputLeaseDuration {
		t.Fatal("driver grant", first.InputLease)
	}
	retainedDriver := renew(0)
	if !retainedDriver.LeaseRetained || retainedDriver.ResourceVersion != first.ResourceVersion || retainedDriver.InputLease == nil || retainedDriver.InputLease.Fence != first.InputLease.Fence || retainedDriver.InputLease.ValidFor > first.InputLease.ValidFor {
		t.Fatal("driver observation extended or rewrote input authority")
	}
	if r := renew(1); r.InputLease != nil {
		t.Fatal("viewer received input")
	}
	if _, err = change(1, true, "take-second-driver"); err != nil {
		t.Fatal(err)
	}
	if r := renew(0); r.InputLease != nil {
		t.Fatal("old driver renewed during handoff")
	}
	if r := renew(1); r.InputLease != nil {
		t.Fatal("new driver activated before withdrawal/expiry")
	}
	expireInput()
	second := renew(1)
	if second.InputLease == nil || second.InputLease.Fence <= first.InputLease.Fence {
		t.Fatal("fenced takeover", second.InputLease)
	}
	// Expiry releases control; a returning callback cannot silently regain it.
	expireInput()
	if r := renew(1); r.InputLease != nil {
		t.Fatal("expired control restored automatically")
	}
	if _, err = change(1, true, "take-after-expiry"); err != nil {
		t.Fatal(err)
	}
	if r := renew(1); r.InputLease == nil {
		t.Fatal("explicit reacquire failed")
	}
	if _, err = change(1, false, "release-driver"); err != nil {
		t.Fatal(err)
	}
	if r := renew(1); r.InputLease != nil {
		t.Fatal("released driver renewed")
	}
	if _, err = change(1, true, "take-after-expiry"); err != nil {
		t.Fatal(err)
	}
	if r := renew(1); r.InputLease != nil {
		t.Fatal("replay resurrected control")
	}
	expireInput()
	// Competing takeovers serialize to one desired driver, and renewals cannot
	// create two input grants even when they race on different access rows.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, errs[i] = change(i, true, fmt.Sprint("take-race-", i)) }(i)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	presence, err = s.ConsolePresence(ctx, "user.quota", sessions[0].ID)
	if err != nil || presence.RequestedSessionID == "" {
		t.Fatal(err)
	}
	grants := make([]app.AccessSessionRecord, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); grants[i] = renew(i) }(i)
	}
	wg.Wait()
	winners := 0
	driver := 0
	for i, g := range grants {
		if g.InputLease != nil {
			winners++
			driver = i
			if g.ID != presence.RequestedSessionID {
				t.Fatal("wrong driver")
			}
		}
	}
	if winners != 1 {
		t.Fatal("simultaneous drivers", winners)
	}
	if _, ended, err := s.EndAccessLease(ctx, app.EndAccessLeaseRequest{GovernanceRecordIDs: governanceIDs("console-end"), ID: sessions[driver].ID, Identity: accessTestIdentity(), LeaseHashes: []app.KeyedTokenHash{tokens[driver]}, Reason: "client_closed"}); err != nil || !ended {
		t.Fatal("disconnect", ended, err)
	}
	if _, err = change(driver, true, "take-disconnected"); !errors.Is(err, app.ErrAuthorityChanged) {
		t.Fatal("disconnected session took control", err)
	}
	// A downgraded plugin cannot bypass an established shared resource boundary.
	legacy := accessBeginTestRequest(instance, digest, "access.downgrade", "downgrade")
	if _, err = s.BeginAccessSession(ctx, legacy); !errors.Is(err, app.ErrAuthorityChanged) {
		t.Fatal("legacy input bypass", err)
	}
	if _, err = s.db.ExecContext(ctx, s.bind(`UPDATE labcentral_local_instances SET generation=generation+1 WHERE instance_id=?`), instance.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = change(2, true, "take-stale-generation"); !errors.Is(err, app.ErrAuthorityChanged) {
		t.Fatal("stale VM generation", err)
	}
	// A valid spectator token never bypasses a changed resource generation.
	_, allowed, err := s.RecheckAccessLease(ctx, app.AccessLeaseRecheckRequest{GovernanceRecordIDs: governanceIDs("stale-viewer"), ID: sessions[2].ID, Identity: accessTestIdentity(), LeaseHashes: []app.KeyedTokenHash{tokens[2]}, NewLease: accessTestHash("stale-viewer"), LeaseDuration: app.DefaultAccessLeaseDuration})
	if err != nil || allowed {
		t.Fatal("stale spectator remained authorized", err)
	}
	var audits int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM labcentral_audit_records WHERE action='console.control.activated'`).Scan(&audits); err != nil || audits < 3 {
		t.Fatal("control audit", audits, err)
	}
}
