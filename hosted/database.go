package hosted

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// DB owns exactly one writer and four query-only WAL readers. Write
// transactions use BEGIN IMMEDIATE (the connection's _txlock setting).
// A transaction context keeps compound hosted decisions on that writer.
type hostedDatabase struct {
	*sql.DB
	read *sql.DB
}
type transactionKey struct{}
type transaction struct {
	*sql.Tx
	owner bool
}

func (t *transaction) Commit() error {
	if t.owner {
		return t.Tx.Commit()
	}
	return nil
}
func (t *transaction) Rollback() error {
	if t.owner {
		return t.Tx.Rollback()
	}
	return nil
}
func (d *hostedDatabase) current(ctx context.Context) *sql.Tx {
	tx, _ := ctx.Value(transactionKey{}).(*sql.Tx)
	return tx
}
func (d *hostedDatabase) BeginTx(ctx context.Context, o *sql.TxOptions) (*transaction, error) {
	if tx := d.current(ctx); tx != nil {
		return &transaction{tx, false}, nil
	}
	tx, err := d.DB.BeginTx(ctx, o)
	return &transaction{tx, true}, err
}
func (d *hostedDatabase) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	if tx := d.current(ctx); tx != nil {
		return tx.ExecContext(ctx, q, args...)
	}
	return d.DB.ExecContext(ctx, q, args...)
}
func (d *hostedDatabase) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if tx := d.current(ctx); tx != nil {
		return tx.QueryContext(ctx, q, args...)
	}
	return d.read.QueryContext(ctx, q, args...)
}
func (d *hostedDatabase) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	if tx := d.current(ctx); tx != nil {
		return tx.QueryRowContext(ctx, q, args...)
	}
	return d.read.QueryRowContext(ctx, q, args...)
}
func (s *Store) Close() error { return errors.Join(s.DB.read.Close(), s.Store.Close()) }
func (s *Store) Put(ctx context.Context, kind, id string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO objects VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body`, kind, id, b)
	return err
}
func (s *Store) getObject(ctx context.Context, kind, id string, v any) error {
	var b []byte
	if err := s.DB.QueryRowContext(ctx, `SELECT body FROM objects WHERE kind=? AND id=?`, kind, id).Scan(&b); err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func (s *Store) List(ctx context.Context, kind string) ([][]byte, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT body FROM objects WHERE kind=? ORDER BY id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
func (s *Store) Delete(ctx context.Context, kind, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM objects WHERE kind=? AND id=?`, kind, id)
	return err
}
func (s *Store) atomic(ctx context.Context, fn func(context.Context) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(context.WithValue(ctx, transactionKey{}, tx.Tx)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if tx.owner {
		s.signal()
	}
	return nil
}

// Buffer small mutation API responses until the decision commits. Slow clients
// never retain a writer transaction, and commit failures cannot report success.
type decisionResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *decisionResponse) Header() http.Header { return w.header }
func (w *decisionResponse) WriteHeader(n int) {
	if w.status == 0 {
		w.status = n
	}
}
func (w *decisionResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.body.Write(b)
}
func (s *Service) coordinate(next endpoint) endpoint {
	return func(w http.ResponseWriter, r *http.Request, v Session) {
		// Read bounded JSON before taking the writer; a slow request body cannot
		// occupy the service's mutation connection.
		if r.Body != nil {
			body, err := io.ReadAll(io.LimitReader(r.Body, (16<<10)+1))
			r.Body.Close()
			if err != nil || len(body) > 16<<10 {
				writeError(w, 400, "Invalid request.")
				return
			}
			r = r.Clone(r.Context())
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		result := &decisionResponse{header: make(http.Header)}
		rejected := errors.New("request rejected")
		err := s.Store.atomic(r.Context(), func(ctx context.Context) error {
			next(result, r.WithContext(ctx), v)
			if result.status >= 400 {
				return rejected
			}
			return nil
		})
		if err != nil && !errors.Is(err, rejected) {
			writeError(w, 503, "Could not commit the operation. Retry shortly.")
			return
		}
		for k, values := range result.header {
			w.Header()[k] = values
		}
		if result.status == 0 {
			result.status = 200
		}
		w.WriteHeader(result.status)
		_, _ = w.Write(result.body.Bytes())
	}
}

func jsonValue(v any) string { b, _ := json.Marshal(v); return string(b) }
