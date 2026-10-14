package hosted

import (
	"context"
)

// Pending allocations protect images even before native instances exist. Compare
// cache keys so another revision sharing these images is protected as well.
func (s *Service) practiceImageBlock(ctx context.Context, keys []string) (string, error) {
	used, err := s.practiceImageKeys(ctx)
	if err != nil {
		return "", err
	}
	for _, key := range keys {
		if used[key] {
			return "Used by a personal cloud run. Finish its cleanup before removing images.", nil
		}
	}
	return "", nil
}

func (s *Service) practiceImageKeys(ctx context.Context) (map[string]bool, error) {
	rows, err := s.Store.DB.QueryContext(ctx, `SELECT DISTINCT json_extract(o.body,'$.template') FROM practice_runs r JOIN objects o ON o.kind='learning-publication' AND o.id=r.lab_id WHERE r.status!='ended'`)
	if err != nil {
		return nil, err
	}
	var templates []string
	for rows.Next() {
		var template string
		if err = rows.Scan(&template); err != nil {
			rows.Close()
			return nil, err
		}
		templates = append(templates, template)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for _, template := range templates {
		used, err := s.templateKeys(ctx, template)
		if err != nil {
			return nil, err
		}
		for _, key := range used {
			keys[key] = true
		}
	}
	return keys, nil
}
