package hosted

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/instructor"
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	"github.com/OutboundSpade/LabCentral/v2/internal/sshtest"
	"golang.org/x/crypto/ssh"
)

func TestWindowsChecksUsePinnedPowerShellAndTypedPayload(t *testing.T) {
	for _, status := range []uint32{0, 42, 43} {
		t.Run(string(rune('a'+status)), func(t *testing.T) {
			s, _, _ := learningFixture(t)
			src, b, err := instructor.LoadTemplate("../examples/windows-iis", true)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := sshtest.NewWithResult(t, nil, func(command, user string, input []byte) ([]byte, uint32) {
				calls.Add(1)
				if user != "Administrator" || !strings.HasPrefix(command, "powershell.exe ") || strings.Contains(command, "check.ps1") || strings.Contains(command, "sudo") {
					t.Error("wrong Windows launcher/identity", user)
				}
				decoded, err := base64.StdEncoding.DecodeString(string(input))
				if err != nil {
					t.Error(err)
				}
				var payload struct {
					Script         string
					Files          []lc.File
					TimeoutSeconds int
				}
				if err = json.Unmarshal(decoded, &payload); err != nil || payload.Script != "check.ps1" || payload.TimeoutSeconds != 90 {
					t.Error("payload lost Windows program", err)
				}
				files := map[string]string{}
				for _, f := range payload.Files {
					files[f.Path] = string(f.Content)
				}
				if !strings.Contains(files["check.ps1"], "Invoke-WebRequest") || !strings.Contains(files["labcentral/hosts.json"], "client") || files["labcentral/context.json"] == "" {
					t.Error("guest data was lost")
				}
				return nil, status
			})
			key, _ := sshtest.Key(t)
			m := lc.PlannedMachine{Name: "server", OS: "windows", HostPublicKey: server.HostKey, Spec: src.Lab.Machines["server"]}
			inst := lc.Instance{ID: "instance", Status: "ready", Plan: lc.Plan{Source: src, Machines: []lc.PlannedMachine{m, {Name: "client", Address: "10.77.0.11", HostPublicKey: server.HostKey}}}}
			s.Engine = &reuseGuestEngine{Engine: s.Engine, instance: inst, machine: m, resource: lc.Resource{Address: server.Address}, key: key}
			ctx, closeJob := guestJobContext(context.Background())
			defer closeJob()
			cache, _ := guestCacheFor(ctx)
			cache.dial = func(ctx context.Context, address, user string, key []byte, host string) (*ssh.Client, error) {
				return remote.Dial(ctx, address, user, key, host)
			}
			step := b.Checks[0].Steps[0]
			step.GuestHosts = []string{"client"}
			err = s.runGuestPlaybooks(ctx, Team{InstanceID: inst.ID}, src, *b, []GuestPlaybook{step})
			if status == 0 && err != nil {
				t.Fatal(err)
			}
			if status == 42 && !errors.Is(err, errPlaybookFailed) {
				t.Fatal("nonzero script exit not a check failure", err)
			}
			if status == 43 && !errors.Is(err, errGuestUnknown) {
				t.Fatal("timeout/lock failure was not unknown", err)
			}
			if calls.Load() != 1 {
				t.Fatal("step replayed", calls.Load())
			}
		})
	}
}
func TestPublicTemplateMachineOSPersistsWithoutPrivateScripts(t *testing.T) {
	s, _, _ := stageFixture(t)
	ctx := context.Background()
	var src lc.Source
	if err := s.Store.Get(ctx, "source", "pinned", &src); err != nil {
		t.Fatal(err)
	}
	for name, im := range src.Lab.Images {
		im.OS = "windows"
		src.Lab.Images[name] = im
	}
	if err := s.Store.Put(ctx, "source", "pinned", src); err != nil {
		t.Fatal(err)
	}
	templates, err := s.publicTemplates(ctx, "")
	catalog := TemplateCatalog{Templates: templates}
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Templates) == 0 || len(catalog.Templates[0].Machines) == 0 || catalog.Templates[0].Machines[0].OS != "windows" {
		t.Fatal("catalog lost OS", catalog)
	}
	raw, _ := json.Marshal(catalog)
	if strings.Contains(string(raw), "PRIVATE-") || strings.Contains(string(raw), "playbook") || strings.Contains(string(raw), `"script":`) {
		t.Fatal("private data reached catalog")
	}
	// Reads still go through the existing authenticated route.
	if w := request(s, "GET", "/api/templates", "", "", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestWindowsCapabilityProblemsBeforeBuild(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*Service)
	}{
		{"base", "environment.bases.windows-2022", func(s *Service) { delete(s.env.Bases, "windows-2022") }},
		{"os", "image OS must match", func(s *Service) {
			b := s.env.Bases["windows-2022"]
			b.OS = ""
			b.User = "ubuntu"
			s.env.Bases["windows-2022"] = b
		}},
		{"installer", "windowsDCVURL", func(s *Service) { s.env.Build.WindowsDCVURL = "" }},
		{"checksum", "windowsDCVSHA256", func(s *Service) { s.env.Build.WindowsDCVSHA256 = "" }},
		{"build capacity", "build instance", func(s *Service) { s.env.Build.InstanceType = "t3.small" }},
		{"runtime capacity", "instance type", func(s *Service) {
			s.env.AWS.Types = []lc.InstanceType{{Name: "t3.small", CPU: 2, MemoryMiB: 2048}, {Name: "build-only", CPU: 1, MemoryMiB: 4096}}
			s.env.Build.InstanceType = "build-only"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, v, _, _ := windowsFixture(t)
			tc.change(s)
			ctx := context.Background()
			if _, err := s.queueTemplateOperation(ctx, "build", "pinned"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal("build admission", err)
			}
			templates, err := s.templates(ctx)
			if err != nil || len(templates) != 1 || !strings.Contains(templates[0].Problem, tc.want) {
				t.Fatal("catalog suppressed by incompatible revision", templates, err)
			}
			v.User.Admin = true
			if err = s.Store.Put(ctx, "session", v.ID, v); err != nil {
				t.Fatal(err)
			}
			admin := decodeLearning[AdminView](t, request(s, "GET", "/api/admin", "", "", ""), 200)
			found := false
			for _, failure := range admin.ImportFailures {
				found = found || strings.Contains(failure.Error, tc.want)
			}
			if !found {
				t.Fatal("problem absent from existing admin error panel", admin.ImportFailures)
			}
			var jobs int
			if err = s.Store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE kind='build'`).Scan(&jobs); err != nil || jobs != 0 {
				t.Fatal("invalid build queued", jobs, err)
			}
		})
	}
}
func TestWindowsScriptImagesBuildAndProtectPinnedKeys(t *testing.T) {
	s, _, _, _ := windowsFixture(t)
	ctx := context.Background()
	e := &imageTestEngine{}
	s.Engine = e
	j, err := s.queueTemplateOperation(ctx, "build", "pinned")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.runTemplateJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	finishImageJob(t, s, j, nil)
	keys, err := s.templateKeys(ctx, "pinned")
	if err != nil || len(keys) != 2 || len(e.images) != 2 {
		t.Fatal("PowerShell image omitted", keys, e.images, err)
	}
	p, err := s.plan(ctx, "pinned")
	if err != nil {
		t.Fatal(err)
	}
	oldWindows := ""
	oldLinux := ""
	for _, im := range p.Images {
		if im.Script != "" {
			oldWindows = im.CacheKey
		} else {
			oldLinux = im.CacheKey
		}
	}
	s.env.Build.WindowsDCVSHA256 = strings.Repeat("c", 64)
	changed, err := s.plan(ctx, "pinned")
	if err != nil {
		t.Fatal(err)
	}
	for _, im := range changed.Images {
		if im.Script != "" && im.CacheKey == oldWindows {
			t.Fatal("Windows pin absent from key")
		}
		if im.Playbook != "" && im.CacheKey != oldLinux {
			t.Fatal("Windows pin changed Linux identity")
		}
	}
	protected, err := s.neededImageKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !protected[oldWindows] || !protected[oldLinux] {
		t.Fatal("saved Windows keys not protected", protected)
	}
	// End the meeting and explicitly remove this revision: both script and playbook images.
	if err = s.Store.Delete(ctx, "meeting", "current"); err != nil {
		t.Fatal(err)
	}
	j, err = s.queueTemplateOperation(ctx, "remove-images", "pinned")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.runTemplateJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	if len(e.removed) != 2 {
		t.Fatal("Windows image not removed", e.removed)
	}
}
func TestCoachOnlyWindowsAttemptsPersistAndRevoke(t *testing.T) {
	for _, scenario := range []string{"", "iis-outage"} {
		t.Run("scenario="+scenario, func(t *testing.T) {
			s, v, _, _ := windowsFixture(t)
			ctx := context.Background()
			var b InstructorBundle
			if err := s.Store.Get(ctx, "template-instructor", "pinned", &b); err != nil {
				t.Fatal(err)
			}
			b.Visibility.Audience = "coaches"
			if err := s.Store.Put(ctx, "template-instructor", "pinned", b); err != nil {
				t.Fatal(err)
			}
			body := `{"requestID":"aaaaaaaaaaaaaaaaaaaaaaaa","scenarioID":"` + scenario + `"}`
			if w := request(s, "POST", "/api/templates/pinned/attempt", body, s.Config.Origin, v.CSRF); w.Code != 404 {
				t.Fatal("student created coach attempt", w.Code)
			}
			v.User.Admin = true
			if err := s.Store.Put(ctx, "session", v.ID, v); err != nil {
				t.Fatal(err)
			}
			if err := s.Store.Put(ctx, "user", v.User.ID, v.User); err != nil {
				t.Fatal(err)
			}
			lab := decodeLearning[LearningLab](t, request(s, "POST", "/api/templates/pinned/attempt", body, s.Config.Origin, v.CSRF), 201)
			view := decodeLearning[LearningView](t, request(s, "GET", "/api/learning/"+lab.ID, "", "", ""), 200)
			if len(view.Machines) != 2 || !view.CloudReady {
				t.Fatal("coach Windows view", view)
			}
			windows := false
			for _, m := range view.Machines {
				windows = windows || m.OS == "windows"
			}
			if !windows {
				t.Fatal("attempt OS lost")
			}
			catalog := decodeLearning[LearningCatalog](t, request(s, "GET", "/api/learning", "", "", ""), 200)
			if len(catalog.Labs) != 1 || catalog.Labs[0].ID != lab.ID {
				t.Fatal("own coach attempt absent", catalog)
			}
			e := practiceEngineFor(t, s)
			run := decodeLearning[PracticeRun](t, request(s, "POST", "/api/learning/"+lab.ID+"/start", `{}`, s.Config.Origin, v.CSRF), 202)
			if scenario == "" {
				if err := s.runPractice(ctx, Job{Kind: "practice-prepare", Target: run.ID}); err != nil {
					t.Fatal(err)
				}
				if e.deploys != 1 {
					t.Fatal("not prepared")
				}
			}
			reopened, err := OpenStore(s.Store.Root)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			recovered := &Service{Store: reopened, env: s.env, Engine: e}
			if _, err = recovered.ownedPublication(ctx, lab.ID, v.User.ID); err != nil {
				t.Fatal("coach ownership lost after restart", err)
			}
			if _, err = recovered.ownedPublication(ctx, lab.ID, "outsider"); err == nil {
				t.Fatal("another coach can read private attempt")
			}
			v.User.Admin = false
			if err = s.Store.Put(ctx, "session", v.ID, v); err != nil {
				t.Fatal(err)
			}
			if err = s.Store.Put(ctx, "user", v.User.ID, v.User); err != nil {
				t.Fatal(err)
			}
			if w := request(s, "GET", "/api/learning/"+lab.ID, "", "", ""); w.Code != 404 {
				t.Fatal("revoked coach attempt exposed", w.Code)
			}
			if err = s.practiceDisclosure(ctx, lab.ID); err == nil {
				t.Fatal("revoked coach retains desktop disclosure")
			}
			if scenario != "" {
				if err = s.runPractice(ctx, Job{Kind: "practice-prepare", Target: run.ID}); err == nil || e.deploys != 0 {
					t.Fatal("revoked coach provisioned", err)
				}
			}
			// Cleanup never requires retained privileges.
			decodeLearning[PracticeRun](t, request(s, "POST", "/api/learning/"+lab.ID+"/stop", `{}`, s.Config.Origin, v.CSRF), 202)
			if err = s.runPractice(ctx, Job{Kind: "practice-end", Target: run.ID}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHostedEnvironmentAllowsLinuxWithoutWindowsPins(t *testing.T) {
	s, _, _ := learningFixture(t)
	if err := ValidateHostedEnvironment(s.env); err != nil {
		t.Fatal("Linux-only environment rejected", err)
	}
	s.env.Build.WindowsDCVURL = "https://example.org/pinned.msi"
	if err := ValidateHostedEnvironment(s.env); err == nil || !strings.Contains(err.Error(), "windowsDCVSHA256") {
		t.Fatal("partial Windows pins accepted", err)
	}
	s.env.Build.WindowsDCVSHA256 = strings.Repeat("a", 64)
	if err := ValidateHostedEnvironment(s.env); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsImportPinsCoachRevisionOrReportsCapabilityProblem(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-base=%v", missing), func(t *testing.T) {
			s, _, _, _ := windowsFixture(t)
			ctx := context.Background()
			root := t.TempDir()
			directory := filepath.Join(root, "windows-iis")
			if err := os.CopyFS(directory, os.DirFS("../examples/windows-iis")); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "instructor.yaml")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, []byte(strings.Replace(string(raw), "audience: students", "audience: coaches", 1)), 0600); err != nil {
				t.Fatal(err)
			}
			if missing {
				delete(s.env.Bases, "windows-2022")
			}
			commit := strings.Repeat("d", 40)
			if err = s.importSnapshot(ctx, root, commit, time.Now(), "Windows example"); err != nil {
				t.Fatal(err)
			}
			var report TemplateImportReport
			if err = s.Store.Get(ctx, "template-import-report", "current", &report); err != nil {
				t.Fatal(err)
			}
			id := digest(commit + ":windows-iis")[:24]
			if missing {
				if len(report.Failures) != 1 || !strings.Contains(report.Failures[0].Error, "environment.bases.windows-2022") {
					t.Fatal(report)
				}
				if err = s.Store.Get(ctx, "template", id, new(Template)); !errors.Is(err, sql.ErrNoRows) {
					t.Fatal("incompatible revision imported", err)
				}
				return
			}
			if len(report.Failures) != 0 {
				t.Fatal(report)
			}

			catalog, err := s.templateProjections(ctx, id, false)
			if err != nil || len(catalog) != 0 {
				t.Fatal("coach import reached student catalog", catalog, err)
			}
			catalog, err = s.templateProjections(ctx, id, true)
			if err != nil || len(catalog) != 1 || !catalog[0].CoachOnly || len(catalog[0].Machines) != 2 {
				t.Fatal(catalog, err)
			}
			keys, err := s.templateKeys(ctx, id)
			if err != nil || len(keys) != 2 {
				t.Fatal("script import lost image key", keys, err)
			}
			var pinned lc.Source
			if err = s.Store.Get(ctx, "source", id, &pinned); err != nil {
				t.Fatal(err)
			}
			for _, f := range pinned.Files {
				if strings.HasPrefix(f.Path, "instructor") {
					t.Fatal("private program entered portable build", f.Path)
				}
			}
		})
	}
}
