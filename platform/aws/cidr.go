package aws

import (
	"context"
	"fmt"
	"time"

	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	t "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type vpcCIDRAPI interface {
	DescribeVpcs(context.Context, *ec2.DescribeVpcsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
	AssociateVpcCidrBlock(context.Context, *ec2.AssociateVpcCidrBlockInput, ...func(*ec2.Options)) (*ec2.AssociateVpcCidrBlockOutput, error)
}

// Associations cannot be tagged. Bind discovery to the exact ownership-verified
// VPC and requested CIDR, then retain the association ID in the create journal.
func findVPCCIDR(ctx context.Context, a vpcCIDRAPI, owner, vpc, cidr string) (t.VpcCidrBlockAssociation, bool, error) {
	out, err := a.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{vpc}})
	if err != nil {
		return t.VpcCidrBlockAssociation{}, false, err
	}
	if len(out.Vpcs) != 1 || aw.ToString(out.Vpcs[0].VpcId) != vpc || !owned(out.Vpcs[0].Tags, owner) {
		return t.VpcCidrBlockAssociation{}, false, fmt.Errorf("ownership_mismatch: VPC CIDR target is unresolved")
	}
	var found t.VpcCidrBlockAssociation
	count := 0
	for _, association := range out.Vpcs[0].CidrBlockAssociationSet {
		if aw.ToString(association.CidrBlock) != cidr {
			continue
		}
		if association.CidrBlockState == nil {
			return found, false, fmt.Errorf("recovery_required: VPC CIDR state is unresolved")
		}
		if association.CidrBlockState.State == t.VpcCidrBlockStateCodeDisassociated {
			continue
		}
		found = association
		count++
	}
	if count > 1 {
		return found, false, fmt.Errorf("recovery_required: ambiguous VPC CIDR association")
	}
	if count == 1 && aw.ToString(found.AssociationId) == "" {
		return found, false, fmt.Errorf("recovery_required: VPC CIDR identity is unresolved")
	}
	return found, count == 1, nil
}

func (p *Provider) associateVPCCIDR(ctx context.Context, a vpcCIDRAPI, owner, operation, vpc, cidr string, wait func(context.Context, time.Duration) error) error {
	key := operation + "/cidr/" + cidr
	pending, err := p.Journal.HasIntent(key)
	if err != nil {
		return err
	}
	if !pending {
		_, found, err := findVPCCIDR(ctx, a, owner, vpc, cidr)
		if err != nil {
			return err
		}
		if found {
			return fmt.Errorf("recovery_required: unrecorded VPC CIDR association exists")
		}
	}
	find := func() (string, bool, error) {
		association, found, err := findVPCCIDR(ctx, a, owner, vpc, cidr)
		return aw.ToString(association.AssociationId), found, err
	}
	id, err := doEC2(ctx, p.Journal, key, func() (string, error) {
		// Recheck ownership immediately before the mutation, including on a first
		// attempt. An unrecorded existing association is not adopted.
		_, found, err := findVPCCIDR(ctx, a, owner, vpc, cidr)
		if err != nil {
			return "", err
		}
		if found {
			// This attempt has not submitted an API mutation. Clear its intent so
			// a later retry cannot adopt an association discovered in this race.
			return "", journal.Rejected(fmt.Errorf("recovery_required: unrecorded VPC CIDR association exists"))
		}
		out, err := a.AssociateVpcCidrBlock(ctx, &ec2.AssociateVpcCidrBlockInput{VpcId: &vpc, CidrBlock: &cidr}, noCreateRetries)
		if err != nil {
			return "", err
		}
		if aw.ToString(out.VpcId) != vpc || out.CidrBlockAssociation == nil || aw.ToString(out.CidrBlockAssociation.AssociationId) == "" || aw.ToString(out.CidrBlockAssociation.CidrBlock) != cidr {
			return "", fmt.Errorf("recovery_required: VPC CIDR create returned an unresolved identity")
		}
		return aw.ToString(out.CidrBlockAssociation.AssociationId), nil
	}, find)
	if err != nil {
		return err
	}
	return waitVPCCIDR(ctx, a, owner, vpc, cidr, id, wait)
}

func waitVPCCIDR(ctx context.Context, a vpcCIDRAPI, owner, vpc, cidr, id string, wait func(context.Context, time.Duration) error) error {
	for attempt := 0; attempt < 60; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		association, found, err := findVPCCIDR(ctx, a, owner, vpc, cidr)
		if err != nil {
			return err
		}
		if found {
			if aw.ToString(association.AssociationId) != id {
				return fmt.Errorf("recovery_required: VPC CIDR association identity changed")
			}
			switch association.CidrBlockState.State {
			case t.VpcCidrBlockStateCodeAssociated:
				return nil
			case t.VpcCidrBlockStateCodeAssociating:
				// Do not create subnets until AWS confirms association.
			default:
				return fmt.Errorf("recovery_required: VPC CIDR association is %s", association.CidrBlockState.State)
			}
		}
		if err := wait(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	return fmt.Errorf("recovery_required: VPC CIDR association readiness is unresolved")
}
