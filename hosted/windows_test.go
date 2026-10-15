package hosted

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

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
