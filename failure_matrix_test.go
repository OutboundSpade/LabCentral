package labcentral

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// This fixture models the provider contract, not AWS/Proxmox's native protocols.
// Native mutation coverage lives in the platform package matrices.
type matrixProvider struct {
	inner         *fakeProvider
	mu            sync.Mutex
	target, fault string
	hit           bool
}

func matrixStep(c Command) string {
	if c.Action == "resource.destroy" {
		return c.Action + "/" + c.Prior.Kind
	}
	return c.Action
}
func (p *matrixProvider) Execute(ctx context.Context, c Command, o Observer) (Result, error) {
	p.mu.Lock()
	selected := matrixStep(c) == p.target && !p.hit
	if selected {
		p.hit = true
	}
	fault := p.fault
	p.mu.Unlock()
	if selected && fault == "failure-before" {
		return Result{}, errors.New("injected failure before native mutation")
	}
	var result Result
	var err error
	if strings.HasPrefix(c.Action, "access.") {
		p.inner.mu.Lock()
		key := "access/" + c.Operation
		current, exists := p.inner.resources[key]
		if exists && current.Owner != c.Owner {
			err = errors.New("ownership_mismatch: access")
		} else if c.Action == "access.close" {
			delete(p.inner.resources, key)
			result.Absent = true
		} else {
			if !exists {
				p.inner.creates++
				p.inner.resources[key] = Resource{Kind: "access", Name: c.Machine.Name, ID: key, Owner: c.Owner}
			}
			data, _ := json.Marshal(AccessInfo{ID: c.Operation, URL: "http://127.0.0.1:1", Expires: time.Now().Add(time.Hour)})
			result.Resource = Resource{Data: data}
		}
		p.inner.mu.Unlock()
	} else {
		result, err = p.inner.Execute(ctx, c, o)
	}
	if selected && fault == "response-loss" && err == nil {
		return Result{}, fmt.Errorf("response lost after %s: %w", matrixStep(c), context.DeadlineExceeded)
	}
	return result, err
}
func matrixPlan(t *testing.T, platform string, build, desktop bool) Plan {
	t.Helper()
	if platform == "aws-windows" {
		return windowsFailurePlan(t)
	}
	s := testSource(t)
	env := testEnvironment()
	if build {
		p := imagePlan(t, "failure-matrix")
		s, env = p.Source, p.Environment
	}
	if desktop {
		m := s.Lab.Machines[SortedMachineNames(s.Lab)[0]]
		m.Access = []string{"ssh", "desktop"}
		s.Lab.Machines[SortedMachineNames(s.Lab)[0]] = m
		// Reparse changed source so the digest and snapshot agree.
		lab := ""
		for _, file := range s.Files {
			if file.Path == "lab.yaml" {
				lab = string(file.Content)
			}
		}
		lab = strings.Replace(lab, "access: [ssh]", "access: [ssh, desktop]", 1)
		if !strings.Contains(lab, "desktop") {
			lab = strings.Replace(lab, "    image:", "    access: [ssh, desktop]\n    image:", 1)
		}
		dir := t.TempDir()
		for _, file := range s.Files {
			name, data := file.Path, file.Content
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if name == "lab.yaml" {
				data = []byte(lab)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		var err error
		s, err = Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		env.AWS.DesktopProfile = "test-license-profile"
		env.Build.DCVURL = "https://example.invalid/dcv.tgz"
		env.Build.DCVSHA256 = strings.Repeat("a", 64)
	}
	if platform == "proxmox" {
		pe := platformOptionsProxmoxEnvironment()
		env.Platform = "proxmox"
		env.Proxmox = pe.Proxmox
		env.Bases = pe.Bases
		env.Build.Bridge = pe.Build.Bridge
		env.Proxmox.ManagementAddresses = &ProxmoxManagementAddresses{CIDR: "192.0.2.0/28", Gateway: "192.0.2.1", DNS: []string{"1.1.1.1"}}
	}
	plan, err := PlanLab(s, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
func openMatrix(t *testing.T, root string, p *matrixProvider) *Engine {
	t.Helper()
	e, err := OpenWithOptions(root, p, nil, EngineOptions{MaxCleanupConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}
func restartMatrix(t *testing.T, e *Engine, p *matrixProvider, root string) *Engine {
	t.Helper()
	// Inventory survives independently of both the engine and provider object.
	p.inner.mu.Lock()
	body, err := json.Marshal(p.inner.resources)
	p.inner.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "native-inventory.json")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	restored := freshFake()
	body, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(body, &restored.resources); err != nil {
		t.Fatal(err)
	}
	restored.creates = p.inner.creates
	p.inner = restored
	p.target = ""
	return openMatrix(t, root, p)
}
func assertMatrixClean(t *testing.T, e *Engine, p *matrixProvider, owner string) {
	t.Helper()
	ctx := context.Background()
	p.inner.mu.Lock()
	defer p.inner.mu.Unlock()
	for _, r := range p.inner.resources {
		if r.Owner == owner {
			t.Fatal("owned native inventory remains", r.Kind)
		}
	}
	if _, ok := p.inner.resources["foreign"]; !ok {
		t.Fatal("foreign native resource deleted")
	}
	var claims int
	if err := e.state.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM claims WHERE owner=?", owner).Scan(&claims); err != nil || claims != 0 {
		t.Fatal("allocation leak", claims, err)
	}
	steps, err := e.state.Uncertain(ctx, owner)
	if err != nil || len(steps) != 0 {
		t.Fatal("unresolved native intents", steps, err)
	}
}
func TestLifecycleFailureMatrix(t *testing.T) {
	for _, platform := range []string{"aws", "proxmox", "aws-windows"} {
		for _, operation := range []string{"image.build", "network.create", "gateway.create", "machine.create", "resource.destroy/machine", "resource.destroy/gateway", "resource.destroy/network", "image.remove", "access.open", "access.close"} {
			if platform == "aws-windows" && !strings.HasPrefix(operation, "access.") {
				continue
			}
			for _, fault := range []string{"failure-before", "response-loss"} {
				t.Run(platform+"/"+operation+"/"+fault, func(t *testing.T) {
					ctx := context.Background()
					root := t.TempDir()
					p := &matrixProvider{inner: freshFake()}
					p.inner.resources["foreign"] = Resource{Kind: "machine", Name: "one", ID: "foreign", Owner: "another-owner"}
					build := strings.HasPrefix(operation, "image.") || strings.HasPrefix(operation, "access.")
					plan := matrixPlan(t, platform, build, strings.HasPrefix(operation, "access."))
					e := openMatrix(t, root, p)
					var instance Instance
					var images []ImageRecord
					var session AccessInfo
					var err error
					if operation != "image.build" && operation != "image.remove" && !strings.HasSuffix(operation, ".create") {
						instance, err = e.Deploy(ctx, plan)
						if err != nil {
							t.Fatal(err)
						}
					}
					if operation == "image.remove" {
						images, err = e.Build(ctx, plan)
						if err != nil {
							t.Fatal(err)
						}
					}
					if operation == "access.close" {
						session, err = e.OpenDesktop(ctx, instance.ID, instance.Plan.Machines[0].Name)
						if err != nil {
							t.Fatal(err)
						}
					}
					p.target, p.fault = operation, fault
					if operation == "image.remove" {
						p.target = "resource.destroy/image"
					}
					switch {
					case operation == "image.build":
						images, err = e.Build(ctx, plan)
					case operation == "image.remove":
						err = e.RemoveImage(ctx, images[0].Key)
					case strings.HasSuffix(operation, ".create"):
						instance, err = e.Deploy(ctx, plan)
					case operation == "access.open":
						_, err = e.OpenDesktop(ctx, instance.ID, instance.Plan.Machines[0].Name)
					case operation == "access.close":
						err = e.CloseAccess(ctx, session.ID)
					default:
						err = e.Destroy(ctx, instance.ID)
					}
					if !p.hit || err == nil {
						t.Fatal("fault not reported", p.hit, err)
					}
					if fault == "response-loss" && !errors.Is(err, context.DeadlineExceeded) {
						t.Fatal("user-visible unknown outcome lost", err)
					}
					if operation == "image.remove" {
						var cleanup Command
						if er := e.state.Get(ctx, "image-cleanup", images[0].Key, &cleanup); er != nil {
							t.Fatal("cleanup intent lost", er)
						}
						if _, er := e.Build(ctx, plan); er == nil {
							t.Fatal("pending removal allowed reuse")
						}
					}
					e = restartMatrix(t, e, p, root)
					switch {
					case operation == "image.build":
						images, err = e.RecoverBuild(ctx, plan.Images[0].CacheKey)
					case operation == "image.remove":
						err = e.RemoveImage(ctx, images[0].Key)
					default:
						instance, err = e.Recover(ctx, instance.ID)
					}
					if err != nil {
						t.Fatal("restart recovery failed", err)
					}
					if instance.ID != "" {
						if operation == "access.open" || operation == "access.close" {
							records, er := e.state.List(ctx, "access")
							if er != nil || len(records) != 0 {
								t.Fatal("access intent survived confirmed revocation", er)
							}
						}
						if err = e.Destroy(ctx, instance.ID); err != nil {
							t.Fatal(err)
						}
						assertMatrixClean(t, e, p, instance.ID)
					}
					images, err = e.Images(ctx)
					if err != nil {
						t.Fatal(err)
					}
					for _, im := range images {
						if err = e.RemoveImage(ctx, im.Key); err != nil {
							t.Fatal(err)
						}
						assertMatrixClean(t, e, p, im.ID)
					}
					var pending int
					if err = e.state.DB.QueryRow("SELECT COUNT(*) FROM claims").Scan(&pending); err != nil || pending != 0 {
						t.Fatal("final reservations", pending, err)
					}
					if len(p.inner.resources) != 1 {
						t.Fatal("final native inventory", p.inner.resources)
					}
				})
			}
		}
	}
}
func TestLifecycleStaleOwnershipMatrix(t *testing.T) {
	for _, platform := range []string{"aws", "proxmox"} {
		for _, kind := range []string{"network", "gateway", "machine", "image"} {
			for _, fault := range []string{"absent", "foreign"} {
				t.Run(platform+"/"+kind+"/"+fault, func(t *testing.T) {
					ctx := context.Background()
					root := t.TempDir()
					p := &matrixProvider{inner: freshFake()}
					p.inner.resources["foreign"] = Resource{Kind: kind, Name: "one", ID: "foreign", Owner: "another-owner"}
					e := openMatrix(t, root, p)
					plan := matrixPlan(t, platform, kind == "image", false)
					var instance Instance
					var images []ImageRecord
					var err error
					if kind == "image" {
						images, err = e.Build(ctx, plan)
					} else {
						instance, err = e.Deploy(ctx, plan)
					}
					if err != nil {
						t.Fatal(err)
					}
					var saved Resource
					for key, r := range p.inner.resources {
						if r.Kind == kind && r.Owner != "another-owner" {
							saved = r
							if fault == "absent" {
								delete(p.inner.resources, key)
							} else {
								r.Owner = "replacement-owner"
								p.inner.resources[key] = r
							}
							break
						}
					}
					cleanup := func() error {
						if kind == "image" {
							return e.RemoveImage(ctx, images[0].Key)
						}
						return e.Destroy(ctx, instance.ID)
					}
					err = cleanup()
					if fault == "foreign" {
						if err == nil || !strings.Contains(err.Error(), "ownership") {
							t.Fatal("ownership change ignored", err)
						}
						e = restartMatrix(t, e, p, root)
						p.inner.resources[saved.ID] = saved
						err = cleanup()
					}
					if err != nil {
						t.Fatal(err)
					}
					if kind == "image" {
						assertMatrixClean(t, e, p, images[0].ID)
					} else {
						assertMatrixClean(t, e, p, instance.ID)
					}
				})
			}
		}
	}
}

func TestImagePublicationReleasesDisposableReservationsAtomically(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	p := &matrixProvider{inner: freshFake()}
	e := openMatrix(t, root, p)
	plan := matrixPlan(t, "proxmox", true, false)
	images, err := e.Build(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	var cmd Command
	if err = e.state.Get(ctx, "build", images[0].Key, &cmd); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = e.state.DB.QueryRow("SELECT COUNT(*) FROM claims WHERE owner=?", cmd.Owner).Scan(&count); err != nil || count != 1 {
		t.Fatal("publication retained disposable claims", count, err)
	}
	if err = e.state.DB.QueryRow("SELECT value FROM claims WHERE owner=?", cmd.Owner).Scan(&count); err != nil || count != cmd.Machine.VMID {
		t.Fatal("image identity claim lost", count, err)
	}
	// A failing publication transaction retains both its old cache state and all
	// allocations; it cannot partially release them or silently publish a new image.
	images[0].Resource.ID = "replacement"
	canceled, stop := context.WithCancel(ctx)
	stop()
	if err = e.publishImage(canceled, images[0].Key, images[0], cmd); err == nil {
		t.Fatal("canceled commit succeeded")
	}
	var stored ImageRecord
	if err = e.state.Get(ctx, "image", images[0].Key, &stored); err != nil || stored.Resource.ID == "replacement" {
		t.Fatal("partial publication", stored, err)
	}
	if err = e.RemoveImage(ctx, images[0].Key); err != nil {
		t.Fatal(err)
	}
	if err = e.state.Get(ctx, "image", images[0].Key, &stored); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
}

func windowsFailurePlan(t *testing.T) Plan {
	t.Helper()
	source, err := sourceYAML(t, []byte(`version: 3
name: windows-matrix
platforms: [aws]
images:
  desktop:
    base: windows-2022
    os: windows
    build: {script: build.ps1, files: [build.ps1]}
machines:
  desktop:
    image: desktop
    cpu: 2
    memory: 4Gi
    disk: 30Gi
    access: [ssh, desktop]
`))
	if err != nil {
		t.Fatal(err)
	}
	env := windowsConformanceEnvironment(source, imagePlan(t, "windows-matrix").Environment)
	env.AWS.DesktopProfile = "example-license-profile"
	plan, err := PlanLab(source, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Machines[0].OS != "windows" {
		t.Fatal("Windows coverage lost native OS")
	}
	return plan
}

func TestAccessOwnershipAndAbsentAccountMatrix(t *testing.T) {
	for _, platform := range []string{"aws", "proxmox", "aws-windows"} {
		for _, fault := range []string{"foreign", "absent"} {
			t.Run(platform+"/"+fault, func(t *testing.T) {
				ctx := context.Background()
				root := t.TempDir()
				p := &matrixProvider{inner: freshFake()}
				p.inner.resources["foreign"] = Resource{Kind: "machine", Owner: "other-owner"}
				e := openMatrix(t, root, p)
				instance, err := e.Deploy(ctx, matrixPlan(t, platform, true, true))
				if err != nil {
					t.Fatal(err)
				}
				info, err := e.OpenDesktop(ctx, instance.ID, instance.Plan.Machines[0].Name)
				if err != nil {
					t.Fatal(err)
				}
				key := "access/" + info.ID
				r := p.inner.resources[key]
				if fault == "foreign" {
					r.Owner = "foreign-owner"
					p.inner.resources[key] = r
				} else {
					delete(p.inner.resources, key)
				}
				e = restartMatrix(t, e, p, root)
				err = e.CloseAccess(ctx, info.ID)
				if fault == "foreign" {
					if err == nil || !strings.Contains(err.Error(), "ownership_mismatch") {
						t.Fatal(err)
					}
					var cmd Command
					if err = e.state.Get(ctx, "access", info.ID, &cmd); err != nil || cmd.Action != "access.close" {
						t.Fatal("close intent lost", cmd, err)
					}
					r.Owner = instance.ID
					p.inner.resources[key] = r
					_, err = e.Recover(ctx, instance.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				records, err := e.state.List(ctx, "access")
				if err != nil || len(records) != 0 {
					t.Fatal(records, err)
				}
				if err = e.Destroy(ctx, instance.ID); err != nil {
					t.Fatal(err)
				}
				assertMatrixClean(t, e, p, instance.ID)
			})
		}
	}
}

type publicationReadGate struct {
	p    *matrixProvider
	fail bool
}

func (p *publicationReadGate) Execute(ctx context.Context, c Command, o Observer) (Result, error) {
	if c.Action == "image.read" && p.fail {
		p.fail = false
		return Result{}, errors.New("native image status response unavailable")
	}
	return p.p.Execute(ctx, c, o)
}
func TestImagePublicationRechecksCompletedIntentAfterRestart(t *testing.T) {
	for _, platform := range []string{"aws", "proxmox"} {
		for _, fault := range []string{"foreign", "absent", "incomplete"} {
			t.Run(platform+"/"+fault, func(t *testing.T) {
				ctx := context.Background()
				root := t.TempDir()
				p := &matrixProvider{inner: freshFake()}
				p.inner.resources["foreign"] = Resource{Kind: "image", Owner: "another-owner"}
				e, err := Open(root, &publicationReadGate{p: p, fail: true}, nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { e.Close() })
				plan := matrixPlan(t, platform, true, false)
				if _, err = e.Build(ctx, plan); err == nil {
					t.Fatal("unconfirmed image published")
				}
				var native Resource
				for _, r := range p.inner.resources {
					if r.Owner != "another-owner" && r.Kind == "image" {
						native = r
					}
				}
				if native.ID == "" {
					t.Fatal("capture was not created")
				}
				creates := p.inner.creates
				altered := native
				switch fault {
				case "foreign":
					altered.Owner = "foreign-owner"
				case "incomplete":
					altered.Complete = false
				}
				if fault == "absent" {
					delete(p.inner.resources, native.ID)
				} else {
					p.inner.resources[native.ID] = altered
				}
				e = restartMatrix(t, e, p, root)
				if _, err = e.RecoverBuild(ctx, plan.Images[0].CacheKey); err == nil || !strings.Contains(err.Error(), "ownership or availability") {
					t.Fatal("stale completed intent published", err)
				}
				var images int
				if err = e.state.DB.QueryRow("SELECT COUNT(*) FROM objects WHERE kind='image'").Scan(&images); err != nil || images != 0 {
					t.Fatal("unverified publication persisted", images, err)
				}
				if p.inner.creates != creates {
					t.Fatal("completed builder replayed")
				}
				p.inner.resources[native.ID] = native
				result, err := e.RecoverBuild(ctx, plan.Images[0].CacheKey)
				if err != nil || len(result) != 1 || result[0].Resource.ID != native.ID {
					t.Fatal(result, err)
				}
				if p.inner.creates != creates {
					t.Fatal("recovery rebuilt captured image")
				}
				if err = e.RemoveImage(ctx, plan.Images[0].CacheKey); err != nil {
					t.Fatal(err)
				}
				assertMatrixClean(t, e, p, native.Owner)
			})
		}
	}
}
