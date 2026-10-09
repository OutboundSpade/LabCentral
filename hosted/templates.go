package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"net/http"
	"slices"
	"sort"
	"time"
)

func (s *Service) templateKeys(ctx context.Context, id string) ([]string, error) {
	var keys []string
	err := s.Store.Get(ctx, "template-images", id, &keys)
	if err == nil {
		return keys, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	p, err := s.plan(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, im := range p.Images {
		if im.Playbook != "" {
			keys = append(keys, im.CacheKey)
		}
	}
	return keys, nil
}
func (s *Service) rawTemplates(ctx context.Context) ([]Template, error) {
	rows, err := s.Store.List(ctx, "template")
	if err != nil {
		return nil, err
	}
	out := []Template{}
	for _, b := range rows {
		var t Template
		if err = json.Unmarshal(b, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}
func (s *Service) templates(ctx context.Context) ([]Template, error) {
	out, err := s.rawTemplates(ctx)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	images, err := s.Engine.Images(ctx)
	if err != nil {
		return nil, err
	}
	instances, err := s.Engine.List(ctx)
	if err != nil {
		return nil, err
	}
	builds, err := s.Engine.Builds(ctx)
	if err != nil {
		return nil, err
	}
	meeting, err := s.Store.Meeting(ctx)
	if err != nil {
		return nil, err
	}
	for n := range out {
		t := &out[n]
		var operation Job
		err = s.Store.DB.QueryRowContext(ctx, `SELECT id,kind,target,status,error,created FROM jobs WHERE target=? AND kind IN ('build','remove-images') ORDER BY created DESC LIMIT 1`, t.ID).Scan(&operation.ID, &operation.Kind, &operation.Target, &operation.Status, &operation.Error, newTime(&operation.Created))
		if err == nil {
			t.Operation = &operation
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		var head string
		if err = s.Store.Get(ctx, "template-head", t.Name, &head); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		t.Latest = head == t.ID
		keys, e := s.templateKeys(ctx, t.ID)
		if e != nil {
			if !errors.Is(e, sql.ErrNoRows) {
				return nil, e
			}
			continue
		}
		t.ImagesTotal = len(keys)
		for _, im := range images {
			if slices.Contains(keys, im.Key) {
				t.ImagesReady++
			}
		}
		for _, build := range builds {
			if slices.Contains(keys, build.Image.CacheKey) && !slices.ContainsFunc(images, func(im lc.ImageRecord) bool { return im.Key == build.Image.CacheKey }) {
				t.BuildsPending++
			}
		}
		t.RemovalBlocked = removalBlock(*t, keys, meeting, instances)
		if t.RemovalBlocked == "" {
			t.RemovalBlocked, err = s.practiceImageBlock(ctx, keys)
			if err != nil {
				return nil, err
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Latest != b.Latest {
			return a.Latest
		}
		at, bt := time.Time{}, time.Time{}
		if a.CommittedAt != nil {
			at = *a.CommittedAt
		} else if a.ImportedAt != nil {
			at = *a.ImportedAt
		}
		if b.CommittedAt != nil {
			bt = *b.CommittedAt
		} else if b.ImportedAt != nil {
			bt = *b.ImportedAt
		}
		if at.Equal(bt) {
			return a.Commit > b.Commit
		}
		return at.After(bt)
	})
	return out, nil
}
func removalBlock(t Template, keys []string, m *Meeting, instances []lc.Instance) string {
	if m != nil && m.Template == t.ID && m.Status != "ended" {
		return "Used by the current meeting. End it before removing images."
	}
	for _, i := range instances {
		if i.Status == "destroyed" {
			continue
		}
		for _, im := range i.Plan.Images {
			if slices.Contains(keys, im.CacheKey) {
				return "Used by retained labs. Finish lab cleanup before removing images."
			}
		}
	}
	return ""
}
func (s *Service) templateOperation(w http.ResponseWriter, r *http.Request, kind string) {
	var q TargetRequest
	if !decode(w, r, &q) {
		return
	}
	j, err := s.queueTemplateOperation(r.Context(), kind, q.ID)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 202, j)
}
func (s *Service) queueTemplateOperation(ctx context.Context, kind, id string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queueTemplateOperationLocked(ctx, kind, id)
}

// Caller holds s.mu so retry validation and reservation cannot be superseded.
func (s *Service) queueTemplateOperationLocked(ctx context.Context, kind, id string) (Job, error) {
	var t Template
	if err := s.Store.Get(ctx, "template", id, &t); err != nil {
		return Job{}, fmt.Errorf("Template revision not found.")
	}
	var active int
	if err := s.Store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE target=? AND kind IN ('build','remove-images') AND status IN ('queued','running')`, id).Scan(&active); err != nil {
		return Job{}, err
	}
	if active > 0 {
		return Job{}, fmt.Errorf("An image operation is already queued or running for this revision.")
	}
	keys, err := s.templateKeys(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if kind == "build" && t.Removing {
		return Job{}, fmt.Errorf("Finish image cleanup before rebuilding this revision.")
	}
	if kind == "remove-images" {
		m, err := s.Store.Meeting(ctx)
		if err != nil {
			return Job{}, err
		}
		instances, err := s.Engine.List(ctx)
		if err != nil {
			return Job{}, err
		}
		if reason := removalBlock(t, keys, m, instances); reason != "" {
			return Job{}, errors.New(reason)
		}
		if reason, err := s.practiceImageBlock(ctx, keys); err != nil {
			return Job{}, err
		} else if reason != "" {
			return Job{}, errors.New(reason)
		}
		t.Removing = true
		t.Built = false
	}
	j := Job{ID: randomID(12), Kind: kind, Target: id, Status: "queued", Created: time.Now().UTC()}
	tx, err := s.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	for _, o := range []struct {
		kind  string
		value any
	}{{"template", t}, {"template-images", keys}, {"template-operation", j.ID}} {
		b, err := json.Marshal(o.value)
		if err != nil {
			return Job{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO objects VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body`, o.kind, id, b); err != nil {
			return Job{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target,status,created) VALUES(?,?,?,?,?)`, j.ID, j.Kind, j.Target, j.Status, j.Created.Format(time.RFC3339Nano)); err != nil {
		return Job{}, err
	}
	return j, tx.Commit()
}
func (s *Service) runTemplateJob(ctx context.Context, j Job) error {
	var latest string
	if err := s.Store.Get(ctx, "template-operation", j.Target, &latest); err == nil && latest != j.ID {
		return fmt.Errorf("image operation was superseded")
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var status string
	if err := s.Store.DB.QueryRowContext(ctx, `SELECT status FROM jobs WHERE id=?`, j.ID).Scan(&status); err != nil {
		return err
	}
	if status == "complete" {
		return nil
	}
	var t Template
	if err := s.Store.Get(ctx, "template", j.Target, &t); err != nil {
		return err
	}
	keys, err := s.templateKeys(ctx, j.Target)
	if err != nil {
		return err
	}
	// Persist resolved keys before any native effect, including older queued builds.
	if err = s.Store.Put(ctx, "template-images", t.ID, keys); err != nil {
		return err
	}
	if j.Kind == "build" {
		if t.Removing {
			return fmt.Errorf("image cleanup remains unresolved")
		}
		p, err := s.plan(ctx, t.ID)
		if err != nil {
			return err
		}
		if _, err = s.Engine.Build(ctx, p); err != nil {
			return err
		}
		now := time.Now().UTC()
		t.Built = true
		t.BuiltAt = &now
		t.RemovedAt = nil
	} else {
		m, err := s.Store.Meeting(ctx)
		if err != nil {
			return err
		}
		instances, err := s.Engine.List(ctx)
		if err != nil {
			return err
		}
		if reason := removalBlock(t, keys, m, instances); reason != "" {
			return errors.New(reason)
		}
		if reason, err := s.practiceImageBlock(ctx, keys); err != nil {
			return err
		} else if reason != "" {
			return errors.New(reason)
		}
		others, err := s.rawTemplates(ctx)
		if err != nil {
			return err
		}
		shared := map[string]bool{}
		for _, other := range others {
			if other.ID == t.ID || !other.Built {
				continue
			}
			ks, err := s.templateKeys(ctx, other.ID)
			if err != nil {
				return err
			}
			for _, k := range ks {
				shared[k] = true
			}
		}
		images, err := s.Engine.Images(ctx)
		if err != nil {
			return err
		}
		builds, err := s.Engine.Builds(ctx)
		if err != nil {
			return err
		}
		var failures []error
		for _, key := range keys {
			if shared[key] {
				continue
			}
			found := false
			for _, im := range images {
				if im.Key == key {
					found = true
					if err = s.Engine.RemoveImage(ctx, key); err != nil {
						failures = append(failures, fmt.Errorf("%s: %w", im.Name, err))
					}
					break
				}
			}
			if !found {
				for _, b := range builds {
					if b.Image.CacheKey == key {
						if err = s.Engine.AbandonBuild(ctx, key); err != nil {
							failures = append(failures, fmt.Errorf("%s: %w", b.Image.Name, err))
						}
						break
					}
				}
			}
		}
		if err = errors.Join(failures...); err != nil {
			return err
		}
		now := time.Now().UTC()
		t.Built = false
		t.Removing = false
		t.RemovedAt = &now
	}
	// Refresh can enrich revision metadata while native work runs. Merge only
	// lifecycle fields into the current record under the shared short fence.
	s.mu.Lock()
	defer s.mu.Unlock()
	var current Template
	if err := s.Store.Get(ctx, "template", t.ID, &current); err != nil {
		return err
	}
	current.Built, current.BuiltAt, current.RemovedAt, current.Removing = t.Built, t.BuiltAt, t.RemovedAt, t.Removing
	return s.Store.Put(ctx, "template", t.ID, current)
}
