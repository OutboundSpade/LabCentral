package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Student catalogs need summaries rather than private answer/solution snapshots.
// Read progress only by keys derived from this authenticated user and the exact
// published IDs, in bounded batches rather than one query per publication.
func (s *Store) learningCatalogLabs(ctx context.Context, user string) ([]LearningLab, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT p.body,`+policyJSON+`,`+cachedPolicyJSON+`,b.id IS NOT NULL,t.id IS NOT NULL FROM objects p LEFT JOIN objects t ON t.kind='template' AND t.id=json_extract(p.body,'$.template') LEFT JOIN objects b ON b.kind='template-instructor' AND b.id=t.id WHERE p.kind='learning-publication' ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	labs := []LearningLab{}
	for rows.Next() {
		var raw []byte
		var policyRaw, cached sql.NullString
		var hasBundle, hasTemplate bool
		if err = rows.Scan(&raw, &policyRaw, &cached, &hasBundle, &hasTemplate); err != nil {
			break
		}
		// Count stages while skipping their bounded private contents. Parsing JSON in
		// Go is substantially faster here than repeated SQLite JSON expressions.
		var publication struct {
			LearningLab
			Published bool       `json:"published"`
			OwnerID   string     `json:"ownerID"`
			Steps     []struct{} `json:"steps"`
		}
		if err = json.Unmarshal(raw, &publication); err != nil {
			break
		}
		if publication.Published && (publication.OwnerID == "" || publication.OwnerID == user) {
			if !hasTemplate {
				err = fmt.Errorf("pinned template disclosure policy unavailable")
				break
			}
			policy, policyErr := decodePolicy(policyRaw, cached, hasBundle)
			if policyErr != nil {
				err = policyErr
				break
			}
			if policy.Visibility.Audience == "coaches" {
				continue
			}
			publication.StepCount = len(publication.Steps)
			publication.CompletedSteps = 0 // Progress comes only from the user's record.
			publication.Attempt = publication.OwnerID != ""
			labs = append(labs, publication.LearningLab)
		}
	}
	rowErr := rows.Err()
	rows.Close() // Release the single connection before the progress query.
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, rowErr
	}
	const batchSize = 128
	for start := 0; start < len(labs); start += batchSize {
		end := min(start+batchSize, len(labs))
		args := make([]any, 0, end-start)
		indices := make(map[string]int, end-start)
		for i := start; i < end; i++ {
			key := progressKey(user, labs[i].ID)
			indices[key] = i
			args = append(args, key)
		}
		query := `SELECT id,body FROM objects WHERE kind='learning-progress' AND id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(args)), ",") + `)`
		rows, err = s.DB.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key string
			var raw []byte
			if err = rows.Scan(&key, &raw); err != nil {
				break
			}
			var progress LearningProgress
			if err = json.Unmarshal(raw, &progress); err != nil {
				break
			}
			labs[indices[key]].CompletedSteps = progress.CompletedSteps
		}
		rowErr = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if rowErr != nil {
			return nil, rowErr
		}
	}
	sort.SliceStable(labs, func(i, j int) bool { return labs[i].PublishedAt.After(labs[j].PublishedAt) })
	return labs, nil
}
