package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/OutboundSpade/LabCentral/v2/internal/sshtest"
)

type matrixVM struct {
	config  map[string]any
	running bool
	rules   []firewallRule
}
type matrixPVE struct {
	mu    sync.Mutex
	t     *testing.T
	vms   map[int]*matrixVM
	calls []string
	// Gateway native calls finish before its SSH readiness check; this fixture
	// deliberately rejects the guest bootstrap, and tests cleanup via discovery.
	gateway bool
}

func (f *matrixPVE) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/api2/json")
	label := faultStep(r.Method, path)
	if r.Method != "GET" && !strings.HasSuffix(path, "/agent/ping") {
		f.calls = append(f.calls, label)
	}
	in := map[string]any{}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&in)
	}
	switch {
	case path == "/access/permissions":
		response(w, map[string]any{"/": map[string]int{"VM.Audit": 1, "Datastore.Audit": 1}})
		return
	case path == "/cluster/resources" || path == "/nodes/node/qemu":
		out := []map[string]any{}
		for id := range f.vms {
			out = append(out, map[string]any{"vmid": id, "node": "node"})
		}
		response(w, out)
		return
	case path == "/nodes/node/storage/local/content":
		response(w, []any{})
		return
	case path == "/nodes/node/network":
		response(w, []bridge{{Interface: "vmbr-labs", Type: "bridge", VLANAware: 1, Active: 1}})
		return
	case strings.Contains(path, "/tasks/"):
		response(w, map[string]string{"status": "stopped", "exitstatus": "OK"})
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/nodes/node/qemu/"), "/")
	id, err := strconv.Atoi(parts[0])
	if err != nil {
		f.t.Errorf("unexpected path %s", path)
		w.WriteHeader(500)
		return
	}
	suffix := ""
	if len(parts) > 1 {
		suffix = "/" + strings.Join(parts[1:], "/")
	}
	if suffix == "/clone" {
		id = int(in["newid"].(float64))
		if _, ok := f.vms[id]; ok {
			f.t.Error("clone duplicated", id)
			w.WriteHeader(400)
			return
		}
		f.vms[id] = &matrixVM{config: map[string]any{"description": in["description"], "digest": "pinned", "net0": "virtio=AA:BB:CC:DD:EE:00", "agent": "enabled=1"}, rules: []firewallRule{{Type: "in", Action: "ACCEPT"}, {Type: "out", Action: "ACCEPT"}}}
		response(w, "UPID:node:clone")
		return
	}
	vm := f.vms[id]
	if vm == nil {
		w.WriteHeader(404)
		return
	}
	switch {
	case suffix == "/config" && r.Method == "GET":
		response(w, vm.config)
	case suffix == "/config" && r.Method == "PUT":
		for key, value := range in {
			vm.config[key] = value
		}
		response(w, nil)
	case suffix == "/template":
		vm.config["template"] = 1
		response(w, "UPID:node:template")
	case suffix == "/resize":
		response(w, nil)
	case suffix == "/status/current":
		status := "stopped"
		if vm.running {
			status = "running"
		}
		response(w, map[string]string{"status": status})
	case suffix == "/status/start":
		vm.running = true
		response(w, "UPID:node:start")
	case suffix == "/status/stop":
		vm.running = false
		response(w, "UPID:node:stop")
	case suffix == "/agent/ping":
		response(w, nil)
	case suffix == "/agent/network-get-interfaces":
		response(w, map[string]any{"result": []map[string]any{{"name": "ens18", "hardware-address": "AA:BB:CC:DD:EE:00", "ip-addresses": []map[string]any{{"ip-address": "192.0.2.20", "prefix": 24, "ip-address-type": "ipv4"}}}}})
	case suffix == "/agent/exec":
		response(w, map[string]int{"pid": 1})
	case suffix == "/agent/exec-status":
		code := 0
		if f.gateway {
			code = 1
		}
		response(w, map[string]int{"exited": 1, "exitcode": code})
	case suffix == "/firewall/options":
		response(w, nil)
	case suffix == "/firewall/rules" && r.Method == "GET":
		out := []map[string]int{}
		for pos := range vm.rules {
			out = append(out, map[string]int{"pos": pos})
		}
		response(w, out)
	case suffix == "/firewall/rules" && r.Method == "POST":
		var rule firewallRule
		b, _ := json.Marshal(in)
		_ = json.Unmarshal(b, &rule)
		vm.rules = append([]firewallRule{rule}, vm.rules...)
		response(w, nil)
	case strings.HasPrefix(suffix, "/firewall/rules/"):
		pos, _ := strconv.Atoi(strings.TrimPrefix(suffix, "/firewall/rules/"))
		if r.Method == "DELETE" {
			vm.rules = append(vm.rules[:pos], vm.rules[pos+1:]...)
		} else {
			vm.rules[pos].Enable = 1
		}
		response(w, nil)
	case suffix == "" && r.Method == "DELETE":
		delete(f.vms, id)
		response(w, "UPID:node:delete")
	default:
		f.t.Errorf("unexpected %s %s", r.Method, path)
		w.WriteHeader(500)
	}
}
func pveMatrixFixture(t *testing.T, gateway bool) (*matrixPVE, *Client, lc.Command) {
	t.Helper()
	f := &matrixPVE{t: t, gateway: gateway, vms: map[int]*matrixVM{2100: {config: map[string]any{"template": 1, "digest": "pinned"}}, 2050: {config: map[string]any{"description": marker("foreign", "server")}}}}
	client, env := testClient(t, f.serve)
	env.Proxmox.LabBridge = "vmbr-labs"
	env.Proxmox.ManagementBridge = "vmbr0"
	env.Proxmox.Storage = "local"
	env.Proxmox.Pool = "test"
	env.Bases = map[string]lc.Base{"ubuntu-24.04": {ID: "2100", Identity: "pinned", DiskGiB: 20}}
	private, public := sshtest.Key(t)
	hostPrivate, _ := sshtest.Key(t)
	machineSSH := sshtest.New(t, nil, func(string) string { return "" })
	gatewaySSH := sshtest.New(t, func(string) string { return machineSSH.Address }, func(string) string { return "" })
	pin, _ := json.Marshal(lc.GatewayIdentity{HostPublicKey: gatewaySSH.HostKey})
	net, _ := json.Marshal(network{CIDR: "10.80.0.0/24", VLAN: 100, Gateway: "10.80.0.1", Segments: []lc.PlannedNetwork{{Name: "lab", CIDR: "10.80.0.0/24", VLAN: 100, Gateway: "10.80.0.1"}}})
	name := "server"
	memory := 2048
	if gateway {
		name = "gateway"
		memory = 768
	}
	cmd := lc.Command{Action: "machine.create", Operation: "owner/machine", Owner: "0123456789abcdef01234567", Environment: env, VLAN: 100, ClientPrivateKey: private, Machine: lc.PlannedMachine{Name: name, NativeImage: "2100", VMID: 2000, CPU: 1, MemoryMiB: memory, DiskGiB: 20, Address: "10.80.0.11", PublicKey: public, HostPrivateKey: string(hostPrivate), HostPublicKey: machineSSH.HostKey}, Network: lc.Resource{Data: net}, Gateway: lc.Resource{Kind: "gateway", Owner: "0123456789abcdef01234567", ID: "2002", Complete: true, Address: gatewaySSH.Address, Data: pin}}
	if gateway {
		cmd.Action = "gateway.create"
		cmd.Prior.Kind = "gateway"
		cmd.Machine.Address = ""
	}
	return f, client, cmd
}
func pveRun(ctx context.Context, p *Provider, c *Client, cmd lc.Command, gateway bool) (lc.Result, error) {
	if gateway {
		return p.gateway(ctx, c, cmd)
	}
	return p.machine(ctx, c, cmd)
}

