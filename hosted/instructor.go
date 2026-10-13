package hosted

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/ssh"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type GuestPlaybook struct {
	Machine    string   `json:"machine" yaml:"machine"`
	Playbook   string   `json:"playbook" yaml:"playbook"`
	Files      []string `json:"files,omitempty" yaml:"files,omitempty"`
	GuestHosts []string `json:"guestHosts,omitempty" yaml:"guestHosts,omitempty"`
}
type TemplateExercise struct {
	ID          string          `json:"id" yaml:"id"`
	Name        string          `json:"name" yaml:"name"`
	Description string          `json:"description" yaml:"description"`
	Walkthrough string          `json:"walkthrough" yaml:"walkthrough"`
	Steps       []GuestPlaybook `json:"steps" yaml:"steps"`
}
type CheckDefinition struct {
	ID     string `json:"id" yaml:"id"`
	Name   string `json:"name" yaml:"name"`
	Points int    `json:"points" yaml:"points"`
}
type TemplateCheck struct {
	ID     string          `json:"id" yaml:"id"`
	Name   string          `json:"name" yaml:"name"`
	Points int             `json:"points" yaml:"points"`
	Steps  []GuestPlaybook `json:"steps" yaml:"steps"`
}

// TemplateStage is private author metadata. Student APIs explicitly project
// instructional fields and solution availability into LearningStep; actual
// solution text requires the deliberate solution-reveal endpoint.
type TemplateStage struct {
	ID           string `json:"id" yaml:"id"`
	Title        string `json:"title" yaml:"title"`
	Instructions string `json:"instructions" yaml:"instructions"`
	Kind         string `json:"kind" yaml:"kind"`
	Prompt       string `json:"prompt" yaml:"prompt"`
	Answer       string `json:"answer" yaml:"answer"`
	CheckID      string `json:"checkID" yaml:"checkID"`
	ExerciseID   string `json:"exerciseID" yaml:"exerciseID"`
	Solution     string `json:"solution" yaml:"solution"`
}
type InstructorBundle struct {
	Audience       string                   `json:"audience,omitempty" yaml:"audience,omitempty"`
	HiddenMachines []string                 `json:"hiddenMachines,omitempty" yaml:"hiddenMachines,omitempty"`
	Assets         []string                 `json:"assets" yaml:"assets"`
	Version        int                      `json:"version" yaml:"version"`
	Notes          string                   `json:"notes" yaml:"notes"`
	Exercises      []TemplateExercise       `json:"exercises" yaml:"exercises"`
	Reset          []GuestPlaybook          `json:"reset" yaml:"reset"`
	Checks         []TemplateCheck          `json:"checks" yaml:"checks"`
	Stages         []TemplateStage          `json:"stages" yaml:"stages"`
	Library        *TemplateLibraryMetadata `json:"library,omitempty" yaml:"library,omitempty"`
	Scenarios      []TemplateScenario       `json:"scenarios,omitempty" yaml:"scenarios,omitempty"`
	Files          []lc.File                `json:"files" yaml:"-"`
}
type InstructorView struct {
	Audience       string               `json:"audience,omitempty"`
	HiddenMachines []string             `json:"hiddenMachines,omitempty"`
	Notes          string               `json:"notes"`
	Checks         []CheckDefinition    `json:"checks"`
	Exercises      []ExerciseDefinition `json:"exercises"`
	Stages         []TemplateStage      `json:"stages"`
}

func (b InstructorBundle) view() InstructorView {
	v := InstructorView{Audience: b.Audience, HiddenMachines: append([]string{}, b.HiddenMachines...), Notes: b.Notes, Checks: []CheckDefinition{}, Exercises: []ExerciseDefinition{}, Stages: append([]TemplateStage{}, b.Stages...)}
	for _, c := range b.Checks {
		v.Checks = append(v.Checks, CheckDefinition{c.ID, c.Name, c.Points})
	}
	for _, x := range b.Exercises {
		v.Exercises = append(v.Exercises, ExerciseDefinition{x.ID, x.Name, x.Description, x.Walkthrough})
	}
	return v
}

