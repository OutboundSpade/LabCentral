package aws

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
)

func twoFirewallCommand() lc.Command {
	c := twoNetworks()
	c.Source.Lab.Images = map[string]lc.Image{"panos": {Base: "panos", Appliance: &lc.ApplianceImage{Kind: "panos"}}}
	c.Source.Lab.Networks["management"] = lc.Network{}
	c.Source.Lab.Networks["isolated"] = lc.Network{}
	c.Source.Lab.Machines["router"] = lc.Machine{Image: "panos", CPU: 2, Memory: "8Gi", Forwarding: true, ManagementClients: []string{"b-client"}, NICs: []lc.NIC{{Network: "management", Management: true}, {Network: "b"}, {Network: "a"}}}
	c.Source.Lab.Machines["second"] = lc.Machine{Image: "panos", CPU: 2, Memory: "8Gi", Forwarding: true, ManagementClients: []string{"headless"}, NICs: []lc.NIC{{Network: "management", Management: true}, {Network: "b"}, {Network: "isolated"}}}
	c.Source.Lab.Machines["headless"] = lc.Machine{CPU: 2, Memory: "2Gi", NICs: []lc.NIC{{Network: "isolated"}}}
	c.Source.Lab.Machines["unlisted-desktop"] = lc.Machine{CPU: 2, Memory: "2Gi", Access: []string{"desktop"}, NICs: []lc.NIC{{Network: "isolated"}}}
	c.Source.Lab.Machines["unlisted-mgt"] = lc.Machine{CPU: 2, Memory: "2Gi", NICs: []lc.NIC{{Network: "management"}}}
	return c
}

func TestPANOSManagementGroupsIsolationAndRecovery(t *testing.T) {
	for _, loss := range []string{"", "CreateSecurityGroup:panos-management-router-sg", "CreateSecurityGroup:panos-client-router-sg", "CreateSecurityGroup:panos-management-second-sg", "CreateSecurityGroup:panos-client-second-sg"} {
		t.Run(loss, func(t *testing.T) {
			f, api := newNetworkEC2(t)
			f.loss = loss
			p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
			c := twoFirewallCommand()
			result, err := p.network(context.Background(), api, c)
			if loss != "" {
				if err == nil {
					t.Fatal("lost group response accepted")
				}
				if _, err := p.findNetwork(context.Background(), api, c); err != nil {
					t.Fatal("intent not discoverable", err)
				}
				result, err = p.network(context.Background(), api, c)
			}
			if err != nil {
				t.Fatal(err)
			}
			var n networkState
			if err := json.Unmarshal(result.Resource.Data, &n); err != nil {
				t.Fatal(err)
			}
			if len(n.PANOSManagement) != 2 {
				t.Fatal("missing per-firewall groups", n)
			}
			groups := map[string][]string{}
			for name, m := range c.Source.Lab.Machines {
				var segment string
				for _, seg := range n.Segments {
					if seg.Name == m.NICs[0].Network {
						segment = seg.SG
					}
				}
				groups[name] = panosManagementGroups([]string{segment, n.ManagementSG}, n, lc.PlannedMachine{Name: name, Appliance: c.Source.Lab.Images[m.Image].ApplianceKind(), Spec: m})
			}
			// Evaluate captured ingress against the groups actually selected for
			// each first NIC, including same-management-subnet self-group bypasses.
			allowed := func(client, firewall string) bool {
				for _, r := range f.requests {
					if r.Get("Action") != "AuthorizeSecurityGroupIngress" || !lc.Contains(groups[firewall], r.Get("GroupId")) {
						continue
					}
					if r.Get("IpPermissions.1.IpProtocol") != "-1" && r.Get("IpPermissions.1.FromPort") != "443" {
						continue
					}
					if lc.Contains(groups[client], r.Get("IpPermissions.1.Groups.1.GroupId")) {
						return true
					}
					if r.Get("IpPermissions.1.IpRanges.1.CidrIp") != "" {
						t.Fatal("MGT inherited CIDR-wide ingress", r)
					}
				}
				return false
			}
			for client, want := range map[string]string{"b-client": "router", "headless": "second", "unlisted-desktop": "", "unlisted-mgt": ""} {
				for _, firewall := range []string{"router", "second"} {
					if allowed(client, firewall) != (want == firewall) {
						t.Fatal("grant union or implicit access", client, firewall, groups)
					}
				}
			}
			for _, grant := range n.PANOSManagement {
				if len(groups[grant.Firewall]) != 2 || groups[grant.Firewall][0] != n.ManagementSG || groups[grant.Firewall][1] != grant.DestinationSG {
					t.Fatal("MGT inherited segment ingress", groups)
				}
				ingress, egress, creates := false, false, 0
				for _, r := range f.requests {
					if r.Get("Action") == "CreateSecurityGroup" && tagValue(r, "labcentral:component") == panosGroupComponent(grant.Firewall, false) {
						creates++
					}
					if r.Get("IpPermissions.1.FromPort") != "443" {
						continue
					}
					if r.Get("Action") == "AuthorizeSecurityGroupIngress" && r.Get("GroupId") == grant.DestinationSG {
						requestValue(t, r, "IpPermissions.1.Groups.1.GroupId", grant.ClientSG)
						ingress = true
					}
					if r.Get("Action") == "AuthorizeSecurityGroupEgress" && r.Get("GroupId") == grant.ClientSG {
						requestValue(t, r, "IpPermissions.1.Groups.1.GroupId", grant.DestinationSG)
						egress = true
					}
				}
				if creates != 1 || !ingress || !egress {
					t.Fatal("missing/replayed per-firewall rules", grant, creates, ingress, egress)
				}
			}
			c.Prior = result.Resource
			f.owners = map[string]string{n.PANOSManagement[0].DestinationSG: "foreign"}
			if _, err := p.destroyNetwork(context.Background(), api, c); err == nil || !strings.Contains(err.Error(), "ownership") {
				t.Fatal("foreign group not retained", err)
			}
			delete(f.owners, n.PANOSManagement[0].DestinationSG)
			f.deleteLoss = "DeleteSecurityGroup"
			if _, err := p.destroyNetwork(context.Background(), api, c); err == nil {
				t.Fatal("lost delete response accepted")
			}
			cleanup, err := p.destroyNetwork(context.Background(), api, c)
			if err != nil || !cleanup.Absent || len(f.objects) != 0 {
				t.Fatal("owned groups leaked", err, f.objects)
			}
		})
	}
}

