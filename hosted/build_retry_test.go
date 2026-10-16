package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/build"
)

// Uses the real engine/journal and the Packer runner's recovery contract, with
// fake native resources. No live platform or Packer qualification is claimed.
type hostedBuildRetryProvider struct {
	mu             sync.Mutex
	failedOwner    string
	cleanupUnknown bool
	starts         map[string]int
	resources      map[string]lc.Resource
	abandoned      []string
}

func (p *hostedBuildRetryProvider) Execute(ctx context.Context, c lc.Command, o lc.Observer) (lc.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch c.Action {
	case "check":
		return lc.Result{}, nil
	case "image.read":
		r, ok := p.resources[c.Prior.ID]
		return lc.Result{Resource: r, Absent: !ok}, nil
	case "image.build":
		if c.Owner == p.failedOwner {
			if !c.Recovery {
				return lc.Result{}, errors.New("failed build replayed")
			}
			runner := &build.Runner{
				Find: func(context.Context, lc.Command) (lc.Resource, bool, error) { return lc.Resource{}, false, nil },
				Cleanup: func(context.Context, lc.Command) error {
					if p.cleanupUnknown {
						return errors.New("native cleanup outcome unknown")
					}
					return nil
				},
				Validate: func(context.Context, lc.Command, lc.Resource) (lc.Resource, error) {
					return lc.Resource{}, errors.New("unexpected validation")
				},
			}
			return runner.Run(ctx, c, o)
		}
		p.starts[c.Image.Name]++
		if c.Image.Name == "database" && p.failedOwner == "" {
			p.failedOwner = c.Owner
			return lc.Result{}, errors.New("Packer build failed")
		}
		r := lc.Resource{Kind: "image", Name: c.Image.Name, ID: c.Owner + "/image", Owner: c.Owner, Complete: true}
		p.resources[r.ID] = r
		return lc.Result{Resource: r}, nil
	case "image.abandon":
		p.abandoned = append(p.abandoned, c.Owner)
		if p.cleanupUnknown {
			return lc.Result{}, errors.New("cleanup unconfirmed")
		}
		return lc.Result{Absent: true}, nil
	}
	return lc.Result{}, fmt.Errorf("unexpected native operation %s", c.Action)
}
func hostedBuildRetryFixture(t *testing.T) (*Service, *hostedBuildRetryProvider, *lc.Engine, Job) {
	t.Helper()
	s, _ := prepareFixture(t)
	ctx := context.Background()
	var session Session
	if err := s.Store.Get(ctx, "session", digest("cookie"), &session); err != nil {
		t.Fatal(err)
	}
	session.User.Admin = true
	if err := s.Store.Put(ctx, "session", session.ID, session); err != nil {
		t.Fatal(err)
	}
	src, err := lc.Load("../examples/web-database")
	if err != nil {
		t.Fatal(err)
	}
	s.env.Build = lc.BuildConfig{Packer: "/packer", PackerSHA256: strings.Repeat("a", 64), PluginDirectory: "/plugins", ToolchainID: "toolchain", AnsibleVersion: "2.19.3", TimeoutSeconds: 3600}
	for _, id := range []string{"first", "second"} {
		if err = s.Store.Put(ctx, "source", id, src); err != nil {
			t.Fatal(err)
		}
		if err = s.Store.Put(ctx, "template", id, Template{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	p := &hostedBuildRetryProvider{starts: map[string]int{}, resources: map[string]lc.Resource{}}
	e, err := lc.Open(t.TempDir(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	s.Engine = e
	j, err := s.queueTemplateOperation(ctx, "build", "first")
	if err != nil {
		t.Fatal(err)
	}
	err = s.runJob(ctx, j)
	if err == nil || !strings.Contains(err.Error(), "Packer build failed") {
		t.Fatal("initial failure hidden", err)
	}
	finishImageJob(t, s, j, err)
	return s, p, e, j
}
func retryBuildJob(t *testing.T, s *Service, failed Job) Job {
	t.Helper()
	w := request(s, "POST", "/api/admin/jobs/retry", jsonValue(TargetRequest{ID: failed.ID}), s.Config.Origin, "csrf")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var j Job
	if err := json.Unmarshal(w.Body.Bytes(), &j); err != nil {
		t.Fatal(err)
	}
	return j
}
func TestHostedBuildRetryKeepsReadySiblingAndSharedKeys(t *testing.T) {
	for _, action := range []string{"retry", "new-build", "shared-templates"} {
		t.Run(action, func(t *testing.T) {
			s, p, e, failed := hostedBuildRetryFixture(t)
			ctx := context.Background()
			before, err := e.Images(ctx)
			if err != nil || len(before) != 1 || before[0].Name != "web" {
				t.Fatal(before, err)
			}
			var jobs []Job
			if action == "new-build" {
				j, err := s.queueTemplateOperation(ctx, "build", "first")
				if err != nil {
					t.Fatal(err)
				}
				jobs = append(jobs, j)
			} else {
				jobs = append(jobs, retryBuildJob(t, s, failed))
			}
			if action == "shared-templates" {
				j, err := s.queueTemplateOperation(ctx, "build", "second")
				if err != nil {
					t.Fatal(err)
				}
				jobs = append(jobs, j)
				firstKeys, err := s.templateKeys(ctx, "first")
				if err != nil {
					t.Fatal(err)
				}
				secondKeys, err := s.templateKeys(ctx, "second")
				if err != nil || jsonValue(firstKeys) != jsonValue(secondKeys) {
					t.Fatal("fixture keys not shared", firstKeys, secondKeys, err)
				}
			}
			var wg sync.WaitGroup
			for _, j := range jobs {
				wg.Go(func() {
					if err := s.runJob(ctx, j); err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			for _, j := range jobs {
				var revision Template
				if err := s.Store.Get(ctx, "template", j.Target, &revision); err != nil || !revision.Built {
					t.Fatal(revision, err)
				}
				finishImageJob(t, s, j, nil)
			}
			images, err := e.Images(ctx)
			if err != nil || len(images) != 2 {
				t.Fatal(images, err)
			}
			for _, im := range images {
				if im.Name == "web" && im.ID != before[0].ID {
					t.Fatal("good sibling replaced", im, before)
				}
			}
			if p.starts["web"] != 1 || p.starts["database"] != 2 || len(p.abandoned) != 1 || p.abandoned[0] != p.failedOwner {
				t.Fatal("shared images rebuilt or wrong generation abandoned", p.starts, p.abandoned)
			}
		})
	}
}
func TestHostedBuildRetryUnknownCleanupIsActionableAndRetryable(t *testing.T) {
	s, p, e, failed := hostedBuildRetryFixture(t)
	ctx := context.Background()
	before, err := e.Builds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.cleanupUnknown = true
	retry := retryBuildJob(t, s, failed)
	err = s.runJob(ctx, retry)
	if err == nil {
		t.Fatal("unknown cleanup accepted")
	}
	finishImageJob(t, s, retry, err)
	// Read the same persisted message and retry eligibility used by Admin → Templates.
	templates, err := s.templates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, revision := range templates {
		if revision.ID != "first" {
			continue
		}
		found = true
		if revision.Built || revision.ImagesReady != 1 || revision.BuildsPending != 1 || revision.Operation == nil {
			t.Fatal(revision)
		}
		message := revision.Operation.Error
		for _, text := range []string{"Retry build", "ready images are kept", "ask the operator", "then retry", "native cleanup outcome unknown"} {
			if !strings.Contains(message, text) {
				t.Fatal("admin message not actionable", message)
			}
		}
	}
	if !found {
		t.Fatal("revision not shown")
	}
	after, err := e.Builds(ctx)
	if err != nil || jsonValue(before) != jsonValue(after) || len(p.abandoned) != 0 || p.starts["database"] != 1 {
		t.Fatal("unknown attempt abandoned or replaced", before, after, p.abandoned, err)
	}
	blocked, attention, err := s.retryDisposition(ctx, retry)
	if err != nil || blocked != "" || !attention {
		t.Fatal(blocked, attention, err)
	}
	p.cleanupUnknown = false
	retry = retryBuildJob(t, s, retry)
	if err := s.runJob(ctx, retry); err != nil {
		t.Fatal(err)
	}
}
func TestHostedBuildRetryAuthorizationBeforeRecovery(t *testing.T) {
	for _, unauthorized := range []string{"not-admin", "bad-csrf"} {
		t.Run(unauthorized, func(t *testing.T) {
			s, p, _, failed := hostedBuildRetryFixture(t)
			csrf := "csrf"
			if unauthorized == "not-admin" {
				var session Session
				if err := s.Store.Get(context.Background(), "session", digest("cookie"), &session); err != nil {
					t.Fatal(err)
				}
				session.User.Admin = false
				if err := s.Store.Put(context.Background(), "session", session.ID, session); err != nil {
					t.Fatal(err)
				}
			} else {
				csrf = "wrong"
			}
			w := request(s, "POST", "/api/admin/jobs/retry", jsonValue(TargetRequest{ID: failed.ID}), s.Config.Origin, csrf)
			if w.Code != 403 || len(p.abandoned) != 0 || p.starts["database"] != 1 {
				t.Fatal(w.Code, p.abandoned, p.starts)
			}
		})
	}
}
