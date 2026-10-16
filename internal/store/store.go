package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

type Store struct {
	DB   *sql.DB
	Root string
}

func Open(root string) (*Store, error) {
	var err error
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0700); err != nil {
		return nil, err
	}
	// Immediate transactions take the write lock at BEGIN and wait for it, instead of
	// failing with SQLITE_BUSY when a read transaction later needs to write while another
	// process (for example a second CLI command on the same state) is writing.
	db, e := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.Join(root, "state.db")}).String()+"?_txlock=immediate&_pragma=busy_timeout(30000)&_pragma=foreign_keys(1)&_pragma=synchronous(FULL)")
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(4)
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version != 0 && version != 2 {
		db.Close()
		return nil, fmt.Errorf("unsupported local state version %d", version)
	}
	_, e = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;
CREATE TABLE IF NOT EXISTS objects(kind TEXT NOT NULL,id TEXT NOT NULL,body BLOB NOT NULL,PRIMARY KEY(kind,id));
CREATE TABLE IF NOT EXISTS steps(id TEXT PRIMARY KEY,owner TEXT NOT NULL,command BLOB NOT NULL,result BLOB,status TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS claims(scope TEXT NOT NULL,value INTEGER NOT NULL,owner TEXT NOT NULL,PRIMARY KEY(scope,value));
PRAGMA user_version=2;`)
	if e != nil {
		db.Close()
		return nil, e
	}
	return &Store{db, root}, nil
}
func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) Lock() (func(), error) {
	return s.LockKey("mutation")
}

// LockKey coordinates a resource across independent library clients/processes.
func (s *Store) LockKey(key string) (func(), error) {
	return s.lockKey(key, false)
}

func (s *Store) lockKey(key string, shared bool) (func(), error) {
	name := fmt.Sprintf("lock-%x", sha256.Sum256([]byte(key)))
	if key == "mutation" {
		name = "mutation.lock"
	}
	f, e := os.OpenFile(filepath.Join(s.Root, name), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = tryLockFile(f, shared); e != nil {
		f.Close()
		return nil, fmt.Errorf("another mutation is running: %w", e)
	}
	return func() { unlockFile(f); f.Close() }, nil
}
func (s *Store) Put(ctx context.Context, kind, id string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = s.DB.ExecContext(ctx, `INSERT INTO objects VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body`, kind, id, b)
	return e
}
func (s *Store) Get(ctx context.Context, kind, id string, v any) error {
	var b []byte
	if e := s.DB.QueryRowContext(ctx, `SELECT body FROM objects WHERE kind=? AND id=?`, kind, id).Scan(&b); e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func (s *Store) List(ctx context.Context, kind string) ([][]byte, error) {
	r, e := s.DB.QueryContext(ctx, `SELECT body FROM objects WHERE kind=? ORDER BY id`, kind)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	var out [][]byte
	for r.Next() {
		var b []byte
		if e = r.Scan(&b); e != nil {
			return nil, e
		}
		out = append(out, b)
	}
	return out, r.Err()
}
func (s *Store) Delete(ctx context.Context, kind, id string) error {
	_, e := s.DB.ExecContext(ctx, `DELETE FROM objects WHERE kind=? AND id=?`, kind, id)
	return e
}

type Step struct {
	ID      string
	Owner   string
	Command []byte
	Result  []byte
	Status  string
}

func (s *Store) Intent(ctx context.Context, id, owner string, v any) (Step, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return Step{}, e
	}
	_, e = s.DB.ExecContext(ctx, `INSERT INTO steps(id,owner,command,status) VALUES(?,?,?,'pending') ON CONFLICT(id) DO NOTHING`, id, owner, b)
	if e != nil {
		return Step{}, e
	}
	var st Step
	e = s.DB.QueryRowContext(ctx, `SELECT id,owner,command,result,status FROM steps WHERE id=?`, id).Scan(&st.ID, &st.Owner, &st.Command, &st.Result, &st.Status)
	return st, e
}
func (s *Store) Started(ctx context.Context, id string) error {
	_, e := s.DB.ExecContext(ctx, `UPDATE steps SET status='uncertain' WHERE id=? AND status='pending'`, id)
	return e
}
func (s *Store) Complete(ctx context.Context, id string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = s.DB.ExecContext(ctx, `UPDATE steps SET result=?,status='complete' WHERE id=?`, b, id)
	return e
}
func (s *Store) Uncertain(ctx context.Context, owner string) ([]Step, error) {
	r, e := s.DB.QueryContext(ctx, `SELECT id,owner,command,result,status FROM steps WHERE owner=? AND status='uncertain' ORDER BY id`, owner)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	var out []Step
	for r.Next() {
		var st Step
		if e = r.Scan(&st.ID, &st.Owner, &st.Command, &st.Result, &st.Status); e != nil {
			return nil, e
		}
		out = append(out, st)
	}
	return out, r.Err()
}
func (s *Store) Reserve(ctx context.Context, scope, owner string, start, end, count int) ([]int, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	r, e := tx.QueryContext(ctx, `SELECT value FROM claims WHERE scope=? AND owner=? ORDER BY value`, scope, owner)
	if e != nil {
		return nil, e
	}
	var values []int
	for r.Next() {
		var n int
		if err := r.Scan(&n); err != nil {
			r.Close()
			return nil, err
		}
		values = append(values, n)
	}
	e = r.Err()
	r.Close()
	if e != nil {
		return nil, e
	}
	for n := start; len(values) < count && n <= end; n++ {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO claims VALUES(?,?,?)`, scope, n, owner)
		if err != nil {
			return nil, err
		}
		k, _ := res.RowsAffected()
		if k == 1 {
			values = append(values, n)
		}
	}
	if len(values) != count {
		return nil, fmt.Errorf("allocation namespace %s is exhausted", scope)
	}
	return values, tx.Commit()
}
func (s *Store) Release(ctx context.Context, owner string) error {
	_, e := s.DB.ExecContext(ctx, `DELETE FROM claims WHERE owner=?`, owner)
	return e
}

func (s *Store) ReleaseValue(ctx context.Context, scope, owner string, value int) error {
	_, e := s.DB.ExecContext(ctx, `DELETE FROM claims WHERE scope=? AND owner=? AND value=?`, scope, owner, value)
	return e
}

func (s *Store) ReleaseScope(ctx context.Context, scope, owner string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM claims WHERE scope=? AND owner=?`, scope, owner)
	return err
}

// LockKeyContext queues contending access requests while preserving cancellation.
func (s *Store) LockKeyContext(ctx context.Context, key string) (func(), error) {
	return s.lockKeyContext(ctx, key, false)
}

// LockKeySharedContext permits concurrent operations while excluding lifecycle
// mutations that acquire LockKey or LockKeyContext for the same resource.
func (s *Store) LockKeySharedContext(ctx context.Context, key string) (func(), error) {
	return s.lockKeyContext(ctx, key, true)
}

func (s *Store) lockKeyContext(ctx context.Context, key string, shared bool) (func(), error) {
	for {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		unlock, e := s.lockKey(key, shared)
		if e == nil {
			return unlock, nil
		}
		if !errors.Is(e, errLockHeld) {
			return nil, e
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// CompletedSteps also cover a crash between committing a native result and
// materializing that resource into its instance. Cleanup must retain both.
func (s *Store) CompletedSteps(ctx context.Context, owner string) ([]Step, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,owner,command,result,status FROM steps WHERE owner=? AND status='complete' ORDER BY id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Step{}
	for rows.Next() {
		var st Step
		if err := rows.Scan(&st.ID, &st.Owner, &st.Command, &st.Result, &st.Status); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}
