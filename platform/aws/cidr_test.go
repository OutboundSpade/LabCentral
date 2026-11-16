package aws

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	t "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type cidrAPIStub struct {
	vpc t.Vpc
	err error
}

func (s *cidrAPIStub) DescribeVpcs(_ context.Context, in *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	if len(in.VpcIds) != 1 || in.VpcIds[0] != "vpc-test" {
		return nil, errors.New("read was not scoped to the exact VPC")
	}
	return &ec2.DescribeVpcsOutput{Vpcs: []t.Vpc{s.vpc}}, s.err
}
func (s *cidrAPIStub) AssociateVpcCidrBlock(context.Context, *ec2.AssociateVpcCidrBlockInput, ...func(*ec2.Options)) (*ec2.AssociateVpcCidrBlockOutput, error) {
	return nil, errors.New("unexpected mutation")
}
func cidrStub() *cidrAPIStub {
	return &cidrAPIStub{vpc: t.Vpc{VpcId: aw.String("vpc-test"), Tags: tags("owner", "network"), CidrBlockAssociationSet: []t.VpcCidrBlockAssociation{{AssociationId: aw.String("assoc-test"), CidrBlock: aw.String("172.20.0.0/16"), CidrBlockState: &t.VpcCidrBlockState{State: t.VpcCidrBlockStateCodeAssociating}}}}}
}
func TestVPCCIDRReadinessAndFailure(tst *testing.T) {
	for _, outcome := range []string{"associated", "failed", "disassociating", "changed", "foreign", "missing", "unknown", "read-error", "duplicate", "cancelled"} {
		tst.Run(outcome, func(tst *testing.T) {
			s := cidrStub()
			calls := 0
			wait := func(context.Context, time.Duration) error {
				calls++
				if calls > 1 {
					tst.Fatal("unexpected repeated wait")
				}
				a := &s.vpc.CidrBlockAssociationSet[0]
				switch outcome {
				case "associated":
					a.CidrBlockState.State = t.VpcCidrBlockStateCodeAssociated
				case "failed":
					a.CidrBlockState.State = t.VpcCidrBlockStateCodeFailed
				case "disassociating":
					a.CidrBlockState.State = t.VpcCidrBlockStateCodeDisassociating
				case "changed":
					a.AssociationId = aw.String("replacement")
				case "foreign":
					s.vpc.Tags = tags("other", "network")
				case "missing":
					s.vpc.CidrBlockAssociationSet = nil
					return context.DeadlineExceeded
				case "unknown":
					a.CidrBlockState = nil
				case "read-error":
					s.err = errors.New("API unavailable")
				case "duplicate":
					s.vpc.CidrBlockAssociationSet = append(s.vpc.CidrBlockAssociationSet, *a)
				case "cancelled":
					return context.Canceled
				}
				return nil
			}
			err := waitVPCCIDR(context.Background(), s, "owner", "vpc-test", "172.20.0.0/16", "assoc-test", wait)
			if (err == nil) != (outcome == "associated") || calls != 1 {
				tst.Fatal("incorrect readiness outcome", err, calls)
			}
		})
	}
}
func TestVPCCIDRBoundedWaitAndCancellation(tst *testing.T) {
	s := cidrStub()
	calls := 0
	err := waitVPCCIDR(context.Background(), s, "owner", "vpc-test", "172.20.0.0/16", "assoc-test", func(context.Context, time.Duration) error { calls++; return nil })
	if err == nil || !strings.Contains(err.Error(), "readiness is unresolved") || calls != 60 {
		tst.Fatal("unbounded or successful unknown outcome", err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = waitVPCCIDR(ctx, s, "owner", "vpc-test", "172.20.0.0/16", "assoc-test", nil)
	if !errors.Is(err, context.Canceled) {
		tst.Fatal(err)
	}
}

func TestVPCCIDRRefusesUnrecordedAssociation(tst *testing.T) {
	root := tst.TempDir()
	p := &Provider{Journal: journal.Journal{Root: root}}
	for attempt := 0; attempt < 2; attempt++ {
		err := p.associateVPCCIDR(context.Background(), cidrStub(), "owner", "operation", "vpc-test", "172.20.0.0/16", nil)
		if err == nil || !strings.Contains(err.Error(), "unrecorded") {
			tst.Fatal("unrecorded association was adopted", err)
		}
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 0 {
		tst.Fatal("preflight refusal created an adoptable intent", err, files)
	}
}