func TestPANOSManagementDefaultsEmptyAndRejectsOldImplicitState(t *testing.T) {
	f, api := newNetworkEC2(t)
	c := twoFirewallCommand()
	for name, m := range c.Source.Lab.Machines {
		m.ManagementClients = nil
		c.Source.Lab.Machines[name] = m
	}
	p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
	r, err := p.network(context.Background(), api, c)
	if err != nil {
		t.Fatal(err)
	}
	var n networkState
	if err := json.Unmarshal(r.Resource.Data, &n); err != nil {
		t.Fatal(err)
	}
	for _, grant := range n.PANOSManagement {
		if grant.ClientSG != "" || len(grant.Clients) != 0 {
			t.Fatal("empty default granted clients", grant)
		}
	}
	for _, r := range f.requests {
		if r.Get("Action") == "AuthorizeSecurityGroupIngress" && r.Get("IpPermissions.1.FromPort") == "443" {
			t.Fatal("implicit HTTPS grant", r)
		}
	}
	if err := p.Journal.Save(c.Operation+"/panos-client-sg", "sg-old"); err != nil {
		t.Fatal(err)
	}
	if err := p.panosManagementNetwork(context.Background(), api, c, &n); err == nil || !strings.Contains(err.Error(), "recovery_required") {
		t.Fatal("old implicit groups adopted", err)
	}
}

func TestPANOSGrantValidationPrecedesAWSRequests(t *testing.T) {
	f, api := newNetworkEC2(t)
	c := twoFirewallCommand()
	m := c.Source.Lab.Machines["router"]
	m.ManagementClients = []string{"missing"}
	c.Source.Lab.Machines["router"] = m
	if _, err := (&Provider{}).network(context.Background(), api, c); err == nil || len(f.requests) != 0 {
		t.Fatal("invalid grant reached AWS", err, len(f.requests))
	}
	c = twoFirewallCommand()
	for _, name := range []string{"third", "fourth", "fifth"} {
		c.Source.Lab.Machines[name] = c.Source.Lab.Machines["router"]
	}
	if _, err := (&Provider{}).network(context.Background(), api, c); err == nil || !strings.Contains(err.Error(), "security groups") || len(f.requests) != 0 {
		t.Fatal("ENI group overflow reached AWS", err, len(f.requests))
	}
}

func TestVyOSOnlyNetworkRequestsUnchangedByManagementGrants(t *testing.T) {
	var baseline []url.Values
	for _, native := range []bool{false, true} {
		f, api := newNetworkEC2(t)
		c := twoNetworks()
		if native {
			c.Source.Lab.Images = map[string]lc.Image{"vyos": {Appliance: &lc.ApplianceImage{Kind: "vyos"}}}
			m := c.Source.Lab.Machines["router"]
			m.Image = "vyos"
			c.Source.Lab.Machines["router"] = m
		}
		if _, err := (&Provider{Journal: journal.Journal{Root: t.TempDir()}}).network(context.Background(), api, c); err != nil {
			t.Fatal(err)
		}
		// The SDK supplies a fresh UUID for this API's automatic idempotency
		// token. Normalize only that field; explicit provider tokens stay tested.
		for _, request := range f.requests {
			if request.Get("Action") == "CreateRouteTable" {
				request.Set("ClientToken", "sdk-generated")
			}
		}
		if !native {
			baseline = f.requests
		} else if !reflect.DeepEqual(baseline, f.requests) {
			t.Fatal("VyOS-only AWS requests gained management effects")
		}
	}
}
