package aws

import (
	"context"
	"encoding/json"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	t "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"time"
)

func cleanupSnapshots(ctx context.Context, a *ec2.Client, c lc.Command) error {
	snapshots := map[string]t.Snapshot{}
	found, err := a.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{OwnerIds: []string{"self"}, Filters: filters(c.Owner, "")})
	if err != nil {
		return err
	}
	for _, s := range found.Snapshots {
		snapshots[aw.ToString(s.SnapshotId)] = s
	}
	// Look up recorded IDs even when somebody removed their ownership tags.
	for _, child := range c.Prior.Children {
		if child.Kind != "snapshot" {
			continue
		}
		r, err := a.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{SnapshotIds: []string{child.ID}})
		if apiCode(err, "InvalidSnapshot.NotFound") {
			continue
		}
		if err != nil {
			return err
		}
		for _, s := range r.Snapshots {
			snapshots[aw.ToString(s.SnapshotId)] = s
		}
	}
	for _, s := range snapshots {
		if !owned(s.Tags, c.Owner) {
			return fmt.Errorf("ownership_mismatch: snapshot %s retained", aw.ToString(s.SnapshotId))
		}
	}
	for _, s := range snapshots {
		if _, err = a.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: s.SnapshotId}); err != nil && !apiCode(err, "InvalidSnapshot.NotFound") {
			return err
		}
		r, err := a.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{SnapshotIds: []string{aw.ToString(s.SnapshotId)}})
		if apiCode(err, "InvalidSnapshot.NotFound") {
			continue
		}
		if err != nil {
			return err
		}
		if len(r.Snapshots) > 0 {
			return fmt.Errorf("snapshot deletion not yet confirmed: %s", aw.ToString(s.SnapshotId))
		}
	}
	return nil
}
func cleanupVolumes(ctx context.Context, a *ec2.Client, c lc.Command) error {
	volumes := map[string]t.Volume{}
	r, err := a.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{Filters: filters(c.Owner, c.Prior.Name)})
	if err != nil {
		return err
	}
	for _, v := range r.Volumes {
		volumes[aw.ToString(v.VolumeId)] = v
	}
	for _, child := range c.Prior.Children {
		if child.Kind != "volume" {
			continue
		}
		r, err := a.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{child.ID}})
		if apiCode(err, "InvalidVolume.NotFound") {
			continue
		}
		if err != nil {
			return err
		}
		for _, v := range r.Volumes {
			volumes[aw.ToString(v.VolumeId)] = v
		}
	}
	for _, v := range volumes {
		if !owned(v.Tags, c.Owner) {
			return fmt.Errorf("ownership_mismatch: volume %s retained", aw.ToString(v.VolumeId))
		}
	}
	for _, v := range volumes {
		if v.State != t.VolumeStateDeleting {
			if len(v.Attachments) > 0 {
				return fmt.Errorf("volume remains attached: %s", aw.ToString(v.VolumeId))
			}
			if _, err = a.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: v.VolumeId}); err != nil && !apiCode(err, "InvalidVolume.NotFound") {
				return err
			}
		}
		if err = ec2.NewVolumeDeletedWaiter(a).Wait(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{aw.ToString(v.VolumeId)}}, 2*time.Minute); err != nil {
			return err
		}
	}
	return nil
}

// Known resource IDs remain authoritative even if a tag-based search omits them.
func checkRecordedNetwork(ctx context.Context, a *ec2.Client, c lc.Command) error {
	if len(c.Prior.Data) == 0 {
		return nil
	}
	var n networkState
	if err := json.Unmarshal(c.Prior.Data, &n); err != nil {
		return err
	}
	check := func(tags []t.Tag, id string) error {
		if !owned(tags, c.Owner) {
			return fmt.Errorf("ownership_mismatch: network child %s retained", id)
		}
		return nil
	}
	if n.Subnet != "" {
		r, e := a.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: []string{n.Subnet}})
		if e != nil && !apiCode(e, "InvalidSubnetID.NotFound") {
			return e
		}
		if e == nil {
			for _, v := range r.Subnets {
				if e = check(v.Tags, n.Subnet); e != nil {
					return e
				}
			}
		}
	}
	if n.SG != "" {
		r, e := a.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{n.SG}})
		if e != nil && !apiCode(e, "InvalidGroup.NotFound") {
			return e
		}
		if e == nil {
			for _, v := range r.SecurityGroups {
				if e = check(v.Tags, n.SG); e != nil {
					return e
				}
			}
		}
	}
	if n.Route != "" {
		r, e := a.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{RouteTableIds: []string{n.Route}})
		if e != nil && !apiCode(e, "InvalidRouteTableID.NotFound") {
			return e
		}
		if e == nil {
			for _, v := range r.RouteTables {
				if e = check(v.Tags, n.Route); e != nil {
					return e
				}
			}
		}
	}
	if n.Gateway != "" {
		r, e := a.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{InternetGatewayIds: []string{n.Gateway}})
		if e != nil && !apiCode(e, "InvalidInternetGatewayID.NotFound") {
			return e
		}
		if e == nil {
			for _, v := range r.InternetGateways {
				if e = check(v.Tags, n.Gateway); e != nil {
					return e
				}
			}
		}
	}
	if n.Endpoint != "" {
		r, e := a.DescribeVpcEndpoints(ctx, &ec2.DescribeVpcEndpointsInput{VpcEndpointIds: []string{n.Endpoint}})
		if e != nil && !apiCode(e, "InvalidVpcEndpointId.NotFound") {
			return e
		}
		if e == nil {
			for _, v := range r.VpcEndpoints {
				if e = check(v.Tags, n.Endpoint); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
