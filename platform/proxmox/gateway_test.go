package proxmox

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
)

func TestGatewayDiscoveryDoesNotRequireGuestReadiness(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || !strings.HasSuffix(r.URL.Path, "/config") {
			t.Fatal("discovery required readiness or changed VM", r.URL.Path)
		}
		response(w, VMConfig{Description: marker("owned", "gateway")})
	})
	cmd := lc.Command{Action: "discover", Owner: "owned", Machine: lc.PlannedMachine{Name: "gateway", VMID: 2100, HostPublicKey: "pinned"}}
	result, err := (&Provider{}).findGateway(context.Background(), c, cmd)
	if err != nil || result.Resource.Kind != "gateway" || result.Resource.ID != "2100" || result.Resource.Complete {
		t.Fatal("unknown gateway cannot be cleaned up", result, err)
	}
	cmd.Owner = "different"
	if _, err = (&Provider{}).findGateway(context.Background(), c, cmd); err == nil {
		t.Fatal("foreign gateway discovered")
	}
}
func TestLabBridgeRejectsUplinkAddressAndMissingVLANAwareness(t *testing.T) {
	for _, b := range []bridge{{Interface: "vmbr3", Type: "bridge", VLANAware: 1, Ports: "eno1"}, {Interface: "vmbr3", Type: "bridge", VLANAware: 1, Address: "192.0.2.1"}, {Interface: "vmbr3", Type: "bridge", VLANAware: 1, CIDR: "192.0.2.1/24"}, {Interface: "vmbr3", Type: "bridge"}} {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				t.Fatal("check mutated bridge")
			}
			response(w, []bridge{b})
		})
		if err := checkLabBridge(context.Background(), c, "vmbr3"); err == nil {
			t.Fatal("unsafe bridge accepted", b)
		}
	}
}
func TestCheckPrivilegeAndStorageErrorsAreActionable(t *testing.T) {
	c, e := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "permissions") {
			response(w, map[string]any{})
		} else {
			response(w, map[string]any{"active": 1, "enabled": 1, "content": "iso"})
		}
	})
	if err := checkPermissions(context.Background(), c, e); err == nil || !strings.Contains(err.Error(), "privilege-separated token") {
		t.Fatal(err)
	}
	if err := checkStorage(context.Background(), c, "local"); err == nil || !strings.Contains(err.Error(), "support images") {
		t.Fatal(err)
	}
}

func TestGatewayRecoveryPreservesManagementMAC(t *testing.T) {
	if got := gatewayNIC("virtio=AA:BB:CC:DD:EE:FF,bridge=vmbr0", "vmbr0", 0); got != "virtio=AA:BB:CC:DD:EE:FF,bridge=vmbr0" {
		t.Fatal(got)
	}
	if got := gatewayNIC("virtio=AA:BB:CC:DD:EE:01,bridge=vmbr3,tag=100", "vmbr3", 100); got != "virtio=AA:BB:CC:DD:EE:01,bridge=vmbr3,tag=100" {
		t.Fatal(got)
	}
}

func TestManagementDiscoveryBindsMACAndReturnsSubnet(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/config") {
			response(w, VMConfig{Net0: "virtio=AA:BB:CC:DD:EE:00,bridge=vmbr0"})
			return
		}
		response(w, map[string]any{"result": []map[string]any{{"name": "eth0", "hardware-address": "aa:bb:cc:dd:ee:00", "ip-addresses": []map[string]any{{"ip-address": "192.0.2.20", "prefix": 24, "ip-address-type": "ipv4"}}}, {"name": "eth1", "hardware-address": "aa:bb:cc:dd:ee:01", "ip-addresses": []map[string]any{{"ip-address": "10.80.0.1", "prefix": 24, "ip-address-type": "ipv4"}}}}})
	})
	address, subnet, err := c.managementNetwork(context.Background(), 2100)
	if err != nil || address != "192.0.2.20" || subnet != "192.0.2.0/24" {
		t.Fatal(address, subnet, err)
	}
}

func TestAuthorMachineNamedGatewayDoesNotBecomePlatformGateway(t *testing.T) {
	_, e := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/config") {
			response(w, VMConfig{Description: marker("owner", "gateway")})
		} else {
			response(w, map[string]string{"status": "running"})
		}
	})
	result, err := (&Provider{}).Execute(context.Background(), lc.Command{Action: "discover", Owner: "owner", Environment: e, Machine: lc.PlannedMachine{Name: "gateway", VMID: 2001, Address: "10.80.0.11"}}, nil)
	if err != nil || result.Resource.Kind != "machine" || result.Resource.Name != "gateway" {
		t.Fatal("author name collided with platform role", result, err)
	}
}

