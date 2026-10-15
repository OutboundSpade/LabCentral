package build

import (
	"encoding/json"
	"github.com/OutboundSpade/LabCentral/v2/internal/pscheck"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
)

func windowsFixture(t *testing.T, desktop bool) (lc.Command, string) {
	t.Helper()
	c, dir := fixture(t, "aws")
	c.Environment.OperatorCIDRs = []string{"192.0.2.1/32"}
	c.Environment.AWS.Types = []lc.InstanceType{{Name: "t3.medium", CPU: 2, MemoryMiB: 4096}}
	c.Environment.Build.WindowsDCVURL = "https://example.invalid/windows.msi"
	c.Environment.Build.WindowsDCVSHA256 = strings.Repeat("b", 64)
	c.Image.Playbook = ""
	c.Image.Script = "images/build.ps1"
	c.Image.Desktop = desktop
	c.Image.DiskGiB = 30
	c.Image.Base = lc.Base{OS: "windows", ID: "ami-0123456789abcdef0", User: "Administrator", Identity: "ami-0123456789abcdef0", DiskGiB: 30}
	c.Source.Lab.Images = map[string]lc.Image{"base": {OS: "windows", Build: &lc.ImageBuild{Script: "images/build.ps1", Files: []string{"images/build.ps1"}}}}
	c.Inputs = []lc.InputValue{{Name: "message", Type: "string", Value: "{{env `AWS_SECRET_ACCESS_KEY`}}; $(touch /tmp/never)"}, {Name: "count", Type: "integer", Value: "2"}, {Name: "enabled", Type: "boolean", Value: "false"}}
	return c, dir
}
func TestWindowsPackerUsesGuestOnlyPowerShell(t *testing.T) {
	for _, desktop := range []bool{false, true} {
		t.Run(map[bool]string{true: "desktop", false: "server"}[desktop], func(t *testing.T) {
			c, dir := windowsFixture(t, desktop)
			cfg, err := Generate(c, dir)
			if err != nil {
				t.Fatal(err)
			}
			var builder map[string]any
			_ = json.Unmarshal(cfg.Builders[0], &builder)
			for key, want := range map[string]any{"communicator": "ssh", "ssh_username": "Administrator", "ssh_file_transfer_method": "sftp", "disable_stop_instance": true} {
				if builder[key] != want {
					t.Fatalf("%s = %v", key, builder[key])
				}
			}
			if name, _ := builder["ssh_keypair_name"].(string); name != "" {
				t.Fatal("Windows build attaches an EC2 Ed25519 key pair")
			}
			ud := builder["user_data"].(string)
			if !strings.HasPrefix(ud, "<powershell>") || strings.Contains(ud, "build.ps1") || strings.Contains(ud, "AWS_SECRET_ACCESS_KEY") {
				t.Fatal("author code entered user data")
			}
			data, _ := json.Marshal(cfg)
			if strings.Contains(string(data), "shell-local") || strings.Contains(string(data), "ansible-local") || strings.Contains(string(data), "AWS_SECRET_ACCESS_KEY") || strings.Contains(string(data), "images/build.ps1") {
				t.Fatal("author values entered host/Packer templates")
			}
			install, restart, author, sysprep := -1, -1, -1, -1
			for i, provisioner := range cfg.Provisioners {
				text := string(provisioner)
				if strings.Contains(text, `"type":"windows-restart"`) {
					restart = i
				}
				if strings.Contains(text, "Get-FileHash") {
					install = i
					if !strings.Contains(text, c.Environment.Build.WindowsDCVSHA256) {
						t.Fatal("missing MSI pin")
					}
					if !strings.Contains(text, "DISABLE_AUTOMATIC_SESSION_CREATION=1") || !strings.Contains(text, "HKEY_USERS") || !strings.Contains(text, "S-1-5-18") {
						t.Fatal("MSI service and SYSTEM-hive settings are not controlled")
					}
				}
				if strings.Contains(text, "LABCENTRAL_INPUTS") {
					author = i
				}
				if strings.Contains(text, "sysprep --shutdown --block") {
					sysprep = i
					if !strings.Contains(text, `dcv\\private\\dcv.pem`) || !strings.Contains(text, `dcv\\private\\dcv.key`) {
						t.Fatal("DCV private certificate not sanitized")
					}
					if !strings.Contains(text, "authorized_keys") || !strings.Contains(text, "Remove-Item -Recurse") {
						t.Fatal("capture not sanitized")
					}
				}
			}
			if author < 0 || sysprep <= author || (desktop && (install < 0 || restart <= install || restart >= author)) || (!desktop && install >= 0) {
				t.Fatal("incorrect guest build order", install, author, sysprep)
			}
			var inputs map[string]any
			raw, _ := os.ReadFile(filepath.Join(dir, "inputs.json"))
			if err = json.Unmarshal(raw, &inputs); err != nil {
				t.Fatal(err)
			}
			if inputs["message"] != c.Inputs[0].Value || inputs["count"] != float64(2) || inputs["enabled"] != false {
				t.Fatal("typed inputs lost", inputs)
			}
			if packer := os.Getenv("LABCENTRAL_TEST_PACKER_SYNTAX"); packer != "" {
				path := filepath.Join(dir, "build.json")
				if err = os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(packer, "validate", "-syntax-only", path)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("Packer syntax: %s: %v", out, err)
				}
			}
		})
	}
}
func TestWindowsBuildCapacityAndInputFailures(t *testing.T) {
	c, dir := windowsFixture(t, false)
	c.Environment.AWS.Types[0].MemoryMiB = 1024
	if _, err := Generate(c, dir); err == nil {
		t.Fatal("undersized Windows build accepted")
	}
	c.Environment.AWS.Types[0].MemoryMiB = 4096
	c.Inputs[1].Value = "not-an-integer"
	if _, err := Generate(c, dir); err == nil {
		t.Fatal("invalid typed input accepted")
	}
}

