package labcentral

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/ssh"
)

func applianceOffset(n int) *int { return &n }

func applianceSource(t *testing.T, change func(*Lab)) Source {
	t.Helper()
	l := Lab{Version: 3, Name: "native-range", Images: map[string]Image{
		"linux": {Base: "ubuntu-24.04"}, "router": {Base: "vyos", Appliance: &ApplianceImage{Kind: "vyos"}}, "firewall": {Base: "panos", Appliance: &ApplianceImage{Kind: "panos"}},
	}, Networks: map[string]Network{
		"wan":        {CIDR: "172.31.26.0/24", Egress: "internet"},
		"transit":    {CIDR: "172.16.0.0/24", DefaultRouter: "vyos"},
		"lan":        {CIDR: "172.20.240.0/24", DefaultRouter: "palo"},
		"kali":       {CIDR: "172.17.0.0/24", DefaultRouter: "vyos"},
		"management": {CIDR: "172.30.0.0/24"},
	}, Machines: map[string]Machine{
		"vyos":   {Image: "router", Forwarding: true, NICs: []NIC{{Network: "wan", Address: applianceOffset(10)}, {Network: "transit", Address: applianceOffset(254)}, {Network: "kali", Address: applianceOffset(254)}}},
		"palo":   {Image: "firewall", Forwarding: true, NICs: []NIC{{Network: "management", Management: true, Address: applianceOffset(200)}, {Network: "transit", Address: applianceOffset(10)}, {Network: "lan", Address: applianceOffset(254)}}},
		"server": {Image: "linux", NICs: []NIC{{Network: "lan", Address: applianceOffset(100)}}},
		"kali":   {Image: "linux", NICs: []NIC{{Network: "kali", Address: applianceOffset(10)}}},
	}}
	for name, m := range l.Machines {
		m.CPU = 2
		m.Memory = "8Gi"
		m.Disk = "60Gi"
		m.Access = []string{"ssh"}
		l.Machines[name] = m
	}
	if change != nil {
		change(&l)
	}
	raw, err := yaml.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	s, err := sourceYAML(t, raw)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func applianceEnvironment() Environment {
	e := testEnvironment()
	e.AWS.AllowEgress = true
	e.AWS.Types = []InstanceType{{Name: "m5.xlarge", CPU: 4, MemoryMiB: 16384, MaxNICs: 4}}
	e.Bases["vyos"] = Base{ID: "ami-0123456789abcdef0", Identity: "ami-0123456789abcdef0", User: "vyos", DiskGiB: 8, Appliance: "vyos"}
	e.Bases["panos"] = Base{ID: "ami-0123456789abcdef1", Identity: "ami-0123456789abcdef1", User: "admin", DiskGiB: 60, Appliance: "panos"}
	return e
}

func TestNativeApplianceChainKeepsManagementSeparate(t *testing.T) {
	p, err := PlanLab(applianceSource(t, func(l *Lab) {
		m := l.Machines["palo"]
		m.ManagementClients = []string{"server"}
		l.Machines["palo"] = m
	}), applianceEnvironment(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]PlannedMachine{}
	for _, m := range p.Machines {
		byName[m.Name] = m
	}
	server, palo, vyos := byName["server"], byName["palo"], byName["vyos"]
	if server.NICs[0].Gateway != "172.20.240.254" || !server.NICs[0].Default || palo.Appliance != "panos" || palo.SSHUser() != "admin" || vyos.SSHUser() != "vyos" {
		t.Fatal("default router or appliance binding lost", server, palo)
	}
	for _, n := range p.Networks {
		if n.Name == "management" && len(n.Routes) != 0 {
			t.Fatal("management became a transit network", n)
		}
		for _, r := range n.Routes {
			if r.Network == "management" || (r.Router == "palo" && r.RouterNIC == 0) {
				t.Fatal("data route uses management NIC", n, r)
			}
		}
	}
	want := map[string]string{p.EdgeCIDR: "172.20.240.1", "172.30.0.200/32": "172.20.240.1", "172.16.0.0/24": "172.20.240.254", "172.17.0.0/24": "172.20.240.254"}
	for _, r := range server.NICs[0].Routes {
		if next, ok := want[r.CIDR]; ok {
			if r.Via != next {
				t.Fatalf("%s went through %s, want %s", r.CIDR, r.Via, next)
			}
			delete(want, r.CIDR)
		}
	}
	if len(want) != 0 {
		t.Fatal("missing management or data route", want)
	}
	defaultFound := false
	for _, nic := range palo.NICs {
		if nic.Management && len(nic.Routes) > 2 {
			t.Fatal("management inherited data routes", nic)
		}
		for _, r := range nic.Routes {
			if r.CIDR == "0.0.0.0/0" {
				defaultFound = true
				if nic.Management || r.Via != "172.16.0.254" {
					t.Fatal("PAN data default uses wrong plane", nic)
				}
			}
		}
	}
	if !defaultFound {
		t.Fatal("PAN data-plane default missing")
	}
}

func TestApplianceVIPRoutesAndSourceRejections(t *testing.T) {
	s := applianceSource(t, func(l *Lab) {
		n := l.Networks["wan"]
		n.Routes = []NetworkRoute{{CIDR: "172.25.26.0/24", Router: "vyos"}}
		l.Networks["wan"] = n
	})
	p, err := PlanLab(s, applianceEnvironment(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range p.Networks {
		for _, r := range n.Routes {
			if r.CIDR == "172.25.26.0/24" {
				found = true
				if r.Router != "vyos" || r.RouterNIC != 0 {
					t.Fatal(r)
				}
			}
		}
	}
	if !found {
		t.Fatal("VIP route missing")
	}
	for _, test := range []string{"management-target", "no-management", "desktop", "no-forwarding", "unknown-kind", "low-memory", "overlap"} {
		t.Run(test, func(t *testing.T) {
			s := applianceSource(t, nil)
			m := s.Lab.Machines["palo"]
			switch test {
			case "management-target":
				n := s.Lab.Networks["management"]
				n.DefaultRouter = "palo"
				s.Lab.Networks["management"] = n
			case "no-management":
				m.NICs[0].Management = false
			case "desktop":
				m.Access = []string{"desktop"}
			case "no-forwarding":
				m.Forwarding = false
			case "unknown-kind":
				s.Lab.Images["firewall"].Appliance.Kind = "linux"
			case "low-memory":
				m.Memory = "4Gi"
			case "overlap":
				n := s.Lab.Networks["wan"]
				n.Routes = []NetworkRoute{{CIDR: "172.20.240.0/24", Router: "vyos"}}
				s.Lab.Networks["wan"] = n
			}
			s.Lab.Machines["palo"] = m
			if ValidateAppliances(s) == nil {
				t.Fatal("invalid appliance source accepted")
			}
		})
	}
	e := applianceEnvironment()
	b := e.Bases["panos"]
	b.Appliance = "vyos"
	b.User = "vyos"
	e.Bases["panos"] = b
	if _, err := PlanLab(applianceSource(t, nil), e, nil); err == nil {
		t.Fatal("incorrect operator base accepted")
	}
}

func TestApplianceConfigurationCannotChangeIdentityOrExecuteShell(t *testing.T) {
	for _, kind := range []string{"vyos", "panos", "unknown"} {
		for _, line := range []string{"run bash", "set system login user attacker", "set mgt-config users attacker", "set network interface ethernet ethernet1/1 layer3 ip 1.2.3.4/24", "set firewall name x; reboot", "set firewall name x | cat", "set firewall name x && reboot", "set firewall name x\tunsafe", "set firewall name labcentral", "set firewall name 'unterminated"} {
			if _, err := ApplianceCommands(kind, []byte(line)); err == nil {
				t.Fatalf("accepted %s: %s", kind, line)
			}
		}
	}
	for kind, line := range map[string]string{"vyos": "set nat source rule 10 translation address 'masquerade'", "panos": "set zone inside network layer3 ethernet1/2"} {
		if _, err := ApplianceCommands(kind, []byte(line)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestApplianceClientKeyIsRSAAndNeverReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client-key")
	pub, err := ensureKey(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pub, ssh.KeyAlgoRSA+" ") {
		t.Fatal("EC2 appliance key is not RSA")
	}
	first, _ := os.ReadFile(path)
	again, err := ensureKey(path, true)
	if err != nil || again != pub {
		t.Fatal("resume changed key", err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatal("resume rewrote private key")
	}
	other := filepath.Join(t.TempDir(), "old-key")
	if _, err := ensureKey(other); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureKey(other, true); err == nil {
		t.Fatal("silently replaced incompatible existing identity")
	}
}
