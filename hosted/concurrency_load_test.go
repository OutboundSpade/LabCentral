package hosted

import (
	"context"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type loadCounter struct{ active, maximum atomic.Int32 }

func (c *loadCounter) enter() func() {
	n := c.active.Add(1)
	for {
		old := c.maximum.Load()
		if n <= old || c.maximum.CompareAndSwap(old, n) {
			break
		}
	}
	return func() { c.active.Add(-1) }
}
func loadWait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type loadProvider struct {
	lc.Provider
	latency time.Duration
	native  loadCounter
}

func (p *loadProvider) Execute(ctx context.Context, c lc.Command, _ lc.Observer) (lc.Result, error) {
	switch c.Action {
	case "check":
		return lc.Result{}, nil
	case "machine.create":
		release := p.native.enter()
		defer release()
		if err := loadWait(ctx, p.latency); err != nil {
			return lc.Result{}, err
		}
		return lc.Result{Resource: lc.Resource{Kind: "machine", Name: c.Machine.Name, ID: c.Owner + "/" + c.Machine.Name, Owner: c.Owner, Address: "192.0.2.10", Complete: true}}, nil
	case "network.create":
		return lc.Result{Resource: lc.Resource{Kind: "network", Name: "private", ID: c.Owner + "/network", Owner: c.Owner, Complete: true}}, nil
	case "resource.destroy":
		if c.Prior.Kind == "machine" {
			release := p.native.enter()
			defer release()
			if err := loadWait(ctx, p.latency); err != nil {
				return lc.Result{}, err
			}
		}
		return lc.Result{Absent: true}, nil
	case "access.close":
		return lc.Result{}, nil
	default:
		return lc.Result{}, fmt.Errorf("unexpected load provider action %s", c.Action)
	}
}

type loadEngine struct {
	Engine
	latency time.Duration
	ssh     loadCounter
}

func (e *loadEngine) WithSSHMaterial(ctx context.Context, _, _ string, _ func(lc.Instance, lc.PlannedMachine, lc.Resource, []byte) error) error {
	release := e.ssh.enter()
	defer release()
	if err := loadWait(ctx, e.latency); err != nil {
		return err
	}
	return errPlaybookFailed // observation keeps students on the periodic action stage
}
func loadDuration(t *testing.T, key string, fallback time.Duration) time.Duration {
	t.Helper()
	if raw := os.Getenv(key); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	return fallback
}
func loadPercentiles(values []time.Duration) (time.Duration, time.Duration) {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	if len(values) == 0 {
		return 0, 0
	}
	return values[(len(values)-1)*50/100], values[(len(values)-1)*95/100]
}

// Opt-in, entirely in-process: authenticated HTTP handlers, real hosted SQLite,
// scheduler and library lifecycle; fake provider/SSH delays perform no network I/O.
// Run the identical harness on main using an identity provider-budget shim.
func TestConcurrencyLoad(t *testing.T) {
	if os.Getenv("LABCENTRAL_LOAD_TEST") != "1" {
		t.Skip("set LABCENTRAL_LOAD_TEST=1 for the local cohort experiment")
	}
	s, _ := prepareFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s.Config.Root = t.TempDir()
	nativeLatency := loadDuration(t, "LABCENTRAL_LOAD_NATIVE", 250*time.Millisecond)
	sshLatency := loadDuration(t, "LABCENTRAL_LOAD_SSH", 100*time.Millisecond)
	duration := loadDuration(t, "LABCENTRAL_LOAD_DURATION", 65*time.Second)
	dir := t.TempDir()
	var fixtureSource lc.Source
	if err := s.Store.Get(ctx, "source", "", &fixtureSource); err != nil {
		t.Fatal(err)
	}
	lab := fmt.Sprintf("version: %d\nname: cohort\nimages:\n  base:\n    base: ubuntu-24.04\nmachines:\n", fixtureSource.Lab.Version)
	for _, name := range []string{"server", "db", "files", "desktop"} {
		lab += "  " + name + ":\n    image: base\n    cpu: 2\n    memory: 2Gi\n    disk: 20Gi\n    access: [ssh]\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "lab.yaml"), []byte(lab), 0600); err != nil {
		t.Fatal(err)
	}
	src, err := lc.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	bundle := InstructorBundle{Version: fixtureSource.Lab.Version, Files: []lc.File{{Path: "observe.yml", Content: []byte("- hosts: localhost\n  tasks: []\n")}}, Checks: []TemplateCheck{{ID: "observe", Name: "Service", Points: 1, Steps: []GuestPlaybook{{Machine: "server", Playbook: "observe.yml"}}}}}
	publication := LearningPublication{ID: "cohort", Title: "Cohort", Template: "", Mode: "self-paced", Published: true, PublishedAt: time.Now().UTC(), ReleasedSteps: 1, Steps: []LearningStepInput{{ID: "observe", Kind: "action", CheckID: "observe"}}}
	for _, o := range []struct {
		kind, id string
		value    any
	}{{"source", "", src}, {"template", "", Template{ID: "", Name: "Cohort", Built: true}}, {"template-images", "", []string{}}, {"template-instructor", "", bundle}, {"learning-publication", "cohort", publication}} {
		if err = s.Store.Put(ctx, o.kind, o.id, o.value); err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.Store.DB.ExecContext(ctx, `DELETE FROM teams;DELETE FROM memberships;DELETE FROM jobs;DELETE FROM objects WHERE kind='meeting'`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	m := Meeting{ID: "cohort-meeting", Template: "", Status: "waiting", Created: now, Expires: now.Add(time.Hour)}
	if err = s.Store.CreateMeeting(ctx, m, 20, 5); err != nil {
		t.Fatal(err)
	}
	native := &loadProvider{latency: nativeLatency}
	provider := loadBudgetProvider(t, s, native)
	engine, err := lc.Open(filepath.Join(s.Config.Root, "engine"), provider, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	fake := &loadEngine{Engine: engine, latency: sshLatency}
	s.Engine = fake
	cookies := []string{}
	for n := range 105 {
		cookie := fmt.Sprintf("load-%d", n)
		cookies = append(cookies, cookie)
		v := Session{ID: digest(cookie), User: User{ID: cookie, Name: cookie, Admin: n >= 100}, CSRF: "csrf", Expires: now.Add(time.Hour), Checked: now}
		if err = s.Store.Put(ctx, "session", v.ID, v); err != nil {
			t.Fatal(err)
		}
		if n < 100 {
			_, err = s.Store.DB.ExecContext(ctx, `INSERT INTO practice_runs(id,user_id,lab_id,instance_id,status,created,expires) VALUES(?,?,'cohort',?,'ready',?,?)`, fmt.Sprintf("run-%d", n), cookie, randomID(12), now.Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = s.Store.Enqueue(ctx, "prepare", m.ID); err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()
	var measurements sync.Mutex
	httpTimes := map[string][]time.Duration{}
	var failures atomic.Int32
	pollCtx, stopPoll := context.WithCancel(ctx)
	var polls sync.WaitGroup
	poll := func(cookie, path string, interval time.Duration) {
		polls.Go(func() {
			tick := time.NewTicker(interval)
			defer tick.Stop()
			for {
				r := httptest.NewRequest("GET", "https://labs.invalid"+path, nil).WithContext(pollCtx)
				r.AddCookie(&http.Cookie{Name: "__Host-labcentral", Value: cookie})
				start := time.Now()
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				elapsed := time.Since(start)
				if w.Code != 200 && pollCtx.Err() == nil {
					failures.Add(1)
				}
				measurements.Lock()
				httpTimes[path] = append(httpTimes[path], elapsed)
				measurements.Unlock()
				select {
				case <-pollCtx.Done():
					return
				case <-tick.C:
				}
			}
		})
	}
	for n := range 100 {
		poll(cookies[n], "/api/learning/cohort", 5*time.Second)
	}
	for n := 100; n < 105; n++ {
		poll(cookies[n], "/api/admin", 2*time.Second)
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() { workerDone <- s.Run(workerCtx) }()
	defer func() { stopPoll(); polls.Wait(); stopWorker(); <-workerDone }()
	var destroyElapsed time.Duration
	destroyDone := make(chan error, 1)
	go func() {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			current, e := s.Store.Meeting(ctx)
			if e != nil {
				destroyDone <- e
				return
			}
			if current.Status == "failed" || current.Status == "partial" {
				jobs, _ := s.Store.Jobs(ctx)
				destroyDone <- fmt.Errorf("preparation failed: %+v", jobs)
				return
			}
			if current.Status == "ready" {
				current.Status = "ending"
				if e = s.Store.Put(ctx, "meeting", "current", current); e != nil {
					destroyDone <- e
					return
				}
				start := time.Now()
				if _, e = s.Store.Enqueue(ctx, "end", m.ID); e != nil {
					destroyDone <- e
					return
				}
				for {
					current, e = s.Store.Meeting(ctx)
					if e != nil {
						destroyDone <- e
						return
					}
					if current.Status == "ended" {
						destroyElapsed = time.Since(start)
						destroyDone <- nil
						return
					}
					select {
					case <-ctx.Done():
						destroyDone <- ctx.Err()
						return
					case <-tick.C:
					}
				}
			}
			select {
			case <-ctx.Done():
				destroyDone <- ctx.Err()
				return
			case <-tick.C:
			}
		}
	}()
	if err = loadWait(ctx, duration); err != nil {
		t.Fatal(err)
	}
	if err = <-destroyDone; err != nil {
		t.Fatal(err)
	}
	stopPoll()
	polls.Wait()
	if failures.Load() != 0 {
		t.Fatalf("HTTP failures: %d", failures.Load())
	}
	for _, path := range []string{"/api/admin", "/api/learning/cohort"} {
		p50, p95 := loadPercentiles(httpTimes[path])
		t.Logf("HTTP %s n=%d p50=%s p95=%s", path, len(httpTimes[path]), p50, p95)
	}
	rows, err := s.Store.DB.QueryContext(ctx, `SELECT j.created,json_extract(o.body,'$.started') FROM jobs j JOIN objects o ON o.kind='job-timing' AND o.id=j.id WHERE j.kind='learning-check'`)
	if err != nil {
		t.Fatal(err)
	}
	var delays []time.Duration
	for rows.Next() {
		var created, started string
		if err = rows.Scan(&created, &started); err != nil {
			t.Fatal(err)
		}
		a, _ := time.Parse(time.RFC3339Nano, created)
		b, _ := time.Parse(time.RFC3339Nano, started)
		delays = append(delays, b.Sub(a))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	var queued int
	if err = s.Store.DB.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE kind='learning-check' AND status='queued'`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	p50, p95 := loadPercentiles(delays)
	t.Logf("CHECK_QUEUE admitted=%d pending=%d p50=%s p95=%s", len(delays), queued, p50, p95)
	t.Logf("OPERATIONS max_native=%d max_ssh=%d destruction=%s native_latency=%s ssh_latency=%s duration=%s", native.native.maximum.Load(), fake.ssh.maximum.Load(), destroyElapsed, nativeLatency, sshLatency, duration)
	if os.Getenv("LABCENTRAL_LOAD_BASELINE") != "1" && (native.native.maximum.Load() > 8 || fake.ssh.maximum.Load() > 24) {
		t.Fatal("aggregate budget exceeded")
	}
}
