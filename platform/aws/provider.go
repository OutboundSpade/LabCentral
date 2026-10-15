// Package aws implements native EC2 lab resources. It does not open core state.
package aws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/guest"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	t "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	"os"
	"time"
)

type Provider struct {
	Abandon func(context.Context, lc.Command, lc.Observer) (lc.Result, error)
	Journal journal.Journal
	Build   func(context.Context, lc.Command, lc.Observer) (lc.Result, error)
}
type networkSegment struct {
	Name   string `json:"name"`
	CIDR   string `json:"cidr"`
	Subnet string `json:"subnet"`
	SG     string `json:"sg"`
	Route  string `json:"route"`
	Egress string `json:"egress"`
}
type networkState struct {
	VPC          string           `json:"vpc"`
	Subnet       string           `json:"subnet"`
	SG           string           `json:"sg"`
	Route        string           `json:"route"`
	Gateway      string           `json:"gateway"`
	Endpoint     string           `json:"endpoint,omitempty"`
	EdgeSubnet   string           `json:"edgeSubnet,omitempty"`
	EdgeRoute    string           `json:"edgeRoute,omitempty"`
	GatewaySG    string           `json:"gatewaySG,omitempty"`
	ManagementSG string           `json:"managementSG,omitempty"`
	Segments     []networkSegment `json:"segments,omitempty"`
}

