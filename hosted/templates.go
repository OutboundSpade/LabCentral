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
	if err = s.actionableRevision(ctx, id); errors.Is(err, errRetainedRevision) {
		var t Template
		if e := s.Store.Get(ctx, "template", id, &t); e != nil {
			return nil, e
		}
		if !t.Built && !t.Removing {
			return []string{}, nil
		}
		// Never recompute old cache keys with the v3 planner. Cleanup requires
		// the saved identities from the original build.
		return nil, err
	} else if err != nil {
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
	operations, heads, err := s.templateReadMetadata(ctx)
	if err != nil {
		return nil, err
	}
	practiceKeys, err := s.practiceImageKeys(ctx)
	if err != nil {
		return nil, err
	}
	for n := range out {
		t := &out[n]
		if operation, ok := operations[t.ID]; ok {
			t.Operation = &operation
		}
		t.Latest = heads[t.Name] == t.ID
		if err := s.actionableRevision(ctx, t.ID); errors.Is(err, errRetainedRevision) {
			t.Retired = true
		} else if err != nil {
			return nil, err
		}
		keys, e := s.templateKeys(ctx, t.ID)
		if e != nil {
			if errors.Is(e, errRetainedRevision) {
				t.RemovalBlocked = "Saved image identities are unavailable for this historical revision."
				continue
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return nil, e
			}
			continue
		}
		t.ImagesReady, t.BuildsPending = 0, 0
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
			for _, key := range keys {
				if practiceKeys[key] {
					t.RemovalBlocked = "Used by a personal cloud run. Finish its cleanup before removing images."
					break
				}
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
func (s *Service) queueTemplateDecision(ctx context.Context, kind, id string) (Job, error) {
	var t Template
	if err := s.Store.Get(ctx, "template", id, &t); err != nil {
		return Job{}, fmt.Errorf("Template revision not found.")
	}
	if kind == "build" {
		if err := s.actionableRevision(ctx, id); err != nil {
			return Job{}, err
		}
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
	if kind == "build" {
		if err := s.revisionPruneBlock(ctx, id); err != nil {
			return Job{}, err
		}
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
		// Save the current plan, rather than stale keys from an earlier build.
		keys = nil
		for _, im := range p.Images {
			if im.Playbook != "" {
				keys = append(keys, im.CacheKey)
			}
		}
		if err = s.Store.Put(ctx, "template-images", t.ID, keys); err != nil {
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
			if errors.Is(err, errRetainedRevision) {
				// No current image can be attributed to an older-format revision
				// without saved keys; unused-image removal treats such images as
				// unused as well.
				continue
			}
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
	// lifecycle fields conditionally into the current record in one SQL update.
	_, err = s.Store.DB.ExecContext(ctx, `UPDATE objects SET body=json_set(body,'$.built',json(?),'$.builtAt',json(?),'$.removedAt',json(?),'$.removing',json(?)) WHERE kind='template' AND id=? AND NOT EXISTS (SELECT 1 FROM objects op WHERE op.kind='template-operation' AND op.id=? AND json_extract(op.body,'$')!=?)`, jsonValue(t.Built), jsonValue(t.BuiltAt), jsonValue(t.RemovedAt), jsonValue(t.Removing), t.ID, t.ID, j.ID)
	return err
}

func (s *Service) queueTemplateOperation(ctx context.Context, kind, id string) (Job, error) {
	var result Job
	err := s.Store.atomic(ctx, func(ctx context.Context) error {
		var err error
		result, err = s.queueTemplateDecision(ctx, kind, id)
		return err
	})
	return result, err
}

func (s *Service) templateReadMetadata(ctx context.Context) (map[string]Job, map[string]string, error) {
	rows, err := s.Store.DB.QueryContext(ctx, `SELECT j.id,j.kind,j.target,j.status,j.error,j.created FROM jobs j WHERE j.kind IN ('build','remove-images') AND j.id=(SELECT newer.id FROM jobs newer WHERE newer.target=j.target AND newer.kind IN ('build','remove-images') ORDER BY newer.created DESC,newer.id DESC LIMIT 1)`)
	if err != nil {
		return nil, nil, err
	}
	operations := map[string]Job{}
	for rows.Next() {
		var j Job
		if err = rows.Scan(&j.ID, &j.Kind, &j.Target, &j.Status, &j.Error, newTime(&j.Created)); err != nil {
			rows.Close()
			return nil, nil, err
		}
		operations[j.Target] = j
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	rows, err = s.Store.DB.QueryContext(ctx, `SELECT id,body FROM objects WHERE kind='template-head'`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	heads := map[string]string{}
	for rows.Next() {
		var name string
		var raw []byte
		if err = rows.Scan(&name, &raw); err != nil {
			return nil, nil, err
		}
		var id string
		if err = json.Unmarshal(raw, &id); err != nil {
			return nil, nil, err
		}
		heads[name] = id
	}
	return operations, heads, rows.Err()
}
