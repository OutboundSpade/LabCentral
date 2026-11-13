package aws

import (
	"context"
	"fmt"
	"sort"

	lc "github.com/OutboundSpade/LabCentral/v2"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	t "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type panosManagementGrant struct {
	Firewall      string   `json:"firewall"`
	Clients       []string `json:"clients,omitempty"`
	DestinationSG string   `json:"destinationSG,omitempty"`
	ClientSG      string   `json:"clientSG,omitempty"`
}

func panosGroupComponent(firewall string, client bool) string {
	if client {
		return "panos-client-" + firewall + "-sg"
	}
	return "panos-management-" + firewall + "-sg"
}

// MGT must not inherit segment self/routed ingress: that would bypass grants
// even for an unlisted neighbor on the management subnet.
func panosManagementGroups(groups []string, n networkState, m lc.PlannedMachine) []string {
	if m.Appliance == "panos" {
		for _, grant := range n.PANOSManagement {
			if grant.Firewall == m.Name && grant.DestinationSG != "" && n.ManagementSG != "" {
				return []string{n.ManagementSG, grant.DestinationSG}
			}
		}
		return nil // Fail closed before RunInstances.
	}
	if m.Appliance != "" {
		return groups
	}
	for _, grant := range n.PANOSManagement {
		if grant.ClientSG != "" && lc.Contains(grant.Clients, m.Name) {
			groups = append(groups, grant.ClientSG)
		}
	}
	return groups
}

func networkGroupOwned(group t.SecurityGroup, id, owner, vpc, component string) bool {
	if aw.ToString(group.GroupId) != id || aw.ToString(group.VpcId) != vpc || !owned(group.Tags, owner) {
		return false
	}
	role, part := "", ""
	for _, tag := range group.Tags {
		switch aw.ToString(tag.Key) {
		case "labcentral:role":
			role = aw.ToString(tag.Value)
		case "labcentral:component":
			part = aw.ToString(tag.Value)
		}
	}
	return role == "network" && part == component
}

func (p *Provider) panosManagementGroup(ctx context.Context, a *ec2.Client, c lc.Command, n networkState, component string) (string, error) {
	id, err := doEC2(ctx, p.Journal, c.Operation+"/"+component, func() (string, error) {
		out, err := a.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{VpcId: &n.VPC, GroupName: aw.String("labcentral-" + c.Owner + "-" + component), Description: aw.String("Explicit per-firewall PAN-OS management"), TagSpecifications: networkSpecs(t.ResourceTypeSecurityGroup, c.Owner, component)}, noCreateRetries)
		if err != nil {
			return "", err
		}
		return aw.ToString(out.GroupId), nil
	}, func() (string, bool, error) { return findNetworkChild(ctx, a, c.Owner, component) })
	if err != nil {
		return "", err
	}
	out, err := a.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{id}})
	if err != nil {
		return "", err
	}
	if len(out.SecurityGroups) != 1 || !networkGroupOwned(out.SecurityGroups[0], id, c.Owner, n.VPC, component) {
		return "", fmt.Errorf("ownership_mismatch: PAN-OS management group retained")
	}
	if err = removeBroadEgress(ctx, a, id); err != nil {
		return "", err
	}
	return id, nil
}

func (p *Provider) panosManagementNetwork(ctx context.Context, a *ec2.Client, c lc.Command, n *networkState) error {
	grants := lc.PANOSManagementGrants(c.Source.Lab)
	if len(grants) == 0 {
		return nil
	}
	// Old range-wide groups may still be attached to running ENIs. Preserve
	// ownership records and require owned teardown, never an implicit migration.
	for _, component := range []string{"panos-management-sg", "panos-client-sg"} {
		pending, err := p.Journal.HasIntent(c.Operation + "/" + component)
		if err != nil {
			return err
		}
		if pending {
			return fmt.Errorf("recovery_required: implicit PAN-OS management network requires ownership-checked teardown and redeployment")
		}
	}
	names := make([]string, 0, len(grants))
	for name := range grants {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, firewall := range names {
		clients := append([]string(nil), grants[firewall]...)
		sort.Strings(clients)
		grant := panosManagementGrant{Firewall: firewall, Clients: clients}
		var err error
		grant.DestinationSG, err = p.panosManagementGroup(ctx, a, c, *n, panosGroupComponent(firewall, false))
		if err != nil {
			return err
		}
		// Inherit only the management segment's outbound policy, not its ingress.
		machine := c.Source.Lab.Machines[firewall]
		for j, seg := range n.Segments {
			if len(machine.NICs) == 0 || seg.Name != machine.NICs[0].Network {
				continue
			}
			component := "lab-sg"
			if j > 0 {
				component = "segment-" + seg.Name + "-sg"
			}
			out, err := a.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{seg.SG}})
			if err != nil {
				return err
			}
			if len(out.SecurityGroups) != 1 || !networkGroupOwned(out.SecurityGroups[0], seg.SG, c.Owner, n.VPC, component) {
				return fmt.Errorf("ownership_mismatch: management segment retained")
			}
			for _, permission := range out.SecurityGroups[0].IpPermissionsEgress {
				if err := authorize(ctx, a, grant.DestinationSG, false, permission); err != nil {
					return err
				}
			}
		}
		if len(clients) > 0 {
			grant.ClientSG, err = p.panosManagementGroup(ctx, a, c, *n, panosGroupComponent(firewall, true))
			if err != nil {
				return err
			}
			permission := t.IpPermission{IpProtocol: aw.String("tcp"), FromPort: aw.Int32(443), ToPort: aw.Int32(443), UserIdGroupPairs: []t.UserIdGroupPair{{GroupId: &grant.ClientSG}}}
			if err := authorize(ctx, a, grant.DestinationSG, true, permission); err != nil {
				return err
			}
			permission.UserIdGroupPairs = []t.UserIdGroupPair{{GroupId: &grant.DestinationSG}}
			if err := authorize(ctx, a, grant.ClientSG, false, permission); err != nil {
				return err
			}
		}
		n.PANOSManagement = append(n.PANOSManagement, grant)
	}
	return nil
}
