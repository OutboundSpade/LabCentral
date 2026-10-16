package build

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
)

func TestAWSBuildInstanceTypeInPacker(t *testing.T) {
	for _, requested := range []string{"", "m5.large"} {
		c, dir := fixture(t, "aws")
		c.Environment.AWS.Types = []lc.InstanceType{{Name: "m5.large", CPU: 2, MemoryMiB: 8192}}
		c.Image.BuildInstanceType = requested
		cfg, err := Generate(c, dir)
		if err != nil {
			t.Fatal(err)
		}
		var builder map[string]any
		if err := json.Unmarshal(cfg.Builders[0], &builder); err != nil {
			t.Fatal(err)
		}
		want := requested
		if want == "" {
			want = "t3.medium"
		}
		if builder["instance_type"] != want {
			t.Fatalf("got %v want %s", builder["instance_type"], want)
		}
	}
	c, dir := fixture(t, "aws")
	c.Image.BuildInstanceType = "forbidden"
	_, err := Generate(c, dir)
	var p *lc.Error
	if !errors.As(err, &p) || p.Code != lc.Unsupported || p.Field != "images.base.platform.aws.buildInstanceType" {
		t.Fatal("unpermitted build type accepted", err)
	}
}

type captureValidationProvider struct {
	machine lc.PlannedMachine
	gateway lc.PlannedMachine
	calls   int
}

var capturedValidation = errors.New("fake validation request captured")

func (p *captureValidationProvider) Execute(_ context.Context, c lc.Command, _ lc.Observer) (lc.Result, error) {
	p.calls++
	if c.Action == "network.create" {
		return lc.Result{Resource: lc.Resource{Kind: "network", ID: "native-fake", Data: []byte(`{}`)}}, nil
	}
	if c.Action == "gateway.create" {
		p.gateway = c.Machine
		return lc.Result{Resource: lc.Resource{Kind: "gateway", ID: "fake-gateway"}}, nil
	}
	p.machine = c.Machine
	return lc.Result{}, capturedValidation
}
func TestBuildInstanceTypeInValidationVM(t *testing.T) {
	for _, platform := range []string{"aws", "proxmox"} {
		c, _ := fixture(t, platform)
		c.Environment.AWS.Types = []lc.InstanceType{{Name: "m5.large", CPU: 4, MemoryMiB: 8192}}
		c.Environment.Proxmox.CIDR, c.Environment.Proxmox.VLANStart, c.Environment.Proxmox.VLANEnd = "10.90.0.0/24", 100, 100
		c.VLAN = 100
		if platform == "proxmox" {
			c.Gateway.Address = "192.0.2.4"
		}
		c.Image.BuildInstanceType = "m5.large"
		p := &captureValidationProvider{}
		_, err := ValidateNative(context.Background(), t.TempDir(), p, c, lc.Resource{ID: "native-image"})
		if !errors.Is(err, capturedValidation) {
			t.Fatal(err)
		}
		want, cpu, memory := "m5.large", 4, 8192
		if platform == "proxmox" {
			want, cpu, memory = "t3.medium", 2, 4096
		}
		if p.machine.InstanceType != want || p.machine.CPU != cpu || p.machine.MemoryMiB != memory {
			t.Fatal(platform, "validation type/capacity lost", p.machine)
		}
		if platform == "proxmox" && p.gateway.Address != c.Gateway.Address {
			t.Fatal("qualification lost its static management reservation", p.gateway)
		}
		if p.machine.Spec.Platform.AWS != nil || p.machine.Spec.Platform.Proxmox != nil {
			t.Fatal("runtime options entered image validation")
		}
	}
	c, _ := fixture(t, "aws")
	c.Image.BuildInstanceType = "forbidden"
	p := &captureValidationProvider{}
	if _, err := ValidateNative(context.Background(), t.TempDir(), p, c, lc.Resource{}); err == nil || p.calls != 0 {
		t.Fatal("provisioned before validation", err, p.calls)
	}
}
