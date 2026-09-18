package localdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/adapter/jobsdb"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func libraryMigration() []string {
	return []string{
		`CREATE TABLE labcentral_template_libraries(library_id TEXT PRIMARY KEY,project_id TEXT NOT NULL REFERENCES labcentral_projects(project_id),author_id TEXT NOT NULL,binding TEXT NOT NULL,reference TEXT NOT NULL,interval_seconds BIGINT NOT NULL CHECK(interval_seconds BETWEEN 60 AND 86400),paused INTEGER NOT NULL DEFAULT 0 CHECK(paused IN(0,1)),pause_reason TEXT NOT NULL DEFAULT '',next_sync_at_us BIGINT NOT NULL,resource_version BIGINT NOT NULL DEFAULT 1,UNIQUE(project_id,binding,reference))`,
		`CREATE TABLE labcentral_library_entries(library_id TEXT NOT NULL REFERENCES labcentral_template_libraries(library_id),entry_key TEXT NOT NULL,template_id TEXT NOT NULL UNIQUE REFERENCES labcentral_lab_templates(template_id),revision_id TEXT NOT NULL REFERENCES labcentral_template_revisions(revision_id),available INTEGER NOT NULL CHECK(available IN(0,1)),commit_id TEXT NOT NULL,PRIMARY KEY(library_id,entry_key))`,
		`CREATE TABLE labcentral_library_syncs(operation_id TEXT PRIMARY KEY REFERENCES labcentral_extension_work(operation_id),library_id TEXT NOT NULL REFERENCES labcentral_template_libraries(library_id),commit_id TEXT NOT NULL DEFAULT '',published INTEGER NOT NULL DEFAULT 0 CHECK(published IN(0,1)))`,
		`CREATE INDEX labcentral_library_due ON labcentral_template_libraries(paused,next_sync_at_us)`,
	}
}

const libraryColumns = `library_id,project_id,author_id,binding,reference,interval_seconds,paused,pause_reason,next_sync_at_us,resource_version`

