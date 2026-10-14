package guest

import (
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
