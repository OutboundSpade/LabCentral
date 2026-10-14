package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/guest"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func routedMachine() (lc.PlannedMachine, []lc.PlannedNetwork) {
	a := lc.PlannedNetwork{Name: "a", CIDR: "10.90.1.0/24", Gateway: "10.90.1.1", VLAN: 101, Egress: "none", Routes: []lc.PlannedRoute{{Network: "b", CIDR: "10.90.2.0/24", Via: "10.90.1.12", Router: "router", RouterNIC: 0}}}
	b := lc.PlannedNetwork{Name: "b", CIDR: "10.90.2.0/24", Gateway: "10.90.2.1", VLAN: 102, Egress: "internet", Routes: []lc.PlannedRoute{{Network: "a", CIDR: "10.90.1.0/24", Via: "10.90.2.12", Router: "router", RouterNIC: 1}}}
	return lc.PlannedMachine{Name: "router", Spec: lc.Machine{Forwarding: true}, Address: "10.90.1.12", VMID: 2100, NativeImage: "2000", DiskGiB: 32, CPU: 2, MemoryMiB: 4096, NICs: []lc.PlannedNIC{{Network: a.Name, CIDR: a.CIDR, Address: "10.90.1.12", VLAN: a.VLAN, Gateway: a.Gateway}, {Network: b.Name, CIDR: b.CIDR, Address: "10.90.2.12", VLAN: b.VLAN, Gateway: b.Gateway, Default: true}}}, []lc.PlannedNetwork{a, b}
}
func TestMultipleNICConfigAndFirewallPaths(t *testing.T) {
	m, n := routedMachine()
	cmd := lc.Command{Machine: m, Environment: lc.Environment{OperatorCIDRs: []string{"203.0.113.10/32"}, Proxmox: lc.ProxmoxConfig{Bridge: "vmbr-labs", URL: "https://198.51.100.2:8006", CIDR: "10.90.0.0/16"}}}
	cfg := machineConfig(cmd, VMConfig{Net0: "virtio=AA:BB:CC:DD:EE:FF", Digest: "clone"}, "")
	for key, want := range map[string]string{"net0": "virtio=AA:BB:CC:DD:EE:FF,bridge=vmbr-labs,tag=101,firewall=1", "net1": "virtio,bridge=vmbr-labs,tag=102,firewall=1", "ipconfig0": "ip=10.90.1.12/24", "ipconfig1": "ip=10.90.2.12/24,gw=10.90.2.1"} {
		if cfg[key] != want {
			t.Fatal(key, cfg)
		}
	}
	rules := machineFirewallRules(cmd.Environment, m, n)
	internet, management := 0, 0
	for _, r := range rules {
		if r.Source == "203.0.113.10/32" && r.Action == "ACCEPT" {
			management++
			if r.Iface != "net0" || r.Dest != m.Address {
				t.Fatal("management on secondary NIC", r)
			}
		}
		if r.Dest == "0.0.0.0/0" && r.Action == "ACCEPT" {
			internet++
			if r.Iface != "net1" || r.Source != "10.90.2.0/24" {
				t.Fatal("egress on non-egress path", r)
			}
		}
		if r.Iface == "net0" && r.Source == n[1].CIDR && r.Action == "ACCEPT" {
			t.Fatal("router permits remote sources on wrong side", r)
		}
	}
	if internet != 1 || management != 1 {
		t.Fatal("rules incomplete", rules)
	}
	client := m
	client.Spec.Forwarding = false
	client.Name = "client"
	client.NICs = client.NICs[:1]
	rules = machineFirewallRules(cmd.Environment, client, n)
	found := false
	for _, r := range rules {
		if r.Type == "in" && r.Source == n[1].CIDR && r.Iface == "net0" {
			found = true
		}
	}
	if !found {
		t.Fatal("client cannot receive routed traffic", rules)
	}
}
func TestMultiNICFirewallInstallIsClosedUntilComplete(t *testing.T) {
	m, n := routedMachine()
	n[0].Egress = "internet"
	rules := machineFirewallRules(lc.Environment{}, m, n)
	effective := []firewallRule{}
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var rule firewallRule
			if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
				t.Error(err)
			}
			effective = append([]firewallRule{rule}, effective...)
			for _, rule := range effective {
				if rule.Dest == "0.0.0.0/0" && rule.Enable != 0 {
					t.Error("open before rule insertion completed")
				}
			}
		} else {
			if len(effective) != len(rules) {
				t.Error("enabled before all NIC policies installed")
			}
			var index int
			_, _ = fmt.Sscanf(r.URL.Path, "/api2/json/nodes/node/qemu/2100/firewall/rules/%d", &index)
			effective[index].Enable = 1
		}
		response(w, nil)
	})
	if err := installFirewallRules(context.Background(), client, 2100, rules); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(effective, rules) {
		t.Fatal("multi-NIC order", effective, rules)
	}
}
func TestGuestNetworkApplyFailureLeavesMachineNotReady(t *testing.T) {
	m, n := routedMachine()
	calls := []string{}
	executions := 0
	client, env := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/config") && r.Method == "GET":
			response(w, VMConfig{Description: marker("owned-network-lab", m.Name), Digest: "clone", Net0: "virtio=AA:BB:CC:DD:EE:FF"})
		case strings.HasSuffix(r.URL.Path, "/status/current"):
			response(w, map[string]string{"status": "running"})
		case strings.HasSuffix(r.URL.Path, "/firewall/rules") && r.Method == "GET":
			response(w, []any{})
		case strings.HasSuffix(r.URL.Path, "/agent/exec"):
			executions++
			var args struct {
				Command []string `json:"command"`
			}
			if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
				t.Error(err)
			}
			if executions == 2 && !strings.Contains(args.Command[2], "netplan apply") {
				t.Error("network not configured through guest agent")
			}
			response(w, map[string]int{"pid": executions})
		case strings.Contains(r.URL.Path, "/agent/exec-status"):
			code := 0
			if executions == 2 {
				code = 1
			}
			response(w, map[string]int{"exited": 1, "exitcode": code})
		default:
			response(w, nil)
		}
	})
	env.Proxmox.Bridge = "vmbr-labs"
	cmd := lc.Command{Owner: "owned-network-lab", Operation: "machine", Environment: env, Machine: m}
	raw, _ := json.Marshal(network{CIDR: n[0].CIDR, Segments: n})
	cmd.Network = lc.Resource{Data: raw}
	p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
	if err := p.Journal.Save("machine/clone", m.VMID); err != nil {
		t.Fatal(err)
	}
	r, err := p.machine(context.Background(), client, cmd)
	if err == nil || r.Resource.Complete || executions != 2 {
		t.Fatal("failed netplan became ready", r, err, calls)
	}
	script, err := guest.NetworkExec(m)
	if err != nil || !strings.Contains(script, "ip -4 -o addr show dev ens19") || !strings.Contains(script, "test $(ip -4 route show default | wc -l) -eq 1") {
		t.Fatal("guest verification absent", err, script)
	}
}

func TestCloneExtraNICsRemovedAndPlannedMACsPreserved(t *testing.T) {
	var v VMConfig
	if err := json.Unmarshal([]byte(`{"net0":"virtio=AA:BB:CC:DD:EE:00","net1":"e1000=AA:BB:CC:DD:EE:01","net2":"virtio=AA:BB:CC:DD:EE:02","digest":"clone"}`), &v); err != nil {
		t.Fatal(err)
	}
	m, _ := routedMachine()
	cfg := machineConfig(lc.Command{Machine: m, Environment: lc.Environment{Proxmox: lc.ProxmoxConfig{Bridge: "vmbr-labs"}}}, v, "")
	if cfg["delete"] != "ipconfig2,net2" || !strings.HasPrefix(cfg["net1"].(string), "virtio=AA:BB:CC:DD:EE:01,") {
		t.Fatal("unplanned NIC or lost MAC", cfg)
	}
}
