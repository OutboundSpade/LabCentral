package hosted

import (
	"context"
	"database/sql"
	"errors"
	"hash/fnv"
	"time"
)

// Student interactions are separate from automatic progress/check updates.
// Passive application polling does not keep an abandoned allocation active.
func (s *Service) touchLearning(ctx context.Context, user, lab string) error {
	now := time.Now().UTC()
	// Coalesce desktop panel reads; check-now/answer still have immediate effects.
	_, err := s.Store.DB.ExecContext(ctx, `INSERT INTO objects(kind,id,body) VALUES('learning-activity',?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body WHERE julianday(json_extract(objects.body,'$')) < julianday(json_extract(excluded.body,'$')) - 10.0/86400`, progressKey(user, lab), jsonValue(now))
	if err != nil {
		return err
	}
	_, err = s.Store.DB.ExecContext(ctx, `UPDATE objects SET body=? WHERE kind='learning-check-due' AND id IN (SELECT id FROM practice_runs WHERE user_id=? AND lab_id=? AND status='ready') AND julianday(json_extract(body,'$'))>julianday(?)`, jsonValue(now.Add(20*time.Second)), user, lab, now.Add(20*time.Second).Format(time.RFC3339Nano))
	return err
}
func learningInterval(key string, active bool) time.Duration {
	base := 20 * time.Second
	if !active {
		base = 60 * time.Second
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	// Stable per-allocation jitter avoids aligned cohorts; cadence stays ±10%.
	return base + time.Duration(int64(h.Sum32()%4001)-2000)*base/20000
}
func (s *Service) learningDue(ctx context.Context, user, lab, run string, created time.Time, now time.Time) (bool, error) {
	var next time.Time
	err := s.Store.Get(ctx, "learning-check-due", run, &next)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	return !next.After(now), nil
}
func (s *Service) nextLearningCheck(ctx context.Context, user, lab, run string, created, now time.Time) error {
	last := created
	err := s.Store.Get(ctx, "learning-activity", progressKey(user, lab), &last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return s.Store.Put(ctx, "learning-check-due", run, now.Add(learningInterval(run, now.Sub(last) < 5*time.Minute)))
}

func (s *Service) learningCandidates(ctx context.Context) ([]struct {
	run, user, lab string
	created        time.Time
}, error) {
	rows, err := s.Store.DB.QueryContext(ctx, `SELECT id,user_id,lab_id,created FROM practice_runs WHERE status='ready' AND expires>?`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []struct {
		run, user, lab string
		created        time.Time
	}
	for rows.Next() {
		var c struct {
			run, user, lab string
			created        time.Time
		}
		if err = rows.Scan(&c.run, &c.user, &c.lab, newTime(&c.created)); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}
