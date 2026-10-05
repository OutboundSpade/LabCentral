package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/OutboundSpade/LabCentral/v2/internal/store"
	"time"
)

type Store struct{ *store.Store }

func OpenStore(root string) (*Store, error) {
	s, e := store.Open(root)
	if e != nil {
		return nil, e
	}
	s.DB.SetMaxOpenConns(1)
	_, e = s.DB.Exec(`CREATE TABLE IF NOT EXISTS memberships(user_id TEXT PRIMARY KEY,team_id TEXT NOT NULL,pending_team TEXT NOT NULL DEFAULT '',switching INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS teams(id TEXT PRIMARY KEY,meeting_id TEXT NOT NULL,name TEXT NOT NULL,capacity INTEGER NOT NULL,instance_id TEXT NOT NULL,status TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY,kind TEXT NOT NULL,target TEXT NOT NULL,status TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',created TEXT NOT NULL);
 CREATE UNIQUE INDEX IF NOT EXISTS active_job ON jobs(kind,target) WHERE status IN ('queued','running');`)
	if e != nil {
		s.Close()
		return nil, e
	}
	return &Store{s}, nil
}
func (s *Store) Meeting(ctx context.Context) (*Meeting, error) {
	var m Meeting
	e := s.Get(ctx, "meeting", "current", &m)
	if e == sql.ErrNoRows {
		return nil, nil
	}
	return &m, e
}
func (s *Store) Teams(ctx context.Context, meeting string) ([]Team, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT t.id,t.meeting_id,t.name,t.capacity,t.instance_id,t.status,(SELECT COUNT(*) FROM memberships m WHERE m.team_id=t.id OR (m.switching=1 AND m.pending_team=t.id)) FROM teams t WHERE meeting_id=? ORDER BY length(t.name),t.name`, meeting)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Team{}
	for rows.Next() {
		var t Team
		if e = rows.Scan(&t.ID, &t.MeetingID, &t.Name, &t.Capacity, &t.InstanceID, &t.Status, &t.Members); e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Store) Membership(ctx context.Context, user string) (Membership, error) {
	m := Membership{UserID: user}
	e := s.DB.QueryRowContext(ctx, `SELECT team_id,pending_team,switching FROM memberships WHERE user_id=?`, user).Scan(&m.TeamID, &m.PendingTeam, &m.Switching)
	if e == sql.ErrNoRows {
		e = nil
	}
	return m, e
}

// BeginSwitch reserves the destination seat before revocation; switching blocks access.
func (s *Store) BeginSwitch(ctx context.Context, user, target, meeting string) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var current, pending string
	var switching bool
	e = tx.QueryRowContext(ctx, `SELECT team_id,pending_team,switching FROM memberships WHERE user_id=?`, user).Scan(&current, &pending, &switching)
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	if switching {
		if pending == target {
			return nil
		}
		return fmt.Errorf("previous team switch is awaiting access revocation")
	}
	if current == target {
		return nil
	}
	if target != "" {
		var cap, count int
		var mid string
		e = tx.QueryRowContext(ctx, `SELECT meeting_id,capacity,(SELECT COUNT(*) FROM memberships WHERE team_id=? OR (switching=1 AND pending_team=?)) FROM teams WHERE id=?`, target, target, target).Scan(&mid, &cap, &count)
		if e != nil || mid != meeting {
			return fmt.Errorf("team is unavailable")
		}
		if count >= cap {
			return fmt.Errorf("team is full")
		}
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO memberships(user_id,team_id,pending_team,switching) VALUES(?,?,?,1) ON CONFLICT(user_id) DO UPDATE SET pending_team=excluded.pending_team,switching=1`, user, current, target)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) FinishSwitch(ctx context.Context, user string) error {
	_, e := s.DB.ExecContext(ctx, `UPDATE memberships SET team_id=pending_team,pending_team='',switching=0 WHERE user_id=? AND switching=1`, user)
	return e
}
func (s *Store) CreateMeeting(ctx context.Context, m Meeting, count, capacity int) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var b []byte
	e = tx.QueryRowContext(ctx, `SELECT body FROM objects WHERE kind='meeting' AND id='current'`).Scan(&b)
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	if len(b) > 0 {
		var old Meeting
		if e = json.Unmarshal(b, &old); e != nil {
			return e
		}
		if old.Status != "ended" {
			return fmt.Errorf("end the current meeting before creating another")
		}
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM memberships`); e != nil {
		return e
	}
	for i := 1; i <= count; i++ {
		_, e = tx.ExecContext(ctx, `INSERT INTO teams VALUES(?,?,?,?,?,?)`, randomID(12), m.ID, fmt.Sprintf("Team %d", i), capacity, randomID(12), "waiting")
		if e != nil {
			return e
		}
	}
	b, e = json.Marshal(m)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO objects VALUES('meeting','current',?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body`, b)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Enqueue(ctx context.Context, kind, target string) (Job, error) {
	j := Job{ID: randomID(12), Kind: kind, Target: target, Status: "queued", Created: time.Now().UTC()}
	_, e := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO jobs(id,kind,target,status,created) VALUES(?,?,?,?,?)`, j.ID, j.Kind, j.Target, j.Status, j.Created.Format(time.RFC3339Nano))
	if e != nil {
		return j, e
	}
	e = s.DB.QueryRowContext(ctx, `SELECT id,kind,target,status,error,created FROM jobs WHERE kind=? AND target=? AND status IN ('queued','running')`, kind, target).Scan(&j.ID, &j.Kind, &j.Target, &j.Status, &j.Error, newTime(&j.Created))
	return j, e
}

type timeScanner struct{ p *time.Time }

func newTime(p *time.Time) *timeScanner { return &timeScanner{p} }
func (t *timeScanner) Scan(v any) error {
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("invalid timestamp")
	}
	n, e := time.Parse(time.RFC3339Nano, s)
	*t.p = n
	return e
}
func (s *Store) Jobs(ctx context.Context) ([]Job, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT id,kind,target,status,error,created FROM jobs WHERE status IN ('queued','running') OR id IN (SELECT id FROM jobs ORDER BY created DESC LIMIT 50) ORDER BY CASE status WHEN 'running' THEN 0 WHEN 'queued' THEN 1 ELSE 2 END,created DESC`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		var j Job
		if e = rows.Scan(&j.ID, &j.Kind, &j.Target, &j.Status, &j.Error, newTime(&j.Created)); e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// QueuedJobs reads the whole queue, so blocked work cannot hide unrelated jobs.
func (s *Store) QueuedJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,kind,target,status,error,created FROM jobs WHERE status='queued' ORDER BY CASE kind WHEN 'end' THEN 0 WHEN 'access-cleanup' THEN 1 ELSE 2 END,created,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		var j Job
		if err = rows.Scan(&j.ID, &j.Kind, &j.Target, &j.Status, &j.Error, newTime(&j.Created)); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func (s *Store) NextJob(ctx context.Context) (*Job, error) {
	jobs, err := s.QueuedJobs(ctx)
	if err != nil || len(jobs) == 0 {
		return nil, err
	}
	return &jobs[0], nil
}

func (s *Store) Teammates(ctx context.Context, team string) ([]Teammate, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT o.body FROM memberships m JOIN objects o ON o.kind='user' AND o.id=m.user_id WHERE m.team_id=? AND m.switching=0 ORDER BY m.user_id`, team)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Teammate{}
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		var u User
		if e = json.Unmarshal(b, &u); e != nil {
			return nil, e
		}
		out = append(out, Teammate{u.Name})
	}
	return out, rows.Err()
}
