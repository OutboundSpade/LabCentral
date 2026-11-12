package aws

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
)

func TestPANOSManagementGroupsIsolationAndRecovery(t *testing.T) {
	for _, loss := range []string{"", "CreateSecurityGroup:panos-management-sg", "CreateSecurityGroup:panos-client-sg"} {
		t.Run(loss, func(t *testing.T) {
			f, api := newNetworkEC2(t)
			f.loss = loss
			p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
			c := twoNetworks()
			c.Source.Lab.Images = map[string]lc.Image{"panos": {Base: "panos", Appliance: &lc.ApplianceImage{Kind: "panos"}}}
			c.Source.Lab.Networks["management"] = lc.Network{}
			c.Source.Lab.Networks["isolated"] = lc.Network{}
			c.Source.Lab.Machines["router"] = lc.Machine{Image: "panos", CPU: 2, Memory: "8Gi", Forwarding: true, NICs: []lc.NIC{{Network: "management", Management: true}, {Network: "b"}, {Network: "a"}}}
			c.Source.Lab.Machines["unrelated"] = lc.Machine{CPU: 2, Memory: "2Gi", NICs: []lc.NIC{{Network: "isolated"}}}
			c.Source.Lab.Machines["isolated-desktop"] = lc.Machine{CPU: 2, Memory: "2Gi", Access: []string{"desktop"}, NICs: []lc.NIC{{Network: "isolated"}}}
			result, err := p.network(context.Background(), api, c)
			if loss != "" {
				if err == nil {
					t.Fatal("lost group response accepted")
				}
				if _, err := p.findNetwork(context.Background(), api, c); err != nil {
					t.Fatal("group intent not recoverable", err)
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
			if n.PANOSManagementSG == "" || n.PANOSClientSG == "" || n.PANOSManagementSG == n.ManagementSG {
				t.Fatal("management not isolated", n)
			}
			created := map[string]int{}
			ingress, egress := false, false
			for _, r := range f.requests {
				if r.Get("Action") == "CreateSecurityGroup" {
					created[tagValue(r, "labcentral:component")]++
				}
				if r.Get("IpPermissions.1.FromPort") != "443" {
					continue
				}
				if r.Get("Action") == "AuthorizeSecurityGroupIngress" {
					if r.Get("GroupId") != n.PANOSManagementSG || r.Get("IpPermissions.1.Groups.1.GroupId") != n.PANOSClientSG || r.Get("IpPermissions.1.IpRanges.1.CidrIp") != "" {
						t.Fatal("HTTPS leaked to unrelated hosts", r)
					}
					ingress = true
				}
				if r.Get("Action") == "AuthorizeSecurityGroupEgress" && r.Get("IpPermissions.1.Groups.1.GroupId") == n.PANOSManagementSG {
					requestValue(t, r, "GroupId", n.PANOSClientSG)
					egress = true
				}
			}
			if !ingress || !egress || created["panos-management-sg"] != 1 || created["panos-client-sg"] != 1 {
				t.Fatal("missing or replayed management rules", created, ingress, egress)
			}
			for _, tc := range []struct {
				kind    string
				desktop bool
				want    string
			}{{"panos", false, n.PANOSManagementSG}, {"vyos", false, ""}, {"", false, ""}, {"", true, n.PANOSClientSG}} {
				m := lc.PlannedMachine{Appliance: tc.kind}
				if tc.desktop {
					m.Spec.Access = []string{"desktop"}
				}
				groups := panosManagementGroups([]string{"segment", "management"}, n, m)
				if len(groups) != 2 && (len(groups) != 3 || groups[2] != tc.want) || (tc.want != "" && len(groups) != 3) {
					t.Fatal("wrong interface group scope", tc, groups)
				}
			}
			c.Prior = result.Resource
			if f.owners == nil {
				f.owners = map[string]string{}
			}
			f.owners[n.PANOSManagementSG] = "foreign"
			if _, err := p.destroyNetwork(context.Background(), api, c); err == nil || !strings.Contains(err.Error(), "ownership") {
				t.Fatal("foreign management group not retained", err)
			}
			delete(f.owners, n.PANOSManagementSG)
			f.deleteLoss = "DeleteSecurityGroup"
			if _, err := p.destroyNetwork(context.Background(), api, c); err == nil {
				t.Fatal("lost teardown response accepted")
			}
			cleanup, err := p.destroyNetwork(context.Background(), api, c)
			if err != nil || !cleanup.Absent || len(f.objects) != 0 {
				t.Fatal("owned management groups leaked", err, f.objects)
			}
		})
	}
}
