package aws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/guest"
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	t "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func (p *Provider) gateway(ctx context.Context, a *ec2.Client, c lc.Command) (lc.Result, error) {
	if err := lc.ValidateAWSGateway(c.Environment); err != nil {
		return lc.Result{}, err
	}
	g := c.Environment.AWS.Gateway.Effective()
	base := c.Environment.Bases[g.Base]
	if c.Machine.NativeImage != base.ID || c.Machine.InstanceType != g.InstanceType || c.Machine.DiskGiB < base.DiskGiB {
		return lc.Result{}, fmt.Errorf("gateway request differs from environment binding")
	}
	if err := lc.CheckEgress(c.Source.Lab, c.Environment); err != nil {
		return lc.Result{}, err
	}
	var n networkState
	if err := json.Unmarshal(c.Network.Data, &n); err != nil {
		return lc.Result{}, err
	}
	if n.EdgeSubnet == "" || n.GatewaySG == "" {
		return lc.Result{}, fmt.Errorf("gateway network is incomplete")
	}
	if err := p.prepareInstancePolicy(c); err != nil {
		return lc.Result{}, err
	}
	id, err := doEC2(ctx, p.Journal, c.Operation+"/instance", func() (string, error) {
		images, err := a.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{c.Machine.NativeImage}})
		if err != nil {
			return "", err
		}
		if len(images.Images) != 1 || images.Images[0].RootDeviceName == nil {
			return "", fmt.Errorf("gateway AMI root device unavailable")
		}
		cidrs, all := []string{}, []string{}
		planned, _, _, err := lc.AWSLayout(c.Source.Lab)
		if err != nil {
			return "", err
		}
		for _, seg := range planned {
			all = append(all, seg.CIDR)
			if seg.Egress == "internet" {
				cidrs = append(cidrs, seg.CIDR)
			}
		}
		ud, err := guest.GatewayCloudInit(c.Machine, cidrs, all...)
		if err != nil {
			return "", err
		}
		out, err := a.RunInstances(ctx, &ec2.RunInstancesInput{
			ImageId: &c.Machine.NativeImage, InstanceType: t.InstanceType(c.Machine.InstanceType), MinCount: aw.Int32(1), MaxCount: aw.Int32(1), ClientToken: aw.String(c.Owner + "-gateway"), UserData: aw.String(base64.StdEncoding.EncodeToString([]byte(ud))),
			MetadataOptions:     &t.InstanceMetadataOptionsRequest{HttpTokens: t.HttpTokensStateRequired, HttpPutResponseHopLimit: aw.Int32(1)},
			NetworkInterfaces:   []t.InstanceNetworkInterfaceSpecification{{DeviceIndex: aw.Int32(0), DeleteOnTermination: aw.Bool(true), SubnetId: &n.EdgeSubnet, Groups: []string{n.GatewaySG}, AssociatePublicIpAddress: aw.Bool(true)}},
			BlockDeviceMappings: []t.BlockDeviceMapping{{DeviceName: images.Images[0].RootDeviceName, Ebs: &t.EbsBlockDevice{VolumeSize: aw.Int32(int32(c.Machine.DiskGiB)), VolumeType: t.VolumeTypeGp3, Encrypted: aw.Bool(true), DeleteOnTermination: aw.Bool(true)}}},
			TagSpecifications:   append(append(specs(t.ResourceTypeInstance, c.Owner, "gateway"), specs(t.ResourceTypeVolume, c.Owner, "gateway")...), specs(t.ResourceTypeNetworkInterface, c.Owner, "gateway")...),
		})
		if err != nil {
			return "", err
		}
		if len(out.Instances) != 1 {
			return "", fmt.Errorf("unexpected gateway instance count")
		}
		return aw.ToString(out.Instances[0].InstanceId), nil
	}, func() (string, bool, error) {
		r, err := p.findGateway(ctx, a, c)
		if err != nil {
			return "", false, err
		}
		return r.Resource.ID, r.Resource.ID != "", nil
	})
	if err != nil {
		return lc.Result{}, err
	}
	if err = ec2.NewInstanceRunningWaiter(a).Wait(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}}, 10*time.Minute); err != nil {
		return lc.Result{}, err
	}
	result, err := p.findGateway(ctx, a, c)
	if err != nil {
		return result, err
	}
	if result.Resource.ID != id {
		return result, fmt.Errorf("gateway identity changed")
	}
	_, err = a.ModifyInstanceAttribute(ctx, &ec2.ModifyInstanceAttributeInput{InstanceId: &id, SourceDestCheck: &t.AttributeBooleanValue{Value: aw.Bool(false)}})
	if err != nil {
		return result, err
	}
	var identity lc.GatewayIdentity
	if err = json.Unmarshal(result.Resource.Data, &identity); err != nil {
		return result, err
	}
	if identity.ENI == "" {
		return result, fmt.Errorf("gateway ENI unavailable")
	}
	// Install the lab default only for an explicitly enabled network. The edge
	// default always targets the IGW and is independent of future lab networks.
	if len(n.Segments) == 0 {
		if lc.InternetAccess(c.Source.Lab) {
			if err = ensureENIRoute(ctx, a, c.Owner, n.VPC, n.Route, identity.ENI, waitRouteRetry); err != nil {
				return result, err
			}
		}
	} else {
		for _, seg := range n.Segments {
			if seg.Egress == "internet" {
				if err = ensureENIRoute(ctx, a, c.Owner, n.VPC, seg.Route, identity.ENI, waitRouteRetry); err != nil {
					return result, err
				}
			}
		}
	}
	if err = remote.Wait(ctx, result.Resource.Address, "lab", c.ClientPrivateKey, c.Machine.HostPublicKey); err != nil {
		return result, err
	}
	result.Resource.Complete = true
	return result, nil
}

