package guest

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"go.yaml.in/yaml/v3"
)

func TestGatewayBootstrapIsPinnedAndForwardingIsOptIn(t *testing.T) {
	m := lc.PlannedMachine{Name: "gateway", PublicKey: "ssh-ed25519 lab-key", HostPublicKey: "ssh-ed25519 pinned-host", HostPrivateKey: "PRIVATE-HOST-KEY"}
	for _, internet := range []bool{false, true} {
		cidrs := []string{}
		if internet {
			cidrs = append(cidrs, "10.77.0.0/24")
		}
		raw, err := GatewayCloudInit(m, cidrs)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			SSHKeys  hostKeys    `yaml:"ssh_keys"`
			Files    []writeFile `yaml:"write_files"`
			Run      []string    `yaml:"runcmd"`
			Password bool        `yaml:"ssh_pwauth"`
		}
		if err = yaml.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Password || doc.SSHKeys.Private != m.HostPrivateKey || doc.SSHKeys.Public != m.HostPublicKey {
			t.Fatal("gateway key/auth policy changed")
		}
		files := map[string]string{}
		for _, f := range doc.Files {
			files[f.Path] = f.Content
		}
		rules := files["/etc/labcentral-gateway.nft"]
		for _, want := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "100.64.0.0/10", "fib daddr type local drop", "policy drop;", "ip daddr @private4 drop", "ct state established,related accept"} {
			if !strings.Contains(rules, want) {
				t.Fatal("missing forwarding boundary", want)
			}
		}
		if strings.Contains(rules, "masquerade") != internet || strings.Contains(rules, "ip saddr 10.77.0.0/24 oifname") != internet {
			t.Fatal("forwarding is not opt-in")
		}
		script := files["/usr/local/sbin/labcentral-gateway"]
		if strings.Index(script, "nft -f") > strings.Index(script, "net.ipv4.ip_forward=1") {
			t.Fatal("forwarding enabled before firewall")
		}
		if !strings.Contains(files["/etc/systemd/system/labcentral-gateway.service"], "ExecStart=/usr/local/sbin/labcentral-gateway") {
			t.Fatal("reboot policy absent")
		}
	}
	if _, err := GatewayCloudInit(m, []string{"0.0.0.0/0; reboot"}); err == nil {
		t.Fatal("unvalidated source in nftables")
	}
}

func TestProxmoxGatewayUsesSharedPolicyAndSeparateManagementNIC(t *testing.T) {
	m := lc.PlannedMachine{Name: "gateway", Address: "192.0.2.20", PublicKey: "ssh-ed25519 client", HostPublicKey: "ssh-ed25519 pinned", HostPrivateKey: "private-host-key"}
	e := lc.Environment{OperatorCIDRs: []string{"192.0.2.10/32"}, Proxmox: lc.ProxmoxConfig{AllowEgress: true}}
	networks := []lc.PlannedNetwork{{Name: "a", CIDR: "10.80.0.0/24", Gateway: "10.80.0.1", Egress: "internet"}, {Name: "b", CIDR: "10.80.1.0/24", Gateway: "10.80.1.1", Egress: "none"}}
	script, err := ProxmoxGatewayExec(m, e, networks)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, match := range regexp.MustCompile(`printf '%s' '([A-Za-z0-9+/=]+)' \| base64 -d > ([^;]+); chmod`).FindAllStringSubmatch(script, -1) {
		raw, err := base64.StdEncoding.DecodeString(match[1])
		if err != nil {
			t.Fatal(err)
		}
		files[match[2]] = string(raw)
	}
	nft := files["/etc/labcentral-gateway.nft"]
	for _, want := range []string{`iifname "ens18" ip saddr 192.0.2.10/32 tcp dport 22 accept`, `ip saddr 10.80.0.0/24 drop`, `ip saddr 10.80.1.0/24 drop`, `fib daddr type local drop`, `ip daddr @private4 drop`, `ip saddr 10.80.0.0/24 oifname "__WAN__" masquerade`} {
		if !strings.Contains(nft, want) {
			t.Fatal("missing gateway policy", want, nft)
		}
	}
	for _, bad := range []string{"icmp type echo-request accept", "\n   tcp dport 22 accept", `ip saddr 10.80.1.0/24 oifname`} {
		if strings.Contains(nft, bad) {
			t.Fatal("gateway policy widened", bad)
		}
	}
	if files["/etc/ssh/ssh_host_ed25519_key"] != m.HostPrivateKey || !strings.Contains(files["/etc/ssh/sshd_config.d/00-labcentral-gateway.conf"], "AllowTcpForwarding local") {
		t.Fatal("pin or SSH restrictions absent")
	}
	netplan := files["/etc/netplan/99-labcentral.yaml"]
	for _, want := range []string{"ens18", "ens19", "ens20", "dhcp4: true", "10.80.0.1/24", "10.80.1.1/24", "accept-ra: false"} {
		if !strings.Contains(netplan, want) {
			t.Fatal("missing NIC configuration", want, netplan)
		}
	}
	if strings.Contains(script, "169.254.169.254/latest") || strings.Index(script, "net.ipv4.ip_forward=0") > strings.Index(script, "netplan apply") {
		t.Fatal("unsafe gateway initialization order")
	}
	e.Proxmox.AllowEgress = false
	if _, err = ProxmoxGatewayExec(m, e, networks); err == nil {
		t.Fatal("gateway bypassed operator egress policy")
	}
}