func client(ctx context.Context, e lc.Environment) (*ec2.Client, *sts.Client, error) {
	opts := []func(*config.LoadOptions) error{config.WithRegion(e.AWS.Region)}
	if e.AWS.Profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(e.AWS.Profile))
	}
	c, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, nil, err
	}
	return ec2.NewFromConfig(c), sts.NewFromConfig(c), nil
}
func filters(owner, role string) []t.Filter {
	f := []t.Filter{{Name: aw.String("tag:labcentral:owner"), Values: []string{owner}}, {Name: aw.String("tag:labcentral:version"), Values: []string{"2"}}}
	if role != "" {
		f = append(f, t.Filter{Name: aw.String("tag:labcentral:role"), Values: []string{role}})
	}
	return f
}
func tags(owner, role string) []t.Tag {
	return []t.Tag{{Key: aw.String("labcentral:owner"), Value: aw.String(owner)}, {Key: aw.String("labcentral:role"), Value: aw.String(role)}, {Key: aw.String("labcentral:version"), Value: aw.String("2")}}
}
func specs(kind t.ResourceType, owner, role string) []t.TagSpecification {
	return []t.TagSpecification{{ResourceType: kind, Tags: tags(owner, role)}}
}
func owned(tags []t.Tag, owner string) bool {
	var o, v string
	for _, t := range tags {
		switch aw.ToString(t.Key) {
		case "labcentral:owner":
			o = aw.ToString(t.Value)
		case "labcentral:version":
			v = aw.ToString(t.Value)
		}
	}
	return owner != "" && o == owner && v == "2"
}
func apiCode(err error, code string) bool {
	var a smithy.APIError
	return errors.As(err, &a) && a.ErrorCode() == code
}
func (p *Provider) Execute(ctx context.Context, c lc.Command, o lc.Observer) (lc.Result, error) {
	if c.Action == "check" || c.Action == "image.build" || c.Action == "network.create" || c.Action == "machine.create" || c.Action == "gateway.create" {
		if err := lc.CheckCapabilities(c.Source.Lab, c.Environment.Platform); err != nil {
			return lc.Result{}, err
		}
		if err := lc.CheckEgress(c.Source.Lab, c.Environment); err != nil {
			return lc.Result{}, err
		}
		if c.Machine.Name != "" {
			if err := lc.CheckCapabilities(lc.Lab{Machines: map[string]lc.Machine{c.Machine.Name: c.Machine.Spec}}, c.Environment.Platform); err != nil {
				return lc.Result{}, err
			}
		}
		if err := lc.CheckCommandPlatformOptions(c); err != nil {
			return lc.Result{}, err
		}
	}

	if c.Environment.Platform != "aws" {
		return lc.Result{}, fmt.Errorf("wrong platform")
	}
	if c.Action == "gateway.create" {
		if err := lc.ValidateAWSGateway(c.Environment); err != nil {
			return lc.Result{}, err
		}
	}
	api, identity, err := client(ctx, c.Environment)
	if err != nil {
		return lc.Result{}, err
	}
	who, err := identity.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return lc.Result{}, err
	}
	if aw.ToString(who.Account) != c.Environment.AWS.Account {
		return lc.Result{}, fmt.Errorf("AWS account does not match environment")
	}
	switch c.Action {
	case "check":
		return p.check(ctx, api, c)
	case "network.create":
		return p.network(ctx, api, c)
	case "gateway.create":
		return p.gateway(ctx, api, c)
	case "machine.create":
		return p.machine(ctx, api, c)
	case "discover":
		if c.Prior.Kind == "gateway" {
			return p.findGateway(ctx, api, c)
		}
		if c.Machine.Name != "" {
			return p.findMachine(ctx, api, c)
		}
		return p.findNetwork(ctx, api, c)
	case "resource.destroy":
		return p.destroy(ctx, api, c)
	case "image.read":
		return p.imageRead(ctx, api, c)
	case "image.abandon":
		if p.Abandon == nil {
			return lc.Result{}, fmt.Errorf("builder unavailable")
		}
		return p.Abandon(ctx, c, o)
	case "image.build":
		if p.Build == nil {
			return lc.Result{}, fmt.Errorf("builder is unavailable")
		}
		return p.Build(ctx, c, o)
	default:
		return lc.Result{}, fmt.Errorf("unsupported AWS action %q", c.Action)
	}
}
func (p *Provider) check(ctx context.Context, a *ec2.Client, c lc.Command) (lc.Result, error) {
	if err := lc.CheckCommandPlatformOptions(c); err != nil {
		return lc.Result{}, err
	}
	e := c.Environment
	if err := checkDesktopRole(ctx, e); err != nil {
		return lc.Result{}, err
	}
	z, err := a.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{ZoneNames: []string{e.AWS.Zone}})
	if err != nil {
		return lc.Result{}, err
	}
	if len(z.AvailabilityZones) != 1 || z.AvailabilityZones[0].State != t.AvailabilityZoneStateAvailable {
		return lc.Result{}, fmt.Errorf("availability zone is unavailable")
	}
	for name, b := range e.Bases {
		if b.Identity != b.ID {
			return lc.Result{}, fmt.Errorf("AWS base identity must be its immutable AMI ID")
		}
		r, err := a.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{b.ID}})
		if err != nil {
			return lc.Result{}, err
		}
		if len(r.Images) != 1 || r.Images[0].State != t.ImageStateAvailable || r.Images[0].Architecture != t.ArchitectureValuesX8664 {
			return lc.Result{}, fmt.Errorf("base AMI must be available x86_64")
		}
		if err := checkAMIPlatform(r.Images[0], b.OS); err != nil {
			return lc.Result{}, fmt.Errorf("base %s: %w", name, err)
		}
	}
	gatewayType := e.AWS.Gateway.Effective().InstanceType
	names := []t.InstanceType{t.InstanceType(gatewayType)}
	for _, x := range e.AWS.Types {
		names = append(names, t.InstanceType(x.Name))
	}
	r, err := a.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{InstanceTypes: names})
	if err != nil {
		return lc.Result{}, err
	}
	actual := map[string]t.InstanceTypeInfo{}
	for _, v := range r.InstanceTypes {
		actual[string(v.InstanceType)] = v
	}
	if v, ok := actual[gatewayType]; !ok || v.ProcessorInfo == nil || !containsArchitecture(v.ProcessorInfo.SupportedArchitectures, t.ArchitectureTypeX8664) {
		return lc.Result{}, fmt.Errorf("gateway instance type %s must support x86_64", gatewayType)
	}
	for _, want := range e.AWS.Types {
		v, ok := actual[want.Name]
		if !ok || v.VCpuInfo == nil || v.MemoryInfo == nil || int(aw.ToInt32(v.VCpuInfo.DefaultVCpus)) != want.CPU || int(aw.ToInt64(v.MemoryInfo.SizeInMiB)) != want.MemoryMiB {
			return lc.Result{}, fmt.Errorf("instance type %s capacity does not match AWS", want.Name)
		}
	}
	limits := map[string]int{}
	for _, want := range e.AWS.Types {
		v := actual[want.Name]
		if v.NetworkInfo == nil || aw.ToInt32(v.NetworkInfo.MaximumNetworkInterfaces) < 1 {
			return lc.Result{}, fmt.Errorf("instance type %s ENI limit is unavailable", want.Name)
		}
		limits[want.Name] = int(aw.ToInt32(v.NetworkInfo.MaximumNetworkInterfaces))
	}
	if err = lc.CheckAWSNICLimits(c.Source.Lab, e, limits); err != nil {
		return lc.Result{}, err
	}
	return lc.Result{Message: "account, zone, base AMIs and instance types verified"}, nil
}
func (p *Provider) network(ctx context.Context, a *ec2.Client, c lc.Command) (lc.Result, error) {
	if err := lc.CheckCapabilities(c.Source.Lab, "aws"); err != nil {
		return lc.Result{}, err
	}
	planned, vpcCIDR, _, err := lc.AWSLayout(c.Source.Lab)
	if err != nil {
		return lc.Result{}, err
	}
	topology := lc.Plan{Source: c.Source, Environment: c.Environment}
	for _, name := range lc.SortedMachineNames(c.Source.Lab) {
		topology.Machines = append(topology.Machines, lc.PlannedMachine{Name: name, Spec: c.Source.Lab.Machines[name]})
	}
	if err = lc.PlanNetworks(&topology, nil); err != nil {
		return lc.Result{}, err
	}
	owner, key := c.Owner, c.Operation
	ns := networkState{}
	vpc, err := doEC2(ctx, p.Journal, key+"/vpc", func() (string, error) {
		r, e := a.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: &vpcCIDR, TagSpecifications: specs(t.ResourceTypeVpc, owner, "network")})
		if e != nil {
			return "", e
		}
		return aw.ToString(r.Vpc.VpcId), nil
	}, func() (string, bool, error) {
		r, e := a.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{Filters: filters(owner, "network")})
		if e != nil {
			return "", false, e
		}
		if len(r.Vpcs) > 1 {
			return "", false, fmt.Errorf("ambiguous owned VPC")
		}
		if len(r.Vpcs) == 1 {
			return aw.ToString(r.Vpcs[0].VpcId), true, nil
		}
		return "", false, nil
	})
	if err != nil {
		return lc.Result{}, err
	}
	ns.VPC = vpc
	if err = ec2.NewVpcAvailableWaiter(a).Wait(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{vpc}}, 2*time.Minute); err != nil {
		return lc.Result{}, err
	}
	igw, err := doEC2(ctx, p.Journal, key+"/gateway", func() (string, error) {
		r, e := a.CreateInternetGateway(ctx, &ec2.CreateInternetGatewayInput{TagSpecifications: specs(t.ResourceTypeInternetGateway, owner, "network")})
		if e != nil {
			return "", e
		}
		return aw.ToString(r.InternetGateway.InternetGatewayId), nil
	}, func() (string, bool, error) {
		r, e := a.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{Filters: filters(owner, "network")})
		if e != nil {
			return "", false, e
		}
		if len(r.InternetGateways) == 1 {
			return aw.ToString(r.InternetGateways[0].InternetGatewayId), true, nil
		}
		return "", false, nil
	})
	if err != nil {
		return lc.Result{}, err
	}
	ns.Gateway = igw
	_, err = a.AttachInternetGateway(ctx, &ec2.AttachInternetGatewayInput{InternetGatewayId: &igw, VpcId: &vpc})
	if err != nil && !apiCode(err, "Resource.AlreadyAssociated") {
		return lc.Result{}, err
	}

	tables := []string{}
	for j, n := range planned {
		seg, err := p.networkSegment(ctx, a, c, vpc, n, j)
		if err != nil {
			return lc.Result{}, err
		}
		ns.Segments = append(ns.Segments, seg)
		tables = append(tables, seg.Route)
	}
	ns.Subnet, ns.SG, ns.Route = ns.Segments[0].Subnet, ns.Segments[0].SG, ns.Segments[0].Route
	if err = p.edgeNetwork(ctx, a, c, &ns); err != nil {
		return lc.Result{}, err
	}
	management := ns.SG
	if len(ns.Segments) > 1 {
		management, err = doEC2(ctx, p.Journal, key+"/management-sg", func() (string, error) {
			r, e := a.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{VpcId: &vpc, GroupName: aw.String("labcentral-" + owner + "-management"), Description: aw.String("First NIC management"), TagSpecifications: networkSpecs(t.ResourceTypeSecurityGroup, owner, "management-sg")})
			if e != nil {
				return "", e
			}
			return aw.ToString(r.GroupId), nil
		}, func() (string, bool, error) { return findNetworkChild(ctx, a, owner, "management-sg") })
		if err != nil {
			return lc.Result{}, err
		}
		ns.ManagementSG = management
		if err = removeBroadEgress(ctx, a, management); err != nil {
			return lc.Result{}, err
		}
	}
	for _, port := range []int32{22, 8443} {
		if err = authorize(ctx, a, management, true, t.IpPermission{IpProtocol: aw.String("tcp"), FromPort: &port, ToPort: &port, UserIdGroupPairs: []t.UserIdGroupPair{{GroupId: &ns.GatewaySG}}}); err != nil {
			return lc.Result{}, err
		}
	}
	// Forwarded packets retain their original source. Guest and subnet routes
	// constrain them to the declared router paths; disconnected segments get no rule.
	for j, n := range topology.Networks {
		for _, r := range n.Routes {
			permission := t.IpPermission{IpProtocol: aw.String("-1"), IpRanges: []t.IpRange{{CidrIp: aw.String(r.CIDR)}}}
			if err = authorize(ctx, a, ns.Segments[j].SG, true, permission); err != nil {
				return lc.Result{}, err
			}
			if err = authorize(ctx, a, ns.Segments[j].SG, false, permission); err != nil {
				return lc.Result{}, err
			}
		}
	}
	// S3 gateway access is restricted to the DCV licensing bucket, not arbitrary S3.
	if c.Environment.AWS.DesktopProfile != "" {
		policy, _ := json.Marshal(struct {
			Version   string
			Statement []struct {
				Effect    string
				Principal string
				Action    string
				Resource  string
			}
		}{"2012-10-17", []struct {
			Effect    string
			Principal string
			Action    string
			Resource  string
		}{{"Allow", "*", "s3:GetObject", "arn:aws:s3:::dcv-license." + c.Environment.AWS.Region + "/*"}}})
		ep, err := doEC2(ctx, p.Journal, key+"/endpoint", func() (string, error) {
			r, e := a.CreateVpcEndpoint(ctx, &ec2.CreateVpcEndpointInput{VpcId: &vpc, VpcEndpointType: t.VpcEndpointTypeGateway, ServiceName: aw.String("com.amazonaws." + c.Environment.AWS.Region + ".s3"), RouteTableIds: tables, PolicyDocument: aw.String(string(policy)), ClientToken: aw.String(owner + "-s3"), TagSpecifications: specs(t.ResourceTypeVpcEndpoint, owner, "network")})
			if e != nil {
				return "", e
			}
			return aw.ToString(r.VpcEndpoint.VpcEndpointId), nil
		}, func() (string, bool, error) {
			r, e := a.DescribeVpcEndpoints(ctx, &ec2.DescribeVpcEndpointsInput{Filters: filters(owner, "network")})
			if e != nil {
				return "", false, e
			}
			if len(r.VpcEndpoints) == 1 {
				return aw.ToString(r.VpcEndpoints[0].VpcEndpointId), true, nil
			}
			return "", false, nil
		})
		if err != nil {
			return lc.Result{}, err
		}
		ns.Endpoint = ep
		pl, err := a.DescribePrefixLists(ctx, &ec2.DescribePrefixListsInput{Filters: []t.Filter{{Name: aw.String("prefix-list-name"), Values: []string{"com.amazonaws." + c.Environment.AWS.Region + ".s3"}}}})
		if err != nil {
			return lc.Result{}, err
		}
		if len(pl.PrefixLists) != 1 {
			return lc.Result{}, fmt.Errorf("S3 prefix list is unavailable")
		}
		for _, seg := range ns.Segments {
			sg := seg.SG
			_, err = a.AuthorizeSecurityGroupEgress(ctx, &ec2.AuthorizeSecurityGroupEgressInput{GroupId: &sg, IpPermissions: []t.IpPermission{{IpProtocol: aw.String("tcp"), FromPort: aw.Int32(443), ToPort: aw.Int32(443), PrefixListIds: []t.PrefixListId{{PrefixListId: pl.PrefixLists[0].PrefixListId}}}}})
			if err != nil && !apiCode(err, "InvalidPermission.Duplicate") {
				return lc.Result{}, err
			}
		}
	}

	b, _ := json.Marshal(ns)
	return lc.Result{Resource: lc.Resource{Kind: "network", Name: "private", ID: vpc, Owner: owner, Complete: true, Data: b}}, nil
}

