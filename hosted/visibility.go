package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
)

type guestIdentity struct {
	Address   string `json:"address"`
	PublicKey string `json:"publicKey"`
}

func (b InstructorBundle) validateVisibility(lab lc.Lab) error {
	if b.Audience != "" && b.Audience != "students" && b.Audience != "coaches" {
		return fmt.Errorf("audience must be students or coaches")
	}
	if len(b.HiddenMachines) > len(lab.Machines) {
		return fmt.Errorf("invalid hidden machines")
	}
	seen := map[string]bool{}
	for _, name := range b.HiddenMachines {
		if _, ok := lab.Machines[name]; !ok || seen[name] || name == "workstation" {
			return fmt.Errorf("hidden machines must be unique declared servers")
		}
		seen[name] = true
	}
	for name, machine := range lab.Machines {
		if len(b.HiddenMachines) > 0 && !lc.Contains(b.HiddenMachines, name) {
			image := lab.Images[machine.Image]
			if len(image.Files) == 0 || lc.Contains(image.Files, "lab.yaml") {
				return fmt.Errorf("student images require explicit build files excluding the topology manifest")
			}
			for _, hidden := range b.HiddenMachines {
				privateImage := lab.Images[lab.Machines[hidden].Image]
				if privateImage.Playbook != image.Playbook && lc.Contains(image.Files, privateImage.Playbook) {
					return fmt.Errorf("private image playbook selected for a student image")
				}
			}
			if len(machine.HostnamePeers) == 0 {
				return fmt.Errorf("student guests require explicit hostname peers when machines are hidden")
			}
			for _, hidden := range b.HiddenMachines {
				if lc.Contains(machine.HostnamePeers, hidden) {
					return fmt.Errorf("hidden machines must not enter student hostname inventories")
				}
			}
		}
	}
	if len(b.HiddenMachines) > 0 && b.Audience != "coaches" {
		return fmt.Errorf("hidden machines require a coach-only template")
	}
	return nil
}
func (b InstructorBundle) validateGuestInputs(src lc.Source, step GuestPlaybook) error {
	if len(step.Files) > 64 || len(step.GuestHosts) > len(src.Lab.Machines) {
		return fmt.Errorf("guest input bounds exceeded")
	}
	if b.Audience == "coaches" && !lc.Contains(b.HiddenMachines, step.Machine) && len(step.Files) == 0 {
		return fmt.Errorf("coach-only templates require explicit bank-guest file selections")
	}
	seen := map[string]bool{}
	for _, path := range step.Files {
		found := false
		for _, f := range b.Files {
			if f.Path == path {
				found = true
			}
		}
		if !found || seen[path] {
			return fmt.Errorf("unknown or duplicate guest file")
		}
		seen[path] = true
	}
	if len(step.Files) > 0 && !lc.Contains(step.Files, step.Playbook) {
		return fmt.Errorf("guest files must include the selected playbook")
	}
	seen = map[string]bool{}
	for _, name := range step.GuestHosts {
		if _, ok := src.Lab.Machines[name]; !ok || seen[name] {
			return fmt.Errorf("unknown or duplicate guest identity")
		}
		seen[name] = true
	}
	return nil
}

// Read only disclosure metadata, never private author programs.
func decodePolicy(raw, cached sql.NullString, exists bool) (InstructorBundle, error) {
	var b InstructorBundle
	if raw.Valid {
		if err := json.Unmarshal([]byte(raw.String), &b); err != nil {
			return b, err
		}
	}
	if b.Audience != "" && b.Audience != "students" && b.Audience != "coaches" {
		return b, fmt.Errorf("invalid disclosure policy")
	}
	if len(b.HiddenMachines) > 32 || (len(b.HiddenMachines) > 0 && b.Audience != "coaches") {
		return b, fmt.Errorf("invalid disclosure policy")
	}
	seen := map[string]bool{}
	for _, name := range b.HiddenMachines {
		if !stepIDPattern.MatchString(name) || seen[name] || name == "workstation" {
			return b, fmt.Errorf("invalid hidden machine policy")
		}
		seen[name] = true
	}
	if cached.Valid {
		var v InstructorView
		if err := json.Unmarshal([]byte(cached.String), &v); err != nil {
			return b, err
		}
		if !exists || v.Audience != b.Audience || len(v.HiddenMachines) != len(b.HiddenMachines) {
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

const policyJSON = `json_object('audience',json_extract(b.body,'$.audience'),'hiddenMachines',json_extract(b.body,'$.hiddenMachines'))`
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
	if err != nil || b.Audience == "coaches" {
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
	return s.studentTemplate(ctx, p.Template)
}
