package labcentral

import (
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `version: 3
name: example
images:
  base:
    base: ubuntu-24.04
machines:
  one:
    image: base
    cpu: 2
    memory: 2Gi
    disk: 20Gi
    access: [ssh]
`

func testSource(t *testing.T) Source {
	t.Helper()
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "lab.yaml"), []byte(sample), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := Load(dir)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func testEnvironment() Environment {
	return Environment{Version: 2, Name: "test", Platform: "aws", OperatorCIDRs: []string{"192.0.2.1/32"}, Bases: map[string]Base{"ubuntu-24.04": {ID: "ami-example", Identity: "ami-example", User: "ubuntu", DiskGiB: 8}}, AWS: AWSConfig{Account: "123456789012", Region: "us-east-1", Zone: "us-east-1a", Types: []InstanceType{{"t3.medium", 2, 4096, 0}}}}
}
func TestStrictAuthoring(t *testing.T) {
	for name, src := range map[string]string{"unknown": sample + "unexpected: value\n", "duplicate": sample + "name: other\n", "alias": strings.Replace(sample, "access: [ssh]", "access: &shared [ssh]", 1), "multiple": sample + "---\nname: second\n", "wrong-version": strings.Replace(sample, "version: 3", "version: 1", 1), "unknown-image": strings.Replace(sample, "image: base", "image: missing", 1)} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			os.WriteFile(filepath.Join(dir, "lab.yaml"), []byte(src), 0600)
			if _, e := Load(dir); e == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
}
func TestSourceCannotEscapeOrChangeAfterHash(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "lab.yaml"), []byte(sample), 0600)
	if e := os.Symlink("/etc/passwd", filepath.Join(dir, "escape")); e != nil {
		t.Fatal(e)
	}
	if _, e := Load(dir); e == nil {
		t.Fatal("symlink accepted")
	}
	s := testSource(t)
	s.Lab.Machines["one"] = Machine{Image: "base", CPU: 32, Memory: "64Gi", Disk: "20Gi"}
	if e := Validate(s); e == nil {
		t.Fatal("declaration was allowed to differ from content")
	}
}
func TestPlanningIsPortableAndDeterministic(t *testing.T) {
	s := testSource(t)
	env := testEnvironment()
	p, e := PlanLab(s, env, nil)
	if e != nil {
		t.Fatal(e)
	}
	if p.Machines[0].CPU != 2 || p.Machines[0].MemoryMiB != 4096 {
		t.Fatal("allocation capacity was not resolved")
	}
	q, e := PlanLab(s, env, nil)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := json.Marshal(p)
	b, _ := json.Marshal(q)
	if string(a) != string(b) {
		t.Fatal("plan is unstable")
	}
	env.AWS.Types = nil
	if _, e = PlanLab(s, env, nil); e == nil {
		t.Fatal("unsupported target accepted")
	}
	if _, e = PlanLab(s, testEnvironment(), map[string]string{"undeclared": "x"}); e == nil {
		t.Fatal("undeclared input accepted")
	}
}
func TestDisjointProxmoxSegments(t *testing.T) {
	p := ProxmoxConfig{CIDR: "10.80.0.0/16", VLANStart: 100, VLANEnd: 199}
	a, ag, e := ProxmoxSubnet(p, 100)
	if e != nil {
		t.Fatal(e)
	}
	b, bg, e := ProxmoxSubnet(p, 101)
	if e != nil {
		t.Fatal(e)
	}
	if a != "10.80.0.0/24" || ag != "10.80.0.1" || b != "10.80.1.0/24" || bg != "10.80.1.1" {
		t.Fatalf("unexpected mapping %s %s %s %s", a, ag, b, bg)
	}
}