func (p *Provider) networkSegment(ctx context.Context, a *ec2.Client, c lc.Command, vpc string, planned lc.PlannedNetwork, index int) (networkSegment, error) {
	owner, key := c.Owner, c.Operation
	subKey, routeKey, sgKey := "subnet", "route", "sg"
	subComponent, routeComponent, sgComponent := "lab-subnet", "lab-route", "lab-sg"
	suffix := ""
	if index > 0 {
		suffix = "-" + planned.Name
		subKey = "segment-" + planned.Name + "-subnet"
		routeKey = "segment-" + planned.Name + "-route"
		sgKey = "segment-" + planned.Name + "-sg"
		subComponent, routeComponent, sgComponent = subKey, routeKey, sgKey
	}
	subnet, err := doEC2(ctx, p.Journal, key+"/"+subKey, func() (string, error) {
		r, e := a.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: &vpc, AvailabilityZone: aw.String(c.Environment.AWS.Zone), CidrBlock: &planned.CIDR, TagSpecifications: networkSpecs(t.ResourceTypeSubnet, owner, subComponent)})
		if e != nil {
			return "", e
		}
		return aw.ToString(r.Subnet.SubnetId), nil
	}, func() (string, bool, error) {
		r, e := a.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{Filters: append(filters(owner, "network"), t.Filter{Name: aw.String("cidr-block"), Values: []string{"10.77.0.0/24"}})})
		if e != nil {
			return "", false, e
		}
		if len(r.Subnets) == 1 {
			return aw.ToString(r.Subnets[0].SubnetId), true, nil
		}
		return "", false, nil
	})
	if err != nil {
		return networkSegment{}, err
	}

	route, err := doEC2(ctx, p.Journal, key+"/"+routeKey, func() (string, error) {
		r, e := a.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{VpcId: &vpc, TagSpecifications: networkSpecs(t.ResourceTypeRouteTable, owner, routeComponent)})
		if e != nil {
			return "", e
		}
		return aw.ToString(r.RouteTable.RouteTableId), nil
	}, func() (string, bool, error) {
		r, e := a.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{Filters: componentFilters(owner, routeComponent)})
		if e != nil {
			return "", false, e
		}
		if len(r.RouteTables) == 1 {
			return aw.ToString(r.RouteTables[0].RouteTableId), true, nil
		}
		return "", false, nil
	})
	if err != nil {
		return networkSegment{}, err
	}

	rt, err := describeRouteTable(ctx, a, route, waitRouteRetry)
	if err != nil {
		return networkSegment{}, err
	}
	associated := false
	for _, r := range rt.RouteTables {
		for _, x := range r.Associations {
			associated = associated || aw.ToString(x.SubnetId) == subnet
		}
	}
	if !associated {
		_, err = a.AssociateRouteTable(ctx, &ec2.AssociateRouteTableInput{RouteTableId: &route, SubnetId: &subnet})
		if err != nil {
			return networkSegment{}, err
		}
	}
	sg, err := doEC2(ctx, p.Journal, key+"/"+sgKey, func() (string, error) {
		r, e := a.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{VpcId: &vpc, GroupName: aw.String("labcentral-" + owner + suffix), Description: aw.String("LabCentral v2 owned lab"), TagSpecifications: networkSpecs(t.ResourceTypeSecurityGroup, owner, sgComponent)})
		if e != nil {
			return "", e
		}
		return aw.ToString(r.GroupId), nil
	}, func() (string, bool, error) {
		r, e := a.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{Filters: componentFilters(owner, sgComponent)})
		if e != nil {
			return "", false, e
		}
		if len(r.SecurityGroups) == 1 {
			return aw.ToString(r.SecurityGroups[0].GroupId), true, nil
		}
		return "", false, nil
	})
	if err != nil {
		return networkSegment{}, err
	}

	// Remove AWS's default unrestricted egress before launching any instance.
	g, err := a.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{sg}})
	if err != nil {
		return networkSegment{}, err
	}
	for _, perm := range g.SecurityGroups[0].IpPermissionsEgress {
		broad := false
		for _, r := range perm.IpRanges {
			broad = broad || aw.ToString(r.CidrIp) == "0.0.0.0/0"
		}
		for _, r := range perm.Ipv6Ranges {
			broad = broad || aw.ToString(r.CidrIpv6) == "::/0"
		}
		if broad {
			if _, err = a.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{GroupId: &sg, IpPermissions: []t.IpPermission{perm}}); err != nil {
				return networkSegment{}, err
			}
		}
	}
	self := t.IpPermission{IpProtocol: aw.String("-1"), UserIdGroupPairs: []t.UserIdGroupPair{{GroupId: &sg}}}
	_, err = a.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{GroupId: &sg, IpPermissions: []t.IpPermission{self}})
	if err != nil && !apiCode(err, "InvalidPermission.Duplicate") {
		return networkSegment{}, err
	}
	_, err = a.AuthorizeSecurityGroupEgress(ctx, &ec2.AuthorizeSecurityGroupEgressInput{GroupId: &sg, IpPermissions: []t.IpPermission{self}})
	if err != nil && !apiCode(err, "InvalidPermission.Duplicate") {
		return networkSegment{}, err
	}

	if planned.Egress == "internet" {
		if err = authorize(ctx, a, sg, false, t.IpPermission{IpProtocol: aw.String("-1"), IpRanges: []t.IpRange{{CidrIp: aw.String("0.0.0.0/0")}}}); err != nil {
			return networkSegment{}, err
		}
	}
	return networkSegment{Name: planned.Name, CIDR: planned.CIDR, Subnet: subnet, Route: route, SG: sg, Egress: planned.Egress}, nil
}

