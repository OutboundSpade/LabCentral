package aws

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

type boundary struct {
	action string
	nth    int
}

func networkBoundaries(t *testing.T, cleanup bool) []boundary {
	t.Helper()
	f, a := newNetworkEC2(t)
	p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
	c := twoNetworks()
	result, err := p.network(context.Background(), a, c)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup {
		f.requests = nil
		c.Prior = result.Resource
		if _, err = p.destroyNetwork(context.Background(), a, c); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]int{}
	var out []boundary
	for _, r := range f.requests {
		action := r.Get("Action")
		if strings.HasPrefix(action, "Describe") {
			continue
		}
		seen[action]++
		out = append(out, boundary{action, seen[action]})
	}
	return out
}
func boundaryClient(t *testing.T, f *networkEC2, a *ec2.Client, target boundary, fault string) (*ec2.Client, *bool) {
	t.Helper()
	hit := false
	seen := 0
	opts := a.Options()
	opts.HTTPClient = optionHTTPClient(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		values, _ := url.ParseQuery(string(body))
		match := false
		if values.Get("Action") == target.action {
			seen++
			match = seen == target.nth && !hit
		}
		if match {
			hit = true
		}
		if match && fault != "response-loss" {
			if fault == "reset-before" {
				return nil, io.ErrUnexpectedEOF
			}
			code := map[string]string{"authorization": "UnauthorizedOperation", "validation": "InvalidParameterValue", "throttle": "RequestLimitExceeded", "server": "InternalError"}[fault]
			status := 400
			if fault == "server" {
				status = 500
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/xml"}}, Body: io.NopCloser(strings.NewReader(`<Response><Errors><Error><Code>` + code + `</Code><Message>injected before effect</Message></Error></Errors></Response>`))}, nil
		}
		response, err := f.do(r)
		if match && err == nil {
			response.Body.Close()
			return nil, io.ErrUnexpectedEOF
		}
		return response, err
	})
	return ec2.New(opts), &hit
}

// Every native network mutation occurrence is selected from the successful trace,
// including per-segment resources, repeated SG rules, associations and endpoints.
func TestAWSNetworkFailureMatrix(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		operation := "deploy"
		if cleanup {
			operation = "destroy"
		}
		for _, step := range networkBoundaries(t, cleanup) {
			for _, fault := range []string{"response-loss", "reset-before", "authorization", "validation", "throttle", "server"} {
				t.Run(fmt.Sprintf("%s/%s/%d/%s", operation, step.action, step.nth, fault), func(t *testing.T) {
					ctx := context.Background()
					f, a := newNetworkEC2(t)
					root := t.TempDir()
					p := &Provider{Journal: journal.Journal{Root: root}}
					c := twoNetworks()
					var result lc.Result
					var err error
					if cleanup {
						result, err = p.network(ctx, a, c)
						if err != nil {
							t.Fatal(err)
						}
						c.Prior = result.Resource
					}
					// A same-role, same-component foreign object must never become a discovery
					// result or cleanup target. Native tag filtering is modeled faithfully.
					f.objects = append(f.objects, networkObject{kind: "sg", id: "sg-foreign", role: "network", component: "lab-sg"})
					f.owners = map[string]string{"sg-foreign": "foreign"}
					damaged, hit := boundaryClient(t, f, a, step, fault)
					if cleanup {
						_, err = p.destroyNetwork(ctx, damaged, c)
					} else {
						_, err = p.network(ctx, damaged, c)
					}
					if !*hit {
						t.Fatal("fault not observed", *hit, err)
					}
					// New provider over the exact persisted journal models provider restart.
					p = &Provider{Journal: journal.Journal{Root: root}}
					if cleanup {
						result, err = p.destroyNetwork(ctx, a, c)
					} else {
						result, err = p.network(ctx, a, c)
					}
					if err != nil {
						if fault != "reset-before" && fault != "server" {
							t.Fatal("recovery failed", err)
						}
						if !strings.Contains(err.Error(), "recovery_required") {
							t.Fatal("unknown create is not actionable", err)
						}
						// No empty discovery may cause a second non-idempotent create.
						before := len(f.objects)
						if _, again := p.network(ctx, a, c); again == nil || len(f.objects) != before {
							t.Fatal("uncertain create replayed", again)
						}
						if len(f.objects) > 1 { // Every retained native resource still has its durable owner journal.
							files, e := osReadJournal(root)
							if e != nil || files == 0 {
								t.Fatal("owned inventory lost its journal", e)
							}
						}
					} else if !cleanup {
						c.Prior = result.Resource
						result, err = p.destroyNetwork(ctx, a, c)
						if err != nil || !result.Absent {
							t.Fatal(result, err)
						}
					}
					foreign := false
					for _, object := range f.objects {
						if object.id == "sg-foreign" {
							foreign = true
						}
					}
					if !foreign {
						t.Fatal("foreign inventory deleted")
					}
					if err == nil && len(f.objects) != 1 {
						t.Fatal("native inventory leaked", f.objects)
					}
				})
			}
		}
	}
}

func TestAWSRecordedNetworkOwnershipMatrix(t *testing.T) {
	for _, kind := range []string{"vpc", "subnet", "route", "sg", "igw", "endpoint"} {
		for _, state := range []string{"absent", "foreign"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				f, a := newNetworkEC2(t)
				p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
				c := twoNetworks()
				result, err := p.network(context.Background(), a, c)
				if err != nil {
					t.Fatal(err)
				}
				c.Prior = result.Resource
				target := ""
				for n, o := range f.objects {
					if o.kind == kind {
						target = o.id
						if state == "absent" {
							f.objects = append(f.objects[:n], f.objects[n+1:]...)
						} else {
							f.owners = map[string]string{o.id: "foreign"}
						}
						break
					}
				}
				if target == "" {
					t.Fatal("missing test target")
				}
				start := len(f.requests)
				result, err = p.destroyNetwork(context.Background(), a, c)
				if state == "foreign" {
					if err == nil {
						t.Fatal("ownership loss accepted")
					}
					for _, r := range f.requests[start:] {
						if !strings.HasPrefix(r.Get("Action"), "Describe") {
							t.Fatal("mutation before ownership resolved", r.Get("Action"))
						}
					}
					// Restoring the exact expected ownership allows existing intent to finish.
					f.owners = nil
					result, err = p.destroyNetwork(context.Background(), a, c)
				}
				if err != nil || !result.Absent || len(f.objects) != 0 {
					t.Fatal("cleanup did not reconcile", result, err, f.objects)
				}
			})
		}
	}
}

func TestAWSCheckpointOwnershipRecheckedBeforeMutation(t *testing.T) {
	for _, kind := range []string{"vpc", "igw", "subnet", "route", "sg"} {
		t.Run(kind, func(t *testing.T) {
			f, a := newNetworkEC2(t)
			root := t.TempDir()
			c := twoNetworks()
			if _, err := (&Provider{Journal: journal.Journal{Root: root}}).network(context.Background(), a, c); err != nil {
				t.Fatal(err)
			}
			for _, o := range f.objects {
				if o.kind == kind {
					f.owners = map[string]string{o.id: "foreign"}
					break
				}
			}
			_, err := (&Provider{Journal: journal.Journal{Root: root}}).network(context.Background(), a, c)
			if err == nil || !strings.Contains(err.Error(), "recovery_required") {
				t.Fatal("stale checkpoint reused", err)
			}
		})
	}
}

func osReadJournal(root string) (int, error) {
	entries, err := os.ReadDir(root)
	return len(entries), err
}
