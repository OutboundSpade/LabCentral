//go:build !faultinject

package aws

import "github.com/aws/aws-sdk-go-v2/service/ec2"

func faultOptions(*ec2.Options) {}
