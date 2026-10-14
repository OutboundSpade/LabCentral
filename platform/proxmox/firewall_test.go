package proxmox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"reflect"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
)

func TestEgressFirewallNativeOrderAndPrivateBlocking(t *testing.T) {
	env := lc.Environment{OperatorCIDRs: []string{"203.0.113.10/32"}, Proxmox: lc.ProxmoxConfig{URL: "https://198.51.100.25:8006", CIDR: "10.90.0.0/16", Gateway: "10.90.0.1", AllowEgress: true}}
	for _, internet := range []bool{false, true} {
		rules := firewallRules(env, "10.90.0.0/24", internet)
		effective := []firewallRule{}
		client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "PUT" {
				// General Internet accept must remain disabled throughout insertion.
				for _, rule := range effective {
					if rule.Dest == "0.0.0.0/0" && rule.Enable != 0 {
						t.Error("Internet accepted before private drops")
					}
				}
				var patch map[string]int
				if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
					t.Error(err)
				}
				effective[len(effective)-1].Enable = patch["enable"]
			} else {
				if r.Method != "POST" || r.URL.Path != "/api2/json/nodes/node/qemu/2100/firewall/rules" {
					t.Error("unexpected firewall mutation", r.Method, r.URL.Path)
				}
				var rule firewallRule
				if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
					t.Error(err)
				}
				effective = append([]firewallRule{rule}, effective...)
			}
			response(w, nil)
		})
		if err := installFirewallRules(context.Background(), client, 2100, rules); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(effective, rules) {
			t.Fatal("native order changed the policy", effective)
		}
		outbound := func(destination string) bool {
			ip := netip.MustParseAddr(destination)
			for _, rule := range effective {
				if rule.Type != "out" {
					continue
				}
				if netip.MustParsePrefix(rule.Dest).Contains(ip) {
					return rule.Action == "ACCEPT"
				}
			}
			return false // VM policy_out remains DROP.
		}
		if !outbound("10.90.0.12") {
			t.Fatal("intra-lab traffic blocked")
		}
		for _, address := range []string{"10.90.1.12", "10.1.0.1", "172.16.0.1", "192.168.0.1", "169.254.169.254", "100.64.0.1", "203.0.113.10", "198.51.100.25", "2001:db8::1"} {
			if outbound(address) {
				t.Fatal("private/management destination allowed", address, internet)
			}
		}
		if outbound("1.1.1.1") != internet {
			t.Fatal("wrong Internet policy", internet)
		}
	}
}
func TestEgressDeniedBeforeNativeConfiguration(t *testing.T) {
	for _, action := range []string{"check", "network.create", "machine.create", "image.build"} {
		_, err := (&Provider{}).Execute(context.Background(), lc.Command{Action: action, Environment: lc.Environment{Platform: "proxmox"}, Source: lc.Source{Lab: lc.Lab{Networks: map[string]lc.Network{"lab": {Egress: "internet"}}}}}, nil)
		if e, ok := err.(*lc.Error); !ok || e.Field != "networks.lab.egress" {
			t.Fatal("reached Proxmox configuration", action, err)
		}
	}
}

func TestInterruptedFirewallInstallNeverEnablesGeneralAccept(t *testing.T) {
	effective := []firewallRule{}
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			w.WriteHeader(500)
			return
		}
		var rule firewallRule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			t.Error(err)
		}
		effective = append([]firewallRule{rule}, effective...)
		response(w, nil)
	})
	rules := firewallRules(lc.Environment{}, "10.90.0.0/24", true)
	if err := installFirewallRules(context.Background(), client, 2100, rules); err == nil {
		t.Fatal("unconfirmed enable accepted")
	}
	for _, r := range effective {
		if r.Dest == "0.0.0.0/0" && r.Enable != 0 {
			t.Fatal("interrupted installation widened egress")
		}
	}
}
