package build

import (
	"context"
	"encoding/json"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"os"
	"os/exec"
	"path/filepath"
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
	for _, platform := range []string{"aws", "proxmox"} {
		t.Run(platform, func(t *testing.T) {
			c, dir := fixture(t, platform)
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
