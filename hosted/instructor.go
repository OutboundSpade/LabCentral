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
	"sync"
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
	cache, owned := guestCacheFor(ctx)
	if owned {
		defer cache.close()
	}
	for _, step := range steps {
		stepCtx, cancel := context.WithTimeout(ctx, step.Timeout()+15*time.Second)
		release, admit := s.acquireSSH(stepCtx)
		if admit != nil {
			cancel()
			return admit
		}
		err := engine.WithSSHMaterial(stepCtx, team.InstanceID, step.Machine, func(inst lc.Instance, m lc.PlannedMachine, r lc.Resource, key []byte) error {
			if inst.Plan.Source.Digest != src.Digest {
				return fmt.Errorf("guest source does not match pinned template")
			}
			client, err := cache.clientFor(stepCtx, inst, step.Machine, r.Address, key, m.HostPublicKey)
			if err != nil {
				return fmt.Errorf("%s SSH unavailable", step.Machine)
			}
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
		release()
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

type guestCacheKey struct{}
type guestConnection struct {
	client           *ssh.Client
	address, hostKey string
}
type guestCache struct {
	dial        func(context.Context, string, string, []byte, string) (*ssh.Client, error)
	mu          sync.Mutex
	connections map[string]guestConnection
	dialers     map[string]*remote.Dialer
	gateways    map[string]string
}

func guestCacheFor(ctx context.Context) (*guestCache, bool) {
	if cache, ok := ctx.Value(guestCacheKey{}).(*guestCache); ok {
		return cache, false
	}
	return &guestCache{connections: map[string]guestConnection{}, dial: remote.Dial}, true
}
func (c *guestCache) client(ctx context.Context, instance, machine, address string, key []byte, hostKey string) (*ssh.Client, error) {
	name := instance + ":" + machine
	c.mu.Lock()
	previous, ok := c.connections[name]
	c.mu.Unlock()
	if ok {
		if previous.address != address || previous.hostKey != hostKey {
			return nil, fmt.Errorf("SSH identity changed within job")
		}
		return previous.client, nil
	}
	client, err := c.dial(ctx, address, "lab", key, hostKey)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.connections[name] = guestConnection{client, address, hostKey}
	c.mu.Unlock()
	return client, nil
}
func (c *guestCache) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer func() {
		for _, d := range c.dialers {
			d.Close()
		}
	}()
	for _, conn := range c.connections {
		conn.client.Close()
	}
}
func guestJobContext(ctx context.Context) (context.Context, func()) {
	if _, ok := ctx.Value(guestCacheKey{}).(*guestCache); ok {
		return ctx, func() {}
	}
	cache, _ := guestCacheFor(ctx)
	return context.WithValue(ctx, guestCacheKey{}, cache), cache.close
}

// A job reuses both its gateway transport and each machine handshake. Identity
// changes within the job are rejected before opening another channel.
func (c *guestCache) clientFor(ctx context.Context, inst lc.Instance, machine, address string, key []byte, hostKey string) (*ssh.Client, error) {
	gateway := lc.InstanceGateway(inst)
	encoded, _ := json.Marshal(gateway)
	c.mu.Lock()
	if c.gateways == nil {
		c.gateways = map[string]string{}
	}
	previous, known := c.gateways[inst.ID]
	if known && previous != string(encoded) {
		c.mu.Unlock()
		return nil, fmt.Errorf("gateway identity changed within job")
	}
	c.gateways[inst.ID] = string(encoded)
	c.mu.Unlock()
	if gateway.Kind == "" {
		return c.client(ctx, inst.ID, machine, address, key, hostKey)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dialers == nil {
		c.dialers = map[string]*remote.Dialer{}
	}
	d := c.dialers[inst.ID]
	if d == nil {
		var err error
		d, err = lc.MachineDialer(gateway, key)
		if err != nil {
			return nil, err
		}
		c.dialers[inst.ID], c.gateways[inst.ID] = d, string(encoded)
	} else if c.gateways[inst.ID] != string(encoded) {
		return nil, fmt.Errorf("gateway identity changed within job")
	}
	name := inst.ID + ":" + machine
	if previous, ok := c.connections[name]; ok {
		if previous.address != address || previous.hostKey != hostKey {
			return nil, fmt.Errorf("SSH identity changed within job")
		}
		return previous.client, nil
	}
	client, err := d.Dial(ctx, address, "lab", key, hostKey)
	if err != nil {
		return nil, err
	}
	c.connections[name] = guestConnection{client, address, hostKey}
	return client, nil
}
