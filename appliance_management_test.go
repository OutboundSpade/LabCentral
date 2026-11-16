package labcentral

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVyOSOnlyPlanRetainsDataAndInfrastructureRoutes(t *testing.T) {
	src := applianceSource(t, func(l *Lab) {
		delete(l.Images, "firewall")
		delete(l.Machines, "palo")
		delete(l.Networks, "lan")
		delete(l.Networks, "management")
		server := l.Machines["server"]
		server.NICs = []NIC{{Network: "transit", Address: applianceOffset(100)}}
		l.Machines["server"] = server
	})
	plan, err := PlanLab(src, applianceEnvironment(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range plan.Machines {
		if m.Name != "server" {
			continue
		}
		if m.NICs[0].Gateway != "172.16.0.254" || !m.NICs[0].Default {
			t.Fatal("VyOS default route changed", m.NICs)
		}
		want := map[string]string{"172.31.26.0/24": "172.16.0.254", "172.17.0.0/24": "172.16.0.254", plan.EdgeCIDR: "172.16.0.1", m.Resolver + "/32": "172.16.0.1"}
		if len(m.NICs[0].Routes) != len(want) {
			t.Fatal("VyOS-only plan gained routes", m.NICs)
		}
		for _, route := range m.NICs[0].Routes {
			if want[route.CIDR] != route.Via {
				t.Fatal("VyOS route changed", route)
			}
		}
	}
	raw, err := json.Marshal(plan)
	if err != nil || strings.Contains(string(raw), `"managementClients"`) {
		t.Fatal("empty grant field changed plan serialization", err)
	}
}

func TestVyOSPolicyQuotingMatchesCloudInitGrammar(t *testing.T) {
	for _, command := range []string{
		"set firewall ipv4 name LAN-IN default-action 'drop'",
		"set firewall ipv4 name LAN-IN description 'Two words'",
		`set firewall ipv4 name LAN-IN description 'Say "hello"'`,
		"delete firewall ipv4 name LAN-IN",
		"set interfaces ethernet eth0 firewall in name 'LAN-IN'",
	} {
		if _, err := ApplianceCommands("vyos", []byte(command)); err != nil {
			t.Fatal(command, err)
		}
	}
	for _, command := range []string{
		"set firewall ipv4 name 'LAN-IN' default-action 'drop'",
		"set firewall ipv4 name 'LAN-IN' default-action drop",
		`set firewall ipv4 name "LAN-IN" default-action 'drop'`,
		"set firewall ipv4 name LAN-IN description 'can't quote'",
		"set firewall ipv4 name LAN-IN description 'quoted' suffix",
		"set interfaces ethernet eth0 description 'a firewall name'",
		"set interfaces ethernet eth0 address 'firewall invalid'",
	} {
		if _, err := ApplianceCommands("vyos", []byte(command)); err == nil {
			t.Fatal("unsupported quoting or interface override accepted", command)
		}
	}
}

func TestPANOSManagementRoutesArePerFirewallAndDefaultEmpty(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		src := applianceSource(t, func(l *Lab) {
			l.Networks["dmz"] = Network{CIDR: "172.20.241.0/24"}
			first := l.Machines["palo"]
			second := first
			second.NICs = []NIC{{Network: "management", Management: true, Address: applianceOffset(201)}, {Network: "lan", Address: applianceOffset(253)}, {Network: "dmz", Address: applianceOffset(254)}}
			if enabled {
				first.ManagementClients = []string{"server"}
				second.ManagementClients = []string{"kali"}
			}
			l.Machines["palo"], l.Machines["second"] = first, second
		})
		plan, err := PlanLab(src, applianceEnvironment(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range plan.Machines {
			if m.Name != "server" && m.Name != "kali" {
				continue
			}
			var routes []PlannedRoute
			for _, r := range m.NICs[0].Routes {
				if strings.HasPrefix(r.CIDR, "172.30.") {
					routes = append(routes, r)
				}
			}
			if !enabled {
				if len(routes) != 0 {
					t.Fatal("implicit management route", m.Name, routes)
				}
				continue
			}
			want, via := "172.30.0.200/32", "172.20.240.1"
			if m.Name == "kali" {
				want, via = "172.30.0.201/32", "172.17.0.1"
			}
			if len(routes) != 1 || routes[0].CIDR != want || routes[0].Via != via {
				t.Fatal("management route union or wrong gateway", m.Name, routes)
			}
		}
	}
}

func TestPANOSManagementClientValidationAndGroupCapacity(t *testing.T) {
	l := Lab{Images: map[string]Image{"panos": {Appliance: &ApplianceImage{Kind: "panos"}}}, Machines: map[string]Machine{"client": {}}}
	for _, clients := range [][]string{{"missing"}, {"client", "client"}, {"self"}} {
		l.Machines["self"] = Machine{Image: "panos", ManagementClients: clients}
		if ValidatePANOSManagement(l) == nil {
			t.Fatal("invalid clients accepted", clients)
		}
	}
	delete(l.Machines, "self")
	for _, name := range []string{"a", "b", "c", "d"} {
		l.Machines[name] = Machine{Image: "panos", ManagementClients: []string{"client"}}
		err := ValidatePANOSManagement(l)
		if (err == nil) != (name != "d") {
			t.Fatal("incorrect first-NIC quota enforcement", name, err)
		}
	}
	l.Machines = map[string]Machine{"client": {ManagementClients: []string{"other"}}, "other": {}}
	if ValidatePANOSManagement(l) == nil {
		t.Fatal("non-firewall management grant accepted")
	}
}
