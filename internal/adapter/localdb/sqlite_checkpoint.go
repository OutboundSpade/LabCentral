package localdb

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"time"
)

// Keep database-page checkpoint I/O off the single application writer. FULL
// still syncs every committed WAL transaction; PASSIVE does not wait for readers
// or acquire the write transaction lock. Only this connection checkpoints.
func (s *Store) startCheckpointer(path string) error {
	query := url.Values{"mode": {"rw"}, "_pragma": {"busy_timeout(0)", "synchronous(FULL)", "wal_autocheckpoint(0)"}}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String())
	if err != nil {
		return fmt.Errorf("open SQLite checkpointer: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return fmt.Errorf("connect SQLite checkpointer: %w", err)
	}
	s.checkpointer = db
	ctx, cancel := context.WithCancel(context.Background())
	s.checkpointCancel, s.checkpointDone = cancel, make(chan struct{})
	go func() {
		defer close(s.checkpointDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.checkpointWAL(ctx); err != nil && ctx.Err() == nil {
					slog.Warn("SQLite background WAL checkpoint failed", "error", err)
				}
			}
		}
	}()
	return nil
}

func (s *Store) checkpointWAL(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := s.checkpointer.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)")
	return err
}