func removeBroadEgress(ctx context.Context, a *ec2.Client, sg string) error {
	g, err := a.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{sg}})
	if err != nil {
		return err
	}
	if len(g.SecurityGroups) != 1 {
		return fmt.Errorf("security group unavailable")
	}
	for _, perm := range g.SecurityGroups[0].IpPermissionsEgress {
		broad := false
		for _, r := range perm.IpRanges {
			broad = broad || aw.ToString(r.CidrIp) == "0.0.0.0/0"
		}
		for _, r := range perm.Ipv6Ranges {
			broad = broad || aw.ToString(r.CidrIpv6) == "::/0"
		}
		if broad {
			if _, err = a.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{GroupId: &sg, IpPermissions: []t.IpPermission{perm}}); err != nil && !apiCode(err, "InvalidPermission.NotFound") {
				return err
			}
		}
	}
	return nil
}
func authorize(ctx context.Context, a *ec2.Client, sg string, ingress bool, permission t.IpPermission) error {
	var err error
	if ingress {
		_, err = a.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{GroupId: &sg, IpPermissions: []t.IpPermission{permission}})
	} else {
		_, err = a.AuthorizeSecurityGroupEgress(ctx, &ec2.AuthorizeSecurityGroupEgressInput{GroupId: &sg, IpPermissions: []t.IpPermission{permission}})
	}
	if apiCode(err, "InvalidPermission.Duplicate") {
		return nil
	}
	return err
}
func (p *Provider) machine(ctx context.Context, a *ec2.Client, c lc.Command) (lc.Result, error) {
	if err := lc.CheckCommandPlatformOptions(c); err != nil {
		return lc.Result{}, err
	}
	var ns networkState
	if err := json.Unmarshal(c.Network.Data, &ns); err != nil {
		return lc.Result{}, err
	}
	dialer, err := lc.MachineDialer(c.Gateway, c.ClientPrivateKey)
	if err != nil {
		return lc.Result{}, err
	}
	defer dialer.Close()
	m := c.Machine
	role := m.Name
	if c.Gateway.Kind != "" {
		role = "machine:" + m.Name
	}
	if c.Recovery && len(m.NICs) == 0 { // Pre-Phase-3 creates have no native instance checkpoint.
		r, err := p.findMachine(ctx, a, c)
		if err != nil {
			return r, err
		}
		if err = p.routerNetworking(ctx, a, c, ns); err != nil {
			return r, err
		}
		if err = waitMachine(ctx, dialer, r.Resource.Address, c); err != nil {
			return r, err
		}
		return r, nil
	}
	im, err := a.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{m.NativeImage}})
	if err != nil {
		return lc.Result{}, err
	}
	if len(im.Images) != 1 || im.Images[0].RootDeviceName == nil {
		return lc.Result{}, fmt.Errorf("AMI root device unavailable")
	}
	if err = checkAMIPlatform(im.Images[0], m.OS); err != nil {
		return lc.Result{}, err
	}
	ud, err := guest.CloudInit(m)
	if m.OS == "windows" {
		if c.Gateway.PrivateAddress == "" {
			return lc.Result{}, fmt.Errorf("Windows requires a private management gateway")
		}
		ud, err = guest.WindowsUserData(m, []string{c.Gateway.PrivateAddress + "/32"})
	}
	if err != nil {
		return lc.Result{}, err
	}
	in := &ec2.RunInstancesInput{ImageId: &m.NativeImage, InstanceType: t.InstanceType(m.InstanceType), MinCount: aw.Int32(1), MaxCount: aw.Int32(1), ClientToken: aw.String(c.Owner + "-" + m.Name), UserData: aw.String(base64.StdEncoding.EncodeToString([]byte(ud))), MetadataOptions: &t.InstanceMetadataOptionsRequest{HttpTokens: t.HttpTokensStateRequired, HttpPutResponseHopLimit: aw.Int32(1)}, NetworkInterfaces: []t.InstanceNetworkInterfaceSpecification{{DeviceIndex: aw.Int32(0), DeleteOnTermination: aw.Bool(true), SubnetId: &ns.Subnet, Groups: []string{ns.SG}, AssociatePublicIpAddress: aw.Bool(false), PrivateIpAddress: &m.Address}}, BlockDeviceMappings: []t.BlockDeviceMapping{{DeviceName: im.Images[0].RootDeviceName, Ebs: &t.EbsBlockDevice{VolumeSize: aw.Int32(int32(m.DiskGiB)), VolumeType: t.VolumeTypeGp3, Encrypted: aw.Bool(true), DeleteOnTermination: aw.Bool(true)}}}, TagSpecifications: append(specs(t.ResourceTypeInstance, c.Owner, role), specs(t.ResourceTypeVolume, c.Owner, role)...)}
	if len(m.NICs) > 0 {
		in.NetworkInterfaces = nil
		for j, nic := range m.NICs {
			var seg networkSegment
			found := false
			for _, candidate := range ns.Segments {
				if candidate.Name == nic.Network {
					seg = candidate
					found = true
					break
				}
			}
			if !found {
				return lc.Result{}, fmt.Errorf("planned network %s is unavailable", nic.Network)
			}
			groups := []string{seg.SG}
			if j == 0 && ns.ManagementSG != "" {
				groups = append(groups, ns.ManagementSG)
			}
			in.NetworkInterfaces = append(in.NetworkInterfaces, t.InstanceNetworkInterfaceSpecification{DeviceIndex: aw.Int32(int32(j)), DeleteOnTermination: aw.Bool(true), SubnetId: aw.String(seg.Subnet), Groups: groups, AssociatePublicIpAddress: aw.Bool(false), PrivateIpAddress: aw.String(nic.Address)})
		}
	}
	if o := m.Spec.Platform.AWS; o != nil {
		if o.Volume != nil {
			ebs := in.BlockDeviceMappings[0].Ebs
			if o.Volume.IOPS != nil {
				ebs.Iops = aw.Int32(int32(*o.Volume.IOPS))
			}
			if o.Volume.Throughput != nil {
				ebs.Throughput = aw.Int32(int32(*o.Volume.Throughput))
			}
		}
		if o.CPUCredits != "" {
			in.CreditSpecification = &t.CreditSpecificationRequest{CpuCredits: aw.String(o.CPUCredits)}
		}
	}

	if m.Spec.Forwarding {
		in.TagSpecifications = append(in.TagSpecifications, specs(t.ResourceTypeNetworkInterface, c.Owner, role)...)
	}
	if lc.Contains(m.Spec.Access, "desktop") {
		in.IamInstanceProfile = &t.IamInstanceProfileSpecification{Name: aw.String(c.Environment.AWS.DesktopProfile)}
	}
	instance, err := doEC2(ctx, p.Journal, c.Operation+"/instance", func() (string, error) {
		out, err := a.RunInstances(ctx, in)
		if err != nil {
			return "", err
		}
		if len(out.Instances) != 1 {
			return "", fmt.Errorf("unexpected instance count")
		}
		return aw.ToString(out.Instances[0].InstanceId), nil
	}, func() (string, bool, error) {
		r, err := p.findMachine(ctx, a, c)
		if err != nil {
			return "", false, err
		}
		return r.Resource.ID, r.Resource.ID != "", nil
	})
	if err != nil {
		return lc.Result{}, err
	}
	if err = ec2.NewInstanceRunningWaiter(a).Wait(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instance}}, 10*time.Minute); err != nil {
		return lc.Result{}, err
	}
	r, err := p.findMachine(ctx, a, c)
	if err != nil {
		return r, err
	}
	if err = p.routerNetworking(ctx, a, c, ns); err != nil {
		return r, err
	}
	if err = waitMachine(ctx, dialer, r.Resource.Address, c); err != nil {
		return r, err
	}
	return r, nil
}
func (p *Provider) findMachine(ctx context.Context, a *ec2.Client, c lc.Command) (lc.Result, error) {
	role := c.Machine.Name
	if c.Gateway.Kind != "" {
		role = "machine:" + role
	}
	f := filters(c.Owner, role)
	f = append(f, t.Filter{Name: aw.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}})
	r, err := a.DescribeInstances(ctx, &ec2.DescribeInstancesInput{Filters: f})
	if err != nil {
		return lc.Result{}, err
	}
	var instances []t.Instance
	for _, rs := range r.Reservations {
		instances = append(instances, rs.Instances...)
	}
	if len(instances) != 1 {
		return lc.Result{}, fmt.Errorf("recovery_required: expected one owned instance, found %d", len(instances))
	}
	i := instances[0]
	if !owned(i.Tags, c.Owner) {
		return lc.Result{}, fmt.Errorf("ownership_mismatch: machine")
	}
	resource := lc.Resource{Kind: "machine", Name: c.Machine.Name, ID: aw.ToString(i.InstanceId), Owner: c.Owner, Address: aw.ToString(i.PublicIpAddress), PrivateAddress: aw.ToString(i.PrivateIpAddress), Complete: true}
	if c.Gateway.Kind != "" {
		resource.Address = resource.PrivateAddress
		resource.Data, _ = json.Marshal(map[string]string{"role": role})
	}
	for _, eni := range i.NetworkInterfaces {
		resource.Children = append(resource.Children, lc.NativeResource{Kind: "eni", ID: aw.ToString(eni.NetworkInterfaceId)})
	}
	if len(c.Machine.NICs) > 0 {
		if len(i.NetworkInterfaces) != len(c.Machine.NICs) {
			return lc.Result{}, fmt.Errorf("owned instance NIC count differs from plan")
		}
		var ns networkState
		if err = json.Unmarshal(c.Network.Data, &ns); err != nil {
			return lc.Result{}, err
		}
		seen := map[int]bool{}
		for _, eni := range i.NetworkInterfaces {
			if eni.Attachment == nil {
				return lc.Result{}, fmt.Errorf("owned instance ENI attachment is unknown")
			}
			index := int(aw.ToInt32(eni.Attachment.DeviceIndex))
			if seen[index] {
				return lc.Result{}, fmt.Errorf("owned instance repeats an ENI device index")
			}
			seen[index] = true

			if index >= len(c.Machine.NICs) || index < 0 || aw.ToString(eni.PrivateIpAddress) != c.Machine.NICs[index].Address || aw.ToString(eni.VpcId) != networkVPC(c.Network) {
				return lc.Result{}, fmt.Errorf("owned instance ENI identity differs from plan")
			}
			expectedSubnet := ""
			for _, seg := range ns.Segments {
				if seg.Name == c.Machine.NICs[index].Network {
					expectedSubnet = seg.Subnet
				}
			}
			if expectedSubnet == "" || aw.ToString(eni.SubnetId) != expectedSubnet {
				return lc.Result{}, fmt.Errorf("owned instance ENI subnet differs from plan")
			}

		}
	}
	for _, mapping := range i.BlockDeviceMappings {
		if mapping.Ebs != nil {
			resource.Children = append(resource.Children, lc.NativeResource{Kind: "volume", ID: aw.ToString(mapping.Ebs.VolumeId)})
		}
	}
	return lc.Result{Resource: resource}, nil
}
func (p *Provider) findNetwork(ctx context.Context, a *ec2.Client, c lc.Command) (lc.Result, error) {
	r, err := a.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{Filters: filters(c.Owner, "network")})
	if err != nil {
		return lc.Result{}, err
	}
	if len(r.Vpcs) != 1 {
		return lc.Result{}, fmt.Errorf("recovery_required: owned VPC outcome remains uncertain")
	}
	planned, _, _, err := lc.AWSLayout(c.Source.Lab)
	if err != nil {
		return lc.Result{}, err
	}
	n := networkState{VPC: aw.ToString(r.Vpcs[0].VpcId)}
	for _, entry := range []struct {
		key    string
		target *string
	}{{"subnet", &n.Subnet}, {"gateway", &n.Gateway}, {"route", &n.Route}, {"sg", &n.SG}, {"endpoint", &n.Endpoint}, {"edge-subnet", &n.EdgeSubnet}, {"edge-route", &n.EdgeRoute}, {"gateway-sg", &n.GatewaySG}} {
		key := c.Operation + "/" + entry.key
		if err := p.Journal.Load(key, entry.target); err != nil && !os.IsNotExist(err) {
			value, err := doEC2(ctx, p.Journal, key, func() (string, error) { return "", fmt.Errorf("cannot replay interrupted discovery") }, func() (string, bool, error) { return findNetworkChild(ctx, a, c.Owner, entry.key, planned[0].CIDR) })
			if err != nil {
				return lc.Result{}, fmt.Errorf("network child intent %s unresolved: %w", entry.key, err)
			}
			*entry.target = value
		}
	}
	loadChild := func(key, kind string) (string, error) {
		value := ""
		err := p.Journal.Load(c.Operation+"/"+key, &value)
		if os.IsNotExist(err) {
			return "", nil
		}
		if err == nil {
			return value, nil
		}
		value, err = doEC2(ctx, p.Journal, c.Operation+"/"+key, func() (string, error) { return "", fmt.Errorf("cannot replay interrupted discovery") }, func() (string, bool, error) { return findNetworkChild(ctx, a, c.Owner, kind) })
		return value, err
	}
	n.ManagementSG, err = loadChild("management-sg", "management-sg")
	if err != nil {
		return lc.Result{}, err
	}
	for j, net := range planned {
		seg := networkSegment{Name: net.Name, CIDR: net.CIDR, Egress: net.Egress}
		if j == 0 {
			seg.Subnet, seg.Route, seg.SG = n.Subnet, n.Route, n.SG
		} else {
			for _, entry := range []struct {
				suffix string
				target *string
			}{{"subnet", &seg.Subnet}, {"route", &seg.Route}, {"sg", &seg.SG}} {
				key := "segment-" + net.Name + "-" + entry.suffix
				*entry.target, err = loadChild(key, key)
				if err != nil {
					return lc.Result{}, err
				}
			}
		}
		n.Segments = append(n.Segments, seg)
	}
	data, _ := json.Marshal(n)
	return lc.Result{Resource: lc.Resource{Kind: "network", Name: "private", ID: n.VPC, Owner: c.Owner, Data: data}}, nil
}
func (p *Provider) imageRead(ctx context.Context, a *ec2.Client, c lc.Command) (lc.Result, error) {
	r, err := a.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{c.Prior.ID}})
	if apiCode(err, "InvalidAMIID.NotFound") {
		return lc.Result{Absent: true}, nil
	}
	if err != nil {
		return lc.Result{}, err
	}
	if len(r.Images) == 0 {
		return lc.Result{Absent: true}, nil
	}
	if !owned(r.Images[0].Tags, c.Owner) {
		return lc.Result{}, fmt.Errorf("ownership_mismatch: AMI")
	}
	res := c.Prior
	res.Complete = r.Images[0].State == t.ImageStateAvailable
	return lc.Result{Resource: res}, nil
}
func (p *Provider) destroy(ctx context.Context, a *ec2.Client, c lc.Command) (lc.Result, error) {
	r := c.Prior
	if r.Owner != c.Owner || c.Owner == "" {
		return lc.Result{}, fmt.Errorf("ownership_mismatch")
	}
	switch r.Kind {
	case "machine", "gateway":
		v, err := a.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{r.ID}})
		if apiCode(err, "InvalidInstanceID.NotFound") {
			err = cleanupVolumes(ctx, a, c)
			return lc.Result{Absent: err == nil}, err
		}
		if err != nil {
			return lc.Result{}, err
		}
		for _, rs := range v.Reservations {
			for _, i := range rs.Instances {
				if !owned(i.Tags, c.Owner) {
					return lc.Result{}, fmt.Errorf("ownership_mismatch: instance")
				}
				if i.State != nil && i.State.Name == t.InstanceStateNameTerminated {
					err = cleanupVolumes(ctx, a, c)
					return lc.Result{Absent: err == nil}, err
				}
			}
		}
		if _, err = a.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{r.ID}}); err != nil {
			return lc.Result{}, err
		}
		err = ec2.NewInstanceTerminatedWaiter(a, func(o *ec2.InstanceTerminatedWaiterOptions) {
			o.MinDelay = 2 * time.Second
			o.MaxDelay = 5 * time.Second
		}).Wait(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{r.ID}}, 10*time.Minute)
		if err == nil {
			err = cleanupVolumes(ctx, a, c)
		}
		return lc.Result{Absent: err == nil}, err
	case "image":
		v, err := a.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{r.ID}})
		if err != nil && !apiCode(err, "InvalidAMIID.NotFound") {
			return lc.Result{}, err
		}
		if v != nil && len(v.Images) > 0 {
			if !owned(v.Images[0].Tags, c.Owner) {
				return lc.Result{}, fmt.Errorf("ownership_mismatch: image")
			}
			if _, err = a.DeregisterImage(ctx, &ec2.DeregisterImageInput{ImageId: &r.ID}); err != nil {
				return lc.Result{}, err
			}
		}
		err = cleanupSnapshots(ctx, a, c)
		if err != nil {
			return lc.Result{}, err
		}
		remaining, err := a.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{r.ID}})
		if apiCode(err, "InvalidAMIID.NotFound") {
			return lc.Result{Absent: true}, nil
		}
		if err != nil {
			return lc.Result{}, err
		}
		if len(remaining.Images) > 0 {
			return lc.Result{}, fmt.Errorf("AMI deletion not yet confirmed: %s", r.ID)
		}
		return lc.Result{Absent: true}, nil
	case "network":
		return p.destroyNetwork(ctx, a, c)
	}
	return lc.Result{}, fmt.Errorf("unknown resource kind")
}
func (p *Provider) destroyNetwork(ctx context.Context, a *ec2.Client, c lc.Command) (lc.Result, error) {
	owner := c.Owner
	if err := checkRecordedNetwork(ctx, a, c); err != nil {
		return lc.Result{}, err
	}
	v, err := a.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{c.Prior.ID}})
	if err != nil && !apiCode(err, "InvalidVpcID.NotFound") {
		return lc.Result{}, err
	}
	if err == nil && (len(v.Vpcs) != 1 || !owned(v.Vpcs[0].Tags, owner)) {
		return lc.Result{}, fmt.Errorf("ownership_mismatch: VPC")
	}
	ep, err := a.DescribeVpcEndpoints(ctx, &ec2.DescribeVpcEndpointsInput{Filters: filters(owner, "network")})
	if err != nil {
		return lc.Result{}, err
	}
	for _, x := range ep.VpcEndpoints {
		if aw.ToString(x.VpcId) != c.Prior.ID {
			return lc.Result{}, fmt.Errorf("endpoint target mismatch")
		}
		r, e := a.DeleteVpcEndpoints(ctx, &ec2.DeleteVpcEndpointsInput{VpcEndpointIds: []string{aw.ToString(x.VpcEndpointId)}})
		if e != nil {
			return lc.Result{}, e
		}
		if len(r.Unsuccessful) > 0 {
			return lc.Result{}, fmt.Errorf("endpoint deletion incomplete")
		}
	}
	// Routes to router and gateway ENIs (including routes more specific than VPC-local
	// routing) reference subnets and must go before those subnets can be deleted. Only
	// routes in owned route tables of this VPC are removed; local and IGW routes stay.
	tables, err := a.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{Filters: filters(owner, "network")})
	if err != nil {
		return lc.Result{}, err
	}
	for _, x := range tables.RouteTables {
		if aw.ToString(x.VpcId) != c.Prior.ID {
			return lc.Result{}, fmt.Errorf("route target mismatch")
		}
		for _, route := range x.Routes {
			if route.DestinationCidrBlock == nil || aw.ToString(route.NetworkInterfaceId) == "" {
				continue
			}
			_, err = a.DeleteRoute(ctx, &ec2.DeleteRouteInput{RouteTableId: x.RouteTableId, DestinationCidrBlock: route.DestinationCidrBlock})
			if err != nil && !apiCode(err, "InvalidRoute.NotFound") {
				return lc.Result{}, err
			}
		}
	}
	sub, err := a.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{Filters: filters(owner, "network")})
	if err != nil {
		return lc.Result{}, err
	}
	for _, x := range sub.Subnets {
		if aw.ToString(x.VpcId) != c.Prior.ID {
			return lc.Result{}, fmt.Errorf("subnet target mismatch")
		}
		if _, err = a.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: x.SubnetId}); err != nil {
			return lc.Result{}, err
		}
	}
	rt, err := a.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{Filters: filters(owner, "network")})
	if err != nil {
		return lc.Result{}, err
	}
	for _, x := range rt.RouteTables {
		if aw.ToString(x.VpcId) != c.Prior.ID {
			return lc.Result{}, fmt.Errorf("route target mismatch")
		}
		if _, err = a.DeleteRouteTable(ctx, &ec2.DeleteRouteTableInput{RouteTableId: x.RouteTableId}); err != nil {
			return lc.Result{}, err
		}
	}
	groups, err := a.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{Filters: filters(owner, "network")})
	if err != nil {
		return lc.Result{}, err
	}
	// Validate every group before removing cross-group references. The default
	// VPC group is unowned and is never touched.
	for _, x := range groups.SecurityGroups {
		if aw.ToString(x.VpcId) != c.Prior.ID || !owned(x.Tags, c.Owner) {
			return lc.Result{}, fmt.Errorf("security group ownership/target mismatch")
		}
	}
	for _, x := range groups.SecurityGroups {
		if len(x.IpPermissions) > 0 {
			if _, err = a.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{GroupId: x.GroupId, IpPermissions: x.IpPermissions}); err != nil && !apiCode(err, "InvalidPermission.NotFound") {
				return lc.Result{}, err
			}
		}
		if len(x.IpPermissionsEgress) > 0 {
			if _, err = a.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{GroupId: x.GroupId, IpPermissions: x.IpPermissionsEgress}); err != nil && !apiCode(err, "InvalidPermission.NotFound") {
				return lc.Result{}, err
			}
		}
	}
	for _, x := range groups.SecurityGroups {
		if _, err = a.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: x.GroupId}); err != nil {
			return lc.Result{}, err
		}
	}
	gateways, err := a.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{Filters: filters(owner, "network")})
	if err != nil {
		return lc.Result{}, err
	}
	for _, x := range gateways.InternetGateways {
		for _, at := range x.Attachments {
			if aw.ToString(at.VpcId) != c.Prior.ID {
				return lc.Result{}, fmt.Errorf("gateway target mismatch")
			}
		}
		if len(x.Attachments) > 0 {
			if _, err = a.DetachInternetGateway(ctx, &ec2.DetachInternetGatewayInput{InternetGatewayId: x.InternetGatewayId, VpcId: &c.Prior.ID}); err != nil {
				return lc.Result{}, err
			}
		}
		if _, err = a.DeleteInternetGateway(ctx, &ec2.DeleteInternetGatewayInput{InternetGatewayId: x.InternetGatewayId}); err != nil {
			return lc.Result{}, err
		}
	}
	_, err = a.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: &c.Prior.ID})
	if err != nil && !apiCode(err, "InvalidVpcID.NotFound") {
		return lc.Result{}, err
	}
	remaining, err := a.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{c.Prior.ID}})
	if apiCode(err, "InvalidVpcID.NotFound") {
		return lc.Result{Absent: true}, nil
	}
	if err != nil {
		return lc.Result{}, err
	}
	if len(remaining.Vpcs) > 0 {
		return lc.Result{}, fmt.Errorf("VPC deletion not yet confirmed")
	}
	return lc.Result{Absent: true}, nil
}