func TestWindowsBuildPowerShellSyntax(t *testing.T) {
	c, dir := windowsFixture(t, true)
	cfg, err := Generate(c, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range cfg.Provisioners {
		var v struct {
			Type   string
			Inline []string
		}
		if err = json.Unmarshal(p, &v); err != nil {
			t.Fatal(err)
		}
		if v.Type == "powershell" {
			pscheck.Syntax(t, strings.Join(v.Inline, "\n"))
		}
	}
}

func TestWindowsPackerOfflinePluginValidation(t *testing.T) {
	packer := os.Getenv("LABCENTRAL_TEST_PACKER_OFFLINE")
	if packer == "" {
		t.Skip("set LABCENTRAL_TEST_PACKER_OFFLINE for local plugin validation")
	}
	c, dir := windowsFixture(t, true)
	cfg, err := Generate(c, dir)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(cfg)
	path := filepath.Join(dir, "build.json")
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(packer, "validate", path)
	// All HTTP routes point at a closed loopback port. This check must never
	// reach an AWS API, IMDS or any operator credential configuration.
	cmd.Env = append(os.Environ(), "AWS_EC2_METADATA_DISABLED=true", "AWS_CONFIG_FILE=/dev/null", "AWS_SHARED_CREDENTIALS_FILE=/dev/null", "AWS_ACCESS_KEY_ID=testing", "AWS_SECRET_ACCESS_KEY=testing", "HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1", "ALL_PROXY=http://127.0.0.1:1", "NO_PROXY=")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("offline Packer plugin validation: %s: %v", output, err)
	}
}

func TestWindowsShutdownStatusIsRestrictedToFinalSysprep(t *testing.T) {
	c, dir := windowsFixture(t, true)
	cfg, err := Generate(c, dir)
	if err != nil {
		t.Fatal(err)
	}
	var builder map[string]any
	if err = json.Unmarshal(cfg.Builders[0], &builder); err != nil {
		t.Fatal(err)
	}
	if builder["disable_stop_instance"] != true || builder["skip_create_ami"] == true {
		t.Fatal("shutdown status bypasses native stopped capture")
	}
	for i, p := range cfg.Provisioners {
		var provisioner map[string]any
		if err = json.Unmarshal(p, &provisioner); err != nil {
			t.Fatal(err)
		}
		codes, exists := provisioner["valid_exit_codes"]
		if i != len(cfg.Provisioners)-1 {
			if exists {
				t.Fatal("shutdown status accepted during installation or author execution")
			}
			continue
		}
		values, ok := codes.([]any)
		if !ok || len(values) != 2 || values[0] != float64(0) || values[1] != float64(2300218) || !strings.Contains(string(p), "sysprep --shutdown --block") {
			t.Fatal("final generalization does not handle the observed Windows shutdown status")
		}
	}
}