func scanLibrary(row scanner) (app.TemplateLibrary, error) {
	var l app.TemplateLibrary
	var paused int
	var next int64
	err := row.Scan(&l.ID, &l.ProjectID, &l.AuthorID, &l.Binding, &l.Reference, &l.IntervalSeconds, &paused, &l.PauseReason, &next, &l.ResourceVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return l, app.ErrNotFound
	}
	l.Paused = paused == 1
	l.NextSyncAt = time.UnixMicro(next).UTC()
	return l, err
}
func (s *Store) GetLibrary(ctx context.Context, id string) (app.TemplateLibrary, error) {
	return scanLibrary(s.db.QueryRowContext(ctx, s.bind(`SELECT `+libraryColumns+` FROM labcentral_template_libraries WHERE library_id=?`), id))
}
func (s *Store) listLibraries(ctx context.Context, where string, args ...any) ([]app.TemplateLibrary, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+libraryColumns+` FROM labcentral_template_libraries WHERE `+where+` ORDER BY library_id LIMIT 100`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []app.TemplateLibrary{}
	for rows.Next() {
		l, err := scanLibrary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
func (s *Store) ListLibraries(ctx context.Context, p domain.ProjectID) ([]app.TemplateLibrary, error) {
	return s.listLibraries(ctx, `project_id=?`, p)
}
func (s *Store) DueLibraries(ctx context.Context) ([]app.TemplateLibrary, error) {
	return s.listLibraries(ctx, `paused=0 AND next_sync_at_us<=`+s.nowMicros()+` AND NOT EXISTS(SELECT 1 FROM labcentral_library_syncs ls JOIN labcentral_jobs j ON j.operation_id=ls.operation_id WHERE ls.library_id=labcentral_template_libraries.library_id AND j.state IN('queued','running'))`)
}
func (s *Store) CreateLibrary(ctx context.Context, r app.CreateLibrary) (out app.TemplateLibrary, err error) {
	l := r.Library
	if _, err = domain.NewTemplateID(l.ID); err != nil {
		return out, err
	}
	if l.IntervalSeconds < 60 || l.IntervalSeconds > 86400 || len(l.Binding) == 0 || len(l.Binding) > 257 || len(l.Reference) == 0 || len(l.Reference) > 1024 {
		return out, fmt.Errorf("invalid library")
	}
	if err = validateTemplateIdempotency(r.IdempotencyKey, r.RequestDigest); err != nil {
		return out, err
	}
	err = s.workTx(ctx, func(tx *sql.Tx) error {
		if err := s.lockIdempotency(ctx, tx, l.AuthorID, "library.create", r.IdempotencyKey); err != nil {
			return err
		}
		if existing, found, err := s.findIdempotency(ctx, tx, l.AuthorID, "library.create", r.IdempotencyKey, r.RequestDigest); err != nil {
			return err
		} else if found {
			out, err = scanLibrary(tx.QueryRowContext(ctx, s.bind(`SELECT `+libraryColumns+` FROM labcentral_template_libraries WHERE library_id=?`), existing))
			return err
		}
		if err := s.requireExtensionAuthorTx(ctx, tx, l.ProjectID, l.AuthorID); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM labcentral_template_libraries WHERE project_id=?`), l.ProjectID).Scan(&count); err != nil {
			return err
		}
		if count >= 100 {
			return fmt.Errorf("library limit reached")
		}
		_, err := tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_template_libraries(library_id,project_id,author_id,binding,reference,interval_seconds,next_sync_at_us) VALUES(?,?,?,?,?,?,`+s.nowMicros()+`)`), l.ID, l.ProjectID, l.AuthorID, l.Binding, l.Reference, l.IntervalSeconds)
		if err != nil && isUniqueViolation(err) {
			return app.ErrAlreadyExists
		}
		if err != nil {
			return err
		}
		if err = s.bindIdempotency(ctx, tx, l.AuthorID, "library.create", r.IdempotencyKey, r.RequestDigest, l.ID); err != nil {
			return err
		}
		if err = s.appendExtensionGovernance(ctx, tx, app.ExtensionWork{ID: domain.OperationID(l.ID), ProjectID: l.ProjectID, ActorID: l.AuthorID}, r.GovernanceRecordIDs, "library.created"); err != nil {
			return err
		}
		out, err = scanLibrary(tx.QueryRowContext(ctx, s.bind(`SELECT `+libraryColumns+` FROM labcentral_template_libraries WHERE library_id=?`), l.ID))
		return err
	})
	return
}
func (s *Store) SetLibraryPaused(ctx context.Context, id string, actor domain.UserID, paused bool, reason string, ids app.GovernanceRecordIDs) error {
	if reason != "" && reason != "authority_revoked" && reason != "operator" {
		return fmt.Errorf("invalid pause reason")
	}
	return s.workTx(ctx, func(tx *sql.Tx) error {
		l, err := scanLibrary(tx.QueryRowContext(ctx, s.bind(`SELECT `+libraryColumns+` FROM labcentral_template_libraries WHERE library_id=?`), id))
		if err != nil {
			return err
		}
		// Automatic revocation records the original actor without requiring revoked authority.
		if reason != "authority_revoked" {
			if err = s.requireExtensionAuthorTx(ctx, tx, l.ProjectID, actor); err != nil {
				return err
			}
		}
		value := 0
		if paused {
			value = 1
		}
		if _, err = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_template_libraries SET paused=?,pause_reason=?,resource_version=resource_version+1 WHERE library_id=?`), value, reason, id); err != nil {
			return err
		}
		return s.appendExtensionGovernance(ctx, tx, app.ExtensionWork{ID: domain.OperationID(id), ProjectID: l.ProjectID, ActorID: actor}, ids, "library.schedule_changed")
	})
}
func (s *Store) LibraryEntries(ctx context.Context, id string) ([]app.LibraryEntry, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT entry_key,template_id,revision_id,available,commit_id FROM labcentral_library_entries WHERE library_id=? ORDER BY entry_key`), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []app.LibraryEntry{}
	for rows.Next() {
		var entry app.LibraryEntry
		var available int
		if err = rows.Scan(&entry.Key, &entry.TemplateID, &entry.RevisionID, &available, &entry.Commit); err != nil {
			return nil, err
		}
		entry.Available = available == 1
		out = append(out, entry)
	}
	return out, rows.Err()
}
func (s *Store) LibraryHistory(ctx context.Context, id string) ([]app.ExtensionWork, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+workColumns+` FROM labcentral_extension_work w JOIN labcentral_jobs j ON j.operation_id=w.operation_id JOIN labcentral_library_syncs ls ON ls.operation_id=w.operation_id WHERE ls.library_id=? ORDER BY w.created_at_us DESC,w.operation_id LIMIT 100`), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []app.ExtensionWork{}
	for rows.Next() {
		work, err := scanExtensionWork(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, work)
	}
	return out, rows.Err()
}
func (s *Store) requireIndependentTemplateTx(ctx context.Context, tx *sql.Tx, id domain.TemplateID) error {
	var count int
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM labcentral_library_entries WHERE template_id=?`), id).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return app.ErrTemplateManaged
	}
	return nil
}

var libraryCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)
var libraryKey = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func (s *Store) PublishLibrary(ctx context.Context, r app.LibraryPublishRequest) error {
	if !libraryCommit.MatchString(r.Commit) || len(r.Entries) > 32 {
		return fmt.Errorf("invalid collection publication")
	}
	c := r.Claim
	fence := app.Fence{OperationID: c.Work.ID, LeaseOwner: c.LeaseOwner, Token: c.Operation.FencingToken, Generation: c.Operation.Generation, Attempt: c.Operation.Attempt}
	return s.workTx(ctx, func(tx *sql.Tx) error {
		if err := s.requireExtensionFenceTx(ctx, tx, fence); err != nil {
			return err
		}
		lock := ""
		if s.dialect == jobsdb.DialectPostgreSQL {
			lock = " FOR UPDATE"
		}
		l, err := scanLibrary(tx.QueryRowContext(ctx, s.bind(`SELECT `+libraryColumns+` FROM labcentral_template_libraries WHERE library_id=?`+lock), c.Work.Request.LibraryID))
		if err != nil {
			return err
		}
		if l.ProjectID != c.Work.ProjectID || l.AuthorID != c.Work.ActorID {
			return app.ErrNotFound
		}
		if err = s.requireExtensionAuthorTx(ctx, tx, l.ProjectID, l.AuthorID); err != nil {
			return err
		}
		var published int
		if err = tx.QueryRowContext(ctx, s.bind(`SELECT published FROM labcentral_library_syncs WHERE operation_id=? AND library_id=?`), c.Work.ID, l.ID).Scan(&published); err != nil {
			return err
		}
		if published == 1 {
			return nil
		}
		if _, err = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_library_entries SET available=0 WHERE library_id=?`), l.ID); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, entry := range r.Entries {
			if !libraryKey.MatchString(entry.Key) || seen[entry.Key] {
				return fmt.Errorf("invalid collection entry")
			}
			seen[entry.Key] = true
			var tid domain.TemplateID
			var prior domain.RevisionID
			err = tx.QueryRowContext(ctx, s.bind(`SELECT template_id,revision_id FROM labcentral_library_entries WHERE library_id=? AND entry_key=?`), l.ID, entry.Key).Scan(&tid, &prior)
			if errors.Is(err, sql.ErrNoRows) {
				create := entry.Template
				create.ProjectID = l.ProjectID
				create.CreatedBy = l.AuthorID
				if err = validateCreateLabTemplate(create); err != nil {
					return err
				}
				record, err := s.createLabTemplateTx(ctx, tx, create)
				if err != nil {
					return err
				}
				tid = record.ID
			} else if err != nil {
				return err
			}
			// Reuse any retained revision with identical source, including reintroduced entries.
			var rid domain.RevisionID
			err = tx.QueryRowContext(ctx, s.bind(`SELECT revision_id FROM labcentral_template_revisions WHERE template_id=? AND bundle_digest=? ORDER BY revision_number LIMIT 1`), tid, entry.Revision.BundleDigest.Hex()).Scan(&rid)
			if errors.Is(err, sql.ErrNoRows) {
				revision := entry.Revision
				revision.TemplateID = tid
				revision.PublishedBy = l.AuthorID
				revision.WorkFence = &fence
				if err = validatePublishTemplateRevision(revision); err != nil {
					return err
				}
				record, err := s.publishTemplateRevisionTx(ctx, tx, revision)
				if err != nil {
					return err
				}
				rid = record.ID
			} else if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_lab_templates SET name=?,description=?,updated_at_us=`+s.nowMicros()+`,resource_version=resource_version+1 WHERE template_id=? AND (name<>? OR description<>?)`), entry.Template.Name, entry.Template.Description, tid, entry.Template.Name, entry.Template.Description); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, s.bind(`INSERT INTO labcentral_library_entries(library_id,entry_key,template_id,revision_id,available,commit_id) VALUES(?,?,?,?,1,?) ON CONFLICT(library_id,entry_key) DO UPDATE SET revision_id=excluded.revision_id,available=1,commit_id=excluded.commit_id`), l.ID, entry.Key, tid, rid, r.Commit); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, s.bind(`UPDATE labcentral_library_syncs SET published=1,commit_id=? WHERE operation_id=?`), r.Commit, c.Work.ID)
		return err
	})
}

func (s *Store) requireLibraryPublicationTx(ctx context.Context, tx *sql.Tx, id domain.TemplateID, fence *app.Fence) error {
	var library string
	err := tx.QueryRowContext(ctx, s.bind(`SELECT library_id FROM labcentral_library_entries WHERE template_id=?`), id).Scan(&library)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if fence == nil {
		return app.ErrTemplateManaged
	}
	var count int
	if err = tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM labcentral_library_syncs WHERE library_id=? AND operation_id=?`), library, fence.OperationID).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return app.ErrTemplateManaged
	}
	return nil
}

func (s *Store) DeferLibraryWork(ctx context.Context, c app.ExtensionWorkClaim, delay time.Duration) error {
	if c.Work.Request.LibraryID == "" || delay < time.Second || delay > time.Hour {
		return fmt.Errorf("invalid library retry")
	}
	return s.workTx(ctx, func(tx *sql.Tx) error {
		if err := s.requireExtensionFenceTx(ctx, tx, app.Fence{OperationID: c.Work.ID, LeaseOwner: c.LeaseOwner, Token: c.Operation.FencingToken, Generation: c.Operation.Generation, Attempt: c.Operation.Attempt}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_jobs SET state='queued',lease_until_us=0,available_at_us=`+s.nowMicros()+`+? WHERE operation_id=?`), delay.Microseconds(), c.Work.ID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, s.bind(`UPDATE labcentral_extension_binding_leases SET lease_until_us=`+s.nowMicros()+`+? WHERE binding_key=? AND operation_id=?`), delay.Microseconds(), c.Work.Request.PluginInstance+"/"+c.Work.Request.BindingID, c.Work.ID)
		return err
	})
}
