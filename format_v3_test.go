package labcentral

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func requireField(t *testing.T, err error, code Code, field string) {
	t.Helper()
	var p *Error
	if !errors.As(err, &p) || p.Code != code || p.Field != field {
		t.Fatalf("expected %s %s; got %v", code, field, err)
	}
}
func TestTypedInputsAndSuppliedValidation(t *testing.T) {
	s, e := sourceYAML(t, []byte(sample+`inputs:
  message: {type: string, default: ""}
  count: {type: integer, default: 12}
  enabled: {type: boolean, default: false}
  optional: {type: integer}
  flag: {type: boolean}
  requested: {type: string, required: true}
`))
	if e != nil {
		t.Fatal(e)
	}
	_, e = PlanLab(s, testEnvironment(), nil)
	requireField(t, e, Invalid, "inputs.requested")
	p, e := PlanLab(s, testEnvironment(), map[string]string{"requested": ""})
	if e != nil {
		t.Fatal(e)
	}
	want := []InputValue{{"count", "integer", "12"}, {"enabled", "boolean", "false"}, {"message", "string", ""}, {"requested", "string", ""}}
	if digest(p.Inputs) != digest(want) {
		t.Fatalf("unexpected effective inputs: %v", p.Inputs)
	}
	for _, tc := range []struct{ name, value string }{{"count", "1.2"}, {"count", "9223372036854775808"}, {"flag", "1"}, {"flag", "True"}, {"message", strings.Repeat("a", 4097)}, {"unknown", "text"}} {
		_, e = PlanLab(s, testEnvironment(), map[string]string{"requested": "ok", tc.name: tc.value})
		requireField(t, e, Invalid, "inputs."+tc.name)
	}
	b, e := json.Marshal(s)
	if e != nil {
		t.Fatal(e)
	}
	var stored Source
	if e = json.Unmarshal(b, &stored); e != nil {
		t.Fatal(e)
	}
	if e = Validate(stored); e != nil {
		t.Fatal(e)
	}
}
func TestSelectedImageCacheScope(t *testing.T) {
	raw := strings.Replace(sample, "    base: ubuntu-24.04", "    base: ubuntu-24.04\n    build: {playbook: build.yml, files: [build.yml]}", 1)
	s, e := sourceYAML(t, []byte(raw))
	if e != nil {
		t.Fatal(e)
	}
	env := conformanceEnvironment()
	key := func(s Source) string {
		t.Helper()
		p, e := PlanLab(s, env, nil)
		if e != nil {
			t.Fatal(e)
		}
		return p.Images[0].CacheKey
	}
	original := key(s)
	s.Files = append(s.Files, File{Path: "README.md", Content: []byte("unrelated")})
	s.Digest = digest(s.Files)
	if key(s) != original {
		t.Fatal("unrelated source changed selected-file cache key")
	}
	s.Files[0].Content = []byte("changed build")
	s.Digest = digest(s.Files)
	if key(s) == original {
		t.Fatal("selected bytes did not affect key")
	}
	for _, replacement := range []string{"    os: linux\n    build:", "    extensions: {org.example.image: 1}\n    build:"} {
		next, e := sourceYAML(t, []byte(strings.Replace(raw, "    build:", replacement, 1)))
		if e != nil {
			t.Fatal(e)
		}
		if key(next) == original {
			t.Fatal("image definition did not affect key")
		}
	}
	s, e = sourceYAML(t, []byte(strings.Replace(raw, ", files: [build.yml]", "", 1)))
	if e != nil {
		t.Fatal(e)
	}
	original = key(s)
	s.Files = append(s.Files, File{Path: "README.md", Content: []byte("changed")})
	s.Digest = digest(s.Files)
	if key(s) == original {
		t.Fatal("whole-source build ignored unrelated change")
	}
}
func TestExtensionCanonicalJSONAndAggregateBound(t *testing.T) {
	one, e := sourceYAML(t, []byte(sample+"extensions: {org.example.value: {b: [1, null], a: true}}\n"))
	if e != nil {
		t.Fatal(e)
	}
	two, e := sourceYAML(t, []byte(sample+"extensions: {org.example.value: {a: true, b: [1, null]}}\n"))
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(one.Lab.Extensions["org.example.value"], two.Lab.Extensions["org.example.value"]) {
		t.Fatal("extension values are not canonical")
	}
	overhead := len(`{"org.example.value":""}`)
	exact := sample + "extensions: {org.example.value: " + strings.Repeat("x", (64<<10)-overhead) + "}\n"
	if _, e = sourceYAML(t, []byte(exact)); e != nil {
		t.Fatal("64 KiB edge rejected", e)
	}
	oversized := strings.Replace(exact, "base: ubuntu-24.04", "base: ubuntu-24.04\n    extensions: {org.example.image: true}", 1)
	emptyObject := strings.Replace(exact, "base: ubuntu-24.04", "base: ubuntu-24.04\n    extensions: {}", 1)
	_, e = sourceYAML(t, []byte(emptyObject))
	requireField(t, e, Invalid, "extensions")
	_, e = sourceYAML(t, []byte(oversized))
	requireField(t, e, Invalid, "extensions")
}
func TestAddressOffsetsAndEmptyPeersPersist(t *testing.T) {
	for _, platform := range []string{"aws", "proxmox"} {
		t.Run(platform, func(t *testing.T) {
			engine, e := Open(t.TempDir(), freshFake(), nil)
			if e != nil {
				t.Fatal(e)
			}
			defer engine.Close()
			explicit := 11
			low := 4
			upper := 250
			env := testEnvironment()
			env.Platform = platform
			env.Proxmox = ProxmoxConfig{CIDR: "10.90.0.0/16", VLANStart: 100, VLANEnd: 199, VMIDStart: 1000, VMIDEnd: 1100}
			instance := Instance{ID: "offsets", VLAN: 101, Plan: Plan{Environment: env, Machines: []PlannedMachine{
				{Name: "a", Spec: Machine{HostnamePeers: HostnamePeers{Explicit: true}}},
				{Name: "b", Spec: Machine{NICs: []NIC{{Network: "lab", Address: &explicit}}}},
				{Name: "c", Spec: Machine{NICs: []NIC{{Network: "lab", Address: &low}}, HostnamePeers: HostnamePeers{Explicit: true, Names: []string{"a"}}}},
				{Name: "d", Spec: Machine{NICs: []NIC{{Network: "lab", Address: &upper}}}},
				{Name: "e"},
			}}}
			if platform == "proxmox" {
				if _, e = engine.state.Reserve(context.Background(), "vlan::", "occupied", 100, 199, 1); e != nil {
					t.Fatal(e)
				}
			}
			if e = engine.prepare(context.Background(), &instance); e != nil {
				t.Fatal(e)
			}
			prefix := "10.77.0."
			if platform == "proxmox" {
				prefix = "10.90.1."
			}
			for j, offset := range []string{"12", "11", "4", "250", "13"} {
				if instance.Plan.Machines[j].Address != prefix+offset {
					t.Fatal(instance.Plan.Machines)
				}
			}
			if len(instance.Plan.Machines[0].Hosts) != 0 || len(instance.Plan.Machines[1].Hosts) != 5 || len(instance.Plan.Machines[2].Hosts) != 1 {
				t.Fatal("incorrect peer filtering", instance.Plan.Machines)
			}
			var stored Instance
			if e = engine.state.Get(context.Background(), "instance", instance.ID, &stored); e != nil {
				t.Fatal(e)
			}
			if !stored.Plan.Machines[0].Spec.HostnamePeers.Explicit || len(stored.Plan.Machines[0].Hosts) != 0 {
				t.Fatal("explicit empty inventory was lost")
			}
		})
	}
}
func TestCapabilitiesPrecedeEnvironmentBindings(t *testing.T) {
	s, e := sourceYAML(t, []byte(strings.Replace(sample, "    base: ubuntu-24.04", "    base: ubuntu-24.04\n    os: windows", 1)))
	if e != nil {
		t.Fatal(e)
	}
	for _, platform := range []string{"aws", "proxmox"} {
		env := Environment{Platform: platform}
		_, e = PlanLab(s, env, nil)
		field := "images.base.os"
		if platform == "aws" {
			field = "machines.one.memory"
		}
		requireField(t, e, Unsupported, field)
	}
	c, _ := PlatformCapabilities("aws")
	c.OS[0] = "windows"
	fresh, _ := PlatformCapabilities("aws")
	if fresh.OS[0] != "linux" {
		t.Fatal("capabilities are mutable global state")
	}
}
