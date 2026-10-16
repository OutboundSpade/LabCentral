package guest

import (
	lc "github.com/OutboundSpade/LabCentral/v2"
	"go.yaml.in/yaml/v3"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestExplicitMultiNICNetplanAndGatewayCIDRs(t *testing.T) {
	m := lc.PlannedMachine{Name: "router", PublicKey: "ssh-ed25519 public", HostPrivateKey: "private", HostPublicKey: "ssh-ed25519 host", Spec: lc.Machine{Forwarding: true}, NICs: []lc.PlannedNIC{{Network: "a", CIDR: "10.77.0.0/24", Address: "10.77.0.12", Gateway: "10.77.0.1", Routes: []lc.PlannedRoute{{CIDR: "10.77.2.0/24", Via: "10.77.0.13"}}}, {Network: "b", CIDR: "10.77.1.0/24", Address: "10.77.1.12", Gateway: "10.77.1.1", Default: true}}}
	for _, platform := range []string{"aws", "proxmox"} {
		raw, err := Netplan(m, platform)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err = yaml.Unmarshal([]byte(raw), &document); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"10.77.0.12/24", "10.77.1.12/24", "via: 10.77.1.1", "via: 10.77.0.13", "dhcp4: false", "accept-ra: false"} {
			if !strings.Contains(raw, want) {
				t.Fatal("missing deterministic config", platform, want, raw)
			}
		}
		if strings.Count(raw, "0.0.0.0/0") != 1 {
			t.Fatal("multiple defaults", raw)
		}
	}
	raw, err := CloudInit(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"net.ipv4.ip_forward=1", "netplan generate && netplan apply", "network: {config: disabled}", "device-number", "169.254.169.254/32"} {
		if !strings.Contains(raw, want) {
			t.Fatal("missing bootstrap", want, raw)
		}
	}
	m.NICs = nil
	gateway, err := GatewayCloudInit(m, []string{"10.77.0.0/24"}, "10.77.0.0/24", "10.77.1.0/24")
	if err != nil {
		t.Fatal(err)
	}
	for _, cidr := range []string{"10.77.0.0/24", "10.77.1.0/24"} {
		if !strings.Contains(gateway, "ip saddr "+cidr+" drop") {
			t.Fatal("gateway input open to lab", gateway)
		}
	}
	if strings.Contains(gateway, "ip saddr 10.77.1.0/24 oifname") {
		t.Fatal("non-egress subnet got forwarding/NAT", gateway)
	}
}

// Execute only the platform-owned YAML transformation in a temporary filesystem;
// this does not apply netplan or assert live guest networking.
func TestProxmoxDNSPreservedOnDefaultNICAndRecovery(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("optional guest script check needs Python and PyYAML")
	}
	if err = exec.Command(python, "-c", "import yaml").Run(); err != nil {
		t.Skip("optional guest script check needs PyYAML")
	}
	root := t.TempDir()
	netplanDir := filepath.Join(root, "netplan")
	if err = os.Mkdir(netplanDir, 0700); err != nil {
		t.Fatal(err)
	}
	m := lc.PlannedMachine{NICs: []lc.PlannedNIC{{Network: "a", CIDR: "10.90.0.0/24", Address: "10.90.0.11"}, {Network: "b", CIDR: "10.90.1.0/24", Address: "10.90.1.11", Gateway: "10.90.1.1", Default: true}}}
	original := filepath.Join(netplanDir, "50-cloud-init.yaml")
	if err = os.WriteFile(original, []byte("network:\n  version: 2\n  ethernets:\n    ens18:\n      nameservers:\n        addresses: [9.9.9.9]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	script := preserveProxmoxDNS(m)
	script = strings.TrimSuffix(strings.TrimPrefix(script, "python3 - <<'LC_NETWORK_DNS'\n"), "LC_NETWORK_DNS\n")
	staging := filepath.Join(root, "new.yaml")
	script = strings.ReplaceAll(script, "'/run/labcentral-network.yaml'", strconv.Quote(staging))
	script = strings.ReplaceAll(script, "'/etc/netplan'", strconv.Quote(netplanDir))
	script = strings.ReplaceAll(script, "'/etc/netplan/99-labcentral.yaml'", strconv.Quote(filepath.Join(netplanDir, "99-labcentral.yaml")))
	if strings.Contains(script, "'/etc/") || strings.Contains(script, "'/run/") {
		t.Fatal("guest script fixture contains a host path")
	}
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := Netplan(m, "proxmox")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(staging, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(python, "-c", script).CombinedOutput(); err != nil {
			t.Fatal("generated guest transformation failed", err, string(out))
		}
		data, err := os.ReadFile(filepath.Join(netplanDir, "99-labcentral.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Network struct {
				Ethernets map[string]struct {
					Nameservers struct{ Addresses []string }
					Routes      []map[string]any
				}
			}
		}
		if err = yaml.Unmarshal(data, &config); err != nil {
			t.Fatal(err)
		}
		if got := config.Network.Ethernets["nic1"].Nameservers.Addresses; len(got) != 2 || got[0] != "1.1.1.1" || got[1] != "9.9.9.9" || len(config.Network.Ethernets["nic0"].Nameservers.Addresses) != 0 {
			t.Fatal("egress DNS lost or bound to non-egress NIC", string(data))
		}
		if attempt == 0 {
			if err = os.Remove(original); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestProxmoxNICsAreMatchedByAssignedMACAndRenamed(t *testing.T) {
	m := lc.PlannedMachine{NICs: []lc.PlannedNIC{{Network: "lab", CIDR: "10.80.0.0/24", Address: "10.80.0.11"}}}
	raw, err := Netplan(m, "proxmox", "AA:BB:CC:DD:EE:FF")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "macaddress: AA:BB:CC:DD:EE:FF") || !strings.Contains(raw, "set-name: ens18") {
		t.Fatal("unreliable name-only NIC binding", raw)
	}
	if _, err = Netplan(m, "proxmox", "not-a-mac"); err == nil {
		t.Fatal("invalid native NIC identity accepted")
	}
}
