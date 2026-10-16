package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
)

const ownedTestDisk = "local:2000/vm-2000-disk-0.qcow2"
const foreignTestDisk = "local:2050/vm-2050-disk-0.qcow2"

type diskInventory struct {
	mu                   sync.Mutex
	t                    *testing.T
	vm, foreignVM, audit bool
	volumes              map[string]int
	target, fault        string
	hit                  bool
	calls                []string
}

func (f *diskInventory) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/api2/json")
	step := faultStep(r.Method, path)
	if r.Method != "GET" {
		f.calls = append(f.calls, step)
	}
	selected := !f.hit && step == f.target
	if selected {
		f.hit = true
	}
	if selected && f.fault != "response-loss" {
		if f.fault == "reset-before" {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		w.WriteHeader(map[string]int{"authorization": 403, "validation": 400, "throttle": 429, "server": 500}[f.fault])
		return
	}
	value := any(nil)
	switch {
	case path == "/access/permissions":
		grants := map[string]int{"VM.Audit": 1}
		if f.audit {
			grants["Datastore.Audit"] = 1
		}
		value = map[string]any{"/": grants}
	case path == "/cluster/resources":
		v := []map[string]any{}
		if f.vm {
			v = append(v, map[string]any{"vmid": 2000, "node": "node"})
		}
		if f.foreignVM {
			v = append(v, map[string]any{"vmid": 2050, "node": "node"})
		}
		value = v
	case path == "/nodes/node/qemu/2000/config":
		value = map[string]any{"description": marker("owner", "server"), "scsi0": ownedTestDisk + ",size=8G", "ide2": "local:iso/operator.iso,media=cdrom"}
	case path == "/nodes/node/storage/local/content" && r.Method == "GET":
		v := []storageVolume{}
		for id, owner := range f.volumes {
			v = append(v, storageVolume{ID: id, VMID: owner, Content: "images"})
		}
		value = v
	case strings.Contains(path, "/tasks/"):
		value = map[string]string{"status": "stopped", "exitstatus": "OK"}
	case path == "/nodes/node/qemu/2000/status/stop":
		value = "UPID:node:stop"
	case path == "/nodes/node/qemu/2000" && r.Method == "DELETE":
		f.vm = false
		value = "UPID:node:delete" // Native disk-free warning: VM gone, disk remains.
	case strings.HasPrefix(path, "/nodes/node/storage/local/content/") && r.Method == "DELETE":
		id := strings.TrimPrefix(path, "/nodes/node/storage/local/content/")
		if f.volumes[id] != 2000 || f.vm {
			f.t.Error("unsafe storage deletion", id, f.volumes[id], f.vm)
			w.WriteHeader(403)
			return
		}
		delete(f.volumes, id)
		value = "UPID:node:disk-delete"
	default:
		f.t.Error("unexpected native request", r.Method, path)
		w.WriteHeader(500)
		return
	}
	if selected && f.fault == "response-loss" {
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
		return
	}
	response(w, value)
}
func diskFixture(t *testing.T) (*diskInventory, *Client, lc.Command) {
	t.Helper()
	f := &diskInventory{t: t, vm: true, foreignVM: true, audit: true, volumes: map[string]int{ownedTestDisk: 2000, foreignTestDisk: 2050}}
	c, env := testClient(t, f.serve)
	return f, c, lc.Command{Owner: "owner", Action: "resource.destroy", Environment: env, Prior: lc.Resource{Kind: "machine", Name: "server", ID: "2000", Owner: "owner"}}
}
func TestProxmoxDiskCleanupFailureMatrix(t *testing.T) {
	for _, step := range []string{"POST/qemu/{id}/status/stop", "DELETE/qemu/{id}", "DELETE/storage/{storage}/content/{volume}"} {
		for _, fault := range []string{"response-loss", "reset-before", "authorization", "validation", "throttle", "server"} {
			t.Run(step+"/"+fault, func(t *testing.T) {
				f, c, cmd := diskFixture(t)
				root := t.TempDir()
				p := &Provider{Journal: journal.Journal{Root: root}}
				f.target, f.fault = step, fault
				result, err := p.destroy(context.Background(), c, cmd)
				if err == nil || result.Absent {
					t.Fatal("native fault not retained", result, err)
				}
				f.mu.Lock()
				hit := f.hit
				f.mu.Unlock()
				if !hit {
					t.Fatal("fault not reached")
				}
				p = &Provider{Journal: journal.Journal{Root: root}}
				result, err = p.destroy(context.Background(), c, cmd)
				if err != nil || !result.Absent {
					t.Fatal(result, err)
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				if f.vm || !f.foreignVM || len(f.volumes) != 1 || f.volumes[foreignTestDisk] != 2050 {
					t.Fatal("disk leak or foreign deletion", f.volumes)
				}
			})
		}
	}
}
func TestProxmoxDiskOwnershipAndAbsence(t *testing.T) {
	for _, fault := range []string{"foreign-owner-same-ID", "unknown-orphan", "out-of-band-VM", "absent-disk", "hidden-storage", "replacement-VM"} {
		t.Run(fault, func(t *testing.T) {
			f, c, cmd := diskFixture(t)
			p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
			if fault == "out-of-band-VM" || fault == "replacement-VM" {
				v, err := c.Config(context.Background(), 2000)
				if err != nil {
					t.Fatal(err)
				}
				if err = p.rememberDisks(context.Background(), c, cmd, 2000, v); err != nil {
					t.Fatal(err)
				}
			}
			f.mu.Lock()
			switch fault {
			case "foreign-owner-same-ID":
				f.volumes[ownedTestDisk] = 2050
			case "unknown-orphan":
				f.vm = false
			case "out-of-band-VM":
				f.vm = false
			case "absent-disk":
				delete(f.volumes, ownedTestDisk)
			case "hidden-storage":
				f.audit = false
			}
			f.mu.Unlock()
			var result lc.Result
			var err error
			if fault == "replacement-VM" {
				err = p.cleanupDisks(context.Background(), c, cmd, 2000)
			} else {
				result, err = p.destroy(context.Background(), c, cmd)
			}
			succeeds := fault == "out-of-band-VM" || fault == "absent-disk"
			if (err == nil) != succeeds {
				t.Fatal(fault, result, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if !succeeds {
				if len(f.calls) != 0 || !strings.Contains(err.Error(), map[string]string{"foreign-owner-same-ID": "ownership_mismatch", "unknown-orphan": "allocation retained", "hidden-storage": "Datastore.Audit", "replacement-VM": "VMID reappeared"}[fault]) {
					t.Fatal("unverified deletion or unactionable error", f.calls, err)
				}
			}
			if !f.foreignVM || f.volumes[foreignTestDisk] != 2050 {
				t.Fatal("foreign inventory changed")
			}
		})
	}
}
func TestDiskConfigDecodingIncludesImplicitDeleteChildren(t *testing.T) {
	var v VMConfig
	if err := json.Unmarshal([]byte(`{"scsi0":"local:2000/root","ide0":"local:2000/cloudinit","efidisk0":"local:2000/efi","tpmstate0":"local:2000/tpm","unused2":"local:2000/old","vmstate":"local:2000/state","scsihw":"virtio-scsi-pci","net0":"virtio=AA:BB:CC:DD:EE:00"}`), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Disks) != 6 || len(v.NICs) != 1 {
		t.Fatal(fmt.Sprint(v.Disks), v.NICs)
	}
}
