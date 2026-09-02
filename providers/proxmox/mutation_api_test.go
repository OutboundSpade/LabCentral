package proxmox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMutationClientUsesOnlyAllowlistedMethodsPathsAndSafeFields(t *testing.T) {
	upidByType := func(taskType string) string {
		return fmt.Sprintf("UPID:pve-a:00000001:00000002:00000003:%s:101:labcentral@pve!qualification:", taskType)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "PVEAPIToken="+testTokenID+"="+testTokenSecret {
			t.Error("mutation request omitted API-token authorization")
		}
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /api2/json/access/permissions":
			writeAPIData(t, response, map[string]any{"/": map[string]any{"VM.Audit": 1}})
		case "GET /api2/json/cluster/nextid":
			assertQuery(t, request, "vmid", "101")
			writeAPIData(t, response, "101")
		case "GET /api2/json/nodes/pve-a/qemu/101/config":
			writeAPIData(t, response, map[string]any{"digest": strings.Repeat("a", 40), "description": "LCOWN1:test", "tags": "labcentral_test", "memory": 2048})
		case "POST /api2/json/nodes/pve-a/qemu/9000/clone":
			assertForm(t, request, map[string]string{"newid": "101", "full": "1", "target": "pve-a", "storage": "local-lvm", "format": "raw", "pool": "lab", "description": "LCOWN1:test", "name": "instance-vm"})
			writeAPIData(t, response, "UPID:pve-a:00000001:00000002:00000003:qmclone:9000:root@pam:")
		case "POST /api2/json/nodes/pve-a/qemu/101/config":
			assertForm(t, request, map[string]string{"digest": strings.Repeat("a", 40), "sockets": "1", "cores": "2", "cpulimit": "1.5", "memory": "2048", "ciupgrade": "0", "onboot": "0", "description": "LCOWN1:test", "tags": "labcentral_test", "net0": "virtio=02:00:00:00:00:01,bridge=vmbr1,tag=200", "ipconfig0": "ip=10.20.0.10/24"})
			writeAPIData(t, response, upidByType("qmconfig"))
		case "GET /api2/json/nodes/pve-a/qemu/101/status/current":
			writeAPIData(t, response, map[string]any{"status": "running", "qmpstatus": "paused", "vmid": 101, "name": "instance-vm"})
		case "POST /api2/json/nodes/pve-a/qemu/101/status/start":
			assertForm(t, request, map[string]string{})
			writeAPIData(t, response, upidByType("qmstart"))
		case "POST /api2/json/nodes/pve-a/qemu/101/status/shutdown":
			assertForm(t, request, map[string]string{})
			writeAPIData(t, response, upidByType("qmshutdown"))
		case "POST /api2/json/nodes/pve-a/qemu/101/status/suspend":
			assertForm(t, request, map[string]string{"todisk": "0"})
			writeAPIData(t, response, upidByType("qmpause"))
		case "POST /api2/json/nodes/pve-a/qemu/101/status/resume":
			assertForm(t, request, map[string]string{})
			writeAPIData(t, response, upidByType("qmresume"))
		case "POST /api2/json/nodes/pve-a/qemu/101/agent/ping":
			assertForm(t, request, map[string]string{})
			writeAPIData(t, response, nil)
		case "GET /api2/json/nodes/pve-a/tasks/UPID:pve-a:00000001:00000002:00000003:qmstart:101:labcentral@pve!qualification:/status":
			if strings.Contains(request.RequestURI, "%2521") || !strings.Contains(request.RequestURI, "%21") {
				t.Errorf("token-qualified task identity was not encoded exactly once: %s", request.RequestURI)
			}
			writeAPIData(t, response, map[string]any{"status": "stopped", "exitstatus": "OK"})
		case "DELETE /api2/json/nodes/pve-a/qemu/101":
			assertQuery(t, request, "purge", "0")
			assertQuery(t, request, "destroy-unreferenced-disks", "0")
			for _, forbidden := range []string{"force", "skiplock"} {
				if request.URL.Query().Get(forbidden) != "" {
					t.Errorf("destructive flag %s was sent", forbidden)
				}
			}
			writeAPIData(t, response, upidByType("qmdestroy"))
		default:
			t.Errorf("unexpected Proxmox mutation request %s %s", request.Method, request.URL.RequestURI())
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server, 0)
	defer client.CloseIdleConnections()
	ctx := context.Background()
	if _, err := client.EffectivePermissions(ctx); err != nil {
		t.Fatal(err)
	}
	if available, err := client.AdvisoryVMIDAvailable(ctx, 101); err != nil || !available {
		t.Fatalf("nextid = %t, %v", available, err)
	}
	if _, err := client.VMConfig(ctx, "pve-a", 101); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CloneVM(ctx, CloneRequest{SourceNode: "pve-a", SourceVMID: 9000, NewVMID: 101, Name: "instance-vm", TargetNode: "pve-a", StorageID: "local-lvm", Format: "raw", Pool: "lab", Description: "LCOWN1:test"}); err != nil {
		t.Fatal(err)
	}
	binding := NetworkBinding{LogicalResourceID: "network.lab", ProfileAlias: "isolated", Bridge: "vmbr1", VLAN: 200, MAC: "02:00:00:00:00:01", IPv4CIDR: "10.20.0.0/24", IPv4Address: "10.20.0.10"}
	if upid, err := client.ConfigureVM(ctx, ConfigureRequest{Node: "pve-a", VMID: 101, Digest: strings.Repeat("a", 40), Sockets: 1, Cores: 2, CPULimit: "1.5", MemoryMiB: 2048, Description: "LCOWN1:test", Tags: "labcentral_test", Networks: []NetworkBinding{binding}}); err != nil || upid.Type != "qmconfig" {
		t.Fatalf("configure UPID = %#v, %v", upid, err)
	}
	if status, err := client.VMStatus(ctx, "pve-a", 101); err != nil || status.Status != "paused" {
		t.Fatalf("status = %#v, %v", status, err)
	}
	for action, taskType := range map[string]string{"start": "qmstart", "stop": "qmshutdown", "pause": "qmpause", "resume": "qmresume"} {
		upid, err := client.LifecycleVM(ctx, LifecycleRequest{Node: "pve-a", VMID: 101, Action: action})
		if err != nil || upid.Type != taskType {
			t.Fatalf("%s UPID = %#v, %v", action, upid, err)
		}
	}
	if err := client.GuestAgentPing(ctx, "pve-a", 101); err != nil {
		t.Fatal(err)
	}
	startUPID, _ := ParseUPID(upidByType("qmstart"))
	if status, err := client.TaskStatus(ctx, "pve-a", startUPID); err != nil || status.ExitStatus != "OK" {
		t.Fatalf("task = %#v, %v", status, err)
	}
	if _, err := client.DeleteVM(ctx, "pve-a", 101); err != nil {
		t.Fatal(err)
	}
}

