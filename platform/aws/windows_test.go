package aws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/OutboundSpade/LabCentral/v2/internal/sshtest"
	"github.com/OutboundSpade/LabCentral/v2/internal/windows"
)

func TestInterruptedWindowsCreateRecoversExactOwnedInstance(t *testing.T) {
	f, a := newNetworkEC2(t)
	f.imagePlatform = "windows"
	p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
	c := networkCommand(false)
	network, err := p.network(context.Background(), a, c)
	if err != nil {
		t.Fatal(err)
	}
	c.Network = network.Resource
	c.Operation = "owner/machine/server"
	private, pub := sshtest.Key(t)
	var readiness atomic.Int32
	server := sshtest.New(t, nil, func(command string) string {
		readiness.Add(1)
		if command != windows.Command(windows.Ready+windows.DesktopReady) {
			t.Error("wrong Windows readiness command")
		}
		return ""
	})
	gateway := sshtest.New(t, func(string) string { return server.Address }, nil)
	data, _ := json.Marshal(lc.GatewayIdentity{HostPublicKey: gateway.HostKey, ENI: "eni-gateway"})
	c.Gateway = lc.Resource{Kind: "gateway", Owner: "owner", ID: "i-gateway", Address: gateway.Address, PrivateAddress: "10.77.255.5", Complete: true, Data: data}
	c.Environment.AWS.Types = []lc.InstanceType{{Name: "t3.medium", CPU: 2, MemoryMiB: 4096, MaxNICs: 2}}
	c.Machine = lc.PlannedMachine{Name: "server", OS: "windows", NativeImage: "ami-base", InstanceType: "t3.medium", CPU: 2, MemoryMiB: 4096, DiskGiB: 30, Address: "10.77.0.11", PublicKey: pub, HostPublicKey: server.HostKey, HostPrivateKey: string(private), Spec: lc.Machine{Image: "windows", CPU: 2, Memory: "4Gi", Disk: "30Gi", Access: []string{"desktop", "ssh"}}, NICs: []lc.PlannedNIC{{Network: "lab", Address: "10.77.0.11", CIDR: "10.77.0.0/24"}}}
	c.ClientPrivateKey = private
	f.loss = "RunInstances:machine:server"
	if _, err = p.machine(context.Background(), a, c); err == nil {
		t.Fatal("response loss assumed failure")
	}
	c.Recovery = true
	result, err := p.machine(context.Background(), a, c)
	if err != nil || !result.Resource.Complete {
		t.Fatal("Windows create did not recover", err)
	}
	launches := 0
	for _, request := range f.requests {
		if request.Get("Action") == "RunInstances" {
			launches++
			ud, err := base64.StdEncoding.DecodeString(request.Get("UserData"))
			if err != nil || !strings.HasPrefix(string(ud), "<powershell>") {
				t.Fatal("non-Windows user data", err)
			}
			requestValue(t, request, "NetworkInterface.1.AssociatePublicIpAddress", "false")
			requestValue(t, request, "IamInstanceProfile.Name", c.Environment.AWS.DesktopProfile)
		}
	}
	if launches != 1 || readiness.Load() != 1 || gateway.Connections.Load() != 1 {
		t.Fatal("recovery duplicated create or bypassed gateway", launches, readiness.Load(), gateway.Connections.Load())
	}
	c.Prior = result.Resource
	destroyed, err := p.destroy(context.Background(), a, c)
	if err != nil || !destroyed.Absent {
		t.Fatal("owned Windows cleanup unconfirmed", err)
	}
}

func TestAMIPlatformMismatchFailsBeforeLaunch(t *testing.T) {
	if err := checkAMIPlatform(ec2types.Image{}, "windows"); err == nil {
		t.Fatal("Linux AMI bound as Windows")
	}
	if err := checkAMIPlatform(ec2types.Image{Platform: ec2types.PlatformValuesWindows}, ""); err == nil {
		t.Fatal("Windows AMI bound as Linux")
	}
	if err := checkAMIPlatform(ec2types.Image{Platform: ec2types.PlatformValuesWindows}, "windows"); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsBuildPreparationDoesNotImportEC2KeyPair(t *testing.T) {
	// No environment or SDK configuration is needed: Windows installs its key in
	// platform user data, before SSH, and never attaches an EC2 Ed25519 key pair.
	p := &Provider{}
	if err := p.PrepareBuild(context.Background(), lc.Command{Image: lc.PlannedImage{Base: lc.Base{OS: "windows"}}}, "ssh-ed25519 key"); err != nil {
		t.Fatal(err)
	}
}
func TestWindowsInterruptedBuildCleanupUsesVerifiedOwnerTags(t *testing.T) {
	f, a := newNetworkEC2(t)
	p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
	f.objects = append(f.objects, networkObject{kind: "instance", id: "i-build", role: "base"})
	c := lc.Command{Owner: "owner", Image: lc.PlannedImage{Name: "base", Base: lc.Base{OS: "windows"}}}
	if err := p.cleanupBuild(context.Background(), a, c); err != nil {
		t.Fatal(err)
	}
	if !f.terminated["i-build"] {
		t.Fatal("interrupted Windows build was not found")
	}
	for _, request := range f.requests {
		switch request.Get("Action") {
		case "DescribeInstances":
			if request.Get("Filter.1.Name") == "key-name" {
				t.Fatal("Windows build discovery depends on an EC2 key pair")
			}
		case "ImportKeyPair", "DescribeKeyPairs", "DeleteKeyPair":
			t.Fatal("Windows cleanup used an EC2 key pair")
		}
	}
}

func TestWindowsBuildCleanupRetainsUnownedInstance(t *testing.T) {
	f, a := newNetworkEC2(t)
	f.objects = append(f.objects, networkObject{kind: "instance", id: "i-build", role: "base"})
	opts := a.Options()
	opts.HTTPClient = optionHTTPClient(func(r *http.Request) (*http.Response, error) {
		response, err := f.do(r)
		if err != nil {
			return response, err
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		response.Body = io.NopCloser(strings.NewReader(strings.ReplaceAll(string(body), "<value>owner</value>", "<value>somebody-else</value>")))
		return response, nil
	})
	p := &Provider{}
	err := p.cleanupBuild(context.Background(), ec2.New(opts), lc.Command{Owner: "owner", Image: lc.PlannedImage{Name: "base", Base: lc.Base{OS: "windows"}}})
	if err == nil || !strings.Contains(err.Error(), "ownership_mismatch") || f.terminated["i-build"] {
		t.Fatal("Windows build cleanup deleted an unowned instance", err)
	}
}