func TestProxmoxGatewayBindsSourceVLANBeforeEstablishedAndBlocksManagement(t *testing.T) {
	m := lc.PlannedMachine{Name: "gateway", PublicKey: "ssh-ed25519 client", HostPublicKey: "ssh-ed25519 host", HostPrivateKey: "private"}
	e := lc.Environment{OperatorCIDRs: []string{"198.51.100.10/32"}, Proxmox: lc.ProxmoxConfig{AllowEgress: true}}
	networks := []lc.PlannedNetwork{{Name: "a", CIDR: "10.80.0.0/24", Gateway: "10.80.0.1", Egress: "internet"}, {Name: "b", CIDR: "10.80.1.0/24", Gateway: "10.80.1.1", Egress: "none"}}
	script, err := ProxmoxGatewayExecOnNetwork(m, e, networks, "192.0.2.0/24", "AA:BB:CC:DD:EE:00", "AA:BB:CC:DD:EE:01", "AA:BB:CC:DD:EE:02")
	if err != nil {
		t.Fatal(err)
	}
	nft := ""
	for _, match := range regexp.MustCompile(`printf '%s' '([A-Za-z0-9+/=]+)' \| base64 -d > /etc/labcentral-gateway.nft;`).FindAllStringSubmatch(script, -1) {
		raw, _ := base64.StdEncoding.DecodeString(match[1])
		nft = string(raw)
	}
	forward := strings.SplitN(nft, "chain forward", 2)
	if len(forward) != 2 {
		t.Fatal("no forwarding policy")
	}
	for _, drop := range []string{`iifname "ens19" ip saddr != 10.80.0.0/24 drop`, `iifname "ens20" ip saddr != 10.80.1.0/24 drop`, `ip daddr 192.0.2.0/24 drop`, `ip daddr 198.51.100.10/32 drop`} {
		if pos := strings.Index(forward[1], drop); pos < 0 || pos > strings.Index(forward[1], "ct state established,related accept") {
			t.Fatal("spoofing/management drop absent or too late", drop, nft)
		}
	}
	if !strings.Contains(nft, `iifname "ens19" ip saddr 10.80.0.0/24 oifname "__WAN__" accept`) || strings.Contains(nft, `iifname "ens20" ip saddr 10.80.1.0/24 oifname`) {
		t.Fatal("wrong per-VLAN egress", nft)
	}
	if !strings.Contains(script, "systemctl restart labcentral-gateway.service") {
		t.Fatal("recovery did not reload policy after disabling forwarding")
	}
}

func TestProxmoxGatewayCanonicalizesOperatorRangeWithoutChangingCaller(t *testing.T) {
	e := lc.Environment{OperatorCIDRs: []string{"192.0.2.10/24"}}
	script, err := ProxmoxGatewayExec(lc.PlannedMachine{Name: "gateway", PublicKey: "ssh-ed25519 client", HostPublicKey: "ssh-ed25519 host", HostPrivateKey: "private"}, e, []lc.PlannedNetwork{{Name: "a", CIDR: "10.80.0.0/24", Gateway: "10.80.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range regexp.MustCompile(`printf '%s' '([A-Za-z0-9+/=]+)' \| base64 -d > /etc/labcentral-gateway.nft;`).FindAllStringSubmatch(script, -1) {
		found = true
		raw, _ := base64.StdEncoding.DecodeString(m[1])
		if !strings.Contains(string(raw), "ip saddr 192.0.2.0/24 tcp dport 22 accept") {
			t.Fatal(string(raw))
		}
	}
	if !found {
		t.Fatal("gateway policy missing")
	}
	if e.OperatorCIDRs[0] != "192.0.2.10/24" {
		t.Fatal("changed caller's environment")
	}
}
