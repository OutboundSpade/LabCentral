package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// Read only disclosure metadata, never private author programs.
func decodePolicy(raw, cached sql.NullString, exists bool) (InstructorBundle, error) {
	var b InstructorBundle
	if raw.Valid {
		if err := json.Unmarshal([]byte(raw.String), &b); err != nil {
			return b, err
		}
	}
	if b.Visibility.Audience != "" && b.Visibility.Audience != "students" && b.Visibility.Audience != "coaches" {
		return b, fmt.Errorf("invalid disclosure policy")
	}
	if len(b.Visibility.HiddenMachines) > 32 || (len(b.Visibility.HiddenMachines) > 0 && b.Visibility.Audience != "coaches") {
		return b, fmt.Errorf("invalid disclosure policy")
	}
	seen := map[string]bool{}
	for _, name := range b.Visibility.HiddenMachines {
		if !stepIDPattern.MatchString(name) || seen[name] || (b.Practice != nil && name == b.Practice.DesktopMachine) {
			return b, fmt.Errorf("invalid hidden machine policy")
		}
		seen[name] = true
	}
	if cached.Valid {
		var v InstructorView
		if err := json.Unmarshal([]byte(cached.String), &v); err != nil {
			return b, err
		}
		if !exists || v.Audience != b.Visibility.Audience || len(v.HiddenMachines) != len(b.Visibility.HiddenMachines) {
			return b, fmt.Errorf("instructor disclosure policy unavailable")
		}
		for _, name := range v.HiddenMachines {
			if !seen[name] {
				return b, fmt.Errorf("instructor disclosure policy differs")
			}
		}
	}
	return b, nil
}

const policyJSON = `json_object('visibility',json_object('audience',coalesce(json_extract(b.body,'$.visibility.audience'),json_extract(b.body,'$.audience')),'hiddenMachines',coalesce(json_extract(b.body,'$.visibility.hiddenMachines'),json_extract(b.body,'$.hiddenMachines'))),'practice',json_extract(b.body,'$.practice'))`
const cachedPolicyJSON = `CASE WHEN json_extract(t.body,'$.instructor') IS NOT NULL THEN json_object('audience',json_extract(t.body,'$.instructor.audience'),'hiddenMachines',json_extract(t.body,'$.instructor.hiddenMachines')) END`

func (s *Store) instructorPolicy(ctx context.Context, id string) (InstructorBundle, error) {
	var raw, cached sql.NullString
	var exists bool
	err := s.DB.QueryRowContext(ctx, `SELECT `+policyJSON+`,`+cachedPolicyJSON+`,b.id IS NOT NULL FROM objects t LEFT JOIN objects b ON b.kind='template-instructor' AND b.id=t.id WHERE t.kind='template' AND t.id=?`, id).Scan(&raw, &cached, &exists)
	if err != nil {
		return InstructorBundle{}, err
	}
	return decodePolicy(raw, cached, exists)
}
func (s *Service) studentTemplate(ctx context.Context, id string) error {
	b, err := s.Store.instructorPolicy(ctx, id)
	if err != nil || b.Visibility.Audience == "coaches" {
		return fmt.Errorf("This lab is unavailable.")
	}
	return nil
}

// Retained personal allocations survive unpublication, but never a private policy.
func (s *Service) practiceDisclosure(ctx context.Context, lab string) error {
	var p LearningPublication
	if err := s.Store.Get(ctx, "learning-publication", lab, &p); err != nil {
		return err
	}
	coach := false
	if p.OwnerID != "" {
		coach = s.coachForUser(ctx, p.OwnerID)
	}
	return s.templateAudience(ctx, p.Template, coach)
}

func (s *Service) templateAudience(ctx context.Context, id string, coach bool) error {
	b, err := s.Store.instructorPolicy(ctx, id)
	if err != nil || (b.Visibility.Audience == "coaches" && !coach) {
		return fmt.Errorf("This lab is unavailable.")
	}
	return nil
}

// Last verified identity is persisted by login/refresh. Request paths refresh
// their session first; background preparation never contacts OIDC under SQL locks.
func (s *Store) coachUser(ctx context.Context, user string) bool {
	if current, ok := ctx.Value(verifiedUserKey{}).(User); ok {
		return current.ID == user && current.Admin
	}
	var u User
	return s.Get(ctx, "user", user, &u) == nil && u.ID == user && u.Admin
}

type verifiedUserKey struct{}

func (s *Service) coachForUser(ctx context.Context, user string) bool {
	if current, ok := ctx.Value(verifiedUserKey{}).(User); ok {
		return current.ID == user && current.Admin
	}
	return s.Store.coachUser(ctx, user)
}