func (p *Provider) findGateway(ctx context.Context, a *ec2.Client, c lc.Command) (lc.Result, error) {
	f := append(filters(c.Owner, "gateway"), t.Filter{Name: aw.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}})
	out, err := a.DescribeInstances(ctx, &ec2.DescribeInstancesInput{Filters: f})
	if err != nil {
		return lc.Result{}, err
	}
	instances := []t.Instance{}
	for _, rs := range out.Reservations {
		instances = append(instances, rs.Instances...)
	}
	if len(instances) != 1 {
		return lc.Result{}, fmt.Errorf("recovery_required: expected one owned gateway, found %d", len(instances))
	}
	i := instances[0]
	if !owned(i.Tags, c.Owner) {
		return lc.Result{}, fmt.Errorf("ownership_mismatch: gateway")
	}
	identity := lc.GatewayIdentity{HostPublicKey: c.Machine.HostPublicKey}
	if len(i.NetworkInterfaces) == 1 {
		identity.ENI = aw.ToString(i.NetworkInterfaces[0].NetworkInterfaceId)
	}
	data, _ := json.Marshal(identity)
	r := lc.Resource{Kind: "gateway", Name: "gateway", ID: aw.ToString(i.InstanceId), Owner: c.Owner, Address: aw.ToString(i.PublicIpAddress), PrivateAddress: aw.ToString(i.PrivateIpAddress), Data: data}
	for _, nic := range i.NetworkInterfaces {
		r.Children = append(r.Children, lc.NativeResource{Kind: "eni", ID: aw.ToString(nic.NetworkInterfaceId)})
	}
	for _, mapping := range i.BlockDeviceMappings {
		if mapping.Ebs != nil {
			r.Children = append(r.Children, lc.NativeResource{Kind: "volume", ID: aw.ToString(mapping.Ebs.VolumeId)})
		}
	}
	if err = p.attachInstancePolicy(c, &r); err != nil {
		return lc.Result{}, err
	}
	return lc.Result{Resource: r}, nil
}
