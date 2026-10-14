package aws

import (
	"context"
	"encoding/json"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	t "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func networkVPC(resource lc.Resource) string {
	var n networkState
	_ = json.Unmarshal(resource.Data, &n)
	return n.VPC
}

// Reconcile every router ENI and outgoing segment route after launch, including
// recovered launches. Inline ENIs are deleted with the exact owned instance.
func (p *Provider) routerNetworking(ctx context.Context, a *ec2.Client, c lc.Command, ns networkState) error {
	if !c.Machine.Spec.Forwarding {
		return nil
	}
	out, err := a.DescribeInstances(ctx, &ec2.DescribeInstancesInput{Filters: append(filters(c.Owner, "machine:"+c.Machine.Name), t.Filter{Name: aw.String("instance-state-name"), Values: []string{"pending", "running", "stopped", "stopping"}})})
	if err != nil {
		return err
	}
	instances := []t.Instance{}
	for _, r := range out.Reservations {
		instances = append(instances, r.Instances...)
	}
	if len(instances) != 1 || !owned(instances[0].Tags, c.Owner) {
		return fmt.Errorf("router instance ownership is unresolved")
	}
	enis := map[int]string{}
	for _, eni := range instances[0].NetworkInterfaces {
		if eni.Attachment == nil {
			return fmt.Errorf("router ENI attachment is unresolved")
		}
		index := int(aw.ToInt32(eni.Attachment.DeviceIndex))
		if index < 0 || index >= len(c.Machine.NICs) || aw.ToString(eni.PrivateIpAddress) != c.Machine.NICs[index].Address || aw.ToString(eni.VpcId) != ns.VPC {
			return fmt.Errorf("router ENI differs from plan")
		}
		id := aw.ToString(eni.NetworkInterfaceId)
		enis[index] = id
		if _, err = a.ModifyNetworkInterfaceAttribute(ctx, &ec2.ModifyNetworkInterfaceAttributeInput{NetworkInterfaceId: &id, SourceDestCheck: &t.AttributeBooleanValue{Value: aw.Bool(false)}}); err != nil {
			return err
		}
	}
	if len(enis) != len(c.Machine.NICs) {
		return fmt.Errorf("router ENI inventory is incomplete")
	}
	topology := lc.Plan{Source: c.Source, Environment: c.Environment}
	for _, name := range lc.SortedMachineNames(c.Source.Lab) {
		topology.Machines = append(topology.Machines, lc.PlannedMachine{Name: name, Spec: c.Source.Lab.Machines[name]})
	}
	if err = lc.PlanNetworks(&topology, nil); err != nil {
		return err
	}
	for _, n := range topology.Networks {
		for _, route := range n.Routes {
			if route.Router != c.Machine.Name {
				continue
			}
			table := ""
			for _, seg := range ns.Segments {
				if seg.Name == n.Name {
					table = seg.Route
					break
				}
			}
			err = ensureDestinationRoute(ctx, a, c.Owner, ns.VPC, table, route.CIDR, enis[route.RouterNIC], true, waitRouteRetry)
			// Only a confirmed unsupported destination/target falls back. Guest routes
			// already exist on all machines. Timeout and authorization failures stay unknown.
			if err != nil && !apiCode(err, "InvalidParameterValue") && !apiCode(err, "InvalidParameterCombination") {
				return err
			}
		}
	}
	return nil
}