// FindBuild discovers a native AMI by its exact build ownership tags.
func (p *Provider) FindBuild(ctx context.Context, c lc.Command) (lc.Resource, bool, error) {
	a, _, err := client(ctx, c.Environment)
	if err != nil {
		return lc.Resource{}, false, err
	}
	r, err := a.DescribeImages(ctx, &ec2.DescribeImagesInput{Owners: []string{"self"}, Filters: []t.Filter{{Name: aw.String("name"), Values: []string{"labcentral-" + c.Owner}}}})
	if err != nil {
		return lc.Resource{}, false, err
	}
	if len(r.Images) == 0 {
		return lc.Resource{}, false, nil
	}
	if len(r.Images) != 1 {
		return lc.Resource{}, false, fmt.Errorf("ambiguous build AMIs")
	}
	im := r.Images[0]
	if !owned(im.Tags, c.Owner) {
		return lc.Resource{}, false, fmt.Errorf("ownership_mismatch: build AMI")
	}
	if im.State != t.ImageStateAvailable {
		return lc.Resource{}, false, fmt.Errorf("native AMI is not available yet")
	}
	out := lc.Resource{Kind: "image", Name: c.Image.Name, ID: aw.ToString(im.ImageId), Owner: c.Owner}
	for _, b := range im.BlockDeviceMappings {
		if b.Ebs != nil {
			out.Children = append(out.Children, lc.NativeResource{Kind: "snapshot", ID: aw.ToString(b.Ebs.SnapshotId)})
		}
	}
	return out, true, nil
}

