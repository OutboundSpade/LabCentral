package aws

import (
	"context"
	"fmt"
	"time"

	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

type routeAPI interface {
	DescribeRouteTables(context.Context, *ec2.DescribeRouteTablesInput, ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error)
	CreateRoute(context.Context, *ec2.CreateRouteInput, ...func(*ec2.Options)) (*ec2.CreateRouteOutput, error)
}

func waitRouteRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Tag-conditioned authorization can lag creation of a route table. Retry only
// explicit denials, within a bounded window, after checking its recorded owner.
// Transport failures remain uncertain and are reconciled by the next recovery.
func ensureInternetRoute(ctx context.Context, a routeAPI, owner, vpc, table, gateway string, wait func(context.Context, time.Duration) error) error {
	return ensureDefaultRoute(ctx, a, owner, vpc, table, gateway, false, wait)
}
func ensureENIRoute(ctx context.Context, a routeAPI, owner, vpc, table, eni string, wait func(context.Context, time.Duration) error) error {
	return ensureDefaultRoute(ctx, a, owner, vpc, table, eni, true, wait)
}
func ensureDefaultRoute(ctx context.Context, a routeAPI, owner, vpc, table, gateway string, eni bool, wait func(context.Context, time.Duration) error) error {
	if owner == "" || vpc == "" || table == "" || gateway == "" {
		return fmt.Errorf("incomplete recorded route identity")
	}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		out, err := a.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{RouteTableIds: []string{table}})
		if err != nil {
			return err
		}
		if len(out.RouteTables) != 1 || aw.ToString(out.RouteTables[0].RouteTableId) != table || aw.ToString(out.RouteTables[0].VpcId) != vpc || !owned(out.RouteTables[0].Tags, owner) {
			return fmt.Errorf("route table %s does not match recorded ownership and VPC", table)
		}
		for _, route := range out.RouteTables[0].Routes {
			if aw.ToString(route.DestinationCidrBlock) == "0.0.0.0/0" {
				if (!eni && aw.ToString(route.GatewayId) != gateway) || (eni && aw.ToString(route.NetworkInterfaceId) != gateway) {
					return fmt.Errorf("route table %s has a conflicting default route", table)
				}
				return nil
			}
		}
		in := &ec2.CreateRouteInput{RouteTableId: &table, DestinationCidrBlock: aw.String("0.0.0.0/0")}
		if eni {
			in.NetworkInterfaceId = &gateway
		} else {
			in.GatewayId = &gateway
		}
		_, err = a.CreateRoute(ctx, in)
		if err == nil {
			return nil
		}
		if attempt >= 5 || (!apiCode(err, "UnauthorizedOperation") && !apiCode(err, "RouteAlreadyExists")) {
			return err
		}
		if err = wait(ctx, time.Second<<attempt); err != nil {
			return err
		}
	}
}
