package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
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
	db, e := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.Join(root, "state.db")}).String()+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(FULL)")
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
	f, e := os.OpenFile(filepath.Join(s.Root, "mutation.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("another mutation is running")
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
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