func TestEffectivePermissionsQueriesExactScopesAndAcceptsNonPropagatingGrants(t *testing.T) {
	wanted := []string{"/nodes/pve", "/storage/local-lvm", "/vms/900000"}
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		path := request.URL.Query().Get("path")
		if !contains(wanted, path) {
			t.Errorf("unexpected permission path %q", path)
		}
		writeAPIData(t, response, map[string]any{path: map[string]any{"VM.Audit": 0}})
	}))
	defer server.Close()
	client := newTestClient(t, server, 0)
	client.permissionPaths = append([]string(nil), wanted...)
	permissions, err := client.EffectivePermissions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range wanted {
		if values := permissions.Paths[path]; len(values) != 1 || values[0] != "VM.Audit" {
			t.Fatalf("permissions[%q] = %#v", path, values)
		}
		if values := permissions.PropagatingPaths[path]; len(values) != 0 {
			t.Fatalf("propagating permissions[%q] = %#v", path, values)
		}
	}
}

func TestMutationClientRejectsUnsafePathInputsAndUPIDsBeforeRequest(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unsafe mutation input reached the HTTP client")
	}))
	defer server.Close()
	client := newTestClient(t, server, 0)
	defer client.CloseIdleConnections()
	if _, err := client.CloneVM(context.Background(), CloneRequest{SourceNode: "../cluster", SourceVMID: 9000, NewVMID: 101}); err == nil {
		t.Fatal("unsafe clone path was accepted")
	}
	for _, value := range []string{"", "UPID:pve-a:bad", "UPID:../pve:00000001:00000002:00000003:qmstart:101:root@pam:", "UPID:pve-a:00000001:00000002:00000003:evil/action:101:root@pam:"} {
		if _, err := ParseUPID(value); err == nil {
			t.Fatalf("invalid UPID accepted: %q", value)
		}
	}
}

func assertForm(t *testing.T, request *http.Request, expected map[string]string) {
	t.Helper()
	if err := request.ParseForm(); err != nil {
		t.Fatal(err)
	}
	if len(request.Form) != len(expected) {
		t.Errorf("form = %#v, expected %#v", request.Form, expected)
	}
	for key, value := range expected {
		if request.Form.Get(key) != value {
			t.Errorf("form %s = %q, want %q", key, request.Form.Get(key), value)
		}
	}
}
