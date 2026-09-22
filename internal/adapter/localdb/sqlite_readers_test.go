package localdb

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
)

func TestSQLiteReadersStayAvailableDuringWrite(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "lab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	setupQuotaTestTenancy(t, s)
	before, err := s.UserByID(ctx, "user.quota")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE labcentral_users SET display_name='Committed name' WHERE user_id='user.quota'`); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	got, err := s.UserByID(readCtx, "user.quota")
	if err != nil || got.DisplayName != before.DisplayName {
		t.Fatalf("read blocked or saw uncommitted data: %v %v", got, err)
	}
	if _, err = s.AuthorizationSnapshot(readCtx, "user.quota", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ListInstances(readCtx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ListProjectGroups(readCtx, "project.alpha", "user.quota", "", 100); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadOperations(readCtx, application.OperationQuery{Limit: 100, Filter: "all"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadOperationalSnapshot(readCtx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got, err = s.UserByID(ctx, "user.quota")
	if err != nil || got.DisplayName != "Committed name" {
		t.Fatalf("committed update not visible: %v %v", got, err)
	}
	// Check two separately held connections, including one opened after startup.
	for i := 0; i < 2; i++ {
		c, e := s.readers.Conn(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer c.Close()
		if _, e = c.ExecContext(ctx, `UPDATE labcentral_users SET disabled=1`); e == nil {
			t.Fatal("reader allowed a mutation")
		}
	}
}

func TestSQLiteFileReaderIdentityAndConsoleContracts(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T, *Store)
	}{
		{"identity", runIdentityConformance}, {"console", consoleControlContract},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, err := Open(context.Background(), filepath.Join(t.TempDir(), "lab.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			test.run(t, s)
		})
	}
}

func TestSQLiteCheckpointDoesNotBorrowApplicationWriter(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "lab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	setupQuotaTestTenancy(t, s)
	writer, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	var automatic, durability int
	if err := writer.QueryRowContext(ctx, "PRAGMA wal_autocheckpoint").Scan(&automatic); err != nil {
		t.Fatal(err)
	}
	if err := writer.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&durability); err != nil {
		t.Fatal(err)
	}
	if automatic != 0 || durability != 2 {
		t.Fatalf("checkpoint/durability: %d/%d", automatic, durability)
	}
	checkCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := s.checkpointWAL(checkCtx); err != nil {
		t.Fatal("checkpoint queued behind application writer", err)
	}
	var busy, pages, copied int
	if err := s.checkpointer.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &pages, &copied); err != nil {
		t.Fatal(err)
	}
	if busy != 0 || pages <= 0 || copied != pages {
		t.Fatalf("checkpoint made no progress: %d %d %d", busy, pages, copied)
	}
	if _, err := s.UserByID(checkCtx, "user.quota"); err != nil {
		t.Fatal(err)
	}
}