type pveBoundary struct {
	step string
	nth  int
}

func pveBoundaries(t *testing.T, gateway, cleanup bool) []pveBoundary {
	t.Helper()
	f, c, cmd := pveMatrixFixture(t, gateway)
	p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
	_, err := pveRun(context.Background(), p, c, cmd, gateway)
	if (err != nil) != gateway {
		t.Fatal("fixture readiness", err)
	}
	if cleanup {
		f.calls = nil
		kind := "machine"
		if gateway {
			kind = "gateway"
		}
		cmd.Prior = lc.Resource{Kind: kind, Name: cmd.Machine.Name, ID: "2000", Owner: cmd.Owner}
		if _, err = p.destroy(context.Background(), c, cmd); err != nil {
			t.Fatal(err)
		}
	}
	counts := map[string]int{}
	out := []pveBoundary{}
	for _, step := range f.calls {
		counts[step]++
		out = append(out, pveBoundary{step, counts[step]})
	}
	return out
}

type pveTransport func(*http.Request) (*http.Response, error)

func (f pveTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProxmoxNativeFailureMatrix(t *testing.T) {
	for _, gateway := range []bool{false, true} {
		for _, cleanup := range []bool{false, true} {
			operation := "machine.create"
			if gateway {
				operation = "gateway.create"
			}
			if cleanup {
				operation = "destroy-" + operation
			}
			for _, step := range pveBoundaries(t, gateway, cleanup) {
				for _, fault := range []string{"response-loss", "reset-before", "authorization", "validation", "throttle", "server"} {
					t.Run(fmt.Sprintf("%s/%s/%d/%s", operation, step.step, step.nth, fault), func(t *testing.T) {
						f, c, cmd := pveMatrixFixture(t, gateway)
						root := t.TempDir()
						p := &Provider{Journal: journal.Journal{Root: root}}
						ctx := context.Background()
						if cleanup {
							_, err := pveRun(ctx, p, c, cmd, gateway)
							if (err != nil) != gateway {
								t.Fatal(err)
							}
							kind := "machine"
							if gateway {
								kind = "gateway"
							}
							cmd.Prior = lc.Resource{Kind: kind, Name: cmd.Machine.Name, ID: "2000", Owner: cmd.Owner}
						}
						native := c.HTTP.Transport
						hits := 0
						hit := false
						copyClient := *c
						copyHTTP := *c.HTTP
						copyClient.HTTP = &copyHTTP
						copyHTTP.Transport = pveTransport(func(r *http.Request) (*http.Response, error) {
							selected := false
							if faultStep(r.Method, r.URL.Path) == step.step {
								hits++
								selected = hits == step.nth && !hit
							}
							if selected {
								hit = true
							}
							if selected && fault != "response-loss" {
								if fault == "reset-before" {
									return nil, io.ErrUnexpectedEOF
								}
								status := map[string]int{"authorization": 403, "validation": 400, "throttle": 429, "server": 500}[fault]
								return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"data":null}`)), Header: http.Header{}}, nil
							}
							res, err := native.RoundTrip(r)
							if selected && err == nil {
								io.Copy(io.Discard, res.Body)
								res.Body.Close()
								return nil, io.ErrUnexpectedEOF
							}
							return res, err
						})
						if cleanup {
							_, _ = p.destroy(ctx, &copyClient, cmd)
						} else {
							_, _ = pveRun(ctx, p, &copyClient, cmd, gateway)
						}
						if !hit {
							t.Fatal("boundary not reached")
						}
						p = &Provider{Journal: journal.Journal{Root: root}}
						var result lc.Result
						var err error
						if cleanup {
							result, err = p.destroy(ctx, c, cmd)
						} else {
							result, err = pveRun(ctx, p, c, cmd, gateway)
						}
						if err != nil && !gateway {
							if !strings.HasSuffix(step.step, "/clone") || (fault != "reset-before" && fault != "server") {
								t.Fatal("recovery failed", err)
							}
							// An unconfirmed clone cannot be reissued merely because config is absent.
							count := len(f.vms)
							_, again := pveRun(ctx, p, c, cmd, gateway)
							if again == nil || len(f.vms) != count {
								t.Fatal("unknown clone replayed")
							}
						}
						if !cleanup { // Use the exact reserved VMID; native destroy re-verifies ownership.
							kind := "machine"
							if gateway {
								kind = "gateway"
							}
							cmd.Prior = lc.Resource{Kind: kind, Name: cmd.Machine.Name, ID: "2000", Owner: cmd.Owner}
							result, err = p.destroy(ctx, c, cmd)
						}
						if err != nil || !result.Absent || len(f.vms) != 2 || f.vms[2050] == nil || f.vms[2100] == nil {
							t.Fatal("native cleanup or foreign inventory", result, err, f.vms)
						}
					})
				}
			}
		}
	}
}

func TestProxmoxOwnershipAndAbsenceMatrix(t *testing.T) {
	for _, kind := range []string{"machine", "gateway", "image"} {
		for _, state := range []string{"absent", "foreign", "locked"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				f, c, cmd := pveMatrixFixture(t, false)
				vm := &matrixVM{config: map[string]any{"description": marker(cmd.Owner, cmd.Machine.Name)}}
				f.vms[2000] = vm
				if kind == "image" {
					vm.config["template"] = 1
				}
				if state == "absent" {
					delete(f.vms, 2000)
				} else if state == "foreign" {
					vm.config["description"] = marker("foreign", cmd.Machine.Name)
				} else {
					vm.config["lock"] = "clone"
				}
				cmd.Prior = lc.Resource{Kind: kind, Name: cmd.Machine.Name, ID: "2000", Owner: cmd.Owner}
				p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
				result, err := p.destroy(context.Background(), c, cmd)
				if state == "foreign" || state == "locked" {
					if err == nil {
						t.Fatal("unsafe ownership/lock cleanup", result)
					}
					if len(f.calls) != 0 {
						t.Fatal("mutated unresolved resource", f.calls)
					}
					vm.config["description"] = marker(cmd.Owner, cmd.Machine.Name)
					delete(vm.config, "lock")
					result, err = p.destroy(context.Background(), c, cmd)
				}
				if err != nil || !result.Absent || len(f.vms) != 2 {
					t.Fatal(result, err, f.vms)
				}
			})
		}
	}
}
