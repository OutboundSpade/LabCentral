package aws

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

const (
	readinessMinDelay      = 2 * time.Second
	readinessMaxDelay      = 10 * time.Second
	instanceRunningMaxWait = 10 * time.Minute
	vpcAvailableMaxWait    = 2 * time.Minute
)

type instanceRunningWaiter interface {
	Wait(context.Context, *ec2.DescribeInstancesInput, time.Duration, ...func(*ec2.InstanceRunningWaiterOptions)) error
}

type vpcAvailableWaiter interface {
	Wait(context.Context, *ec2.DescribeVpcsInput, time.Duration, ...func(*ec2.VpcAvailableWaiterOptions)) error
}

// Configure only polling delays: the SDK retains its state acceptors, jittered
// backoff and the client's retry/throttle handling. Errors remain unknown outcomes
// for the existing journal and recovery paths to reconcile.
func waitInstanceRunning(ctx context.Context, waiter instanceRunningWaiter, id string) error {
	return waiter.Wait(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}}, instanceRunningMaxWait, func(o *ec2.InstanceRunningWaiterOptions) {
		o.MinDelay = readinessMinDelay
		o.MaxDelay = readinessMaxDelay
	})
}

func waitVPCAvailable(ctx context.Context, waiter vpcAvailableWaiter, id string) error {
	return waiter.Wait(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{id}}, vpcAvailableMaxWait, func(o *ec2.VpcAvailableWaiterOptions) {
		o.MinDelay = readinessMinDelay
		o.MaxDelay = readinessMaxDelay
	})
}
