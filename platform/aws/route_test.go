package aws

import (
	"context"
	"errors"
	"testing"
	"time"

	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

type routeFixture struct {
	table  types.RouteTable
	calls  int
	create func(int) error
}

func (f *routeFixture) DescribeRouteTables(context.Context, *ec2.DescribeRouteTablesInput, ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	return &ec2.DescribeRouteTablesOutput{RouteTables: []types.RouteTable{f.table}}, nil
}
func (f *routeFixture) CreateRoute(_ context.Context, in *ec2.CreateRouteInput, _ ...func(*ec2.Options)) (*ec2.CreateRouteOutput, error) {
	if aw.ToString(in.RouteTableId) != "rtb-owned" || aw.ToString(in.GatewayId) != "igw-owned" || aw.ToString(in.DestinationCidrBlock) != "0.0.0.0/0" {
		return nil, errors.New("unexpected route target")
	}
	f.calls++
	return &ec2.CreateRouteOutput{}, f.create(f.calls)
}
func newRouteFixture() *routeFixture {
	return &routeFixture{table: types.RouteTable{RouteTableId: aw.String("rtb-owned"), VpcId: aw.String("vpc-owned"), Tags: tags("owner", "network")}, create: func(int) error { return nil }}
}
func noRouteWait(context.Context, time.Duration) error { return nil }

func TestRouteAuthorizationPropagationAndPersistentDenial(t *testing.T) {
	denied := &smithy.GenericAPIError{Code: "UnauthorizedOperation"}
	for _, permanent := range []bool{false, true} {
		f := newRouteFixture()
		f.create = func(n int) error {
			if permanent || n < 3 {
				return denied
			}
			return nil
		}
		var delay time.Duration
		err := ensureInternetRoute(context.Background(), f, "owner", "vpc-owned", "rtb-owned", "igw-owned", func(_ context.Context, d time.Duration) error { delay += d; return nil })
		if permanent {
			if !errors.Is(err, denied) || f.calls != 6 || delay != 31*time.Second {
				t.Fatalf("unbounded/lost denial: calls=%d delay=%s err=%v", f.calls, delay, err)
			}
		} else if err != nil || f.calls != 3 {
			t.Fatalf("transient denial not recovered: %d %v", f.calls, err)
		}
	}
}

func TestRouteOwnershipAndConflicts(t *testing.T) {
	for _, change := range []func(*routeFixture){
		func(f *routeFixture) { f.table.Tags = tags("another-owner", "network") },
		func(f *routeFixture) { f.table.VpcId = aw.String("vpc-other") },
		func(f *routeFixture) { f.table.RouteTableId = aw.String("rtb-other") },
		func(f *routeFixture) {
			f.table.Routes = []types.Route{{DestinationCidrBlock: aw.String("0.0.0.0/0"), GatewayId: aw.String("igw-other")}}
		},
	} {
		f := newRouteFixture()
		change(f)
		if err := ensureInternetRoute(context.Background(), f, "owner", "vpc-owned", "rtb-owned", "igw-owned", noRouteWait); err == nil || f.calls != 0 {
			t.Fatalf("mutated foreign/conflicting route: calls=%d err=%v", f.calls, err)
		}
	}
}

func TestRouteLostResponseReconcilesWithoutDuplicateMutation(t *testing.T) {
	f := newRouteFixture()
	f.create = func(int) error {
		f.table.Routes = []types.Route{{DestinationCidrBlock: aw.String("0.0.0.0/0"), GatewayId: aw.String("igw-owned")}}
		return context.DeadlineExceeded
	}
	if err := ensureInternetRoute(context.Background(), f, "owner", "vpc-owned", "rtb-owned", "igw-owned", noRouteWait); !errors.Is(err, context.DeadlineExceeded) || f.calls != 1 {
		t.Fatalf("ambiguous outcome retried: %d %v", f.calls, err)
	}
	if err := ensureInternetRoute(context.Background(), f, "owner", "vpc-owned", "rtb-owned", "igw-owned", noRouteWait); err != nil || f.calls != 1 {
		t.Fatalf("recovery duplicated route: %d %v", f.calls, err)
	}
}

func TestRouteRetryRechecksOwnershipAndCancellation(t *testing.T) {
	f := newRouteFixture()
	f.create = func(int) error { f.table.Tags = nil; return &smithy.GenericAPIError{Code: "UnauthorizedOperation"} }
	if err := ensureInternetRoute(context.Background(), f, "owner", "vpc-owned", "rtb-owned", "igw-owned", noRouteWait); err == nil || f.calls != 1 {
		t.Fatalf("stale ownership retried: %d %v", f.calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitRouteRetry(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type invisibleRouteTables struct {
	routeAPI
	misses, calls int
}

func (f *invisibleRouteTables) DescribeRouteTables(context.Context, *ec2.DescribeRouteTablesInput, ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	f.calls++
	if f.calls <= f.misses {
		return nil, &smithy.GenericAPIError{Code: "InvalidRouteTableID.NotFound"}
	}
	return &ec2.DescribeRouteTablesOutput{RouteTables: []types.RouteTable{{RouteTableId: aw.String("rtb-new")}}}, nil
}

// A just-created route table may be briefly invisible; only that case is retried.
func TestDescribeRouteTableRetriesEventualVisibility(t *testing.T) {
	f := &invisibleRouteTables{misses: 3}
	out, err := describeRouteTable(context.Background(), f, "rtb-new", noRouteWait)
	if err != nil || f.calls != 4 || len(out.RouteTables) != 1 {
		t.Fatalf("visible table not returned: %d %v", f.calls, err)
	}
	f = &invisibleRouteTables{misses: 100}
	if _, err = describeRouteTable(context.Background(), f, "rtb-new", noRouteWait); err == nil || f.calls != 10 {
		t.Fatalf("missing table retried without bound: %d %v", f.calls, err)
	}
}

type remoteRouteFixture struct {
	table types.RouteTable
	calls int
	lose  bool
}

func (f *remoteRouteFixture) DescribeRouteTables(context.Context, *ec2.DescribeRouteTablesInput, ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	return &ec2.DescribeRouteTablesOutput{RouteTables: []types.RouteTable{f.table}}, nil
}
func (f *remoteRouteFixture) CreateRoute(_ context.Context, in *ec2.CreateRouteInput, _ ...func(*ec2.Options)) (*ec2.CreateRouteOutput, error) {
	if aw.ToString(in.DestinationCidrBlock) != "10.77.1.0/24" || aw.ToString(in.NetworkInterfaceId) != "eni-router" || in.GatewayId != nil {
		return nil, errors.New("incorrect more-specific ENI route")
	}
	f.calls++
	f.table.Routes = append(f.table.Routes, types.Route{DestinationCidrBlock: in.DestinationCidrBlock, NetworkInterfaceId: in.NetworkInterfaceId})
	if f.lose {
		return nil, context.DeadlineExceeded
	}
	return &ec2.CreateRouteOutput{}, nil
}
func TestRemoteENIRouteRetainsVPCLocalAndRecoversResponseLoss(t *testing.T) {
	f := &remoteRouteFixture{lose: true, table: types.RouteTable{RouteTableId: aw.String("rtb-owned"), VpcId: aw.String("vpc-owned"), Tags: tags("owner", "network"), Routes: []types.Route{{DestinationCidrBlock: aw.String("10.77.0.0/16"), GatewayId: aw.String("local")}}}}
	reconcile := func() error {
		return ensureDestinationRoute(context.Background(), f, "owner", "vpc-owned", "rtb-owned", "10.77.1.0/24", "eni-router", true, noRouteWait)
	}
	if err := reconcile(); !errors.Is(err, context.DeadlineExceeded) || f.calls != 1 {
		t.Fatal("unknown route outcome retried", err, f.calls)
	}
	if err := reconcile(); err != nil || f.calls != 1 {
		t.Fatal("route duplicated after loss", err, f.calls)
	}
	if len(f.table.Routes) != 2 || aw.ToString(f.table.Routes[0].GatewayId) != "local" {
		t.Fatal("VPC local route replaced", f.table.Routes)
	}
	f.table.Routes[1].NetworkInterfaceId = aw.String("eni-other")
	if err := reconcile(); err == nil || f.calls != 1 {
		t.Fatal("conflicting remote ENI route overwritten", err)
	}
}
