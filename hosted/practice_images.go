package hosted

import (
	"context"
	"encoding/json"
	"slices"
)

// Pending allocations protect images even before native instances exist. Compare
// cache keys so another revision sharing these images is protected as well.
func (s *Service) practiceImageBlock(ctx context.Context, keys []string) (string, error) {
	rows, err := s.Store.DB.QueryContext(ctx, `SELECT o.body FROM practice_runs r JOIN objects o ON o.kind='learning-publication' AND o.id=r.lab_id WHERE r.status!='ended'`)
	if err != nil {
		return "", err
	}
	publications := []LearningPublication{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			break
		}
		var p LearningPublication
		if err = json.Unmarshal(raw, &p); err != nil {
			break
		}
		publications = append(publications, p)
	}
	errJoin := rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	if errJoin != nil {
		return "", errJoin
	}
	for _, p := range publications {
		used, err := s.templateKeys(ctx, p.Template)
		if err != nil {
			return "", err
		}
		for _, key := range keys {
			if slices.Contains(used, key) {
				return "Used by a personal cloud run. Finish its cleanup before removing images.", nil
			}
		}
	}
	return "", nil
}