// Instructor material is extracted before portable source loading: the bundle
// never enters images, handovers or build provenance. Student APIs use explicit
// instructional projections, never the private bundle.
func extractInstructor(root string) (*InstructorBundle, error) {
	path := filepath.Join(root, "instructor.yaml")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if _, err := os.Lstat(filepath.Join(root, "instructor")); err == nil {
			return nil, fmt.Errorf("instructor programs require instructor.yaml")
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > 128<<10 {
		return nil, fmt.Errorf("instructor manifest too large")
	}
	var node yaml.Node
	if err = yaml.Unmarshal(raw, &node); err != nil {
		return nil, err
	}
	var visit func(*yaml.Node, int) error
	visit = func(n *yaml.Node, depth int) error {
		if depth > 32 || n.Kind == yaml.AliasNode || n.Anchor != "" {
			return fmt.Errorf("instructor aliases/deep nesting unsupported")
		}
		if n.Kind == yaml.MappingNode {
			seen := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				k := n.Content[i]
				if k.Tag != "!!str" || seen[k.Value] {
					return fmt.Errorf("duplicate/non-string instructor key")
				}
				seen[k.Value] = true
			}
		}
		for _, c := range n.Content {
			if err := visit(c, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err = visit(&node, 0); err != nil {
		return nil, err
	}
	var bundle InstructorBundle
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err = decoder.Decode(&bundle); err != nil {
		return nil, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("expected one instructor document")
	}
	if (bundle.Version != 1 && bundle.Version != 2) || len(bundle.Exercises) > 20 || len(bundle.Checks) > 20 || len(bundle.Notes) > 32000 || len(bundle.Stages) > 20 || (bundle.Version == 1 && len(bundle.Stages) > 0) {
		return nil, fmt.Errorf("invalid instructor manifest bounds/version")
	}
	files := filepath.Join(root, "instructor")
	size := 0
	err = filepath.WalkDir(files, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(files, path)
		if d.Type()&os.ModeSymlink != 0 || !regexp.MustCompile(`^[a-zA-Z0-9_./-]+$`).MatchString(rel) || strings.HasPrefix(rel, ".") {
			return fmt.Errorf("unsafe instructor file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		size += len(data)
		if size > 1<<20 || len(bundle.Files) >= 64 {
			return fmt.Errorf("instructor programs exceed limits")
		}
		bundle.Files = append(bundle.Files, lc.File{Path: filepath.ToSlash(rel), Content: data})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err = os.Remove(path); err != nil {
		return nil, err
	}
	if err = os.RemoveAll(files); err != nil {
		return nil, err
	}
	return &bundle, nil
}
func (b InstructorBundle) validate(src lc.Source) error {
	if err := b.validateVisibility(src.Lab); err != nil {
		return err
	}
	assetBytes := 0
	if len(b.Assets) > 20 {
		return fmt.Errorf("too many instructor assets")
	}
	for _, asset := range b.Assets {
		found := false
		for _, f := range src.Files {
			if f.Path == asset {
				assetBytes += len(f.Content)
				found = true
			}
		}
		if !found {
			return fmt.Errorf("missing instructor asset %q", asset)
		}
	}

	if assetBytes > 1<<20 {
		return fmt.Errorf("instructor assets exceed 1 MiB")
	}
	seen := map[string]bool{"reset": true, "check": true}
	validate := func(steps []GuestPlaybook) error {
		if len(steps) < 1 || len(steps) > 8 {
			return fmt.Errorf("instructor actions require one to eight steps")
		}
		for _, step := range steps {
			if err := b.validateGuestInputs(src, step); err != nil {
				return err
			}
			if _, ok := src.Lab.Machines[step.Machine]; !ok {
				return fmt.Errorf("unknown instructor machine %q", step.Machine)
			}
			found := false
			for _, f := range b.Files {
				if f.Path == step.Playbook {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("missing instructor playbook %q", step.Playbook)
			}
		}
		return nil
	}
	if len(b.Exercises) > 0 {
		if err := validate(b.Reset); err != nil {
			return err
		}
	}
	for _, x := range b.Exercises {
		if !regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`).MatchString(x.ID) || seen[x.ID] || x.Name == "" {
			return fmt.Errorf("invalid instructor exercise")
		}
		seen[x.ID] = true
		if err := validate(x.Steps); err != nil {
			return err
		}
	}
	for _, c := range b.Checks {
		if !regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`).MatchString(c.ID) || seen[c.ID] || c.Name == "" || c.Points < 1 || c.Points > 100 {
			return fmt.Errorf("invalid instructor check")
		}
		seen[c.ID] = true
		if err := validate(c.Steps); err != nil {
			return err
		}
	}
	if err := b.validateStages(); err != nil {
		return err
	}
	return b.validateScenarios()
}

func (b InstructorBundle) validateStages() error {
	if len(b.Stages) > 20 || (len(b.Stages) > 0 && b.Version != 2) {
		return fmt.Errorf("template stages require instructor version 2 and at most 20 stages")
	}
	checks, exercises, seen := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range b.Checks {
		checks[c.ID] = true
	}
	for _, x := range b.Exercises {
		exercises[x.ID] = true
	}
	for _, stage := range b.Stages {
		if !stepIDPattern.MatchString(stage.ID) || seen[stage.ID] || !cleanName(stage.Title) || strings.TrimSpace(stage.Instructions) == "" || len(stage.Instructions) > 6000 || len(stage.Prompt) > 1000 || len(stage.Solution) > 6000 {
			return fmt.Errorf("template stages require unique IDs, titles and bounded instructions, prompts and solutions")
		}
		seen[stage.ID] = true
		if stage.ExerciseID != "" && !exercises[stage.ExerciseID] {
			return fmt.Errorf("stage %q references an unknown exercise", stage.ID)
		}
		switch stage.Kind {
		case "answer":
			if strings.TrimSpace(stage.Answer) == "" || len(stage.Answer) > 1000 || stage.CheckID != "" {
				return fmt.Errorf("answer stages require a bounded answer and no action check")
			}
		case "action":
			if !checks[stage.CheckID] || stage.Answer != "" {
				return fmt.Errorf("action stages require a pinned check and no answer")
			}
		case "instructor":
			if stage.Answer != "" || stage.CheckID != "" {
				return fmt.Errorf("instructor stages cannot contain an answer or check")
			}
		default:
			return fmt.Errorf("stage kind must be answer, action or instructor")
		}
	}
	return nil
}

// The fixed launcher writes a private temporary bundle and runs author Ansible
// only inside a verified guest. Output stays bounded and is not put in APIs.
var errPlaybookFailed = errors.New("guest playbook did not pass")

const guestPlaybookLauncher = `import base64,json,pathlib,subprocess,sys,tempfile
p=json.loads(base64.b64decode(sys.argv[1]))
with tempfile.TemporaryDirectory(prefix='labcentral-check-',dir='/run') as root:
 for f in p['files']:
  target=pathlib.Path(root)/f['path']; target.parent.mkdir(parents=True,exist_ok=True); target.write_bytes(base64.b64decode(f['content'])); target.chmod(0o600)
 with tempfile.TemporaryFile() as output:
  result=subprocess.run(['/opt/labcentral-ansible/bin/ansible-playbook','--inventory','localhost,','--connection','local',str(pathlib.Path(root)/p['playbook'])],cwd=root,stdout=output,stderr=output,timeout=90)
  if result.returncode: sys.exit(42 if result.returncode==2 else 43)
`

func (s *Service) runGuestPlaybooks(ctx context.Context, team Team, src lc.Source, b InstructorBundle, steps []GuestPlaybook) error {
	engine, ok := s.Engine.(exerciseSSH)
	if !ok {
		return fmt.Errorf("guest execution unavailable")
	}
	for _, step := range steps {
		err := engine.WithSSHMaterial(ctx, team.InstanceID, step.Machine, func(inst lc.Instance, m lc.PlannedMachine, r lc.Resource, key []byte) error {
			if inst.Plan.Source.Digest != src.Digest {
				return fmt.Errorf("guest source does not match pinned template")
			}
			client, err := remote.Dial(ctx, r.Address, "lab", key, m.HostPublicKey)
			if err != nil {
				return fmt.Errorf("%s SSH unavailable", step.Machine)
			}
			defer client.Close()
			files, err := b.guestFiles(src, inst, step)
			if err != nil {
				return err
			}
			payload, _ := json.Marshal(struct {
				Files    []lc.File `json:"files"`
				Playbook string    `json:"playbook"`
			}{files, step.Playbook})
			_, err = remote.Run(ctx, client, "sudo -n timeout --kill-after=5s 100s flock -w 5 /run/lock/labcentral-exercise.lock python3 -c "+quote(guestPlaybookLauncher)+" "+quote(base64.StdEncoding.EncodeToString(payload)))
			if err != nil {
				var exit *ssh.ExitError
				if errors.As(err, &exit) && exit.ExitStatus() == 42 {
					return fmt.Errorf("%s: %w", step.Machine, errPlaybookFailed)
				}
				return fmt.Errorf("%s guest check outcome unavailable", step.Machine)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (b InstructorBundle) guestFiles(src lc.Source, inst lc.Instance, step GuestPlaybook) ([]lc.File, error) {
	if err := b.validateGuestInputs(src, step); err != nil {
		return nil, err
	}
	files := []lc.File{}
	for _, f := range b.Files {
		if len(step.Files) == 0 || lc.Contains(step.Files, f.Path) {
			files = append(files, f)
		}
	}
	if len(step.GuestHosts) > 0 {
		hosts := map[string]guestIdentity{}
		for _, name := range step.GuestHosts {
			for _, machine := range inst.Plan.Machines {
				if machine.Name == name {
					hosts[name] = guestIdentity{machine.Address, machine.HostPublicKey}
				}
			}
		}
		if len(hosts) != len(step.GuestHosts) {
			return nil, fmt.Errorf("pinned guest identities unavailable")
		}
		data, _ := json.Marshal(hosts)
		files = append(files, lc.File{Path: "guest-hosts.json", Content: data})
	}
	for _, asset := range b.Assets {
		for _, file := range src.Files {
			if file.Path == asset {
				file.Path = "template/" + file.Path
				files = append(files, file)
			}
		}
	}
	return files, nil
}
