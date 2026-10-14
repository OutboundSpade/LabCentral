package hosted

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/instructor"
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	"golang.org/x/crypto/ssh"
	"os"
	"path/filepath"
	"time"
)

type InstructorBundle = instructor.Bundle
type GuestPlaybook = instructor.Step
type TemplateBreak = instructor.Break
type TemplateCheck = instructor.Check
type TemplateStage = instructor.Stage
type TemplateLibraryMetadata = instructor.Library
type TemplateScenario = instructor.Scenario
type ScenarioVariant = instructor.Variant

type CheckDefinition struct {
	ID     string `json:"id" yaml:"id"`
	Name   string `json:"name" yaml:"name"`
	Points int    `json:"points" yaml:"points"`
}
type InstructorView struct {
	Audience       string            `json:"audience,omitempty"`
	HiddenMachines []string          `json:"hiddenMachines,omitempty"`
	Notes          string            `json:"notes"`
	Checks         []CheckDefinition `json:"checks"`
	Breaks         []BreakDefinition `json:"breaks"`
	Stages         []TemplateStage   `json:"stages"`
}

func instructorView(b InstructorBundle) InstructorView {
	v := InstructorView{Audience: b.Visibility.Audience, HiddenMachines: append([]string{}, b.Visibility.HiddenMachines...), Notes: b.Notes, Checks: []CheckDefinition{}, Breaks: []BreakDefinition{}, Stages: append([]TemplateStage{}, b.Stages...)}
	for _, c := range b.Checks {
		v.Checks = append(v.Checks, CheckDefinition{c.ID, c.Name, c.Points})
	}
	for _, x := range b.Breaks {
		v.Breaks = append(v.Breaks, BreakDefinition{x.ID, x.Name, x.Description, x.Walkthrough})
	}
	return v
}

// Extract only from the disposable hosted snapshot.
func extractInstructor(root string) (*InstructorBundle, error) {
	b, e := instructor.Read(root)
	if e != nil || b == nil {
		return b, e
	}
	if e = os.Remove(filepath.Join(root, "instructor.yaml")); e != nil {
		return nil, e
	}
	if e = os.RemoveAll(filepath.Join(root, "instructor")); e != nil {
		return nil, e
	}
	return b, nil
}

// The fixed launcher writes a private temporary bundle and runs author Ansible
// only inside a verified guest. Output stays bounded and is not put in APIs.
var errPlaybookFailed = errors.New("guest playbook did not pass")
var errGuestUnknown = errors.New("guest step outcome is unknown")

const guestPlaybookLauncher = `import base64,json,pathlib,subprocess,sys,tempfile
p=json.loads(base64.b64decode(sys.argv[1]))
with tempfile.TemporaryDirectory(prefix='labcentral-check-',dir='/run') as root:
 for f in p['files']:
  target=pathlib.Path(root)/f['path']; target.parent.mkdir(parents=True,exist_ok=True); target.write_bytes(base64.b64decode(f['content'])); target.chmod(0o600)
 with tempfile.TemporaryFile() as output:
  result=subprocess.run(['/opt/labcentral-ansible/bin/ansible-playbook','--inventory','localhost,','--connection','local',str(pathlib.Path(root)/p['playbook'])],cwd=root,stdout=output,stderr=output,timeout=p['timeoutSeconds'])
  if result.returncode: sys.exit(42 if result.returncode==2 else 43)
`

func (s *Service) runGuestPlaybooks(ctx context.Context, team Team, src lc.Source, b InstructorBundle, steps []GuestPlaybook, extensions ...lc.Extensions) error {
	if src.Lab.Version == 1 || src.Lab.Version == 2 {
		return errRetainedRevision
	}
	ctx, stop := context.WithTimeout(ctx, instructor.Deadline(steps))
	defer stop()
	engine, ok := s.Engine.(exerciseSSH)
	if !ok {
		return fmt.Errorf("guest execution unavailable")
	}
	owner := lc.Extensions(nil)
	if len(extensions) > 0 {
		owner = extensions[0]
	}
	for _, step := range steps {
		stepCtx, cancel := context.WithTimeout(ctx, step.Timeout()+15*time.Second)
		err := engine.WithSSHMaterial(stepCtx, team.InstanceID, step.Machine, func(inst lc.Instance, m lc.PlannedMachine, r lc.Resource, key []byte) error {
			if inst.Plan.Source.Digest != src.Digest {
				return fmt.Errorf("guest source does not match pinned template")
			}
			client, err := remote.Dial(stepCtx, r.Address, "lab", key, m.HostPublicKey)
			if err != nil {
				return fmt.Errorf("%s SSH unavailable", step.Machine)
			}
			defer client.Close()
			files, err := b.GuestFiles(src, inst, step, owner)
			if err != nil {
				return err
			}
			payload, _ := json.Marshal(struct {
				Files          []lc.File `json:"files"`
				Playbook       string    `json:"playbook"`
				TimeoutSeconds int       `json:"timeoutSeconds"`
			}{files, step.Playbook, int(step.Timeout() / time.Second)})
			_, err = remote.Run(stepCtx, client, fmt.Sprintf("sudo -n timeout --kill-after=5s %ds flock -w 5 /run/lock/labcentral-exercise.lock python3 -c ", int(step.Timeout()/time.Second)+10)+quote(guestPlaybookLauncher)+" "+quote(base64.StdEncoding.EncodeToString(payload)))
			if err != nil {
				var exit *ssh.ExitError
				if errors.As(err, &exit) && exit.ExitStatus() == 42 {
					return fmt.Errorf("%s: %w", step.Machine, errPlaybookFailed)
				}
				return fmt.Errorf("%s: %w", step.Machine, errGuestUnknown)
			}
			return nil
		})
		if stepCtx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			err = fmt.Errorf("%s: %w", step.Machine, errGuestUnknown)
		}
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}
