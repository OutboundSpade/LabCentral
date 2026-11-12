package aws

import (
	"context"
	"fmt"

	lc "github.com/OutboundSpade/LabCentral/v2"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	t "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// Only PAN-OS NIC0 receives the HTTPS destination group. Desktop guests get a
// separate source group on NIC0, including guests on otherwise isolated segments.
func panosManagementGroups(groups []string, n networkState, m lc.PlannedMachine) []string {
	if m.Appliance == "panos" && n.PANOSManagementSG != "" {
		groups = append(groups, n.PANOSManagementSG)
	} else if m.Appliance == "" && lc.Contains(m.Spec.Access, "desktop") && n.PANOSClientSG != "" {
		groups = append(groups, n.PANOSClientSG)
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

func (p *Provider) panosManagementNetwork(ctx context.Context, a *ec2.Client, c lc.Command, n *networkState) error {
	needed := false
	for _, machine := range c.Source.Lab.Machines {
		if c.Source.Lab.Images[machine.Image].ApplianceKind() == "panos" {
			needed = true
		}
	}
	if !needed {
		return nil
	}
	// Remove the previous implementation's CIDR-wide HTTPS grants if an
	// interrupted network setup is being resumed with this version.
	management := n.ManagementSG
	managementComponent := "management-sg"
	if management == "" {
		management = n.SG
		managementComponent = "lab-sg"
	}
	current, err := a.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{management}})
	if err != nil {
		return err
	}
	if len(current.SecurityGroups) != 1 || !networkGroupOwned(current.SecurityGroups[0], management, c.Owner, n.VPC, managementComponent) {
		return fmt.Errorf("ownership_mismatch: shared management group retained")
	}
	for _, segment := range n.Segments {
		_, err := a.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{GroupId: &management, IpPermissions: []t.IpPermission{{IpProtocol: aw.String("tcp"), FromPort: aw.Int32(443), ToPort: aw.Int32(443), IpRanges: []t.IpRange{{CidrIp: &segment.CIDR}}}}})
		if err != nil && !apiCode(err, "InvalidPermission.NotFound") {
			return err
		}
	}
	for _, entry := range []struct {
		component string
		id        *string
	}{
		{"panos-management-sg", &n.PANOSManagementSG}, {"panos-client-sg", &n.PANOSClientSG},
	} {
		id, err := doEC2(ctx, p.Journal, c.Operation+"/"+entry.component, func() (string, error) {
			out, err := a.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{VpcId: &n.VPC, GroupName: aw.String("labcentral-" + c.Owner + "-" + entry.component), Description: aw.String("Scoped PAN-OS management"), TagSpecifications: networkSpecs(t.ResourceTypeSecurityGroup, c.Owner, entry.component)}, noCreateRetries)
			if err != nil {
				return "", err
			}
			return aw.ToString(out.GroupId), nil
		}, func() (string, bool, error) { return findNetworkChild(ctx, a, c.Owner, entry.component) })
		if err != nil {
			return err
		}
		out, err := a.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{id}})
		if err != nil {
			return err
		}
		if len(out.SecurityGroups) != 1 || !networkGroupOwned(out.SecurityGroups[0], id, c.Owner, n.VPC, entry.component) {
			return fmt.Errorf("ownership_mismatch: PAN-OS management group retained")
		}
		*entry.id = id
		if err = removeBroadEgress(ctx, a, id); err != nil {
			return err
		}
	}
	permission := t.IpPermission{IpProtocol: aw.String("tcp"), FromPort: aw.Int32(443), ToPort: aw.Int32(443), UserIdGroupPairs: []t.UserIdGroupPair{{GroupId: &n.PANOSClientSG}}}
	if err := authorize(ctx, a, n.PANOSManagementSG, true, permission); err != nil {
		return err
	}
	permission.UserIdGroupPairs = []t.UserIdGroupPair{{GroupId: &n.PANOSManagementSG}}
	return authorize(ctx, a, n.PANOSClientSG, false, permission)
}
