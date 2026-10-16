package labcentral

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// Models Packer failure, positive cleanup confirmation and unknown native outcomes.
type retryBuildProvider struct {
	*fakeProvider
	retryMu        sync.Mutex
	failedOwner    string
	cleanupUnknown bool
	abandonUnknown bool
	abandoned      []string
	recovered      []string
}

func (p *retryBuildProvider) Execute(ctx context.Context, c Command, o Observer) (Result, error) {
	p.retryMu.Lock()
	defer p.retryMu.Unlock()
	if c.Action == "image.build" {
		if p.failedOwner == "" {
			p.failedOwner = c.Owner
			return Result{}, errors.New("Packer build failed")
		}
		if c.Owner == p.failedOwner {
			if !c.Recovery {
				return Result{}, errors.New("failed attempt was replayed without recovery")
			}
			p.recovered = append(p.recovered, c.Owner)
			if p.cleanupUnknown {
				return Result{}, errors.New("native cleanup outcome unknown")
			}
			return Result{Absent: true}, nil
		}
	}
	if c.Action == "image.abandon" {
		if len(p.recovered) == 0 {
			return Result{}, errors.New("abandoned before reconciliation")
		}
		p.abandoned = append(p.abandoned, c.Owner)
		if p.abandonUnknown {
			return Result{}, errors.New("abandon cleanup outcome unknown")
		}
		return Result{Absent: true}, nil
	}
	return p.fakeProvider.Execute(ctx, c, o)
}

