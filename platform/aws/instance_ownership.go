package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	lc "github.com/OutboundSpade/LabCentral/v2"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	t "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type instancePolicy struct {
	TaggedENIs bool `json:"taggedENIs"`
}

// Save the launch policy before the native create. Old pending/done requests keep
// their original policy; a newer binary must not reinterpret their untagged NICs.
func (p *Provider) prepareInstancePolicy(c lc.Command) error {
	var policy instancePolicy
	if err := p.Journal.Load(c.Operation+"/instance-policy", &policy); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	known, err := p.Journal.HasIntent(c.Operation + "/instance")
	if err != nil || known {
		return err
	}
	return p.Journal.Save(c.Operation+"/instance-policy", instancePolicy{TaggedENIs: true})
}
func (p *Provider) attachInstancePolicy(c lc.Command, r *lc.Resource) error {
	var policy instancePolicy
	if err := p.Journal.Load(c.Operation+"/instance-policy", &policy); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !policy.TaggedENIs {
		return nil
	}
	data := map[string]any{}
	if len(r.Data) > 0 {
		if err := json.Unmarshal(r.Data, &data); err != nil {
			return err
		}
	}
	data["taggedENIs"] = true
	var err error
	r.Data, err = json.Marshal(data)
	return err
}
func marked(tags []t.Tag) bool {
	for _, tag := range tags {
		if aw.ToString(tag.Key) == "labcentral:owner" || aw.ToString(tag.Key) == "labcentral:version" {
			return true
		}
	}
	return false
}

// Termination is also a delete of every delete-on-termination disk/NIC. Verify
// those children before issuing it, including known disks omitted from tag queries.
// Retained pre-marker NICs require the exact parent attachment; foreign markings
// always block cleanup. New launches require their explicit owner tags as well.
func verifyInstanceChildren(ctx context.Context, a *ec2.Client, c lc.Command, i t.Instance, requireTaggedENIs bool) error {
	var policy instancePolicy
	if len(c.Prior.Data) > 0 {
		if err := json.Unmarshal(c.Prior.Data, &policy); err != nil {
			return err
		}
	}
	requireTaggedENIs = requireTaggedENIs || policy.TaggedENIs
	volumes := map[string]bool{}
	for _, mapping := range i.BlockDeviceMappings {
		if mapping.Ebs != nil {
			volumes[aw.ToString(mapping.Ebs.VolumeId)] = true
		}
	}
	for _, child := range c.Prior.Children {
		if child.Kind == "volume" {
			if _, ok := volumes[child.ID]; !ok {
				volumes[child.ID] = false
			}
		}
	}
	for id, attached := range volumes {
		result, err := a.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{id}})
		if apiCode(err, "InvalidVolume.NotFound") && !attached {
			continue
		}
		if err != nil {
			return fmt.Errorf("recovery_required: cannot verify instance volume before termination: %w", err)
		}
		if len(result.Volumes) == 0 && !attached {
			continue
		}
		if len(result.Volumes) != 1 || aw.ToString(result.Volumes[0].VolumeId) != id || !owned(result.Volumes[0].Tags, c.Owner) {
			return fmt.Errorf("ownership_mismatch: attached or recorded volume %s; instance retained", id)
		}
	}
	enis := map[string]bool{}
	recordedENIs := map[string]bool{}
	var legacyGateway struct {
		ENI string `json:"eni"`
	}
	if len(c.Prior.Data) > 0 {
		if err := json.Unmarshal(c.Prior.Data, &legacyGateway); err != nil {
			return err
		}
		if legacyGateway.ENI != "" {
			recordedENIs[legacyGateway.ENI] = true
		}
	}
	for _, nic := range i.NetworkInterfaces {
		enis[aw.ToString(nic.NetworkInterfaceId)] = true
	}
	for _, child := range c.Prior.Children {
		if child.Kind == "eni" {
			recordedENIs[child.ID] = true
			if _, ok := enis[child.ID]; !ok {
				enis[child.ID] = false
			}
		}
	}
	for id, attached := range enis {
		result, err := a.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{NetworkInterfaceIds: []string{id}})
		if apiCode(err, "InvalidNetworkInterfaceID.NotFound") && !attached {
			continue
		}
		if err != nil {
			return fmt.Errorf("recovery_required: cannot verify instance NIC before termination: %w", err)
		}
		if len(result.NetworkInterfaces) == 0 && !attached {
			continue
		}
		if len(result.NetworkInterfaces) != 1 {
			return fmt.Errorf("recovery_required: instance NIC identity is unconfirmed; instance retained")
		}
		current := result.NetworkInterfaces[0]
		if aw.ToString(current.NetworkInterfaceId) != id {
			return fmt.Errorf("ownership_mismatch: NIC identity changed; instance retained")
		}
		if attached && (current.Attachment == nil || aw.ToString(current.Attachment.InstanceId) != aw.ToString(i.InstanceId)) {
			return fmt.Errorf("ownership_mismatch: NIC attachment changed; instance retained")
		}
		if !attached && current.Attachment != nil {
			return fmt.Errorf("recovery_required: recorded NIC attachment is inconsistent; instance retained")
		}
		if (requireTaggedENIs || marked(current.TagSet) || !attached) && !owned(current.TagSet, c.Owner) {
			return fmt.Errorf("ownership_mismatch: NIC %s; instance retained", id)
		}
		// The original inline primary cannot be detached/replaced. An unmarked
		// secondary may have been attached by somebody else after launch, so an
		// older receipt must identify it explicitly before implicit termination.
		if !requireTaggedENIs && !marked(current.TagSet) && attached && !recordedENIs[id] && (current.Attachment.DeviceIndex == nil || *current.Attachment.DeviceIndex != 0) {
			return fmt.Errorf("ownership_mismatch: unrecorded legacy secondary NIC; instance retained")
		}
	}

	return nil
}
