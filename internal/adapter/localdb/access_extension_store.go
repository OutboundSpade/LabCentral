package localdb

import (
	"context"
	"database/sql"
	"github.com/OutboundSpade/LabCentral/internal/adapter/jobsdb"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"strings"
)

// Never edit migration 0011: already published checksums remain authoritative.
func (s *Store) extensibleAccessMigration() []string {
	if s.dialect == jobsdb.DialectPostgreSQL {
		return []string{
			"ALTER TABLE labcentral_access_sessions DROP CONSTRAINT labcentral_access_sessions_method_check",
			"ALTER TABLE labcentral_access_sessions ADD CONSTRAINT labcentral_access_method_length CHECK (length(method) BETWEEN 1 AND 64)",
			"ALTER TABLE labcentral_access_sessions ADD COLUMN delivery_claimed INTEGER NOT NULL DEFAULT 0 CHECK (delivery_claimed IN (0,1))",
		}
	}
	// No table references access_sessions; preserve every existing field and index.
	original := accessSessionMigrationStatements(s.integerType(), s.blobType())
	create := strings.Replace(original[0], "CREATE TABLE labcentral_access_sessions (", "CREATE TABLE labcentral_access_sessions_next (", 1)
	create = strings.Replace(create, "CHECK (method IN ('ssh', 'vnc'))", "CHECK (length(method) BETWEEN 1 AND 64)", 1)
	statements := []string{create, "INSERT INTO labcentral_access_sessions_next SELECT * FROM labcentral_access_sessions", "DROP TABLE labcentral_access_sessions", "ALTER TABLE labcentral_access_sessions_next RENAME TO labcentral_access_sessions"}
	statements = append(statements, original[1:]...)
	return append(statements, "ALTER TABLE labcentral_access_sessions ADD COLUMN delivery_claimed INTEGER NOT NULL DEFAULT 0 CHECK (delivery_claimed IN (0,1))")
}

func (s *Store) ClaimAccessDelivery(ctx context.Context, id domain.AccessSessionID, subject domain.UserID, hashes []application.KeyedTokenHash, ids application.GovernanceRecordIDs) (application.AccessSessionRecord, error) {
	return s.mutateAccessSession(ctx, func(tx *sql.Tx) (application.AccessSessionRecord, error) {
		record, err := s.getAccessSessionTx(ctx, tx, id, true)
		if err != nil {
			return record, err
		}
		now, err := s.databaseNowTx(ctx, tx)
		if err != nil {
			return record, err
		}
		allowed, err := s.accessCurrentlyAuthorizedTx(ctx, tx, record, now)
		if err != nil {
			return record, err
		}
		if !allowed || record.SubjectID != subject || record.State != domain.AccessSessionReady || !record.LaunchExpiresAt.After(now) || !record.ExpiresAt.After(now) || !keyedHashMatches(record.LaunchTokenKeyID, record.LaunchTokenHash, hashes) {
			return record, application.ErrAuthorityChanged
		}
		result, err := tx.ExecContext(ctx, s.bind("UPDATE labcentral_access_sessions SET delivery_claimed=1 WHERE access_session_id=? AND delivery_claimed=0"), id)
		if err != nil {
			return record, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return record, err
		}
		if count != 1 {
			return record, application.ErrAuthorityChanged
		}
		err = s.appendAccessGovernanceTx(ctx, tx, record, ids, "access.credential.delivery", "access-session.delivery-claimed", application.AuditSucceeded, "")
		return record, err
	})
}
