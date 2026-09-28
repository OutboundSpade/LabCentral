package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"
)

// Maintain is a local operator entry point. It queues the same durable operations
// as the authenticated API; it does not bypass provider ownership checks.
func Maintain(ctx context.Context, root, operation string) (any, error) {
	st, e := OpenStore(filepath.Join(root, "hosted"))
	if e != nil {
		return nil, e
	}
	defer st.Close()
	if operation == "status" {
		m, e := st.Meeting(ctx)
		if e != nil {
			return nil, e
		}
		jobs, e := st.Jobs(ctx)
		return struct {
			Meeting *Meeting `json:"meeting"`
			Jobs    []Job    `json:"jobs"`
		}{m, jobs}, e
	}
	tx, e := st.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	switch operation {
	case "recover":
		_, e = tx.ExecContext(ctx, `UPDATE jobs SET status='queued' WHERE status='failed' AND NOT EXISTS (SELECT 1 FROM jobs active WHERE active.kind=jobs.kind AND active.target=jobs.target AND active.status IN ('queued','running')) AND id IN (SELECT id FROM jobs j WHERE j.status='failed' AND j.created=(SELECT MAX(j2.created) FROM jobs j2 WHERE j2.kind=j.kind AND j2.target=j.target))`)
	case "cleanup":
		var b []byte
		e = tx.QueryRowContext(ctx, `SELECT body FROM objects WHERE kind='meeting' AND id='current'`).Scan(&b)
		if e == sql.ErrNoRows {
			return struct {
				Status string `json:"status"`
			}{"no meeting"}, nil
		}
		if e != nil {
			return nil, e
		}
		var m Meeting
		if e = json.Unmarshal(b, &m); e != nil {
			return nil, e
		}
		if m.Status == "ended" {
			return struct {
				Status string `json:"status"`
			}{"already ended"}, nil
		}
		m.Status = "ending"
		m.Expires = time.Now().UTC()
		b, e = json.Marshal(m)
		if e != nil {
			return nil, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE objects SET body=? WHERE kind='meeting' AND id='current'`, b); e != nil {
			return nil, e
		}
		_, e = tx.ExecContext(ctx, `INSERT OR IGNORE INTO jobs(id,kind,target,status,created) VALUES(?,'end',?,'queued',?)`, randomID(12), m.ID, time.Now().UTC().Format(time.RFC3339Nano))
	default:
		return nil, fmt.Errorf("unknown maintenance operation")
	}
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return struct {
		Status string `json:"status"`
	}{"queued"}, nil
}
