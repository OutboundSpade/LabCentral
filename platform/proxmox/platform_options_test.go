package proxmox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
)

func boolOption(v bool) *bool { return &v }
func TestRuntimeOptionsInNativeConfigPUT(t *testing.T) {
	cases := []struct {
		name     string
		options  *lc.ProxmoxMachineOptions
		cpu, nic string
		balloon  *int
	}{
		{name: "defaults", nic: "virtio"},
		{name: "empty", options: &lc.ProxmoxMachineOptions{}, nic: "virtio"},
	}
	for _, model := range []string{"virtio", "e1000", "e1000e", "vmxnet3", "rtl8139"} {
		zero := 0
		cases = append(cases, struct {
			name     string
			options  *lc.ProxmoxMachineOptions
			cpu, nic string
			balloon  *int
		}{model, &lc.ProxmoxMachineOptions{CPUType: "host", NICModel: model, MemoryBalloon: boolOption(false)}, "host", model, &zero})
	}
	memory := 4096
	cases = append(cases, struct {
		name     string
		options  *lc.ProxmoxMachineOptions
		cpu, nic string
		balloon  *int
	}{"balloon", &lc.ProxmoxMachineOptions{CPUType: "kvm64", MemoryBalloon: boolOption(true)}, "kvm64", "virtio", &memory})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var captured map[string]any
			puts := 0
			c, env := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api2/json/nodes/node/qemu/2100/config" {
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(500)
					return
				}
				if r.Method == "GET" {
					response(w, VMConfig{Description: marker("owned-lab", "one"), Net0: "e1000e=AA:BB:CC:DD:EE:FF,bridge=old", Digest: "clone-digest"})
				} else if r.Method == "PUT" {
					if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
						t.Error(err)
					}
					puts++
					// Stop after capture; no fake acceptance of guest boot or networking.
					w.WriteHeader(400)
				} else {
					t.Errorf("unexpected mutation %s", r.Method)
					w.WriteHeader(500)
				}
			})
			env.Proxmox.CPUTypes = []string{"host", "kvm64"}
			env.Proxmox.Bridge = "vmbr-labs"
			cmd := lc.Command{Owner: "owned-lab", Operation: "op", Environment: env, VLAN: 123, Network: lc.Resource{Data: []byte(`{"cidr":"10.90.0.0/24","gateway":"10.90.0.1"}`)}, Machine: lc.PlannedMachine{Name: "one", NativeImage: "2000", VMID: 2100, CPU: 2, MemoryMiB: 4096, DiskGiB: 32, Address: "10.90.0.11", PublicKey: "ssh-ed25519 public", Spec: lc.Machine{Platform: lc.MachinePlatform{Proxmox: tc.options, AWS: &lc.AWSMachineOptions{InstanceType: "forbidden", CPUCredits: "unlimited"}}}}}
			p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
			if err := p.Journal.Save("op/clone", 2100); err != nil {
				t.Fatal(err)
			}
			_, err := p.machine(context.Background(), c, cmd)
			if err == nil || puts != 1 {
				t.Fatalf("PUT was not captured once: puts=%d err=%v", puts, err)
			}
			if captured["net0"] != tc.nic+"=AA:BB:CC:DD:EE:FF,bridge=vmbr-labs,tag=123,firewall=1" {
				t.Fatal("MAC/model/policy changed", captured)
			}
			if cpu, _ := captured["cpu"].(string); cpu != tc.cpu {
				t.Fatal("CPU model lost", captured)
			}
			if tc.balloon == nil {
				if _, ok := captured["balloon"]; ok {
					t.Fatal("omission did not preserve clone balloon", captured)
				}
			} else if captured["balloon"] != float64(*tc.balloon) {
				t.Fatal("balloon option lost", captured)
			}
			if captured["memory"] != float64(4096) || captured["cores"] != float64(2) || captured["ipconfig0"] != "ip=10.90.0.11/24,gw=10.90.0.1" || captured["digest"] != "clone-digest" {
				t.Fatal("portable config changed", captured)
			}
			for _, forbidden := range []string{"bios", "machine", "scsihw", "scsi0", "ide0", "sata0"} {
				if _, ok := captured[forbidden]; ok {
					t.Fatal("base boot properties overridden", forbidden)
				}
			}
		})
	}
}
func TestCPUAllowlistRejectsBeforeNativeConfiguration(t *testing.T) {
	for _, action := range []string{"check", "image.build", "machine.create", "network.create"} {
		c := lc.Command{Action: action, Environment: lc.Environment{Platform: "proxmox"}, Machine: lc.PlannedMachine{Name: "one", Spec: lc.Machine{Platform: lc.MachinePlatform{Proxmox: &lc.ProxmoxMachineOptions{CPUType: "host"}}}}}
		_, err := (&Provider{}).Execute(context.Background(), c, nil)
		var p *lc.Error
		if !errors.As(err, &p) || p.Code != lc.Unsupported || p.Field != "machines.one.platform.proxmox.cpuType" {
			t.Fatalf("%s reached native configuration: %v", action, err)
		}
	}
	// A direct check also validates before its first fake HTTP call.
	c, env := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("HTTP reached for invalid options: %s", r.URL.Path)
	})
	_, err := (&Provider{}).check(context.Background(), c, lc.Command{Environment: env, Source: lc.Source{Lab: lc.Lab{Machines: map[string]lc.Machine{"one": {Platform: lc.MachinePlatform{Proxmox: &lc.ProxmoxMachineOptions{CPUType: "host"}}}}}}})
	if err == nil || !strings.Contains(err.Error(), "cpuType") {
		t.Fatal(err)
	}
}
