package guest

import (
	"strings"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/sshtest"
	"go.yaml.in/yaml/v3"
)

func TestApplianceBootstrapUsesVendorFormats(t *testing.T) {
	_, pub := sshtest.Key(t)
	m := lc.PlannedMachine{Name: "router", Appliance: "vyos", Address: "172.31.26.10", PublicKey: pub, NICs: []lc.PlannedNIC{{CIDR: "172.31.26.0/24", Address: "172.31.26.10", Default: true, Gateway: "172.31.26.1"}, {CIDR: "172.16.0.0/24", Address: "172.16.0.254", Routes: []lc.PlannedRoute{{CIDR: "172.20.240.0/24", Via: "172.16.0.10"}}}}}
	raw, err := ApplianceUserData(m, "owned-instance", []string{"set nat source rule 10 translation address 'masquerade'"})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Commands []string                         `yaml:"vyos_config_commands"`
		Files    []struct{ Path, Content string } `yaml:"write_files"`
	}
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Files) != 1 || cfg.Files[0].Path != "/opt/vyatta/etc/config/scripts/vyos-postconfig-bootup.script" || !strings.Contains(cfg.Files[0].Content, "LABCENTRAL_HOST_KEY owned-instance") {
		t.Fatal("first-boot host attestation missing", cfg)
	}
	if strings.Contains(raw, "PRIVATE KEY") || strings.Contains(raw, "runcmd:") || !strings.Contains(raw, "next-hop '172.16.0.10'") || !strings.Contains(raw, "eth1 address '172.16.0.254/24'") {
		t.Fatal("incorrect VyOS bootstrap", raw)
	}
	m.Appliance = "panos"
	raw, err = ApplianceUserData(m, "owned-instance", nil)
	if err != nil || !strings.HasPrefix(raw, "type=dhcp-client\n") || strings.Contains(raw, "cloud-config") {
		t.Fatal("PAN-OS got Linux cloud-init", err, raw)
	}
}

func TestPANOSCommandsSkipManagementAndKeepDataDefault(t *testing.T) {
	m := lc.PlannedMachine{NICs: []lc.PlannedNIC{
		{Management: true, CIDR: "172.30.0.0/24", Address: "172.30.0.200"},
		{CIDR: "172.16.0.0/24", Address: "172.16.0.10", Routes: []lc.PlannedRoute{{CIDR: "0.0.0.0/0", Via: "172.16.0.254"}}},
		{CIDR: "172.20.240.0/24", Address: "172.20.240.254"},
	}}
	raw := strings.Join(PANOSNetworkCommands(m), "\n")
	for _, want := range []string{"ethernet1/1 layer3 ip 172.16.0.10/24", "ethernet1/2 layer3 ip 172.20.240.254/24", "destination 0.0.0.0/0", "nexthop ip-address 172.16.0.254"} {
		if !strings.Contains(raw, want) {
			t.Fatal("missing PAN-OS interface/route", want, raw)
		}
	}
	if strings.Contains(raw, "ethernet1/0") || strings.Contains(raw, "172.30.") {
		t.Fatal("management NIC became a data interface", raw)
	}
}
