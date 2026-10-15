package aws

import (
	"context"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	t "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"strings"
)

func waitMachine(ctx context.Context, d *remote.Dialer, address string, c lc.Command) error {
	if c.Machine.OS == "windows" {
		return remote.WaitWindows(ctx, d, address, c.ClientPrivateKey, c.Machine.HostPublicKey, lc.Contains(c.Machine.Spec.Access, "desktop"))
	}
	return remote.WaitWithDialer(ctx, d, address, "lab", c.ClientPrivateKey, c.Machine.HostPublicKey)
}

func checkAMIPlatform(image t.Image, os string) error {
	actual := "linux"
	if strings.EqualFold(string(image.Platform), string(t.PlatformValuesWindows)) {
		actual = "windows"
	}
	if actual != lc.OSFamily(os) {
		return fmt.Errorf("AMI OS %s does not match declared OS %s", actual, lc.OSFamily(os))
	}
	return nil
}
