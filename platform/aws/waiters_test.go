package aws

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

type instanceRunningWaitFunc func(context.Context, *ec2.DescribeInstancesInput, time.Duration, ...func(*ec2.InstanceRunningWaiterOptions)) error

func (f instanceRunningWaitFunc) Wait(ctx context.Context, in *ec2.DescribeInstancesInput, maxWait time.Duration, opts ...func(*ec2.InstanceRunningWaiterOptions)) error {
	return f(ctx, in, maxWait, opts...)
}

type vpcAvailableWaitFunc func(context.Context, *ec2.DescribeVpcsInput, time.Duration, ...func(*ec2.VpcAvailableWaiterOptions)) error

func (f vpcAvailableWaitFunc) Wait(ctx context.Context, in *ec2.DescribeVpcsInput, maxWait time.Duration, opts ...func(*ec2.VpcAvailableWaiterOptions)) error {
	return f(ctx, in, maxWait, opts...)
}

func TestReadinessWaiterConfiguration(t *testing.T) {
	apiError := errors.New("Describe failed")
	for _, outcome := range []struct {
		name string
		err  error
	}{{"ready", nil}, {"cancelled", context.Canceled}, {"timeout", context.DeadlineExceeded}, {"API error", apiError}} {
		t.Run(outcome.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			instanceWaiter := instanceRunningWaitFunc(func(gotCtx context.Context, in *ec2.DescribeInstancesInput, maxWait time.Duration, opts ...func(*ec2.InstanceRunningWaiterOptions)) error {
				calls++
				if gotCtx != ctx || maxWait != 10*time.Minute || !reflect.DeepEqual(in, &ec2.DescribeInstancesInput{InstanceIds: []string{"i-test"}}) {
					t.Fatalf("incorrect instance wait scope/deadline: %v, %v", in, maxWait)
				}
				o := ec2.InstanceRunningWaiterOptions{MinDelay: 15 * time.Second, MaxDelay: 120 * time.Second, LogWaitAttempts: true,
					Retryable: func(context.Context, *ec2.DescribeInstancesInput, *ec2.DescribeInstancesOutput, error) (bool, error) {
						return true, apiError
					},
				}
				for _, opt := range opts {
					opt(&o)
				}
				if o.MinDelay != 2*time.Second || o.MaxDelay != 10*time.Second || !o.LogWaitAttempts {
					t.Fatalf("incorrect instance waiter options: %+v", o)
				}
				if retry, err := o.Retryable(ctx, in, nil, nil); !retry || err != apiError {
					t.Fatal("instance state acceptor replaced")
				}
				return outcome.err
			})
			if err := waitInstanceRunning(ctx, instanceWaiter, "i-test"); err != outcome.err || calls != 1 {
				t.Fatalf("instance wait retried or changed outcome: calls=%d, err=%v", calls, err)
			}

			calls = 0
			vpcWaiter := vpcAvailableWaitFunc(func(gotCtx context.Context, in *ec2.DescribeVpcsInput, maxWait time.Duration, opts ...func(*ec2.VpcAvailableWaiterOptions)) error {
				calls++
				if gotCtx != ctx || maxWait != 2*time.Minute || !reflect.DeepEqual(in, &ec2.DescribeVpcsInput{VpcIds: []string{"vpc-test"}}) {
					t.Fatalf("incorrect VPC wait scope/deadline: %v, %v", in, maxWait)
				}
				o := ec2.VpcAvailableWaiterOptions{MinDelay: 15 * time.Second, MaxDelay: 120 * time.Second, LogWaitAttempts: true,
					Retryable: func(context.Context, *ec2.DescribeVpcsInput, *ec2.DescribeVpcsOutput, error) (bool, error) {
						return true, apiError
					},
				}
				for _, opt := range opts {
					opt(&o)
				}
				if o.MinDelay != 2*time.Second || o.MaxDelay != 10*time.Second || !o.LogWaitAttempts {
					t.Fatalf("incorrect VPC waiter options: %+v", o)
				}
				if retry, err := o.Retryable(ctx, in, nil, nil); !retry || err != apiError {
					t.Fatal("VPC state acceptor replaced")
				}
				return outcome.err
			})
			if err := waitVPCAvailable(ctx, vpcWaiter, "vpc-test"); err != outcome.err || calls != 1 {
				t.Fatalf("VPC wait retried or changed outcome: calls=%d, err=%v", calls, err)
			}
		})
	}
}
