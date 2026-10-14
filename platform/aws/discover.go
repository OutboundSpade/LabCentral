package aws

import (
	"context"
	"fmt"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	t "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// Reconcile an interrupted child intent without replaying the mutation. A
// temporarily empty tag query never proves an uncertain create did not happen.
func findNetworkChild(ctx context.Context, a *ec2.Client, owner, kind string) (string, bool, error) {
	f := filters(owner, "network")
	switch kind {
	case "subnet":
		f = append(f, t.Filter{Name: aw.String("cidr-block"), Values: []string{"10.77.0.0/24"}})
	case "edge-subnet":
		f = componentFilters(owner, "edge-subnet")
	case "route":
		f = componentFilters(owner, "lab-route")
	case "edge-route":
		f = componentFilters(owner, "edge-route")
	case "sg":
		f = componentFilters(owner, "lab-sg")
	case "gateway-sg":
		f = componentFilters(owner, "edge-sg")
	}
	ids := []string{}
	switch kind {
	case "subnet", "edge-subnet":
		out, err := a.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{Filters: f})
		if err != nil {
			return "", false, err
		}
		for _, v := range out.Subnets {
			if !owned(v.Tags, owner) {
				return "", false, fmt.Errorf("subnet ownership mismatch")
			}
			ids = append(ids, aw.ToString(v.SubnetId))
		}
	case "route", "edge-route":
		out, err := a.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{Filters: f})
		if err != nil {
			return "", false, err
		}
		for _, v := range out.RouteTables {
			if !owned(v.Tags, owner) {
				return "", false, fmt.Errorf("route ownership mismatch")
			}
			ids = append(ids, aw.ToString(v.RouteTableId))
		}
	case "sg", "gateway-sg":
		out, err := a.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{Filters: f})
		if err != nil {
			return "", false, err
		}
		for _, v := range out.SecurityGroups {
			if !owned(v.Tags, owner) {
				return "", false, fmt.Errorf("group ownership mismatch")
			}
			ids = append(ids, aw.ToString(v.GroupId))
		}
	case "gateway":
		out, err := a.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{Filters: f})
		if err != nil {
			return "", false, err
		}
		for _, v := range out.InternetGateways {
			if !owned(v.Tags, owner) {
				return "", false, fmt.Errorf("IGW ownership mismatch")
			}
			ids = append(ids, aw.ToString(v.InternetGatewayId))
		}
	case "endpoint":
		out, err := a.DescribeVpcEndpoints(ctx, &ec2.DescribeVpcEndpointsInput{Filters: f})
		if err != nil {
			return "", false, err
		}
		for _, v := range out.VpcEndpoints {
			if !owned(v.Tags, owner) {
				return "", false, fmt.Errorf("endpoint ownership mismatch")
			}
			ids = append(ids, aw.ToString(v.VpcEndpointId))
		}
	default:
		return "", false, fmt.Errorf("unknown network child")
	}
	if len(ids) > 1 {
		return "", false, fmt.Errorf("ambiguous owned %s", kind)
	}
	if len(ids) == 1 && ids[0] != "" {
		return ids[0], true, nil
	}
	return "", false, nil
}
