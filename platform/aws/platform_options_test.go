package aws

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

type optionHTTPClient func(*http.Request) (*http.Response, error)

func (f optionHTTPClient) Do(r *http.Request) (*http.Response, error) { return f(r) }
func optionCommand() lc.Command {
	return lc.Command{Owner: "owner", Environment: lc.Environment{Platform: "aws", AWS: lc.AWSConfig{Types: []lc.InstanceType{{Name: "t3.large", CPU: 2, MemoryMiB: 8192}}}}, Machine: lc.PlannedMachine{Name: "one", NativeImage: "ami-test", InstanceType: "t3.large", CPU: 2, MemoryMiB: 8192, DiskGiB: 32, Address: "10.77.0.11", PublicKey: "ssh-ed25519 public", HostPrivateKey: "private", HostPublicKey: "ssh-ed25519 host", Spec: lc.Machine{CPU: 2, Memory: "2Gi", Disk: "32Gi"}}, Network: lc.Resource{Data: []byte(`{"subnet":"subnet-test","sg":"sg-test"}`)}}
}
func TestRuntimeOptionsInNativeRunInstances(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options *lc.AWSMachineOptions
		want    map[string]string
	}{
		{"defaults", nil, map[string]string{}},
		{"empty-volume", &lc.AWSMachineOptions{Volume: &lc.AWSVolumeOptions{}}, map[string]string{}},
		{"all", &lc.AWSMachineOptions{InstanceType: "t3.large", CPUCredits: "unlimited", Volume: &lc.AWSVolumeOptions{Type: "gp3", IOPS: aw.Int(16000), Throughput: aw.Int(1000)}}, map[string]string{"BlockDeviceMapping.1.Ebs.Iops": "16000", "BlockDeviceMapping.1.Ebs.Throughput": "1000", "CreditSpecification.CpuCredits": "unlimited"}},
		{"standard-partial", &lc.AWSMachineOptions{CPUCredits: "standard", Volume: &lc.AWSVolumeOptions{Throughput: aw.Int(750)}}, map[string]string{"BlockDeviceMapping.1.Ebs.Throughput": "750", "CreditSpecification.CpuCredits": "standard"}},
		{"iops-only", &lc.AWSMachineOptions{Volume: &lc.AWSVolumeOptions{IOPS: aw.Int(4000)}}, map[string]string{"BlockDeviceMapping.1.Ebs.Iops": "4000"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var captured url.Values
			calls := 0
			api := ec2.New(ec2.Options{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), RetryMaxAttempts: 1, HTTPClient: optionHTTPClient(func(r *http.Request) (*http.Response, error) {
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				body := `<DescribeImagesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><imagesSet><item><imageId>ami-test</imageId><rootDeviceName>/dev/sda1</rootDeviceName></item></imagesSet></DescribeImagesResponse>`
				status := 200
				switch r.Form.Get("Action") {
				case "DescribeImages":
				case "RunInstances":
					captured = r.Form
					calls++
					status = 400
					body = `<Response><Errors><Error><Code>InvalidParameterValue</Code><Message>fake request captured</Message></Error></Errors><RequestID>test</RequestID></Response>`
				default:
					t.Errorf("unexpected native request %s", r.Form.Get("Action"))
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/xml"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})})
			c := optionCommand()
			c.Machine.Spec.Platform.AWS = tc.options
			// Foreign options must have no effect on the AWS request.
			c.Machine.Spec.Platform.Proxmox = &lc.ProxmoxMachineOptions{CPUType: "forbidden", NICModel: "e1000", MemoryBalloon: aw.Bool(false)}
			_, err := (&Provider{Journal: journal.Journal{Root: t.TempDir()}}).machine(context.Background(), api, c)
			if err == nil || calls != 1 {
				t.Fatalf("request not captured once: calls=%d err=%v", calls, err)
			}
			for _, key := range []string{"BlockDeviceMapping.1.Ebs.Iops", "BlockDeviceMapping.1.Ebs.Throughput", "CreditSpecification.CpuCredits"} {
				if captured.Get(key) != tc.want[key] {
					t.Errorf("%s: got %q want %q", key, captured.Get(key), tc.want[key])
				}
			}
			for key, want := range map[string]string{"InstanceType": "t3.large", "BlockDeviceMapping.1.Ebs.VolumeType": "gp3", "BlockDeviceMapping.1.Ebs.VolumeSize": "32", "BlockDeviceMapping.1.Ebs.Encrypted": "true", "BlockDeviceMapping.1.Ebs.DeleteOnTermination": "true", "MetadataOptions.HttpTokens": "required", "MetadataOptions.HttpPutResponseHopLimit": "1", "NetworkInterface.1.AssociatePublicIpAddress": "false", "NetworkInterface.1.PrivateIpAddress": "10.77.0.11"} {
				if captured.Get(key) != want {
					t.Errorf("%s: got %q want %q", key, captured.Get(key), want)
				}
			}
		})
	}
}
func TestOptionChecksRejectBeforeNativeConfiguration(t *testing.T) {
	for _, action := range []string{"check", "image.build", "machine.create", "network.create"} {
		for _, tc := range []struct {
			options *lc.AWSMachineOptions
			field   string
			code    lc.Code
		}{
			{&lc.AWSMachineOptions{InstanceType: "forbidden"}, "instanceType", lc.Unsupported},
			{&lc.AWSMachineOptions{CPUCredits: "standard"}, "cpuCredits", lc.Invalid},
			{&lc.AWSMachineOptions{Volume: &lc.AWSVolumeOptions{IOPS: aw.Int(16000)}}, "volume.iops", lc.Invalid},
			{&lc.AWSMachineOptions{Volume: &lc.AWSVolumeOptions{Throughput: aw.Int(751)}}, "volume.throughput", lc.Invalid},
		} {
			c := optionCommand()
			c.Action = action
			c.Machine.DiskGiB = 20
			c.Machine.Spec.Disk = "20Gi"
			c.Environment.AWS.Types = []lc.InstanceType{{Name: "m5.large", CPU: 2, MemoryMiB: 8192}}
			c.Machine.Spec.Platform.AWS = tc.options
			_, err := (&Provider{}).Execute(context.Background(), c, nil)
			var p *lc.Error
			if !errors.As(err, &p) || p.Code != tc.code || p.Field != "machines.one.platform.aws."+tc.field {
				t.Fatalf("%s reached native configuration: %v", action, err)
			}
		}
	}
}