func TestSharedExamplesValidate(t *testing.T) {
	for _, name := range []string{"single", "web-database", "workstation"} {
		source, err := Load("examples/" + name)
		if err != nil {
			t.Fatal(name, err)
		}
		env := testEnvironment()
		env.AWS.DesktopProfile = "license-only"
		env.Build = BuildConfig{Packer: "/packer", PackerSHA256: strings.Repeat("a", 64), PluginDirectory: "/plugins", ToolchainID: "toolchain", AnsibleVersion: "2.19.3", TimeoutSeconds: 3600}
		if _, err = PlanLab(source, env, nil); err != nil {
			t.Fatal(name, err)
		}
		env.Platform = "proxmox"
		env.Bases["ubuntu-24.04"] = Base{ID: "2000", Identity: "pinned-config", User: "ubuntu", DiskGiB: 20}
		env.Proxmox = ProxmoxConfig{URL: "https://pve.example.invalid:8006", Node: "pve", Storage: "local", Pool: "test", LabBridge: "vmbr-labs", ManagementBridge: "vmbr0", TokenID: "test@pve!test", TokenEnv: "TEST_TOKEN", VMIDStart: 2100, VMIDEnd: 2199, VLANStart: 100, VLANEnd: 115, CIDR: "10.90.0.0/20"}
		if _, err = PlanLab(source, env, nil); err != nil {
			t.Fatal(name, "proxmox", err)
		}
	}
}

func TestDesktopPolicyInvalidatesOnlyDesktopBuilds(t *testing.T) {
	for _, name := range []string{"single", "workstation"} {
		source, err := Load("examples/" + name)
		if err != nil {
			t.Fatal(err)
		}
		env := testEnvironment()
		env.AWS.DesktopProfile = "license-only"
		env.Build = BuildConfig{Packer: "/packer", PackerSHA256: strings.Repeat("a", 64), PluginDirectory: "/plugins", ToolchainID: "toolchain", AnsibleVersion: "2.19.3", TimeoutSeconds: 3600}
		plan, err := PlanLab(source, env, nil)
		if err != nil {
			t.Fatal(err)
		}
		image := plan.Images[0]
		key := image.CacheKey
		image.CacheKey = ""
		previous := digest(struct {
			S string
			I PlannedImage
			E imageEnvironment
			V []InputValue
		}{imageSourceDigest(source, image.Name, env.Platform), image, buildEnvironment(env), plan.Inputs})
		if (key != previous) != image.Desktop {
			t.Fatal("desktop policy must invalidate desktop images while preserving unchanged server builds", name)
		}
	}
}

func TestHostnamePeerSelectionValidation(t *testing.T) {
	for _, peers := range []string{"[missing]", "[one, one]"} {
		dir := t.TempDir()
		declaration := strings.Replace(sample, "access: [ssh]", "access: [ssh]\n    hostnamePeers: "+peers, 1)
		if err := os.WriteFile(filepath.Join(dir, "lab.yaml"), []byte(declaration), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); err == nil {
			t.Fatal("invalid peer inventory accepted", peers)
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lab.yaml"), []byte(strings.Replace(sample, "access: [ssh]", "access: [ssh]\n    hostnamePeers: [one]", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	src, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(src.Lab.Machines["one"].HostnamePeers.Names) != 1 {
		t.Fatal("hostname inventory not retained")
	}
}

func TestImageBuildFileSelectionValidation(t *testing.T) {
	for name, files := range map[string]string{"unknown": "[build.yml, missing.txt]", "duplicate": "[build.yml, build.yml]", "missing playbook": "[data.txt]"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			declaration := strings.Replace(sample, "base: ubuntu-24.04", "base: ubuntu-24.04\n    build:\n      playbook: build.yml\n      files: "+files, 1)
			for path, data := range map[string]string{"lab.yaml": declaration, "build.yml": "- hosts: localhost\n  tasks: []\n", "data.txt": "training data"} {
				if err := os.WriteFile(filepath.Join(dir, path), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Load(dir); err == nil {
				t.Fatal("invalid build selection accepted")
			}
		})
	}
}

func TestProxmoxGatewayReservedAtOne(t *testing.T) {
	p := ProxmoxConfig{CIDR: "10.80.0.0/16", VLANStart: 100, VLANEnd: 199}
	for v := 100; v <= 199; v++ {
		cidr, gw, err := ProxmoxSubnet(p, v)
		if err != nil || gw != offsetAddress(netip.MustParsePrefix(cidr), 1) {
			t.Fatal(cidr, gw, err)
		}
	}
}

func TestProxmoxReservesNativeBridgeVLAN(t *testing.T) {
	e := platformOptionsProxmoxEnvironment()
	e.Proxmox.VLANStart = 1
	err := ValidateEnvironment(e)
	if err == nil || !strings.Contains(err.Error(), "VLAN 1 is reserved") {
		t.Fatal(err)
	}
}
