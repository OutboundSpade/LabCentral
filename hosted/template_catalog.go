package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"net/http"
	"sort"
	"strings"
	"time"
)

// PublicTemplate is an explicit instructional projection of a pinned revision.
// Never marshal Template or InstructorBundle through a student-facing route.
type PublicTemplate struct {
	CoachOnly        bool                      `json:"coachOnly,omitempty"`
	ID               string                    `json:"id"`
	Name             string                    `json:"name"`
	Commit           string                    `json:"commit"`
	CommitTitle      string                    `json:"commitTitle"`
	CommittedAt      *time.Time                `json:"committedAt,omitempty"`
	Description      string                    `json:"description"`
	Handover         string                    `json:"handover"`
	ImagesBuilt      bool                      `json:"imagesBuilt"`
	Machines         []PublicTemplateMachine   `json:"machines"`
	StageCount       int                       `json:"stageCount"`
	Stages           []LearningStep            `json:"stages"`
	PracticeStageIDs []string                  `json:"practiceStageIDs"`
	Library          *TemplateLibraryMetadata  `json:"library,omitempty"`
	Scenarios        []TemplateScenarioSummary `json:"scenarios"`
}
type PublicTemplateMachine struct {
	Name   string   `json:"name"`
	CPU    int      `json:"cpu"`
	Memory string   `json:"memory"`
	Disk   string   `json:"disk"`
	Access []string `json:"access"`
}
type TemplateCatalog struct {
	Templates []PublicTemplate `json:"templates"`
}

// Read only metadata from pinned snapshots. Guest files and private instructor
// programs stay in SQLite rather than being copied and base64-decoded per view.
// The public projection below remains the explicit disclosure boundary.
func (s *Service) publicTemplates(ctx context.Context, id string) ([]PublicTemplate, error) {
	return s.templateProjections(ctx, id, false)
}

func (s *Service) templateProjections(ctx context.Context, id string, coach bool) ([]PublicTemplate, error) {
	query := `SELECT json_remove(t.body,'$.instructor'),src.id,json_extract(src.body,'$.lab'),json_extract(b.body,'$.stages'),` + policyJSON + `,` + cachedPolicyJSON + `,b.id IS NOT NULL
 ,json_extract(b.body,'$.library'),json_extract(b.body,'$.scenarios') FROM objects t
 LEFT JOIN objects src ON src.kind='source' AND src.id=t.id
 LEFT JOIN objects b ON b.kind='template-instructor' AND b.id=t.id
 WHERE t.kind='template'`
	args := []any{}
	if id != "" {
		query += ` AND t.id=?`
		args = append(args, id)
	}
	rows, err := s.Store.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PublicTemplate{}
	for rows.Next() {
		var rawTemplate []byte
		var sourceID, rawLab, rawStages, rawPolicy, cachedPolicy, rawLibrary, rawScenarios sql.NullString
		var hasBundle bool
		if err := rows.Scan(&rawTemplate, &sourceID, &rawLab, &rawStages, &rawPolicy, &cachedPolicy, &hasBundle, &rawLibrary, &rawScenarios); err != nil {
			return nil, err
		}
		policy, policyErr := decodePolicy(rawPolicy, cachedPolicy, hasBundle)
		if policyErr != nil {
			return nil, policyErr
		}
		if policy.Audience == "coaches" && !coach {
			continue
		}
		if !sourceID.Valid {
			return nil, fmt.Errorf("pinned template source is unavailable")
		}
		var t Template
		if err := json.Unmarshal(rawTemplate, &t); err != nil {
			return nil, err
		}
		if sourceID.String != t.ID {
			return nil, fmt.Errorf("pinned template source identity does not match")
		}
		var lab lc.Lab
		if rawLab.Valid {
			if err := json.Unmarshal([]byte(rawLab.String), &lab); err != nil {
				return nil, err
			}
		}
		stages := []TemplateStage{}
		if rawStages.Valid {
			if err := json.Unmarshal([]byte(rawStages.String), &stages); err != nil {
				return nil, err
			}
		}
		public := PublicTemplate{CoachOnly: policy.Audience == "coaches", ID: t.ID, Name: t.Name, Commit: t.Commit, CommitTitle: t.CommitTitle, CommittedAt: t.CommittedAt, Handover: t.Handover, ImagesBuilt: t.Built && !t.Removing, Description: lab.Description, Machines: []PublicTemplateMachine{}, Stages: []LearningStep{}, PracticeStageIDs: []string{}}
		public.Scenarios = []TemplateScenarioSummary{}
		if rawLibrary.Valid {
			if err := json.Unmarshal([]byte(rawLibrary.String), &public.Library); err != nil {
				return nil, err
			}
		}
		if rawScenarios.Valid {
			var scenarios []TemplateScenario
			if err := json.Unmarshal([]byte(rawScenarios.String), &scenarios); err != nil {
				return nil, err
			}
			for _, scenario := range scenarios {
				public.Scenarios = append(public.Scenarios, scenario.summary())
			}
		}
		for name, machine := range lab.Machines {
			public.Machines = append(public.Machines, PublicTemplateMachine{name, machine.CPU, machine.Memory, machine.Disk, append([]string{}, machine.Access...)})
		}
		sort.Slice(public.Machines, func(i, j int) bool { return public.Machines[i].Name < public.Machines[j].Name })
		for _, stage := range stages {
			public.Stages = append(public.Stages, LearningStep{stage.ID, stage.Title, stage.Instructions, stage.Kind, stage.Prompt, strings.TrimSpace(stage.Solution) != ""})
			if (stage.Kind == "answer" || stage.Kind == "action") && stage.ExerciseID == "" {
				public.PracticeStageIDs = append(public.PracticeStageIDs, stage.ID)
			}
		}
		public.StageCount = len(public.Stages)
		out = append(out, public)
	}
	return out, rows.Err()
}

func (s *Service) templateCatalog(w http.ResponseWriter, r *http.Request, v Session) {
	templates, err := s.templateProjections(r.Context(), "", v.User.Admin)
	out := TemplateCatalog{Templates: templates}
	if err != nil {
		writeError(w, 503, "Templates are unavailable. Please retry.")
		return
	}
	sort.Slice(out.Templates, func(i, j int) bool {
		a, b := out.Templates[i], out.Templates[j]
		if a.CommittedAt != nil && b.CommittedAt != nil && !a.CommittedAt.Equal(*b.CommittedAt) {
			return a.CommittedAt.After(*b.CommittedAt)
		}
		if (a.CommittedAt == nil) != (b.CommittedAt == nil) {
			return a.CommittedAt != nil
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	writeJSON(w, 200, out)
}

func (s *Service) templateExplorer(w http.ResponseWriter, r *http.Request, v Session) {
	templates, err := s.templateProjections(r.Context(), r.PathValue("id"), v.User.Admin)
	if err == nil && len(templates) == 0 {
		err = sql.ErrNoRows
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "This template revision is unavailable.")
		return
	}
	if err != nil {
		writeError(w, 503, "This template revision is unavailable. Please retry.")
		return
	}
	writeJSON(w, 200, templates[0])
}
