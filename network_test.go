package labcentral

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestInternetRequiresOperatorPermission(t *testing.T) {
	src, err := sourceYAML(t, []byte(sample+"networks: {lab: {egress: internet}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"aws", "proxmox"} {
		env := testEnvironment()
		if platform == "proxmox" {
			env = platformOptionsProxmoxEnvironment()
		}
		env.Platform = platform
		_, err = PlanLab(src, env, nil)
		requireField(t, err, Unsupported, "networks.lab.egress")
		if !strings.Contains(err.Error(), "environment."+platform+".allowEgress") {
			t.Fatal("missing operator setting", err)
		}
		if platform == "aws" {
			env.AWS.AllowEgress = true
		} else {
			env.Proxmox.AllowEgress = true
			env.Proxmox.Gateway = "10.90.0.1"
		}
		plan, err := PlanLab(src, env, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(plan.Notices, " "), "not filtered beyond private-range blocking") {
			t.Fatal("missing egress disclosure", plan.Notices)
		}
	}
}
func TestGatewayEnvironmentDefaultsAndValidation(t *testing.T) {
	env := testEnvironment()
	if err := ValidateEnvironment(env); err != nil {
		t.Fatal(err)
	}
	if got := env.AWS.Gateway.Effective(); got.Base != "ubuntu-24.04" || got.InstanceType != "t3.micro" {
		t.Fatal(got)
	}
	env.AWS.Gateway.Base = "missing"
	requireField(t, ValidateEnvironment(env), Invalid, "environment.aws.gateway.base")
	env.AWS.Gateway.Base = "ubuntu-24.04"
	for _, name := range []string{"t3.micro;reboot", "t3", "T3.micro", "t3.micro\n"} {
		env.AWS.Gateway.InstanceType = name
		requireField(t, ValidateEnvironment(env), Invalid, "environment.aws.gateway.instanceType")
	}
}

type gatewayFailureProvider struct {
	*fakeProvider
	lose, failDelete bool
	actions          []string
}

func (p *gatewayFailureProvider) Execute(ctx context.Context, c Command, o Observer) (Result, error) {
	p.actions = append(p.actions, c.Action+":"+c.Prior.Kind)
	if c.Action == "gateway.create" {
		if c.Machine.HostPrivateKey == "" || len(c.ClientPrivateKey) == 0 {
			return Result{}, fmt.Errorf("gateway credentials absent")
		}
		if !c.Network.Complete {
			return Result{}, fmt.Errorf("gateway before network")
		}
	}
	if c.Action == "machine.create" && c.Environment.Platform == "aws" && c.Gateway.Kind != "gateway" {
		return Result{}, fmt.Errorf("machine before gateway")
	}
	if c.Action == "resource.destroy" && c.Prior.Kind == "gateway" && p.failDelete {
		p.failDelete = false
		return Result{}, errors.New("interrupted gateway deletion")
	}
	r, err := p.fakeProvider.Execute(ctx, c, o)
	if c.Action == "gateway.create" && p.lose {
		p.lose = false
		return Result{}, errors.New("gateway response lost")
	}
	return r, err
}
func TestGatewayLostCreateAndDeleteRecoverAcrossReopen(t *testing.T) {
	ctx := context.Background()
	p := &gatewayFailureProvider{fakeProvider: freshFake(), lose: true}
	root := t.TempDir()
	engine, err := Open(root, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanLab(testSource(t), testEnvironment(), nil)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := engine.Deploy(ctx, plan)
	if err == nil || inst.Gateway == nil {
		t.Fatal("lost outcome accepted", err)
	}
	if p.creates != 2 {
		t.Fatal("machine created before ready gateway", p.creates)
	}
	engine.Close()
	engine, err = Open(root, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	ready, err := engine.Recover(ctx, inst.ID)
	if err != nil || ready.Status != "ready" || p.creates != 3 {
		t.Fatal("gateway replaced instead of recovered", err, p.creates)
	}
	if len(ready.Plan.Machines) != 1 || len(ready.Resources) != 3 {
		t.Fatal("gateway entered author inventory", ready)
	}
	p.failDelete = true
	if err = engine.Destroy(ctx, inst.ID); err == nil {
		t.Fatal("lost delete accepted")
	}
	if _, ok := p.resources[inst.ID+"/network"]; !ok {
		t.Fatal("network removed while gateway uncertain")
	}
	if err = engine.Destroy(ctx, inst.ID); err != nil {
		t.Fatal(err)
	}
	if len(p.resources) != 0 {
		t.Fatal("resources leaked", p.resources)
	}
}
func TestLegacyAWSRecordRecoversAndDestroysWithoutGateway(t *testing.T) {
	ctx := context.Background()
	p := freshFake()
	engine := openFake(t, t.TempDir(), p)
	plan, err := PlanLab(testSource(t), testEnvironment(), nil)
	if err != nil {
		t.Fatal(err)
	}
	inst := Instance{ID: strings.Repeat("a", 24), Status: "ready", Plan: plan}
	// Seed the original complete intents and public machine record directly, as a
	// retained v2 installation would have saved them before gateway support.
	for _, c := range []Command{{Action: "network.create", Operation: inst.ID + "/network", Owner: inst.ID}, {Action: "machine.create", Operation: inst.ID + "/machine/one", Owner: inst.ID, Machine: PlannedMachine{Name: plan.Machines[0].Name, HostPrivateKey: "fixture"}, ClientPrivateKey: []byte("fixture")}} {
		r, err := p.Execute(ctx, c, nil)
		if err != nil {
			t.Fatal(err)
		}
		if c.Action == "machine.create" {
			c.Operation = inst.ID + "/machine/" + c.Machine.Name
		}
		if _, err = engine.state.Intent(ctx, c.Operation, inst.ID, c); err != nil {
			t.Fatal(err)
		}
		if err = engine.state.Complete(ctx, c.Operation, r); err != nil {
			t.Fatal(err)
		}
		inst.Resources = append(inst.Resources, r.Resource)
	}
	if err = engine.state.Put(ctx, "instance", inst.ID, inst); err != nil {
		t.Fatal(err)
	}
	recovered, err := engine.Recover(ctx, inst.ID)
	if err != nil || recovered.Gateway != nil || p.creates != 2 {
		t.Fatal("legacy lab was retrofitted", err, p.creates)
	}
	if err = engine.Destroy(ctx, inst.ID); err != nil {
		t.Fatal(err)
	}
	if len(p.resources) != 0 {
		t.Fatal("legacy public machines leaked")
	}
}
func TestDestroyFindsCompletedGatewayMissingFromInstance(t *testing.T) {
	ctx := context.Background()
	p := freshFake()
	engine := openFake(t, t.TempDir(), p)
	plan, _ := PlanLab(testSource(t), testEnvironment(), nil)
	inst := Instance{ID: strings.Repeat("b", 24), Status: "recovery_required", Plan: plan}
	cmd := Command{Action: "gateway.create", Operation: inst.ID + "/gateway", Owner: inst.ID}
	r, _ := p.Execute(ctx, cmd, nil)
	engine.state.Intent(ctx, cmd.Operation, inst.ID, cmd)
	engine.state.Complete(ctx, cmd.Operation, r)
	engine.state.Put(ctx, "instance", inst.ID, inst)
	if err := engine.Destroy(ctx, inst.ID); err != nil {
		t.Fatal(err)
	}
	if len(p.resources) != 0 {
		t.Fatal("complete but unmaterialized gateway leaked")
	}
}
func TestGatewayManagementRejectsMissingPin(t *testing.T) {
	data, _ := json.Marshal(GatewayIdentity{ENI: "eni-owned"})
	if _, err := MachineDialer(Resource{Kind: "gateway", Address: "192.0.2.1", Complete: true, Data: data}, nil); err == nil {
		t.Fatal("missing gateway pin accepted")
	}
}

func TestExpectedGatewayCannotFallBackToDirectAccess(t *testing.T) {
	inst := Instance{ID: "owner", Gateway: &PlannedMachine{Name: "gateway"}}
	if _, err := MachineDialer(InstanceGateway(inst), nil); err == nil {
		t.Fatal("missing durable gateway became direct management")
	}
}

func TestLostGatewayCreateCanDestroyWithoutReadiness(t *testing.T) {
	ctx := context.Background()
	p := &gatewayFailureProvider{fakeProvider: freshFake(), lose: true}
	engine, err := Open(t.TempDir(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	plan, err := PlanLab(testSource(t), testEnvironment(), nil)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := engine.Deploy(ctx, plan)
	if err == nil {
		t.Fatal("response loss accepted")
	}
	if err = engine.Destroy(ctx, inst.ID); err != nil {
		t.Fatal("uncertain gateway discovery could not clean up", err)
	}
	if len(p.resources) != 0 || p.creates != 2 {
		t.Fatal("cleanup leaked or provisioned machines", p.creates, p.resources)
	}
}
