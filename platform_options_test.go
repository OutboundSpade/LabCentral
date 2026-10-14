package labcentral

import (
	"reflect"
	"strings"
	"testing"
)

func platformOptionsProxmoxEnvironment() Environment {
	e := conformanceEnvironment()
	e.Platform = "proxmox"
	e.Bases["ubuntu-24.04"] = Base{ID: "2000", Identity: "pinned", User: "ubuntu", DiskGiB: 8}
	e.Proxmox = ProxmoxConfig{URL: "https://pve.example.invalid:8006", Node: "pve", Storage: "local", Pool: "test", Bridge: "vmbr-labs", TokenID: "test@pve!test", TokenEnv: "TEST_TOKEN", VMIDStart: 2100, VMIDEnd: 2199, VLANStart: 100, VLANEnd: 115, CIDR: "10.90.0.0/20", VerifiedIsolation: true, CPUTypes: []string{"host", "kvm64"}}
	return e
}
func optionSource(t *testing.T, machine, image string, selectedFiles bool) Source {
	t.Helper()
	raw := sample
	if image != "" {
		raw = strings.Replace(raw, "    base: ubuntu-24.04", "    base: ubuntu-24.04\n    platform: "+image, 1)
	}
	if machine != "" {
		raw += "    platform: " + machine + "\n"
	}
	if selectedFiles {
		raw = strings.Replace(raw, "    base: ubuntu-24.04", "    base: ubuntu-24.04\n    build: {playbook: build.yml, files: [build.yml]}", 1)
	}
	s, err := sourceYAML(t, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestPlatformOptionPlanning(t *testing.T) {
	e := conformanceEnvironment()
	s := optionSource(t, "{aws: {instanceType: t3.large, cpuCredits: standard, volume: {type: gp3, iops: 10000, throughput: 1000}}, proxmox: {cpuType: host}}", "{aws: {buildInstanceType: t3.large}}", false)
	p, err := PlanLab(s, e, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Machines[0].InstanceType != "t3.large" || p.Machines[0].CPU != 2 || p.Machines[0].MemoryMiB != 8192 || p.Images[0].BuildInstanceType != "t3.large" {
		t.Fatal("explicit selection lost", p)
	}
	e = platformOptionsProxmoxEnvironment()
	if _, err = PlanLab(s, e, nil); err != nil {
		t.Fatal("foreign AWS block affected Proxmox", err)
	}
	e.Proxmox.CPUTypes = nil
	_, err = PlanLab(s, e, nil)
	requireField(t, err, Unsupported, "machines.one.platform.proxmox.cpuType")
	if _, err = PlanLab(optionSource(t, "{aws: {instanceType: not-allowed, cpuCredits: unlimited, volume: {iops: 16000}}}", "{aws: {buildInstanceType: not-allowed}}", false), e, nil); err != nil {
		t.Fatal("AWS allowlist/ratios affected Proxmox", err)
	}
	if _, err = PlanLab(optionSource(t, "{proxmox: {cpuType: not-allowed, nicModel: rtl8139, memoryBalloon: false}}", "", false), conformanceEnvironment(), nil); err != nil {
		t.Fatal("Proxmox allowlist affected AWS", err)
	}
}
func TestAWSOptionPlanningRejections(t *testing.T) {
	for _, tc := range []struct {
		machine, image, field string
		code                  Code
	}{
		{"{aws: {instanceType: forbidden}}", "", "machines.one.platform.aws.instanceType", Unsupported},
		{"{aws: {instanceType: tiny}}", "", "machines.one.platform.aws.instanceType", Invalid},
		{"{aws: {instanceType: m5.large, cpuCredits: unlimited}}", "", "machines.one.platform.aws.cpuCredits", Invalid},
		{"{aws: {volume: {iops: 10001}}}", "", "machines.one.platform.aws.volume.iops", Invalid},
		{"{aws: {volume: {throughput: 751}}}", "", "machines.one.platform.aws.volume.throughput", Invalid},
		{"", "{aws: {buildInstanceType: forbidden}}", "images.base.platform.aws.buildInstanceType", Unsupported},
	} {
		e := conformanceEnvironment()
		e.AWS.Types = append(e.AWS.Types, InstanceType{"tiny", 1, 1024}, InstanceType{"m5.large", 2, 8192})
		_, err := PlanLab(optionSource(t, tc.machine, tc.image, false), e, nil)
		requireField(t, err, tc.code, tc.field)
	}
	// The automatically selected type must also support an explicit credit policy.
	e := conformanceEnvironment()
	e.AWS.Types = []InstanceType{{"m5.large", 2, 8192}}
	_, err := PlanLab(optionSource(t, "{aws: {cpuCredits: standard}}", "", false), e, nil)
	requireField(t, err, Invalid, "machines.one.platform.aws.cpuCredits")
	for _, family := range []string{"t2", "t3", "t3a", "t4g"} {
		e.AWS.Types = []InstanceType{{family + ".large", 2, 8192}}
		if _, err := PlanLab(optionSource(t, "{aws: {cpuCredits: unlimited}}", "", false), e, nil); err != nil {
			t.Fatal(family, err)
		}
	}
	if AWSBurstable("t3") || AWSBurstable("t3foo.large") || AWSBurstable("m5.large") {
		t.Fatal("incorrect burstable family classification")
	}
}
func TestPlatformOptionImageCacheScope(t *testing.T) {
	for _, selected := range []bool{false, true} {
		for _, platform := range []string{"aws", "proxmox"} {
			e := conformanceEnvironment()
			if platform == "proxmox" {
				e = platformOptionsProxmoxEnvironment()
			}
			key := func(machine, image string) string {
				t.Helper()
				p, err := PlanLab(optionSource(t, machine, image, selected), e, nil)
				if err != nil {
					t.Fatal(err)
				}
				return p.Images[0].CacheKey
			}
			before := key("", "")
			if key("{aws: {instanceType: t3.large, cpuCredits: unlimited, volume: {iops: 4000, throughput: 1000}}, proxmox: {cpuType: host, nicModel: e1000e, memoryBalloon: false}}", "") != before {
				t.Fatal(platform, selected, "runtime options changed image key")
			}
			if platform == "aws" && key("", "{aws: {buildInstanceType: t3.large}}") == before {
				t.Fatal(selected, "image options did not affect image key")
			}
			if platform == "proxmox" && key("", "{aws: {buildInstanceType: forbidden}}") != before {
				t.Fatal(selected, "foreign image options affected image key")
			}
		}
	}
}
func TestProxmoxCPUAllowlistValidation(t *testing.T) {
	e := platformOptionsProxmoxEnvironment()
	for _, tc := range []struct {
		values []string
		field  string
	}{
		{[]string{""}, "environment.proxmox.cpuTypes[0]"},
		{[]string{" "}, "environment.proxmox.cpuTypes[0]"},
		{[]string{"host", "host"}, "environment.proxmox.cpuTypes[1]"},
		{make([]string, 33), "environment.proxmox.cpuTypes"},
	} {
		e.Proxmox.CPUTypes = tc.values
		requireField(t, ValidateEnvironment(e), Invalid, tc.field)
	}
	e.Proxmox.CPUTypes = make([]string, 32)
	for i := range e.Proxmox.CPUTypes {
		e.Proxmox.CPUTypes[i] = strings.Repeat("a", i+1)
	}
	if err := ValidateEnvironment(e); err != nil {
		t.Fatal("32 models rejected", err)
	}
	e.Proxmox.CPUTypes = nil
	if err := ValidateEnvironment(e); err != nil {
		t.Fatal("optional allowlist rejected", err)
	}
}
func TestOptionCapabilities(t *testing.T) {
	for _, platform := range []string{"aws", "proxmox"} {
		c, ok := PlatformCapabilities(platform)
		if !ok || len(c.MachineOptions) == 0 {
			t.Fatal(platform, c)
		}
	}
	value := false
	options := &ProxmoxMachineOptions{MemoryBalloon: &value}
	requireField(t, checkImplementedOptions(options, nil, "machines.one.platform.proxmox"), Unsupported, "machines.one.platform.proxmox.memoryBalloon")
	if err := checkImplementedOptions(options, []string{"memoryBalloon"}, "machines.one.platform.proxmox"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(options.MemoryBalloon, &value) {
		t.Fatal("presence lost")
	}
}
