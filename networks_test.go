package labcentral

import (
	"context"
	"go.yaml.in/yaml/v3"
	"reflect"
	"strings"
	"testing"
)

func topologySource(t *testing.T, networks map[string]Network, machines map[string]Machine) Source {
	t.Helper()
	for name, m := range machines {
		m.Image = "base"
		m.CPU = 2
		m.Memory = "2Gi"
		m.Disk = "20Gi"
		m.Access = []string{"ssh"}
		machines[name] = m
	}
	raw, err := yaml.Marshal(Lab{Version: 3, Name: "networks", Networks: networks, Images: map[string]Image{"base": {Base: "ubuntu-24.04"}}, Machines: machines})
	if err != nil {
		t.Fatal(err)
	}
	s, err := sourceYAML(t, raw)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func chainSource(t *testing.T) Source {
	return topologySource(t, map[string]Network{"a": {}, "b": {}, "c": {}}, map[string]Machine{
		"a-client": {NICs: []NIC{{Network: "a"}}}, "b-client": {NICs: []NIC{{Network: "b"}}}, "c-client": {NICs: []NIC{{Network: "c"}}},
		"r1": {Forwarding: true, NICs: []NIC{{Network: "a"}, {Network: "b"}}}, "r2": {Forwarding: true, NICs: []NIC{{Network: "b"}, {Network: "c"}}},
	})
}
func routed(nic PlannedNIC) []PlannedRoute {
	rs := []PlannedRoute{}
	for _, r := range nic.Routes {
		if r.Router != "" {
			rs = append(rs, r)
		}
	}
	return rs
}
func TestNetworkChainAddressesRoutesAndInventory(t *testing.T) {
	for _, e := range []Environment{testEnvironment(), platformOptionsProxmoxEnvironment()} {
		p, err := PlanLab(chainSource(t), e, nil)
		if err != nil {
			t.Fatal(err)
		}
		prefix := "10.77."
		if e.Platform == "proxmox" {
			prefix = "10.90."
		}
		a, b, c, r1, r2 := p.Machines[0], p.Machines[1], p.Machines[2], p.Machines[3], p.Machines[4]
		if a.Address != prefix+"0.11" || b.Address != prefix+"1.11" || c.Address != prefix+"2.11" || r1.NICs[1].Address != prefix+"1.12" || r2.Address != prefix+"1.13" {
			t.Fatal("address allocation changed", p.Machines)
		}
		expected := []PlannedRoute{{Network: "b", CIDR: prefix + "1.0/24", Via: prefix + "0.12", Router: "r1", RouterNIC: 0}, {Network: "c", CIDR: prefix + "2.0/24", Via: prefix + "0.12", Router: "r1", RouterNIC: 0}}
		if !reflect.DeepEqual(routed(a.NICs[0]), expected) {
			t.Fatal("multi-hop route", a.NICs)
		}
		if len(routed(r1.NICs[0])) != 0 || len(routed(r1.NICs[1])) != 1 || routed(r1.NICs[1])[0].Via != prefix+"1.13" {
			t.Fatal("router route loops back to itself", r1.NICs)
		}
		for _, host := range b.Hosts {
			if host.Name == "r1" && host.Address != r1.NICs[1].Address {
				t.Fatal("shared network inventory", b.Hosts)
			}
			if host.Name == "c-client" && host.Address != c.Address {
				t.Fatal("fallback inventory", b.Hosts)
			}
		}
		again, err := PlanLab(p.Source, e, nil)
		if err != nil || !reflect.DeepEqual(p, again) {
			t.Fatal("non-deterministic plan", err)
		}
	}
}
func TestRouterTopologyRejectionsAndStar(t *testing.T) {
	cases := []struct {
		name     string
		machines map[string]Machine
		field    string
	}{
		{"ambiguous", map[string]Machine{"r1": {Forwarding: true, NICs: []NIC{{Network: "a"}, {Network: "b"}}}, "r2": {Forwarding: true, NICs: []NIC{{Network: "a"}, {Network: "b"}}}}, "machines.r2.forwarding"},
		{"cycle", map[string]Machine{"r1": {Forwarding: true, NICs: []NIC{{Network: "a"}, {Network: "b"}}}, "r2": {Forwarding: true, NICs: []NIC{{Network: "b"}, {Network: "c"}}}, "r3": {Forwarding: true, NICs: []NIC{{Network: "c"}, {Network: "a"}}}}, "networks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := PlanLab(topologySource(t, map[string]Network{"a": {}, "b": {}, "c": {}}, tc.machines), testEnvironment(), nil)
			requireField(t, err, Invalid, tc.field)
		})
	}
	for _, forward := range []bool{false, true} {
		p, err := PlanLab(topologySource(t, map[string]Network{"a": {}, "b": {}, "c": {}}, map[string]Machine{"router": {Forwarding: forward, NICs: []NIC{{Network: "a"}, {Network: "b"}, {Network: "c"}}}}), testEnvironment(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range p.Networks {
			want := 0
			if forward {
				want = 2
			}
			if len(n.Routes) != want {
				t.Fatal("star or nonrouter reachability", p.Networks)
			}
		}
	}
}
func TestAuthorCIDRLayoutAndFailures(t *testing.T) {
	for _, tc := range []struct {
		networks         map[string]Network
		vpc, edge, field string
		code             Code
	}{
		{map[string]Network{"a": {CIDR: "192.168.2.0/24"}, "b": {CIDR: "192.168.255.240/28"}}, "192.168.0.0/16", "192.168.255.224/28", "", ""},
		{map[string]Network{"a": {CIDR: "10.77.255.0/24"}, "b": {}}, "10.77.0.0/16", "10.77.254.240/28", "", ""},
		{map[string]Network{"a": {CIDR: "10.2.0.0/24"}, "b": {}}, "", "", "networks.b.cidr", Unsupported},
		{map[string]Network{"a": {CIDR: "10.2.0.0/24"}, "b": {CIDR: "10.3.0.0/24"}}, "", "", "networks.b.cidr", Unsupported},
		{map[string]Network{"a": {CIDR: "10.77.1.0/24"}, "b": {}}, "", "", "networks.b.cidr", Invalid},
		{map[string]Network{"a": {CIDR: "10.2.0.0/23"}, "b": {CIDR: "10.2.1.0/24"}}, "", "", "networks.b.cidr", Invalid},
		{map[string]Network{"a": {CIDR: "10.2.0.0/16"}}, "", "", "networks", Unsupported},
	} {
		t.Run(tc.vpc+tc.field, func(t *testing.T) {
			p, err := PlanLab(topologySource(t, tc.networks, map[string]Machine{"client": {NICs: []NIC{{Network: "a"}}}}), testEnvironment(), nil)
			if tc.field != "" {
				requireField(t, err, tc.code, tc.field)
				return
			}
			if err != nil || p.VPCCIDR != tc.vpc || p.EdgeCIDR != tc.edge {
				t.Fatal("layout", p.VPCCIDR, p.EdgeCIDR, err)
			}
		})
	}
	s := topologySource(t, map[string]Network{"a": {CIDR: "10.2.0.0/24"}}, map[string]Machine{"client": {NICs: []NIC{{Network: "a"}}}})
	_, err := PlanLab(s, platformOptionsProxmoxEnvironment(), nil)
	requireField(t, err, Unsupported, "networks.a.cidr")
}
func TestDefaultNICAndInstanceLimits(t *testing.T) {
	e := testEnvironment()
	e.AWS.AllowEgress = true
	s := topologySource(t, map[string]Network{"a": {}, "b": {Egress: "internet"}, "c": {Egress: "internet"}}, map[string]Machine{"client": {NICs: []NIC{{Network: "a"}, {Network: "c"}, {Network: "b"}}}})
	p, err := PlanLab(s, e, nil)
	if err != nil {
		t.Fatal(err)
	}
	n := p.Machines[0].NICs
	if n[0].Default || !n[1].Default || n[2].Default {
		t.Fatal("default does not follow NIC order", n)
	}
	e.AWS.Types[0].MaxNICs = 2
	_, err = PlanLab(s, e, nil)
	requireField(t, err, Unsupported, "machines.client.nics[2]")
	e.AWS.Types[0].MaxNICs = 0
	requireField(t, CheckAWSNICLimits(s.Lab, e, map[string]int{"t3.medium": 2}), Unsupported, "machines.client.nics[2]")
}
func TestSingleNetworkAndRuntimeImageKeyStability(t *testing.T) {
	e := testEnvironment()
	base, err := sourceYAML(t, []byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	original, err := PlanLab(base, e, nil)
	if err != nil {
		t.Fatal(err)
	}
	if original.Machines[0].Address != "10.77.0.11" || original.Networks[0].CIDR != "10.77.0.0/24" || original.VPCCIDR != "10.77.0.0/16" || original.EdgeCIDR != "10.77.255.0/28" {
		t.Fatal("legacy layout moved", original)
	}
	changed, err := sourceYAML(t, []byte(strings.Replace(sample, "    access: [ssh]", "    access: [ssh]\n    nics: [{network: a}, {network: b}]\n    forwarding: true", 1)+"networks: {a: {}, b: {}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	e.AWS.AllowEgress = true
	e.AWS.Gateway.InstanceType = "t3.small"
	e.AWS.Types[0].MaxNICs = 8
	e.AWS.DesktopProfile = "different-license"
	e.OperatorCIDRs = []string{"203.0.113.1/32"}
	next, err := PlanLab(changed, e, nil)
	if err != nil {
		t.Fatal(err)
	}
	if next.Images[0].CacheKey != original.Images[0].CacheKey {
		t.Fatal("runtime configuration rebuilt image")
	}
	e.AWS.Region = "us-west-2"
	e.AWS.Zone = "us-west-2a"
	next, err = PlanLab(changed, e, nil)
	if err != nil || next.Images[0].CacheKey == original.Images[0].CacheKey {
		t.Fatal("platform build target not bound", err)
	}
}
func TestMultiVLANReservationSurvivesInterruptedPreparation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	fake := freshFake()
	engine := openFake(t, root, fake)
	env := platformOptionsProxmoxEnvironment()
	p, err := PlanLab(chainSource(t), env, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := "vlan:" + env.Proxmox.URL + ":" + env.Proxmox.LabBridge
	if _, err = engine.state.Reserve(ctx, scope, "occupied", 100, 115, 1); err != nil {
		t.Fatal(err)
	}
	owner := strings.Repeat("d", 24)
	claims, err := engine.state.Reserve(ctx, scope, owner, 100, 115, 3)
	if err != nil {
		t.Fatal(err)
	}
	i := Instance{ID: owner, Name: p.Source.Lab.Name, Status: "preparing", Plan: p}
	if err = engine.state.Put(ctx, "instance", owner, i); err != nil {
		t.Fatal(err)
	}
	engine.Close()
	engine = openFake(t, root, fake)
	defer engine.Close()
	ready, err := engine.Recover(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	for j, n := range ready.Plan.Networks {
		if n.VLAN != claims[j] {
			t.Fatal("reservation changed after interruption", claims, ready.Plan.Networks)
		}
	}
	if ready.Plan.Machines[0].Address != "10.90.1.11" {
		t.Fatal("preview address not rebased", ready.Plan.Machines)
	}
	fake.failDelete = true
	if err = engine.Destroy(ctx, owner); err == nil {
		t.Fatal("uncertain deletion accepted")
	}
	retained, err := engine.state.Reserve(ctx, scope, owner, 100, 115, 3)
	if err != nil || !reflect.DeepEqual(retained, claims) {
		t.Fatal("uncertain cleanup released VLANs", err)
	}
	fake.failDelete = false
	if err = engine.Destroy(ctx, owner); err != nil {
		t.Fatal(err)
	}
	reused, err := engine.state.Reserve(ctx, scope, "new-owner", 100, 115, 3)
	if err != nil || !reflect.DeepEqual(reused, claims) {
		t.Fatal("confirmed cleanup did not release all VLANs", reused, err)
	}
}

type nicCheckProvider struct{ *fakeProvider }

func (p *nicCheckProvider) Execute(ctx context.Context, c Command, o Observer) (Result, error) {
	if c.Action == "check" {
		if err := CheckAWSNICLimits(c.Source.Lab, c.Environment, map[string]int{"t3.medium": 1}); err != nil {
			return Result{}, err
		}
	}
	return p.fakeProvider.Execute(ctx, c, o)
}
func TestNativeNICLimitCheckPrecedesReservationAndBuild(t *testing.T) {
	provider := &nicCheckProvider{freshFake()}
	engine, err := Open(t.TempDir(), provider, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	s := topologySource(t, map[string]Network{"a": {}, "b": {}}, map[string]Machine{"router": {Forwarding: true, NICs: []NIC{{Network: "a"}, {Network: "b"}}}})
	p, err := PlanLab(s, testEnvironment(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Deploy(context.Background(), p)
	requireField(t, err, Unsupported, "machines.router.nics[1]")
	if provider.creates != 0 {
		t.Fatal("native mutation preceded ENI limit check")
	}
}

func TestMultiVLANExhaustionRollsBackWholeReservation(t *testing.T) {
	ctx := context.Background()
	engine := openFake(t, t.TempDir(), freshFake())
	defer engine.Close()
	env := platformOptionsProxmoxEnvironment()
	p, err := PlanLab(chainSource(t), env, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := "vlan:" + env.Proxmox.URL + ":" + env.Proxmox.LabBridge
	if _, err = engine.state.Reserve(ctx, scope, "occupied", 100, 115, 14); err != nil {
		t.Fatal(err)
	}
	inst := Instance{ID: "new-owner", Plan: p}
	if err = engine.prepare(ctx, &inst); err == nil {
		t.Fatal("partial VLAN reservation accepted")
	}
	var count int
	if err = engine.state.DB.QueryRowContext(ctx, "SELECT count(*) FROM claims WHERE owner=?", inst.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial claims survived failure", count, err)
	}
}
