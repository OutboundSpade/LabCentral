package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/OutboundSpade/LabCentral/v2/instructor"
)

// These adapters read retained hosted snapshots only. Author YAML still goes
// through the strict v3 loader; neither source nor private programs are migrated.
func retainedStages(raw []byte) ([]TemplateStage, error) {
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	stages := make([]TemplateStage, 0, len(entries))
	for _, entry := range entries {
		var stage TemplateStage
		if err := json.Unmarshal(entry, &stage); err != nil {
			return nil, err
		}
		var old struct {
			Kind       string `json:"kind"`
			Answer     string `json:"answer"`
			CheckID    string `json:"checkID"`
			ExerciseID string `json:"exerciseID"`
		}
		if err := json.Unmarshal(entry, &old); err != nil {
			return nil, err
		}
		if old.Kind != "" {
			stage.Break = old.ExerciseID
			switch old.Kind {
			case "answer":
				stage.Completion = instructor.Completion{Answer: &instructor.Answer{Accepted: []string{old.Answer}, CaseSensitive: true}}
			case "action":
				stage.Completion = instructor.Completion{Check: old.CheckID}
			case "instructor":
				value := true
				stage.Completion = instructor.Completion{Instructor: &value}
			default:
				return nil, fmt.Errorf("invalid retained stage kind %q", old.Kind)
			}
		}
		stages = append(stages, stage)
	}
	return stages, nil
}

func retainedScenarios(raw []byte) ([]TemplateScenario, error) {
	var scenarios []TemplateScenario
	if err := json.Unmarshal(raw, &scenarios); err != nil {
		return nil, err
	}
	var old []struct {
		Variants []struct {
			ExerciseIDs []string `json:"exerciseIDs"`
			StageIDs    []string `json:"stageIDs"`
		} `json:"variants"`
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		return nil, err
	}
	for i := range scenarios {
		for j := range scenarios[i].Variants {
			variant := &scenarios[i].Variants[j]
			if variant.Breaks == nil {
				variant.Breaks = old[i].Variants[j].ExerciseIDs
			}
			if variant.Stages == nil {
				variant.Stages = old[i].Variants[j].StageIDs
			}
		}
	}
	return scenarios, nil
}

func decodeRetainedBundle(raw []byte, b *InstructorBundle) error {
	if err := json.Unmarshal(raw, b); err != nil {
		return err
	}
	if b.Version != 1 && b.Version != 2 {
		return nil
	}
	var old struct {
		Audience       string          `json:"audience"`
		HiddenMachines []string        `json:"hiddenMachines"`
		Exercises      []TemplateBreak `json:"exercises"`
		Stages         json.RawMessage `json:"stages"`
		Scenarios      json.RawMessage `json:"scenarios"`
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		return err
	}
	b.Visibility = instructor.Visibility{Audience: old.Audience, HiddenMachines: old.HiddenMachines}
	b.Breaks = old.Exercises
	var err error
	if len(old.Stages) > 0 {
		b.Stages, err = retainedStages(old.Stages)
		if err != nil {
			return err
		}
	}
	if len(old.Scenarios) > 0 {
		b.Scenarios, err = retainedScenarios(old.Scenarios)
	}
	return err
}

func (v *InstructorView) UnmarshalJSON(raw []byte) error {
	type plain InstructorView
	var snapshot struct {
		*plain
		Exercises []BreakDefinition `json:"exercises"`
		Stages    json.RawMessage   `json:"stages"`
	}
	*v = InstructorView{}
	snapshot.plain = (*plain)(v)
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return err
	}
	if v.Breaks == nil {
		v.Breaks = snapshot.Exercises
	}
	if len(snapshot.Stages) > 0 {
		var err error
		v.Stages, err = retainedStages(snapshot.Stages)
		return err
	}
	return nil
}

func (a *templateAttempt) UnmarshalJSON(raw []byte) error {
	type plain templateAttempt
	var snapshot struct {
		*plain
		ExerciseIDs []string        `json:"exerciseIDs"`
		Bundle      json.RawMessage `json:"bundle"`
	}
	*a = templateAttempt{}
	snapshot.plain = (*plain)(a)
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return err
	}
	if a.Breaks == nil {
		a.Breaks = snapshot.ExerciseIDs
	}
	if len(snapshot.Bundle) > 0 {
		return decodeRetainedBundle(snapshot.Bundle, &a.Bundle)
	}
	return nil
}

const retainedRevisionMessage = "This historical revision supports history and cleanup only. Refresh templates and choose a v3 revision for new actions."

var errRetainedRevision = errors.New(retainedRevisionMessage)

// Read the version without materializing guest files. Cleanup must never call
// this guard: it operates on saved allocation and image identities.
func (s *Service) actionableRevision(ctx context.Context, id string) error {
	var version int
	if err := s.Store.DB.QueryRowContext(ctx, `SELECT coalesce(json_extract(body,'$.lab.version'),0) FROM objects WHERE kind='source' AND id=?`, id).Scan(&version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// This is a historical-format guard. The caller's existing source
			// availability and planning checks still govern missing snapshots.
			return nil
		}
		return err
	}
	if version == 1 || version == 2 {
		return errRetainedRevision
	}
	return nil
}

func (s *Store) Get(ctx context.Context, kind, id string, value any) error {
	if bundle, ok := value.(*InstructorBundle); ok && kind == "template-instructor" {
		var raw []byte
		if err := s.DB.QueryRowContext(ctx, `SELECT body FROM objects WHERE kind=? AND id=?`, kind, id).Scan(&raw); err != nil {
			return err
		}
		return decodeRetainedBundle(raw, bundle)
	}
	return s.Store.Get(ctx, kind, id, value)
}
