package hosted

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
)

func capacityFixture(t *testing.T) (*Service, Session, LearningPublication, SiteCapacity) {
	s, v, p := learningFixture(t)
	s.env = proxmoxEnvironment()
	s.env.Build = lc.BuildConfig{Packer: "/packer", PackerSHA256: strings.Repeat("a", 64), PluginDirectory: "/plugins", ToolchainID: "toolchain", AnsibleVersion: "2.19.3", Bridge: "vmbr0", TimeoutSeconds: 3600}
	plan, err := s.plan(context.Background(), p.Template)
	if err != nil {
		t.Fatal(err)
	}
	size, err := planCapacity(plan)
	if err != nil {
		t.Fatal(err)
	}
	s.Config.Capacity = size
	return s, v, p, size
}
func TestSiteCapacityPracticeRaceAndCleanupReservation(t *testing.T) {
	s, v, p, size := capacityFixture(t)
	ctx := context.Background()
	b := consoleUser(t, s, "student-b", false)
	sessions := []Session{v, b}
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for n := range sessions {
		wg.Go(func() { codes[n] = consoleRequest(s, sessions[n], "/api/learning/"+p.ID+"/start").Code })
	}
	wg.Wait()
	if codes[0]+codes[1] != 202+409 {
		t.Fatal("overbooked", codes)
	}
	var winner Session
	if codes[0] == 202 {
		winner = v
	} else {
		winner = b
	}
	active, err := s.Store.activePractice(ctx, winner.User.ID)
	if err != nil || active == nil {
		t.Fatal(active, err)
	}
	if _, err = s.Store.endPractice(ctx, active.ID, winner.User.ID); err != nil {
		t.Fatal(err)
	}
	// Stopping, expiry, failed and unconfirmed native cleanup all keep capacity.
	if err = s.Store.atomic(ctx, func(ctx context.Context) error { return s.checkCapacity(ctx, size) }); err == nil {
		t.Fatal("released before confirmation")
	}
	root := s.Store.Root
	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	next := &Service{Config: s.Config, Store: reopened, Engine: s.Engine, env: s.env}
	if err = next.Store.atomic(ctx, func(ctx context.Context) error { return next.checkCapacity(ctx, size) }); err == nil {
		t.Fatal("restart lost reservation")
	}
	if _, err = s.Store.DB.ExecContext(ctx, `UPDATE practice_runs SET status='ended',ended=expires WHERE id=?`, active.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Store.atomic(ctx, func(ctx context.Context) error { return s.checkCapacity(ctx, size) }); err != nil {
		t.Fatal(err)
	}
}
func TestSiteCapacityMeetingIncludesGatewayAndWaitingTeams(t *testing.T) {
	s, v, p, size := capacityFixture(t)
	v.User.Admin = true
	if err := s.Store.Put(context.Background(), "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	create := func(count int) int {
		body, _ := json.Marshal(CreateMeeting{Name: "Capacity meeting", Template: p.Template, TeamCount: count, Capacity: 2, Hours: 1})
		return request(s, "POST", "/api/admin/meeting", string(body), s.Config.Origin, v.CSRF).Code
	}
	// Gateway alone pushes this request beyond the ceiling.
	s.Config.Capacity = SiteCapacity{size.VCPU - 1, size.MemoryMiB}
	if status := create(1); status != 409 {
		t.Fatal(status)
	}
	s.Config.Capacity = SiteCapacity{size.VCPU * 2, size.MemoryMiB * 2}
	if status := create(2); status != 201 {
		t.Fatal(status)
	}
	if got := consoleRequest(s, v, "/api/learning/"+p.ID+"/start"); got.Code != 409 || !strings.Contains(got.Body.String(), "Site capacity exceeded") {
		t.Fatal(got.Code, got.Body.String())
	}
	// Lowering the configured ceiling refuses preparation of an old waiting meeting.
	s.Config.Capacity = size
	if got := consoleRequest(s, v, "/api/admin/prepare"); got.Code != 409 {
		t.Fatal(got.Code, got.Body.String())
	}
}
func TestSiteCapacityBuildReservationPersists(t *testing.T) {
	s, _, p, _ := capacityFixture(t)
	ctx := context.Background()
	plan, err := s.plan(ctx, p.Template)
	if err != nil {
		t.Fatal(err)
	}
	s.Config.Capacity = SiteCapacity{3, 4864}
	if err = s.reserveBuildCapacity(ctx, p.Template, plan); err != nil {
		t.Fatal(err)
	}
	if err = s.Store.atomic(ctx, func(ctx context.Context) error { return s.checkCapacity(ctx, SiteCapacity{1, 1}) }); err == nil {
		t.Fatal("build not counted")
	}
	if err = s.reserveBuildCapacity(ctx, p.Template, plan); err != nil {
		t.Fatal("retry double counted", err)
	}
	s.Config.Capacity = SiteCapacity{2, 4864}
	if err = s.reserveBuildCapacity(ctx, p.Template, plan); err == nil {
		t.Fatal("overbooked build")
	}
	var reserved SiteCapacity
	if err = s.Store.Get(ctx, "capacity-build", p.Template, &reserved); err != nil || reserved.VCPU != 3 {
		t.Fatal("lost failed reservation", reserved, err)
	}
}

func TestSiteCapacityValidationAndMemoryRefusal(t *testing.T) {
	for _, capacity := range []SiteCapacity{{VCPU: 1}, {MemoryMiB: 1024}, {VCPU: -1, MemoryMiB: 1024}, {VCPU: 1, MemoryMiB: -1}} {
		if err := capacity.Validate(); err == nil {
			t.Fatal("invalid ceiling accepted", capacity)
		}
	}
	s, v, p, size := capacityFixture(t)
	s.Config.Capacity = SiteCapacity{VCPU: 100, MemoryMiB: size.MemoryMiB - 1}
	response := consoleRequest(s, v, "/api/learning/"+p.ID+"/start")
	if response.Code != 409 || !strings.Contains(response.Body.String(), "Site capacity exceeded") {
		t.Fatal(response.Code, response.Body.String())
	}
	if active, err := s.Store.activePractice(context.Background(), v.User.ID); err != nil || active != nil {
		t.Fatal("refused run was persisted", active, err)
	}
}

type capacityInventoryEngine struct {
	Engine
	instances []lc.Instance
	builds    []lc.Command
	images    []lc.ImageRecord
}

func (e capacityInventoryEngine) List(context.Context) ([]lc.Instance, error) {
	return e.instances, nil
}
func (e capacityInventoryEngine) Images(context.Context) ([]lc.ImageRecord, error) {
	return e.images, nil
}
func (e capacityInventoryEngine) Builds(context.Context) ([]lc.Command, error) { return e.builds, nil }
func TestSiteCapacityRetainedNativeInventoryAndDeduplication(t *testing.T) {
	s, v, p, size := capacityFixture(t)
	ctx := context.Background()
	plan, err := s.plan(ctx, p.Template)
	if err != nil {
		t.Fatal(err)
	}
	run := decodeLearning[PracticeRun](t, consoleRequest(s, v, "/api/learning/"+p.ID+"/start"), 202)
	record, err := s.Store.practice(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.Engine = capacityInventoryEngine{Engine: s.Engine, instances: []lc.Instance{{ID: record.InstanceID, Status: "preparing", Plan: plan}}}
	if err = s.Store.atomic(ctx, func(ctx context.Context) error { return s.checkCapacity(ctx, SiteCapacity{}) }); err != nil {
		t.Fatal("counted engine and hosted allocation twice", err)
	}
	s.Engine = capacityInventoryEngine{Engine: s.Engine, instances: []lc.Instance{{ID: "orphan", Status: "failed", Plan: plan}}}
	if err = s.Store.atomic(ctx, func(ctx context.Context) error { return s.checkCapacity(ctx, SiteCapacity{}) }); err == nil {
		t.Fatal("orphan allocation omitted")
	}
	s.Config.Capacity = SiteCapacity{VCPU: size.VCPU + 3, MemoryMiB: size.MemoryMiB + 4864}
	s.Engine = capacityInventoryEngine{Engine: s.Engine, builds: []lc.Command{{Source: plan.Source, Environment: plan.Environment}}}
	if err = s.Store.atomic(ctx, func(ctx context.Context) error { return s.checkCapacity(ctx, SiteCapacity{1, 1}) }); err == nil {
		t.Fatal("retained build omitted")
	}
	if err = s.reserveBuildCapacity(ctx, p.Template, plan); err != nil {
		t.Fatal("retained build and its new reservation double counted", err)
	}
	if err = s.Store.atomic(ctx, func(ctx context.Context) error { return s.checkCapacity(ctx, SiteCapacity{}) }); err != nil {
		t.Fatal("reserved build double counted", err)
	}
}

func TestSiteCapacityPublishedBuildProvenanceConsumesNoCapacity(t *testing.T) {
	s, _, p, size := capacityFixture(t)
	ctx := context.Background()
	plan, err := s.plan(ctx, p.Template)
	if err != nil {
		t.Fatal(err)
	}
	s.Engine = capacityInventoryEngine{Engine: s.Engine, builds: []lc.Command{{Source: plan.Source, Environment: plan.Environment, Image: lc.PlannedImage{CacheKey: "published"}}}, images: []lc.ImageRecord{{Key: "published"}}}
	if err = s.Store.atomic(ctx, func(ctx context.Context) error { return s.checkCapacity(ctx, size) }); err != nil {
		t.Fatal("stopped image provenance counted as live build", err)
	}
}
