package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/OutboundSpade/LabCentral/v2/internal/sshtest"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

func computeMatrixFixture(t *testing.T, gateway bool) (*networkEC2, *ec2.Client, *Provider, lc.Command) {
	t.Helper()
	f, api := newNetworkEC2(t)
	p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
	c := twoNetworks()
	network, err := p.network(context.Background(), api, c)
	if err != nil {
		t.Fatal(err)
	}
	c.Network = network.Resource
	private, public := sshtest.Key(t)
	server := sshtest.New(t, nil, nil)
	c.ClientPrivateKey = private
	if gateway {
		c.Action = "gateway.create"
		c.Operation = "owner/gateway"
		c.Prior.Kind = "gateway"
		c.Machine = lc.PlannedMachine{Name: "gateway", NativeImage: "ami-base", InstanceType: "t3.micro", DiskGiB: 8, PublicKey: public, HostPrivateKey: string(private), HostPublicKey: server.HostKey}
		f.public = server.Address
	} else {
		jump := sshtest.New(t, func(string) string { return server.Address }, nil)
		topology := lc.Plan{Source: c.Source, Environment: c.Environment}
		for _, name := range lc.SortedMachineNames(c.Source.Lab) {
			topology.Machines = append(topology.Machines, lc.PlannedMachine{Name: name, Spec: c.Source.Lab.Machines[name]})
		}
		if err = lc.PlanNetworks(&topology, nil); err != nil {
			t.Fatal(err)
		}
		c.Machine = topology.Machines[2]
		c.Machine.NativeImage = "ami-base"
		c.Machine.InstanceType = "t3.large"
		c.Machine.DiskGiB = 20
		c.Machine.PublicKey = public
		c.Machine.HostPrivateKey = string(private)
		c.Machine.HostPublicKey = server.HostKey
		data, _ := json.Marshal(lc.GatewayIdentity{HostPublicKey: jump.HostKey, ENI: "eni-gateway"})
		c.Gateway = lc.Resource{Kind: "gateway", Owner: "owner", ID: "i-gateway", Address: jump.Address, Complete: true, Data: data}
		c.Action = "machine.create"
		c.Operation = "owner/machine/router"
	}
	f.requests = nil
	return f, api, p, c
}
func computeMatrixRun(ctx context.Context, p *Provider, api *ec2.Client, c lc.Command, gateway bool) (lc.Result, error) {
	if gateway {
		return p.gateway(ctx, api, c)
	}
	return p.machine(ctx, api, c)
}
func computeBoundaries(t *testing.T, gateway bool) []boundary {
	t.Helper()
	f, api, p, c := computeMatrixFixture(t, gateway)
	if _, err := computeMatrixRun(context.Background(), p, api, c, gateway); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	var out []boundary
	for _, request := range f.requests {
		action := request.Get("Action")
		if strings.HasPrefix(action, "Describe") {
			continue
		}
		counts[action]++
		out = append(out, boundary{action, counts[action]})
	}
	return out
}
func TestAWSComputeFailureMatrix(t *testing.T) {
	for _, gateway := range []bool{false, true} {
		for _, step := range computeBoundaries(t, gateway) {
			for _, fault := range []string{"response-loss", "reset-before", "authorization", "validation", "throttle", "server"} {
				t.Run(fmt.Sprintf("gateway=%t/%s/%d/%s", gateway, step.action, step.nth, fault), func(t *testing.T) {
					f, api, p, c := computeMatrixFixture(t, gateway)
					root := p.Journal.Root
					role := "machine:router"
					if gateway {
						role = "gateway"
					}
					f.objects = append(f.objects, networkObject{kind: "instance", id: "i-foreign", role: role})
					f.owners = map[string]string{"i-foreign": "foreign"}
					broken, hit := boundaryClient(t, f, api, step, fault)
					_, _ = computeMatrixRun(context.Background(), p, broken, c, gateway)
					if !*hit {
						t.Fatal("boundary not reached")
					}
					p = &Provider{Journal: journal.Journal{Root: root}}
					c.Recovery = true
					result, err := computeMatrixRun(context.Background(), p, api, c, gateway)
					if err != nil {
						if step.action != "RunInstances" || (fault != "reset-before" && fault != "server") {
							t.Fatal("restart failed", err)
						}
						if !strings.Contains(err.Error(), "recovery_required") {
							t.Fatal("unknown outcome not actionable", err)
						}
						count := len(f.objects)
						if _, retry := computeMatrixRun(context.Background(), p, api, c, gateway); retry == nil || len(f.objects) != count {
							t.Fatal("uncertain launch replayed", retry)
						}
						return
					}
					c.Prior = result.Resource
					if result, err = p.destroy(context.Background(), api, c); err != nil || !result.Absent {
						t.Fatal(result, err)
					}
					c.Prior = c.Network
					if result, err = p.destroyNetwork(context.Background(), api, c); err != nil || !result.Absent {
						t.Fatal(result, err)
					}
					if len(f.objects) != 1 || f.objects[0].id != "i-foreign" {
						t.Fatal("leak or foreign deletion", f.objects)
					}
				})
			}
		}
	}
}

func TestAWSInstanceChildrenOwnershipAndDetachedNIC(t *testing.T) {
	for _, fault := range []string{"foreign-attached", "unmarked-attached", "foreign-detached", "unmarked-detached", "owned-detached"} {
		t.Run(fault, func(t *testing.T) {
			f, api, p, c := computeMatrixFixture(t, false)
			ctx := context.Background()
			result, err := computeMatrixRun(ctx, p, api, c, false)
			if err != nil {
				t.Fatal(err)
			}
			c.Prior = result.Resource
			nic := ""
			for _, child := range c.Prior.Children {
				if child.Kind == "eni" {
					nic = child.ID
				}
			}
			if nic == "" {
				t.Fatal("missing native child receipt")
			}
			if strings.Contains(fault, "detached") {
				f.detached = map[string]bool{nic: true}
			}
			if strings.HasPrefix(fault, "foreign") {
				f.owners = map[string]string{nic: "foreign"}
			}
			if strings.HasPrefix(fault, "unmarked") {
				f.tagsRemoved = map[string]bool{nic: true}
			}
			p = &Provider{Journal: journal.Journal{Root: p.Journal.Root}}
			result, err = p.destroy(ctx, api, c)
			if fault != "owned-detached" {
				if err == nil || !strings.Contains(err.Error(), "ownership_mismatch") || f.terminated[c.Prior.ID] {
					t.Fatal("unsafe child cleanup", result, err, f.terminated)
				}
				f.owners = map[string]string{}
				f.tagsRemoved = nil
				result, err = p.destroy(ctx, api, c)
			}
			if err != nil || !result.Absent {
				t.Fatal(result, err)
			}
			if strings.Contains(fault, "detached") && !f.enisDeleted[nic] {
				t.Fatal("detached known NIC leaked")
			}
		})
	}
}