func TestBuildWithRecoveryRetainsUnknownAttempt(t *testing.T) {
	ctx := context.Background()
	plan := imagePlan(t, "retry")
	p := &retryBuildProvider{fakeProvider: freshFake(), cleanupUnknown: true}
	e, err := Open(t.TempDir(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err = e.BuildWithRecovery(ctx, plan); err == nil {
		t.Fatal("initial Packer failure hidden")
	}
	for range 2 {
		if _, err = e.BuildWithRecovery(ctx, plan); err == nil {
			t.Fatal("unknown cleanup accepted")
		}
		builds, err := e.Builds(ctx)
		if err != nil || len(builds) != 1 || builds[0].Owner != p.failedOwner || len(p.abandoned) != 0 {
			t.Fatal("unknown attempt abandoned or replaced", builds, p.abandoned, err)
		}
	}
	p.cleanupUnknown = false
	images, err := e.BuildWithRecovery(ctx, plan)
	if err != nil || len(images) != 1 || images[0].Resource.Owner == p.failedOwner || len(p.abandoned) != 1 {
		t.Fatal("confirmed cleanup did not permit replacement", images, p.abandoned, err)
	}
}

func TestBuildWithRecoveryPublishesLostSuccessfulResponse(t *testing.T) {
	ctx := context.Background()
	p := freshFake()
	p.loseOnce = true
	e := openFake(t, t.TempDir(), p)
	plan := imagePlan(t, "response-loss")
	if _, err := e.BuildWithRecovery(ctx, plan); err == nil {
		t.Fatal("response loss hidden")
	}
	builds, err := e.Builds(ctx)
	if err != nil || len(builds) != 1 {
		t.Fatal(builds, err)
	}
	images, err := e.BuildWithRecovery(ctx, plan)
	if err != nil || len(images) != 1 || images[0].Resource.Owner != builds[0].Owner || p.creates != 1 {
		t.Fatal("successful unknown attempt replaced", images, p.creates, err)
	}
}

func TestBuildWithRecoveryRestartBoundaries(t *testing.T) {
	for _, boundary := range []string{"during-abandon", "after-abandon"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			plan := imagePlan(t, "restart")
			key := plan.Images[0].CacheKey
			root := t.TempDir()
			p := &retryBuildProvider{fakeProvider: freshFake()}
			e, err := Open(root, p, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { e.Close() }()
			if _, err = e.BuildWithRecovery(ctx, plan); err == nil {
				t.Fatal("initial failure hidden")
			}
			if boundary == "during-abandon" {
				p.abandonUnknown = true
			} else {
				// Fail the next intent write only after the old attempt was fully removed.
				_, err = e.state.DB.Exec(`CREATE TRIGGER interrupt_rebuild BEFORE INSERT ON objects WHEN NEW.kind='build' BEGIN SELECT RAISE(FAIL,'simulated restart before replacement intent'); END`)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err = e.BuildWithRecovery(ctx, plan); err == nil {
				t.Fatal("interruption hidden")
			}
			var cleanup Command
			cleanupErr := e.state.Get(ctx, "image-cleanup", key, &cleanup)
			builds, err := e.Builds(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if boundary == "during-abandon" {
				if cleanupErr != nil || len(builds) != 1 || builds[0].Owner != p.failedOwner {
					t.Fatal("cleanup intent lost", cleanupErr, builds)
				}
			} else if !errors.Is(cleanupErr, sql.ErrNoRows) || len(builds) != 0 {
				t.Fatal("abandonment did not finish before interruption", cleanupErr, builds)
			}
			if err = e.Close(); err != nil {
				t.Fatal(err)
			}
			e, err = Open(root, p, nil)
			if err != nil {
				t.Fatal(err)
			}
			if boundary == "after-abandon" {
				if _, err = e.state.DB.Exec(`DROP TRIGGER interrupt_rebuild`); err != nil {
					t.Fatal(err)
				}
			}
			p.abandonUnknown = false
			images, err := e.BuildWithRecovery(ctx, plan)
			if err != nil || len(images) != 1 || images[0].Resource.Owner == p.failedOwner || p.creates != 1 {
				t.Fatal("restart failed to finish replacement", images, err)
			}
		})
	}
}

// The subprocess holds an actual engine cache-key lock while native work runs.
type processBuildProvider struct{}

func (processBuildProvider) Execute(_ context.Context, c Command, _ Observer) (Result, error) {
	switch c.Action {
	case "check":
		return Result{}, nil
	case "image.build":
		fmt.Fprintln(os.Stdout, "building")
		var input [1]byte
		_, _ = os.Stdin.Read(input[:])
		return Result{Resource: Resource{ID: "native-image", Owner: c.Owner, Kind: "image", Complete: true}}, nil
	}
	return Result{}, fmt.Errorf("unexpected child action %s", c.Action)
}
func TestBuildWithRecoveryProcessHelper(t *testing.T) {
	root := os.Getenv("LABCENTRAL_BUILD_RETRY_TEST_ROOT")
	if root == "" {
		return
	}
	e, err := Open(root, processBuildProvider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err = e.Build(context.Background(), imagePlan(t, "process")); err != nil {
		t.Fatal(err)
	}
}
func TestBuildWithRecoveryNeverAbandonsOtherProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBuildWithRecoveryProcessHelper$")
	cmd.Env = append(os.Environ(), "LABCENTRAL_BUILD_RETRY_TEST_ROOT="+root)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stdin.Close()
		if cmd.ProcessState == nil {
			cmd.Wait()
		}
	}()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "building" {
		t.Fatal("child did not begin native build", scanner.Err())
	}
	p := &retryBuildProvider{fakeProvider: freshFake()}
	e, err := Open(root, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	waiting, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	_, err = e.BuildWithRecovery(waiting, imagePlan(t, "process"))
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("retry overlapped active process", err)
	}
	if len(p.abandoned) != 0 || len(p.recovered) != 0 || p.failedOwner != "" {
		t.Fatal("retry touched active attempt", p)
	}
	stdin.Close()
	for scanner.Scan() {
	} // Drain the test harness output before waiting.
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	images, err := e.Images(ctx)
	if err != nil || len(images) != 1 || !images[0].Resource.Complete {
		t.Fatal(images, err)
	}
}
