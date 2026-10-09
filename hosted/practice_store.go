package hosted

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type practiceRecord struct {
	PracticeRun
	InstanceID string
}

const practiceColumns = `id,user_id,lab_id,instance_id,status,created,expires,ended,error`

func scanPractice(row interface{ Scan(...any) error }) (practiceRecord, error) {
	var p practiceRecord
	var ended string
	err := row.Scan(&p.ID, &p.UserID, &p.LabID, &p.InstanceID, &p.Status, newTime(&p.Created), newTime(&p.Expires), &ended, &p.Error)
	if err == nil && ended != "" {
		t, e := time.Parse(time.RFC3339Nano, ended)
		err = e
		p.EndedAt = &t
	}
	return p, err
}
func (s *Store) practice(ctx context.Context, id string) (practiceRecord, error) {
	return scanPractice(s.DB.QueryRowContext(ctx, `SELECT `+practiceColumns+` FROM practice_runs WHERE id=?`, id))
}
func (s *Store) activePractice(ctx context.Context, user string) (*PracticeRun, error) {
	p, err := scanPractice(s.DB.QueryRowContext(ctx, `SELECT `+practiceColumns+` FROM practice_runs WHERE user_id=? AND status!='ended'`, user))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p.PracticeRun, nil
}

type practiceQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

const practiceQuotaSQL = `SELECT created,ended FROM practice_runs WHERE user_id=? AND ended=''
 UNION ALL SELECT created,ended FROM practice_runs WHERE user_id=? AND ended>=?`

func weekBounds(now time.Time) (time.Time, time.Time) {
	now = now.UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	start := day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
	return start, start.AddDate(0, 0, 7)
}
func practiceQuota(ctx context.Context, db practiceQuery, user string, minutes int, now time.Time) (CloudQuota, error) {
	start, end := weekBounds(now)
	q := CloudQuota{LimitMinutes: minutes, RemainingSeconds: minutes * 60, ResetsAt: end}
	// Two previous UTC dates conservatively cover the offsets accepted by
	// time.Parse and fractional seconds at Monday midnight. Overlap remains calculated
	// below with time.Parse; pending cleanup is included regardless of its age.
	cutoff := start.AddDate(0, 0, -2).Format("2006-01-02")
	rows, err := db.QueryContext(ctx, practiceQuotaSQL, user, user, cutoff)
	if err != nil {
		return q, err
	}
	defer rows.Close()
	var used time.Duration
	for rows.Next() {
		var created, ended string
		if err = rows.Scan(&created, &ended); err != nil {
			return q, err
		}
		from, e := time.Parse(time.RFC3339Nano, created)
		if e != nil {
			return q, e
		}
		to := now
		if ended != "" {
			to, e = time.Parse(time.RFC3339Nano, ended)
			if e != nil {
				return q, e
			}
			if to.After(now) {
				to = now
			}
		}
		if from.Before(start) {
			from = start
		}
		if to.After(end) {
			to = end
		}
		if to.After(from) {
			used += to.Sub(from)
		}
	}
	if err = rows.Err(); err != nil {
		return q, err
	}
	// Round up once so sub-second reservations cannot repeatedly escape billing.
	q.UsedSeconds = int((used + time.Second - 1) / time.Second)
	q.RemainingSeconds = max(0, minutes*60-q.UsedSeconds)
	return q, nil
}
func (s *Store) quota(ctx context.Context, user string, minutes int, now time.Time) (CloudQuota, error) {
	return practiceQuota(ctx, s.DB, user, minutes, now)
}

// Reserve quota, the exact engine identity and the provisioning job in one
// transaction before any native mutation. One active allocation spans all labs.
func (s *Store) reservePractice(ctx context.Context, user, lab string, minutes int, now time.Time) (PracticeRun, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return PracticeRun{}, err
	}
	defer tx.Rollback()
	existing, e := scanPractice(tx.QueryRowContext(ctx, `SELECT `+practiceColumns+` FROM practice_runs WHERE user_id=? AND status!='ended'`, user))
	if e == nil {
		if existing.LabID != lab {
			return PracticeRun{}, fmt.Errorf("End your other cloud lab before starting this one.")
		}
		return existing.PracticeRun, nil
	}
	if e != sql.ErrNoRows {
		return PracticeRun{}, e
	}
	q, err := practiceQuota(ctx, tx, user, minutes, now)
	if err != nil {
		return PracticeRun{}, err
	}
	if q.RemainingSeconds < 60 {
		return PracticeRun{}, fmt.Errorf("Your weekly cloud quota is exhausted. Continue locally or wait for the reset.")
	}
	expires := now.Add(time.Duration(q.RemainingSeconds) * time.Second)
	if expires.After(q.ResetsAt) {
		expires = q.ResetsAt
	}
	p := PracticeRun{ID: randomID(12), LabID: lab, UserID: user, Status: "queued", Created: now.UTC(), Expires: expires.UTC()}
	_, err = tx.ExecContext(ctx, `INSERT INTO practice_runs(id,user_id,lab_id,instance_id,status,created,expires) VALUES(?,?,?,?,?,?,?)`, p.ID, user, lab, randomID(12), p.Status, p.Created.Format(time.RFC3339Nano), p.Expires.Format(time.RFC3339Nano))
	if err != nil {
		return p, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target,status,created) VALUES(?,'practice-prepare',?,'queued',?)`, randomID(12), p.ID, p.Created.Format(time.RFC3339Nano))
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}
func (s *Store) endPractice(ctx context.Context, id, user string) (PracticeRun, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return PracticeRun{}, err
	}
	defer tx.Rollback()
	p, err := scanPractice(tx.QueryRowContext(ctx, `SELECT `+practiceColumns+` FROM practice_runs WHERE id=? AND user_id=?`, id, user))
	if err != nil {
		return p.PracticeRun, err
	}
	if p.Status == "ended" {
		return p.PracticeRun, nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE practice_runs SET status='ending' WHERE id=?`, id)
	if err != nil {
		return p.PracticeRun, err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO jobs(id,kind,target,status,created) VALUES(?,'practice-end',?,'queued',?)`, randomID(12), id, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return p.PracticeRun, err
	}
	p.Status = "ending"
	return p.PracticeRun, tx.Commit()
}
