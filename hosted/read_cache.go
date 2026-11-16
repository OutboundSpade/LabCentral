package hosted

import (
	"context"
	"encoding/json"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"sync"
	"time"
)

// readMemo bounds retained entries and coalesces identical concurrent work. Values
// are immutable; callers copy slices or pointers they need to modify. Errors are
// shared with waiters but are not retained for subsequent requests.
type readMemo[T any] struct {
	mu         sync.Mutex
	generation string
	entries    map[string]*readEntry[T]
}
type readEntry[T any] struct {
	done  chan struct{}
	value T
	err   error
}

func (m *readMemo[T]) get(ctx context.Context, generation, key string, fn func() (T, error)) (T, error) {
	m.mu.Lock()
	if m.generation != generation {
		m.entries = nil
		m.generation = generation
	}
	if entry := m.entries[key]; entry != nil {
		m.mu.Unlock()
		select {
		case <-entry.done:
			return entry.value, entry.err
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
	}
	if len(m.entries) >= 128 {
		m.entries = nil
	}
	if m.entries == nil {
		m.entries = map[string]*readEntry[T]{}
	}
	entry := &readEntry[T]{done: make(chan struct{})}
	m.entries[key] = entry
	m.mu.Unlock()
	entry.value, entry.err = fn()
	m.mu.Lock()
	if entry.err != nil && m.entries[key] == entry {
		delete(m.entries, key)
	}
	close(entry.done)
	m.mu.Unlock()
	return entry.value, entry.err
}
func (s *Store) readGeneration(ctx context.Context, scope string) (int64, error) {
	var n int64
	err := s.DB.QueryRowContext(ctx, "SELECT version FROM hosted_read_versions WHERE scope=?", scope).Scan(&n)
	return n, err
}

// SQL triggers cover direct writes and transactions, including rollback. Session,
// progress, checks and score-cache writes do not invalidate template projections.
func installReadVersions(db *hostedDatabase) error {
	_, err := db.DB.Exec("CREATE TABLE IF NOT EXISTS hosted_read_versions(scope TEXT PRIMARY KEY,version INTEGER NOT NULL); INSERT OR IGNORE INTO hosted_read_versions VALUES('catalog',0),('source',0),('admin',0),('engine',0);")
	if err != nil {
		return err
	}
	for _, action := range []string{"INSERT", "UPDATE", "DELETE"} {
		row := "NEW"
		if action == "DELETE" {
			row = "OLD"
		}
		for _, scope := range []string{"catalog", "source", "admin"} {
			kinds := "'template','source','template-instructor','template-head','template-retired-name'"
			if scope == "source" {
				kinds = "'source'"
			}
			if scope == "admin" {
				kinds += ",'template-images','template-operation','meeting','learning-publication','image-prune','capacity-build'"
			}
			query := fmt.Sprintf("CREATE TRIGGER IF NOT EXISTS hosted_%s_objects_%s AFTER %s ON objects WHEN %s.kind IN (%s) BEGIN UPDATE hosted_read_versions SET version=version+1 WHERE scope='%s'; END", scope, action, action, row, kinds, scope)
			if _, err = db.DB.Exec(query); err != nil {
				return err
			}
		}
		for _, table := range []string{"jobs", "practice_runs", "teams"} {
			condition := ""
			if table == "jobs" {
				condition = fmt.Sprintf(" WHEN %s.kind IN ('build','remove-images','prune-images','prepare','end','practice-start','practice-end')", row)
			}
			query := fmt.Sprintf("CREATE TRIGGER IF NOT EXISTS hosted_admin_%s_%s AFTER %s ON %s%s BEGIN UPDATE hosted_read_versions SET version=version+1 WHERE scope IN ('admin','engine'); END", table, action, action, table, condition)
			if _, err = db.DB.Exec(query); err != nil {
				return err
			}
		}
	}
	return nil
}

type inventorySnapshot struct {
	images    []lc.ImageRecord
	instances []lc.Instance
	builds    []lc.Command
}
type inventoryReadKey struct{}

func (s *Service) readImages(ctx context.Context) ([]lc.ImageRecord, error) {
	if v, ok := ctx.Value(inventoryReadKey{}).(*inventorySnapshot); ok {
		return v.images, nil
	}
	return s.Engine.Images(ctx)
}
func (s *Service) readInstances(ctx context.Context) ([]lc.Instance, error) {
	if v, ok := ctx.Value(inventoryReadKey{}).(*inventorySnapshot); ok {
		return v.instances, nil
	}
	return s.Engine.List(ctx)
}
func (s *Service) readBuilds(ctx context.Context) ([]lc.Command, error) {
	if v, ok := ctx.Value(inventoryReadKey{}).(*inventorySnapshot); ok {
		return v.builds, nil
	}
	return s.Engine.Builds(ctx)
}

// Inventory methods read local journals, not provider discovery. Polls share a
// five-second snapshot. Lifecycle queue/status writes invalidate it. Mutations
// still read the authoritative journal and revalidate their durable intent.
func (s *Service) inventoryRead(ctx context.Context) (*inventorySnapshot, string, error) {
	generation, err := s.Store.readGeneration(ctx, "engine")
	if err != nil {
		return nil, "", err
	}
	now := time.Now()
	if s.inventoryClock != nil {
		now = s.inventoryClock()
	}
	key := fmt.Sprintf("%d/%d", generation, now.UnixNano()/int64(5*time.Second))
	value, err := s.inventoryMemo.get(ctx, key, "", func() (*inventorySnapshot, error) {
		v := &inventorySnapshot{}
		var err error
		if v.images, err = s.Engine.Images(ctx); err != nil {
			return nil, err
		}
		if v.instances, err = s.Engine.List(ctx); err != nil {
			return nil, err
		}
		if v.builds, err = s.Engine.Builds(ctx); err != nil {
			return nil, err
		}
		return v, nil
	})
	return value, key, err
}

type adminTemplatesRead struct {
	templates []Template
	summary   *ImageSummary
}

func (s *Service) adminTemplates(ctx context.Context) ([]Template, *ImageSummary, error) {
	if s.Engine == nil {
		out, err := s.templates(ctx)
		return out, nil, err
	}
	inventory, key, err := s.inventoryRead(ctx)
	if err != nil {
		return nil, nil, err
	}
	generation, err := s.Store.readGeneration(ctx, "admin")
	if err != nil {
		return nil, nil, err
	}
	key = fmt.Sprintf("%s/%d", key, generation)
	value, err := s.adminMemo.get(ctx, key, "", func() (adminTemplatesRead, error) {
		ctx = context.WithValue(ctx, inventoryReadKey{}, inventory)
		out, err := s.templates(ctx)
		if err != nil {
			return adminTemplatesRead{}, err
		}
		summary, _, inventoryErr := s.imageInventory(ctx)
		var ptr *ImageSummary
		if inventoryErr == nil {
			ptr = &summary
		}
		return adminTemplatesRead{out, ptr}, nil
	})
	out := append([]Template{}, value.templates...)
	for n := range out {
		if out[n].Operation != nil {
			op := *out[n].Operation
			out[n].Operation = &op
		}
	}
	return out, value.summary, err
}

// sourceMetadata supplies topology and pins without copying guest file bytes.
// The returned source is immutable and must never be sent to a planner or guest.
func (s *Store) sourceMetadata(ctx context.Context, id string) (lc.Source, error) {
	read := func() (lc.Source, error) {
		var src lc.Source
		var raw []byte
		err := s.DB.QueryRowContext(ctx, "SELECT json_object('lab',json_extract(body,'$.lab'),'digest',json_extract(body,'$.digest')) FROM objects WHERE kind='source' AND id=?", id).Scan(&raw)
		if err == nil {
			err = json.Unmarshal(raw, &src)
		}
		return src, err
	}
	if s.DB.current(ctx) != nil {
		return read()
	}
	generation, err := s.readGeneration(ctx, "source")
	if err != nil {
		return lc.Source{}, err
	}
	return s.sourceMetadataMemo.get(ctx, fmt.Sprint(generation), id, read)
}
