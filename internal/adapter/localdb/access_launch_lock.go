package localdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"sync"

	"github.com/OutboundSpade/LabCentral/internal/adapter/jobsdb"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

// LockAccessLaunch serializes discovery through upstream authentication for one
// resource. It never occupies SQLite's writer or locks an authority row.
func (s *Store) LockAccessLaunch(ctx context.Context, instance domain.InstanceID, resource domain.LogicalResourceID) (func(), error) {
	key := sha256.Sum256([]byte("labcentral.access-launch/v1\x00" + string(instance) + "\x00" + string(resource)))
	if s.dialect == jobsdb.DialectPostgreSQL {
		// A transaction-scoped advisory lock releases on cancellation or process loss;
		// no data locks or writes are held across the external handshake.
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", int64(binary.BigEndian.Uint64(key[:8]))); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		return func() { _ = tx.Rollback() }, nil
	}
	// SQLite's process ownership lock guarantees there is one active Store.
	s.accessLaunchOnce.Do(func() {
		for i := range s.accessLaunchSlots {
			s.accessLaunchSlots[i] = make(chan struct{}, 1)
		}
	})
	slot := s.accessLaunchSlots[key[0]]
	select {
	case slot <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-slot }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