func (p *Provider) PrepareBuild(ctx context.Context, c lc.Command, publicKey string) error {
	if c.Image.Base.OS == "windows" {
		return nil
	} // The fixed EC2Launch bootstrap installs this key directly.

	a, _, err := client(ctx, c.Environment)
	if err != nil {
		return err
	}
	name := "labcentral-" + c.Owner
	_, err = doEC2(ctx, p.Journal, c.Operation+"/build-key", func() (string, error) {
		r, e := a.ImportKeyPair(ctx, &ec2.ImportKeyPairInput{KeyName: &name, PublicKeyMaterial: []byte(publicKey), TagSpecifications: specs(t.ResourceTypeKeyPair, c.Owner, "build-key")})
		if e != nil {
			return "", e
		}
		return aw.ToString(r.KeyPairId), nil
	}, func() (string, bool, error) {
		r, e := a.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{KeyNames: []string{name}})
		if apiCode(e, "InvalidKeyPair.NotFound") {
			return "", false, nil
		}
		if e != nil {
			return "", false, e
		}
		if len(r.KeyPairs) != 1 || !owned(r.KeyPairs[0].Tags, c.Owner) {
			return "", false, fmt.Errorf("ownership_mismatch: build keypair")
		}
		return aw.ToString(r.KeyPairs[0].KeyPairId), true, nil
	})
	return err
}
func (p *Provider) CleanupBuild(ctx context.Context, c lc.Command) error {
	a, _, err := client(ctx, c.Environment)
	if err != nil {
		return err
	}
	return p.cleanupBuild(ctx, a, c)
}

