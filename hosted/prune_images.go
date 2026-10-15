package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
)

// Public progress deliberately omits cache keys and native provider identities.
type ImagePruneOutcome struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}
type ImageSummary struct {
	Images       int `json:"images"`
	UnusedImages int `json:"unusedImages"`
}
type pruneImage struct {
	Key     string            `json:"key"`
	Outcome ImagePruneOutcome `json:"outcome"`
}
type imagePrune struct {
	Images     []pruneImage `json:"images"`
	ReadKeys   []string     `json:"readKeys"`
	Reconciled bool         `json:"reconciled"`
}

// Include saved identities and, when available, the current planner's identities.
// Rebuilding with a changed environment must not make still-needed images vanish.
func (s *Service) protectedRevisionKeys(ctx context.Context, id string) ([]string, error) {
	keys, err := s.templateKeys(ctx, id)
	if err != nil {
		return nil, err
	}
	var src lc.Source
	if err = s.Store.Get(ctx, "source", id, &src); errors.Is(err, sql.ErrNoRows) {
		return keys, nil
	} else if err != nil {
		return nil, err
	}
	if err = s.actionableRevision(ctx, id); errors.Is(err, errRetainedRevision) {
		return keys, nil
	} else if err != nil {
		return nil, err
	}
	p, err := s.plan(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, im := range p.Images {
		if im.Playbook != "" && !slices.Contains(keys, im.CacheKey) {
			keys = append(keys, im.CacheKey)
		}
	}
	return keys, nil
}

func (s *Service) neededImageKeys(ctx context.Context) (map[string]bool, error) {
	_, heads, err := s.templateReadMetadata(ctx)
	if err != nil {
		return nil, err
	}
	revisions := map[string]bool{}
	for _, id := range heads {
		revisions[id] = true
	}
	m, err := s.Store.Meeting(ctx)
	if err != nil {
		return nil, err
	}
	if m != nil && m.Status != "ended" {
		revisions[m.Template] = true
	}
	// Pending and cleanup allocations retain the exact publication revision.
	rows, err := s.Store.DB.QueryContext(ctx, `SELECT DISTINCT json_extract(o.body,'$.template') FROM practice_runs r LEFT JOIN objects o ON o.kind='learning-publication' AND o.id=r.lab_id WHERE r.status!='ended'`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id sql.NullString
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		if !id.Valid {
			rows.Close()
			return nil, fmt.Errorf("active practice revision is unavailable")
		}
		revisions[id.String] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// A queued build is protected before it has an engine build intent.
	rows, err = s.Store.DB.QueryContext(ctx, `SELECT DISTINCT target FROM jobs WHERE kind='build' AND status IN ('queued','running')`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		revisions[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for id := range revisions {
		ks, err := s.protectedRevisionKeys(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, k := range ks {
			keys[k] = true
		}
	}
	// Build records outlive successful builds and are only removed with their image.
	// Protect a build only while it has no published image yet.
	builds, err := s.Engine.Builds(ctx)
	if err != nil {
		return nil, err
	}
	images, err := s.Engine.Images(ctx)
	if err != nil {
		return nil, err
	}
	published := map[string]bool{}
	for _, im := range images {
		published[im.Key] = true
	}
	for _, b := range builds {
		if !published[b.Image.CacheKey] {
			keys[b.Image.CacheKey] = true
		}
	}
	return keys, nil
}
func (s *Service) imageInventory(ctx context.Context) (ImageSummary, imagePrune, error) {
	summary, plan := ImageSummary{}, imagePrune{Images: []pruneImage{}, ReadKeys: []string{}}
	images, err := s.Engine.Images(ctx)
	if err != nil {
		return summary, plan, err
	}
	summary.Images = len(images)
	if len(images) == 0 {
		return summary, plan, nil
	}
	keys, err := s.neededImageKeys(ctx)
	if err != nil {
		return summary, plan, err
	}
	for _, im := range images {
		if keys[im.Key] {
			plan.ReadKeys = append(plan.ReadKeys, im.Key)
		} else {
			plan.Images = append(plan.Images, pruneImage{Key: im.Key, Outcome: ImagePruneOutcome{Name: im.Name, Status: "pending"}})
		}
	}
	summary.UnusedImages = len(plan.Images)
	return summary, plan, nil
}
func (s *Service) pruneImages(w http.ResponseWriter, r *http.Request, v Session) {
	s.coordinate(func(w http.ResponseWriter, r *http.Request, _ Session) {
		var active int
		err := s.Store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM jobs WHERE kind='prune-images' AND status IN ('queued','running')`).Scan(&active)
		if err != nil {
			writeError(w, 503, "Image cleanup is unavailable.")
			return
		}
		if active != 0 {
			writeError(w, 409, "Image cleanup is already queued or running.")
			return
		}
		var unresolved int
		if err = s.Store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM objects p,json_each(p.body,'$.images') im WHERE p.kind='image-prune' AND (json_extract(im.value,'$.outcome.status') IN ('removing','unknown') OR (json_extract(im.value,'$.outcome.status')='removed' AND coalesce(json_extract(p.body,'$.reconciled'),0)=0))`).Scan(&unresolved); err != nil {
			writeError(w, 503, "Image cleanup is unavailable.")
			return
		}
		if unresolved > 0 {
			writeError(w, 409, "Image cleanup needs attention. Retry cleanup in Live activity.")
			return
		}
		_, plan, err := s.imageInventory(r.Context())
		if err != nil {
			writeError(w, 503, "Image inventory is unavailable. Please retry.")
			return
		}
		id := randomID(12)
		if err = s.Store.Put(r.Context(), "image-prune", id, plan); err != nil {
			writeError(w, 503, "Could not save image cleanup intent.")
			return
		}
		j, err := s.Store.Enqueue(r.Context(), "prune-images", id)
		if err != nil {
			writeError(w, 503, "Could not queue image cleanup.")
			return
		}
		writeJSON(w, 202, j)
	})(w, r, v)
}

// Called within the same writer transaction as allocation decisions. The worker
// reserves candidates in that transaction before any potentially slow removal.
func (s *Service) imagePruneBlock(ctx context.Context, keys []string) error {
	rows, err := s.Store.DB.QueryContext(ctx, `SELECT p.body FROM objects p WHERE p.kind='image-prune' AND EXISTS (SELECT 1 FROM json_each(p.body,'$.images') im WHERE (json_extract(im.value,'$.outcome.status') IN ('removing','unknown') OR (json_extract(im.value,'$.outcome.status')='removed' AND coalesce(json_extract(p.body,'$.reconciled'),0)=0)))`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var p imagePrune
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &p); err != nil {
			return err
		}
		for _, im := range p.Images {
			if (im.Outcome.Status == "removing" || im.Outcome.Status == "unknown" || (im.Outcome.Status == "removed" && !p.Reconciled)) && slices.Contains(keys, im.Key) {
				return fmt.Errorf("Image cleanup is unresolved. Retry cleanup in Live activity before using this revision.")
			}
		}
	}
	return rows.Err()
}
func (s *Service) revisionPruneBlock(ctx context.Context, id string) error {
	var pending int
	if err := s.Store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM objects p,json_each(p.body,'$.images') im WHERE p.kind='image-prune' AND (json_extract(im.value,'$.outcome.status') IN ('removing','unknown') OR (json_extract(im.value,'$.outcome.status')='removed' AND coalesce(json_extract(p.body,'$.reconciled'),0)=0))`).Scan(&pending); err != nil {
		return err
	}
	if pending == 0 {
		return nil
	}
	keys, err := s.protectedRevisionKeys(ctx, id)
	if err != nil {
		return err
	}
	return s.imagePruneBlock(ctx, keys)
}

func (s *Service) runPruneImages(ctx context.Context, j Job) error {
	var latest, status string
	if err := s.Store.DB.QueryRowContext(ctx, `SELECT id,status FROM jobs WHERE kind='prune-images' AND target=? ORDER BY created DESC,id DESC LIMIT 1`, j.Target).Scan(&latest, &status); err != nil {
		return err
	}
	if latest != j.ID {
		return fmt.Errorf("image cleanup was superseded")
	}
	if status == "complete" {
		return nil
	}
	var p imagePrune
	if err := s.Store.Get(ctx, "image-prune", j.Target, &p); err != nil {
		return err
	}
	// Freeze the deletion set when queued; revalidation can only shrink it. Its
	// image claims therefore remain sufficient even after a new revision/build.
	err := s.Store.atomic(ctx, func(ctx context.Context) error {
		needed, err := s.neededImageKeys(ctx)
		if err != nil {
			return err
		}
		for n := range p.Images {
			im := &p.Images[n]
			if im.Outcome.Status == "removed" {
				continue
			}
			if needed[im.Key] {
				if im.Outcome.Status == "unknown" || im.Outcome.Status == "removing" {
					return fmt.Errorf("A needed image has unresolved cleanup. Operator reconciliation is required.")
				}
				im.Outcome.Status, im.Outcome.Message = "skipped", "Kept for a revision or build."
			} else {
				im.Outcome.Status, im.Outcome.Message = "removing", ""
			}
		}
		p.Reconciled = false
		return s.Store.Put(ctx, "image-prune", j.Target, p)
	})
	if err != nil {
		return err
	}
	var failures []error
	for n := range p.Images {
		im := &p.Images[n]
		if im.Outcome.Status != "removing" {
			continue
		}
		// A response may have been lost after confirmed removal. Absence in the
		// engine journal confirms completion; it is never inferred from a timeout.
		images, err := s.Engine.Images(ctx)
		if err == nil && slices.ContainsFunc(images, func(i lc.ImageRecord) bool { return i.Key == im.Key }) {
			err = s.Engine.RemoveImage(ctx, im.Key)
		}
		im.Outcome.Status, im.Outcome.Message = "removed", ""
		if err != nil {
			var problem *lc.Error
			if errors.As(err, &problem) && problem.Code == lc.Invalid && problem.Message == "image is referenced by a retained instance" {
				im.Outcome.Status, im.Outcome.Message = "skipped", "Referenced by a retained instance."
			} else {
				im.Outcome.Status, im.Outcome.Message = "unknown", "Removal unconfirmed. Retry cleanup."
				failures = append(failures, fmt.Errorf("%s: removal unconfirmed", im.Outcome.Name))
			}
		}
		saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		save := s.Store.Put(saveCtx, "image-prune", j.Target, p)
		cancel()
		if save != nil {
			return errors.Join(save, errors.Join(failures...))
		}
	}
	// Reconcile every revision, including revisions whose old image keys were
	// overwritten by a rebuild. History and original keys remain available.
	saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	save := s.Store.atomic(saveCtx, func(ctx context.Context) error {
		if err := s.reconcilePrunedRevisions(ctx); err != nil {
			return err
		}
		p.Reconciled = true
		return s.Store.Put(ctx, "image-prune", j.Target, p)
	})
	return errors.Join(errors.Join(failures...), save)
}
func (s *Service) reconcilePrunedRevisions(ctx context.Context) error {
	images, err := s.Engine.Images(ctx)
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, im := range images {
		present[im.Key] = true
	}
	templates, err := s.rawTemplates(ctx)
	if err != nil {
		return err
	}
	for _, t := range templates {
		keys, err := s.templateKeys(ctx, t.ID)
		if errors.Is(err, errRetainedRevision) {
			// An older-format revision without saved keys owns no image this prune
			// kept, so it is no longer built. Leave unresolved cleanup to its job.
			if t.Built && !t.Removing {
				if _, err = s.Store.DB.ExecContext(ctx, `UPDATE objects SET body=json_set(body,'$.built',json('false')) WHERE kind='template' AND id=?`, t.ID); err != nil {
					return err
				}
			}
			continue
		}
		if err != nil {
			return err
		}
		ready := 0
		for _, key := range keys {
			if present[key] {
				ready++
			}
		}
		if ready == len(keys) {
			continue
		}
		if _, err = s.Store.DB.ExecContext(ctx, `UPDATE objects SET body=json_set(body,'$.built',json('false'),'$.imagesReady',?,'$.imagesTotal',?) WHERE kind='template' AND id=?`, ready, len(keys), t.ID); err != nil {
			return err
		}
	}
	return nil
}
