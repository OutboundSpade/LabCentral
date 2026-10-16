//go:build faultinject

package labcentral

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type matrixHTTPProvider struct{ endpoint string }

func (p matrixHTTPProvider) Execute(ctx context.Context, c Command, _ Observer) (Result, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", p.endpoint, bytes.NewReader(b))
	if err != nil {
		return Result{}, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer res.Body.Close()
	var reply struct {
		Result Result
		Error  string
	}
	if err = json.NewDecoder(res.Body).Decode(&reply); err != nil {
		return Result{}, err
	}
	if reply.Error != "" {
		return reply.Result, errors.New(reply.Error)
	}
	return reply.Result, nil
}

type matrixChild struct {
	Root, Endpoint, Operation, Instance, Key, Session string
	Plan                                              Plan
	Blocked                                           bool
}

func matrixChildCommand(t *testing.T, settings matrixChild, selector map[string]any) *exec.Cmd {
	t.Helper()
	file := filepath.Join(t.TempDir(), "child.json")
	b, _ := json.Marshal(settings)
	if err := os.WriteFile(file, b, 0600); err != nil {
		t.Fatal(err)
	}
	selection, _ := json.Marshal(selector)
	cmd := exec.Command(os.Args[0], "-test.run=^TestLifecycleProcessMatrix$")
	cmd.Env = append(os.Environ(), "LC_MATRIX_CHILD="+file, "LABCENTRAL_FAULT="+string(selection))
	return cmd
}
func runMatrixChild(t *testing.T) {
	t.Helper()
	var settings matrixChild
	b, err := os.ReadFile(os.Getenv("LC_MATRIX_CHILD"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &settings); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if settings.Blocked {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 80*time.Millisecond)
		defer cancel()
	}
	e, err := OpenWithOptions(settings.Root, matrixHTTPProvider{settings.Endpoint}, nil, EngineOptions{MaxCleanupConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	switch settings.Operation {
	case "build":
		_, err = e.Build(ctx, settings.Plan)
	case "deploy":
		_, err = e.DeployWithID(ctx, settings.Plan, settings.Instance)
	case "destroy":
		err = e.Destroy(ctx, settings.Instance)
	case "recover":
		_, err = e.Recover(ctx, settings.Instance)
	case "recover-build":
		_, err = e.RecoverBuild(ctx, settings.Key)
	case "image-remove":
		err = e.RemoveImage(ctx, settings.Key)
	case "access-open":
		_, err = e.OpenDesktop(ctx, settings.Instance, settings.Plan.Machines[0].Name)
	case "access-close":
		err = e.CloseAccess(ctx, settings.Session)
	default:
		t.Fatal("unknown child operation", settings.Operation)
	}
	if settings.Blocked {
		if err == nil || (!strings.Contains(err.Error(), "busy") && !strings.Contains(err.Error(), "deadline")) {
			t.Fatalf("second process was not fenced: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
}
func TestLifecycleProcessMatrix(t *testing.T) {
	if os.Getenv("LC_MATRIX_CHILD") != "" {
		runMatrixChild(t)
		return
	}
	for _, platform := range []string{"aws", "proxmox", "aws-windows"} {
		for _, operation := range []string{"build", "publish", "network-create", "gateway-create", "machine-create", "destroy-machine", "destroy-gateway", "destroy-network", "image-remove", "access-open", "access-close", "reserve-vlan", "reserve-vmid", "reserve-management", "reserve-commit", "reserve-build"} {
			if platform == "aws-windows" && !strings.HasPrefix(operation, "access-") {
				continue
			}
			for _, fault := range []string{"kill-before", "kill-during", "kill-after", "concurrent"} {
				// Publication is a local transaction; transport WroteRequest applies only
				// to native HTTP boundaries. Before/after kills cover the commit atomically.
				if strings.HasPrefix(operation, "reserve-") && platform != "proxmox" {
					continue
				}
				if (operation == "publish" || strings.HasPrefix(operation, "reserve-")) && fault == "kill-during" {
					continue
				}
				t.Run(platform+"/"+operation+"/"+fault, func(t *testing.T) {
					backend := &matrixProvider{inner: freshFake()}
					backend.inner.resources["foreign"] = Resource{Kind: "machine", Name: "one", ID: "foreign", Owner: "another-owner"}
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var c Command
						if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
							w.WriteHeader(400)
							return
						}
						if c.Action != "check" && c.Action != "image.read" && c.Action != "discover" {
							requests.Add(1)
						}
						result, err := backend.Execute(r.Context(), c, nil)
						message := ""
						if err != nil {
							message = err.Error()
						}
						_ = json.NewEncoder(w).Encode(struct {
							Result Result
							Error  string
						}{result, message})
					}))
					defer server.Close()
					root := t.TempDir()
					plan := matrixPlan(t, platform, operation == "build" || operation == "publish" || operation == "reserve-build" || operation == "image-remove" || strings.HasPrefix(operation, "access-"), strings.HasPrefix(operation, "access-"))
					e, err := OpenWithOptions(root, matrixHTTPProvider{server.URL}, nil, EngineOptions{MaxCleanupConcurrency: 1})
					if err != nil {
						t.Fatal(err)
					}
					settings := matrixChild{Root: root, Endpoint: server.URL, Plan: plan, Instance: id()}
					var images []ImageRecord
					if strings.HasPrefix(operation, "destroy-") || strings.HasPrefix(operation, "access-") {
						instance, er := e.DeployWithID(context.Background(), plan, settings.Instance)
						if er != nil {
							t.Fatal(er)
						}
						settings.Plan = instance.Plan
					}
					if operation == "image-remove" {
						images, err = e.Build(context.Background(), plan)
						if err != nil {
							t.Fatal(err)
						}
						settings.Key = images[0].Key
					}
					if operation == "access-close" {
						info, er := e.OpenDesktop(context.Background(), settings.Instance, settings.Plan.Machines[0].Name)
						if er != nil {
							t.Fatal(er)
						}
						settings.Session = info.ID
					}
					action, step, nth := "machine.create", "provider.result", 1
					switch operation {
					case "build":
						settings.Operation = "build"
						action = "image.build"
						settings.Key = plan.Images[0].CacheKey
					case "publish":
						settings.Operation = "build"
						action = "image.build"
						step = "publish"
						settings.Key = plan.Images[0].CacheKey
					case "reserve-vlan", "reserve-vmid", "reserve-management", "reserve-commit":
						settings.Operation = "deploy"
						action = "deploy"
						step = "reserve." + strings.TrimPrefix(operation, "reserve-")
					case "reserve-build":
						settings.Operation = "build"
						action = "image.build"
						step = "reserve.commit"
						settings.Key = plan.Images[0].CacheKey
					case "network-create":
						settings.Operation = "deploy"
						action = "network.create"
					case "gateway-create":
						settings.Operation = "deploy"
						action = "gateway.create"
					case "machine-create":
						settings.Operation = "deploy"
					case "destroy-machine":
						settings.Operation = "destroy"
						action = "resource.destroy"
					case "destroy-gateway":
						settings.Operation = "destroy"
						action = "resource.destroy"
						nth = 2
					case "destroy-network":
						settings.Operation = "destroy"
						action = "resource.destroy"
						nth = 3
					case "image-remove":
						settings.Operation = "image-remove"
						action = "resource.destroy"
					case "access-open":
						settings.Operation = "access-open"
						action = "access.open"
					case "access-close":
						settings.Operation = "access-close"
						action = "access.close"
					}
					if err = e.Close(); err != nil {
						t.Fatal(err)
					}
					marker := filepath.Join(t.TempDir(), "hit")
					selected := fault
					if fault == "concurrent" {
						selected = "pause-after"
					}
					child := matrixChildCommand(t, settings, map[string]any{"operation": plan.Environment.Platform + "/" + action, "step": step, "nth": nth, "fault": selected, "marker": marker})
					var output bytes.Buffer
					child.Stdout = &output
					child.Stderr = &output
					if err = child.Start(); err != nil {
						t.Fatal(err)
					}
					if fault == "concurrent" {
						deadline := time.Now().Add(5 * time.Second)
						for {
							if _, err = os.Stat(marker); err == nil {
								break
							}
							if time.Now().After(deadline) {
								child.Process.Kill()
								child.Wait()
								t.Fatal("selected operation not reached", output.String())
							}
							time.Sleep(2 * time.Millisecond)
						}
						count := requests.Load()
						second := settings
						second.Blocked = true
						operations := []string{"recover", "destroy"}
						if operation == "build" || operation == "publish" || operation == "reserve-build" || operation == "image-remove" {
							operations = []string{"recover-build", "image-remove"}
						}
						for _, secondOperation := range operations {
							second.Operation = secondOperation
							bytes, err := matrixChildCommand(t, second, nil).CombinedOutput()
							if err != nil {
								child.Process.Kill()
								child.Wait()
								t.Fatalf("second client: %v %s", err, bytes)
							}
						}
						if requests.Load() != count {
							child.Process.Kill()
							child.Wait()
							t.Fatal("second process crossed native fence")
						}
						if err = child.Process.Kill(); err != nil {
							t.Fatal(err)
						}
					}
					err = child.Wait()
					var status *exec.ExitError
					if !errors.As(err, &status) || status.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
						t.Fatalf("not SIGKILL: %v %s", err, output.String())
					}
					if _, err = os.Stat(marker); err != nil {
						t.Fatal("fault did not trigger", err)
					}
					e, err = OpenWithOptions(root, matrixHTTPProvider{server.URL}, nil, EngineOptions{MaxCleanupConcurrency: 1})
					if err != nil {
						t.Fatal(err)
					}
					defer e.Close()
					ctx := context.Background()
					switch operation {
					case "build", "publish", "reserve-build":
						images, err = e.RecoverBuild(ctx, settings.Key)
					case "image-remove":
						err = e.RemoveImage(ctx, settings.Key)
					default:
						_, err = e.Recover(ctx, settings.Instance)
						if err == nil {
							err = e.Destroy(ctx, settings.Instance)
						}
					}
					if err != nil {
						t.Fatal("persisted restart recovery", err)
					}
					images, err = e.Images(ctx)
					if err != nil {
						t.Fatal(err)
					}
					for _, image := range images {
						if err = e.RemoveImage(ctx, image.Key); err != nil {
							t.Fatal(err)
						}
					}
					backend.inner.mu.Lock()
					defer backend.inner.mu.Unlock()
					if len(backend.inner.resources) != 1 || backend.inner.resources["foreign"].Owner != "another-owner" {
						t.Fatal("native leak or foreign deletion", backend.inner.resources)
					}
					var claims int
					if err = e.state.DB.QueryRow("SELECT COUNT(*) FROM claims").Scan(&claims); err != nil || claims != 0 {
						t.Fatal("reservation leak", claims, err)
					}
				})
			}
		}
	}
}
