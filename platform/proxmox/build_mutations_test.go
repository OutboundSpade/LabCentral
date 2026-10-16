package proxmox

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/OutboundSpade/LabCentral/v2/internal/nativeproxy"
)

func TestProxmoxBuilderCreateJournalMatrix(t *testing.T) {
	for _, action := range []string{"clone", "template"} {
		for _, fault := range []string{"success", "preflight", "response-loss", "empty-unknown", "foreign", "locked", "authorization", "validation", "throttle", "server"} {
			t.Run(action+"/"+fault, func(t *testing.T) {
				ctx := context.Background()
				inventory, api, c := pveMatrixFixture(t, false)
				c.Image.Name = "image"
				root := t.TempDir()
				g := &proxmoxBuildGuard{p: &Provider{Journal: journal.Journal{Root: root}}, api: api, c: c}
				path := "/api2/json/nodes/node/qemu/2000/" + action
				r := &http.Request{Method: "POST", URL: &url.URL{Path: path}}
				calls := 0
				native := func(context.Context) (nativeproxy.Reply, error) {
					calls++
					if fault == "preflight" {
						return nativeproxy.Reply{}, nativeproxy.BeforeSubmission(errors.New("ownership_mismatch: guard"))
					}
					if fault == "authorization" || fault == "validation" || fault == "throttle" || fault == "server" {
						return nativeproxy.Reply{Status: map[string]int{"authorization": 403, "validation": 400, "throttle": 429, "server": 500}[fault], Body: []byte(`{"data":null}`)}, nil
					}
					inventory.vms[2000] = &matrixVM{config: map[string]any{"description": marker(c.Owner, c.Image.Name)}}
					if action == "template" {
						inventory.vms[2000].config["template"] = 1
					}
					if fault == "foreign" {
						inventory.vms[2000].config["description"] = marker("foreign", c.Image.Name)
					}
					if fault == "locked" {
						inventory.vms[2000].config["lock"] = "clone"
					}
					if fault == "empty-unknown" {
						delete(inventory.vms, 2000)
					}
					if fault != "success" {
						return nativeproxy.Reply{}, errors.New("lost after send")
					}
					return nativeproxy.Reply{Status: 200, Body: []byte(`{"data":"UPID:node:build"}`)}, nil
				}
				_, _ = g.do(ctx, r, nil, native)
				g = &proxmoxBuildGuard{p: &Provider{Journal: journal.Journal{Root: root}}, api: api, c: c}
				reply, err := g.do(ctx, r, nil, native)
				if fault == "preflight" {
					if calls != 2 || err == nil || !nativeproxy.IsBeforeSubmission(err) {
						t.Fatal("local guard denial lost definite outcome", calls, err)
					}
					if e := g.p.reconcileBuildMutations(ctx, api, c); e != nil {
						t.Fatal(e)
					}
					return
				}
				rejected := fault == "authorization" || fault == "validation" || fault == "throttle"
				if rejected {
					if calls != 2 || err != nil || reply.Status < 400 {
						t.Fatal(calls, reply, err)
					}
					return
				}
				if calls != 1 {
					t.Fatal("uncertain build mutation replayed", calls)
				}
				unknown := fault == "empty-unknown" || fault == "foreign" || fault == "locked" || fault == "server"
				if unknown {
					if err == nil {
						t.Fatal("unconfirmed outcome discarded")
					}
					if e := g.p.reconcileBuildMutations(ctx, api, c); e == nil || !strings.Contains(e.Error(), "recovery_required") {
						t.Fatal(e)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				want := 409
				if fault == "success" {
					want = 200
				}
				if reply.Status != want {
					t.Fatal(reply)
				}
				if err = g.p.reconcileBuildMutations(ctx, api, c); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestProxmoxBuilderAtomicCloneMarkerAndStaleOwnership(t *testing.T) {
	inventory, api, c := pveMatrixFixture(t, false)
	c.Image.Name = "image"
	c.Image.Base = c.Environment.Bases["ubuntu-24.04"]
	p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
	req := &http.Request{Method: "POST", URL: &url.URL{Path: "/api2/json/nodes/node/qemu/2100/clone"}}
	body, err := p.guardBuildRequest(context.Background(), api, c, req, []byte("newid=2000&description=Packer+ephemeral+build+VM"))
	if err != nil {
		t.Fatal(err)
	}
	values, _ := url.ParseQuery(string(body))
	if values.Get("description") != marker(c.Owner, c.Image.Name) || values.Get("pool") != c.Environment.Proxmox.Pool {
		t.Fatal("clone omitted atomic ownership", values)
	}
	for _, desc := range []string{marker("foreign", c.Image.Name), "Packer ephemeral build VM", ""} {
		inventory.vms[2000] = &matrixVM{config: map[string]any{"description": desc, "tags": "labcentral-v2;" + c.Owner}}
		for _, path := range []string{"/config", "/status/stop", "/template", ""} {
			req.URL.Path = "/api2/json/nodes/node/qemu/2000" + path
			req.Method = "POST"
			if path == "" {
				req.Method = "DELETE"
			}
			_, err = p.guardBuildRequest(context.Background(), api, c, req, nil)
			if err == nil || !strings.Contains(err.Error(), "ownership_mismatch") {
				t.Fatal(path, desc, err)
			}
		}
	}
}

func TestProxmoxAbandonUncapturedBuilderRetainsStorageBinding(t *testing.T) {
	inventory, _, c := pveMatrixFixture(t, false)
	c.Image.Name = "image"
	inventory.vms[2000] = &matrixVM{config: map[string]any{"description": marker(c.Owner, c.Image.Name)}}
	p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
	if err := p.CleanupBuild(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	inventory.mu.Lock()
	defer inventory.mu.Unlock()
	if inventory.vms[2000] != nil || inventory.vms[2050] == nil {
		t.Fatal("builder leaked or foreign VM deleted")
	}
}