func TestPendingBridgeConfigCannotMasqueradeAsLiveIsolation(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []bridge{{Interface: "vmbr3", Type: "bridge", VLANAware: 1, Active: 1}}, "changes": "- bridge-ports eno1\n+ bridge-ports none\n"})
	})
	if err := checkLabBridge(context.Background(), c, "vmbr3"); err == nil || !strings.Contains(err.Error(), "pending network changes") {
		t.Fatal("pending isolation was treated as active", err)
	}
}

func TestGatewayRefusesChangedPinnedBaseBeforeClone(t *testing.T) {
	c, e := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Fatalf("changed base caused native mutation: %s", r.Method)
		}
		if strings.HasSuffix(r.URL.Path, "/network") {
			response(w, []bridge{{Interface: "vmbr3", Type: "bridge", VLANAware: 1, Active: 1}})
		} else {
			response(w, VMConfig{Template: 1, Digest: "changed"})
		}
	})
	e.Proxmox.LabBridge = "vmbr3"
	e.Bases = map[string]lc.Base{"ubuntu-24.04": {ID: "2100", Identity: "pinned", DiskGiB: 20}}
	network, _ := json.Marshal(network{Segments: []lc.PlannedNetwork{{Name: "private", VLAN: 100, CIDR: "10.80.0.0/24", Gateway: "10.80.0.1"}}})
	_, err := (&Provider{Journal: journal.Journal{Root: t.TempDir()}}).gateway(context.Background(), c, lc.Command{Owner: "0123456789abcdef0123456789abcdef", Environment: e, Machine: lc.PlannedMachine{Name: "gateway", NativeImage: "2100", CPU: 1, MemoryMiB: 768, DiskGiB: 20}, Network: lc.Resource{Data: network}})
	if err == nil || !strings.Contains(err.Error(), "base template identity changed") {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestMissingManagementBridgeHasReachabilityHint(t *testing.T) {
	c, e := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		response(w, []bridge{{Interface: "vmbr0", Type: "bridge", Active: 1}})
	})
	e.Proxmox.ManagementBridge = "missing"
	if err := checkOperatorBridges(context.Background(), c, e); err == nil || !strings.Contains(err.Error(), "operator SSH reachability") {
		t.Fatal(err)
	}
	e.Proxmox.ManagementBridge = "vmbr0"
	e.Build.Bridge = "vmbr0"
	if err := checkOperatorBridges(context.Background(), c, e); err != nil {
		t.Fatal(err)
	}
}

func TestAgentPropertyChecksEnabledFlag(t *testing.T) {
	for value, want := range map[string]bool{"": false, "0": false, "enabled=0,freeze-fs-on-backup=1": false, "1": true, "enabled=1": true, "type=virtio,enabled=1": true} {
		if got := guestAgentEnabled(value); got != want {
			t.Fatalf("%q: got %v want %v", value, got, want)
		}
	}
}

func TestRangeCheckRejectsForeignLabBridgeConnectionsAndFullVLANs(t *testing.T) {
	for _, kind := range []string{"foreign-vm", "foreign-container", "full-vlan"} {
		t.Run(kind, func(t *testing.T) {
			c, e := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Fatal("range check mutated provider")
				}
				id, typ := 102, "qemu"
				if kind == "foreign-container" {
					typ = "lxc"
				}
				if kind == "full-vlan" {
					id = 2000
				}
				if strings.Contains(r.URL.Path, "cluster/resources") {
					response(w, []map[string]any{{"vmid": id, "node": "node", "type": typ}})
					return
				}
				if typ == "lxc" {
					response(w, map[string]string{"net0": "name=eth0,bridge=vmbr3,tag=100"})
					return
				}
				desc := "foreign"
				if kind == "full-vlan" {
					desc = marker("owner", "server")
				}
				response(w, VMConfig{Description: desc, Net0: "virtio=AA:BB:CC:DD:EE:00,bridge=vmbr3,tag=100"})
			})
			e.Proxmox.VMIDStart = 2000
			e.Proxmox.VMIDEnd = 2099
			e.Proxmox.VLANStart = 100
			e.Proxmox.VLANEnd = 100
			e.Proxmox.LabBridge = "vmbr3"
			err := checkRanges(context.Background(), c, e)
			want := "outside the owned VMID range"
			if kind == "full-vlan" {
				want = "VLAN range is full"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%v, wanted %s", err, want)
			}
		})
	}
}