func (p *Provider) cleanupBuild(ctx context.Context, a *ec2.Client, c lc.Command) error {
	instanceFilters := []t.Filter{{Name: aw.String("key-name"), Values: []string{"labcentral-" + c.Owner}}}
	if c.Image.Base.OS == "windows" {
		instanceFilters = filters(c.Owner, c.Image.Name)
	}
	r, err := a.DescribeInstances(ctx, &ec2.DescribeInstancesInput{Filters: instanceFilters})
	if err != nil {
		return err
	}
	for _, rs := range r.Reservations {
		for _, i := range rs.Instances {
			if !owned(i.Tags, c.Owner) {
				return fmt.Errorf("ownership_mismatch: build instance")
			}
			if i.State != nil && i.State.Name == t.InstanceStateNameTerminated {
				continue
			}
			id := aw.ToString(i.InstanceId)
			if _, err = a.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{id}}); err != nil {
				return err
			}
			if err = ec2.NewInstanceTerminatedWaiter(a, func(o *ec2.InstanceTerminatedWaiterOptions) {
				o.MinDelay = 2 * time.Second
				o.MaxDelay = 5 * time.Second
			}).Wait(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}}, 10*time.Minute); err != nil {
				return err
			}
		}
	}
	vol, err := a.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{Filters: filters(c.Owner, c.Image.Name)})
	if err != nil {
		return err
	}
	for _, v := range vol.Volumes {
		if !owned(v.Tags, c.Owner) || len(v.Attachments) > 0 {
			return fmt.Errorf("build volume ownership or attachment is unresolved")
		}
		if _, err = a.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: v.VolumeId}); err != nil {
			return err
		}
	}
	if c.Image.Base.OS == "windows" {
		return nil
	}
	keys, err := a.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{KeyNames: []string{"labcentral-" + c.Owner}})
	if apiCode(err, "InvalidKeyPair.NotFound") {
		return nil
	}
	if err != nil {
		return err
	}
	for _, k := range keys.KeyPairs {
		if !owned(k.Tags, c.Owner) {
			return fmt.Errorf("ownership_mismatch: build keypair")
		}
		if _, err = a.DeleteKeyPair(ctx, &ec2.DeleteKeyPairInput{KeyPairId: k.KeyPairId}); err != nil {
			return err
		}
	}
	return nil
}

func containsArchitecture(values []t.ArchitectureType, want t.ArchitectureType) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
