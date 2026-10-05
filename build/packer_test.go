package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixture(t *testing.T, platform string) (lc.Command, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "source", "images"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "source", "images", "base.yml"), []byte("- hosts: localhost\n  tasks: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := key(filepath.Join(dir, "build.key")); err != nil {
		t.Fatal(err)
	}
	return lc.Command{Owner: "build-test", Image: lc.PlannedImage{Name: "base", Playbook: "images/base.yml", DiskGiB: 20, Base: lc.Base{ID: "2000", User: "ubuntu"}}, Machine: lc.PlannedMachine{VMID: 2001}, Environment: lc.Environment{Platform: platform, AWS: lc.AWSConfig{Region: "us-east-1"}, Proxmox: lc.ProxmoxConfig{URL: "https://pve.example.invalid:8006", Node: "pve", Storage: "local-lvm", Pool: "test", TokenID: "test@pve!test"}, Build: lc.BuildConfig{AnsibleVersion: "2.19.3", Bridge: "vmbr-build", InstanceType: "t3.medium", SubnetID: "subnet-01234567890123456", SecurityGroupID: "sg-01234567890123456"}}}, dir
}
func TestGeneratedPackerConfiguration(t *testing.T) {
	for _, scenario := range []struct {
		platform string
		desktop  bool
	}{{"aws", false}, {"aws", true}, {"proxmox", false}, {"proxmox", true}} {
		name := scenario.platform
		if scenario.desktop {
			name += "-desktop"
		}
		t.Run(name, func(t *testing.T) {
			platform := scenario.platform
			c, dir := fixture(t, platform)
			c.Image.Desktop = scenario.desktop
			c.Environment.Build.DCVURL = "https://example.invalid/dcv.tgz"
			c.Environment.Build.DCVSHA256 = strings.Repeat("0", 64)
			if platform == "aws" {
				c.Image.Base.ID = "ami-01234567890123456"
			}
			c.Inputs = []lc.InputValue{{Name: "message", Type: "string", Value: "{{env `AWS_SECRET_ACCESS_KEY`}}; $(touch /tmp/never)"}}
			cfg, err := Generate(c, dir)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(cfg)
			if strings.Contains(string(b), "AWS_SECRET_ACCESS_KEY") {
				t.Fatal("authored input entered Packer template")
			}
			if strings.Contains(string(b), "shell-local") {
				t.Fatal("local code execution")
			}
			if !strings.Contains(string(b), "ansible-local") {
				t.Fatal("missing guest controller")
			}
			packer := os.Getenv("LABCENTRAL_TEST_PACKER")
			if packer != "" {
				path := filepath.Join(dir, "build.json")
				if err = os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(packer, "validate", "-syntax-only", path)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("Packer syntax: %s: %v", out, err)
				}
				if true {
					cmd = exec.Command(packer, "validate", path)
					cmd.Env = append(os.Environ(), "PROXMOX_TOKEN=test", "AWS_ACCESS_KEY_ID=testing", "AWS_SECRET_ACCESS_KEY=testing", "AWS_EC2_METADATA_DISABLED=true")
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("Packer plugin validation: %s: %v", out, err)
					}
				}
			}
		})
	}
}

type cleanupProvider struct {
	calls int
	fail  bool
}

func (p *cleanupProvider) Execute(_ context.Context, c lc.Command, _ lc.Observer) (lc.Result, error) {
	p.calls++
	return lc.Result{Absent: !p.fail}, nil
}
func TestValidationCleanupCheckpoint(t *testing.T) {
	p := &cleanupProvider{fail: true}
	j := journal.Journal{Root: t.TempDir()}
	c := lc.Command{Owner: "build-test"}
	im := lc.Resource{ID: "ami-test"}
	r := validationRecord{ImageID: im.ID, ChecksDone: true, Machine: lc.Resource{ID: "vm", Kind: "machine"}, Network: lc.Resource{ID: "net", Kind: "network"}}
	if _, err := finishValidation(context.Background(), p, c, im, j, &r); err == nil {
		t.Fatal("unconfirmed cleanup accepted")
	}
	p.fail = false
	if _, err := finishValidation(context.Background(), p, c, im, j, &r); err != nil {
		t.Fatal(err)
	}
	before := p.calls
	var restored validationRecord
	if err := j.Load("receipt", &restored); err != nil {
		t.Fatal(err)
	}
	if _, err := finishValidation(context.Background(), p, c, im, j, &restored); err != nil {
		t.Fatal(err)
	}
	if p.calls != before {
		t.Fatal("completed validation repeated external mutations")
	}
}

type validationRetryProvider struct {
	commands []lc.Command
}

func (p *validationRetryProvider) Execute(_ context.Context, c lc.Command, _ lc.Observer) (lc.Result, error) {
	p.commands = append(p.commands, c)
	return lc.Result{}, errors.New("native response unavailable")
}

func TestFailedValidationRetryRequiresConfirmedCleanup(t *testing.T) {
	for _, cleaned := range []bool{false, true} {
		t.Run(fmt.Sprint(cleaned), func(t *testing.T) {
			root := t.TempDir()
			c := lc.Command{Owner: "build-test"}
			image := lc.Resource{ID: "ami-test"}
			j := journal.Journal{Root: filepath.Join(root, c.Owner, "validation")}
			old := validationRecord{ImageID: image.ID, ChecksDone: true, CheckError: "DCV status unavailable", Cleaned: cleaned, Machine: lc.Resource{ID: "old-vm", Kind: "machine"}, Network: lc.Resource{ID: "old-net", Kind: "network"}}
			if err := j.Save("receipt", old); err != nil {
				t.Fatal(err)
			}
			p := &validationRetryProvider{}
			if _, err := ValidateNative(context.Background(), root, p, c, image); err == nil {
				t.Fatal("unknown native outcome accepted")
			}
			if len(p.commands) != 1 {
				t.Fatal(p.commands)
			}
			command := p.commands[0]
			if !cleaned {
				if command.Action != "resource.destroy" || command.Owner != "build-test-validation" {
					t.Fatal("new guest provisioned before cleanup confirmation", command)
				}
				return
			}
			if command.Action != "network.create" || command.Owner != "build-test-validation-1" {
				t.Fatal("retry reused previous guest identity", command)
			}
			var archived, current validationRecord
			if err := j.Load("receipt-attempt-0", &archived); err != nil || !reflect.DeepEqual(archived, old) {
				t.Fatal("previous receipt lost", err, archived)
			}
			if err := j.Load("receipt", &current); err != nil || current.Attempt != 1 || current.Network.ID != "" || current.Machine.ID != "" || current.ChecksDone {
				t.Fatal("retry checkpoint not durable", err, current)
			}
			if validationMachineKey(current) == validationMachineKey(archived) {
				t.Fatal("retry would discover previous machine command")
			}
		})
	}
}
