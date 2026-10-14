package aws

import (
	"context"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	t "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func networkSpecs(kind t.ResourceType, owner, component string) []t.TagSpecification {
	s := specs(kind, owner, "network")
	s[0].Tags = append(s[0].Tags, t.Tag{Key: aw.String("labcentral:component"), Value: &component})
	return s
}
func componentFilters(owner, component string) []t.Filter {
	return append(filters(owner, "network"), t.Filter{Name: aw.String("tag:labcentral:component"), Values: []string{component}})
}
func (p *Provider) edgeNetwork(ctx context.Context, a *ec2.Client, c lc.Command, n *networkState) error {
	var err error
	n.EdgeSubnet, err = doEC2(ctx, p.Journal, c.Operation+"/edge-subnet", func() (string, error) {
		r, e := a.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: &n.VPC, AvailabilityZone: &c.Environment.AWS.Zone, CidrBlock: aw.String("10.77.255.0/28"), TagSpecifications: networkSpecs(t.ResourceTypeSubnet, c.Owner, "edge-subnet")})
		if e != nil {
			return "", e
		}
		return aw.ToString(r.Subnet.SubnetId), nil
	}, func() (string, bool, error) {
		r, e := a.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{Filters: componentFilters(c.Owner, "edge-subnet")})
		if e != nil {
			return "", false, e
		}
		if len(r.Subnets) > 1 {
			return "", false, fmt.Errorf("ambiguous edge subnet")
		}
		if len(r.Subnets) == 1 {
			return aw.ToString(r.Subnets[0].SubnetId), true, nil
		}
		return "", false, nil
	})
	if err != nil {
		return err
	}
	n.EdgeRoute, err = doEC2(ctx, p.Journal, c.Operation+"/edge-route", func() (string, error) {
		r, e := a.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{VpcId: &n.VPC, TagSpecifications: networkSpecs(t.ResourceTypeRouteTable, c.Owner, "edge-route")})
		if e != nil {
			return "", e
		}
		return aw.ToString(r.RouteTable.RouteTableId), nil
	}, func() (string, bool, error) {
		r, e := a.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{Filters: componentFilters(c.Owner, "edge-route")})
		if e != nil {
			return "", false, e
		}
		if len(r.RouteTables) > 1 {
			return "", false, fmt.Errorf("ambiguous edge route table")
		}
		if len(r.RouteTables) == 1 {
			return aw.ToString(r.RouteTables[0].RouteTableId), true, nil
		}
		return "", false, nil
	})
	if err != nil {
		return err
	}
	if err = ensureInternetRoute(ctx, a, c.Owner, n.VPC, n.EdgeRoute, n.Gateway, waitRouteRetry); err != nil {
		return err
	}
	rt, err := describeRouteTable(ctx, a, n.EdgeRoute, waitRouteRetry)
	if err != nil {
		return err
	}
	associated := false
	for _, rt := range rt.RouteTables {
		for _, v := range rt.Associations {
			associated = associated || aw.ToString(v.SubnetId) == n.EdgeSubnet
		}
	}
	if !associated {
		if _, err = a.AssociateRouteTable(ctx, &ec2.AssociateRouteTableInput{RouteTableId: &n.EdgeRoute, SubnetId: &n.EdgeSubnet}); err != nil {
			return err
		}
	}
	n.GatewaySG, err = doEC2(ctx, p.Journal, c.Operation+"/gateway-sg", func() (string, error) {
		r, e := a.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{VpcId: &n.VPC, GroupName: aw.String("labcentral-" + c.Owner + "-edge"), Description: aw.String("LabCentral owned gateway"), TagSpecifications: networkSpecs(t.ResourceTypeSecurityGroup, c.Owner, "edge-sg")})
		if e != nil {
			return "", e
		}
		return aw.ToString(r.GroupId), nil
	}, func() (string, bool, error) {
		r, e := a.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{Filters: componentFilters(c.Owner, "edge-sg")})
		if e != nil {
			return "", false, e
		}
		if len(r.SecurityGroups) > 1 {
			return "", false, fmt.Errorf("ambiguous gateway security group")
		}
		if len(r.SecurityGroups) == 1 {
			return aw.ToString(r.SecurityGroups[0].GroupId), true, nil
		}
		return "", false, nil
	})
	if err != nil {
		return err
	}
	ingress := []t.IpPermission{{IpProtocol: aw.String("-1"), UserIdGroupPairs: []t.UserIdGroupPair{{GroupId: &n.SG}}}}
	ranges := []t.IpRange{}
	for _, cidr := range c.Environment.OperatorCIDRs {
		ranges = append(ranges, t.IpRange{CidrIp: aw.String(cidr)})
	}
	ingress = append(ingress, t.IpPermission{IpProtocol: aw.String("tcp"), FromPort: aw.Int32(22), ToPort: aw.Int32(22), IpRanges: ranges})
	// Separate calls preserve reconciliation if one rule already exists.
	for _, rule := range ingress {
		_, err = a.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{GroupId: &n.GatewaySG, IpPermissions: []t.IpPermission{rule}})
		if err != nil && !apiCode(err, "InvalidPermission.Duplicate") {
			return err
		}
	}
	_, err = a.AuthorizeSecurityGroupEgress(ctx, &ec2.AuthorizeSecurityGroupEgressInput{GroupId: &n.GatewaySG, IpPermissions: []t.IpPermission{{IpProtocol: aw.String("-1"), IpRanges: []t.IpRange{{CidrIp: aw.String("0.0.0.0/0")}}}}})
	if apiCode(err, "InvalidPermission.Duplicate") {
		return nil
	}
	return err
}
