// Package journal records provider-owned mutations independently of core state.
package journal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type Journal struct{ Root string }
type record struct {
	Rejected bool            `json:"rejected,omitempty"`
	Done     bool            `json:"done"`
	Result   json.RawMessage `json:"result,omitempty"`
}

func (j Journal) path(key string) string {
	h := sha256.Sum256([]byte(key))
	return filepath.Join(j.Root, hex.EncodeToString(h[:])+".json")
}
func (j Journal) write(path string, r record) error {
	if e := os.MkdirAll(j.Root, 0700); e != nil {
		return e
	}
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(j.Root, "stage-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	d, e := os.Open(j.Root)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

// Do never retries an uncertain create. find must return its exact owned result.
func Do[T any](ctx context.Context, j Journal, key string, create func() (T, error), find func() (T, bool, error)) (T, error) {
	var zero T
	p := j.path(key)
	b, e := os.ReadFile(p)
	if e == nil {
		var r record
		if e = json.Unmarshal(b, &r); e != nil {
			return zero, e
		}
		if !r.Rejected {
			if r.Done {
				e = json.Unmarshal(r.Result, &zero)
				return zero, e
			}
			v, found, e := find()
			if e != nil {
				return zero, e
			}
			if !found {
				return zero, fmt.Errorf("recovery_required: uncertain mutation %s; no authoritative result yet", key)
			}
			b, e = json.Marshal(v)
			if e != nil {
				return zero, e
			}
			return v, j.write(p, record{Done: true, Result: b})
		}
	}
	if e != nil && !os.IsNotExist(e) {
		return zero, e
	}
	if e = ctx.Err(); e != nil {
		return zero, e
	}
	if e = j.write(p, record{}); e != nil {
		return zero, e
	}
	v, e := create()
	if e != nil {
		var rejected *rejectedError
		if errors.As(e, &rejected) {
			if save := j.write(p, record{Rejected: true}); save != nil {
				return zero, errors.Join(e, save)
			}
		}
		return zero, e
	}
	b, e = json.Marshal(v)
	if e != nil {
		return zero, e
	}
	return v, j.write(p, record{Done: true, Result: b})
}

// Save atomically stores an adapter-owned checkpoint with a durable rename.
func (j Journal) Save(key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return j.write(j.path(key), record{Done: true, Result: b})
}
func (j Journal) Load(key string, value any) error {
	b, err := os.ReadFile(j.path(key))
	if err != nil {
		return err
	}
	var r record
	if err = json.Unmarshal(b, &r); err != nil {
		return err
	}
	if r.Rejected {
		return os.ErrNotExist
	}
	if !r.Done {
		return fmt.Errorf("incomplete checkpoint")
	}
	return json.Unmarshal(r.Result, value)
}

// Rejected marks an authoritative API denial that guarantees no mutation occurred.
// Adapters must never use this for timeouts, transport errors or ambiguous failures.
func Rejected(err error) error { return &rejectedError{err} }

type rejectedError struct{ error }

func (e *rejectedError) Unwrap() error { return e.error }

// HasIntent distinguishes retained mutations (including unknown outcomes) from a
// definitely rejected submission. It is used to preserve older request policies
// when a provider resumes an operation created by an earlier binary.
func (j Journal) HasIntent(key string) (bool, error) {
	b, err := os.ReadFile(j.path(key))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var r record
	if err = json.Unmarshal(b, &r); err != nil {
		return false, err
	}
	return !r.Rejected, nil
}
