package instructor

import (
	"encoding/json"
	"errors"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadTemplatePreservesAuthorsDirectoryAndPrivateDigest(t *testing.T) {
	root := filepath.Join(t.TempDir(), "lab")
	if e := os.CopyFS(root, os.DirFS("../templates/ccdc-service-recovery")); e != nil {
		t.Fatal(e)
	}
	src, b, e := LoadTemplate(root)
	if e != nil || b == nil {
		t.Fatal(e)
	}
	for _, f := range src.Files {
		if f.Path == "instructor.yaml" || strings.HasPrefix(f.Path, "instructor/") {
			t.Fatal("private material in source", f.Path)
		}
	}
	if e = os.WriteFile(filepath.Join(root, "instructor.yaml"), []byte("version: 3\nvisibility: {audience: students}\nnotes: changed\n"), 0600); e != nil {
		t.Fatal(e)
	}
	next, _, e := LoadTemplate(root)
	if e != nil || next.Digest != src.Digest {
		t.Fatal("private notes changed portable digest", e)
	}
	if _, e = os.Stat(filepath.Join(root, "instructor", "reset.yml")); e != nil {
		t.Fatal("author files removed", e)
	}
	if e = os.Remove(filepath.Join(root, "README.md")); e != nil {
		t.Fatal(e)
	}
	if _, _, e = LoadTemplate(root); e != nil {
		t.Fatal(e)
	}
	if _, _, e = LoadTemplate(root, true); e == nil {
		t.Fatal("template without README accepted")
	}
}
func TestAnswerAlternativesAndCaseFolding(t *testing.T) {
	for _, test := range []struct {
		answer Answer
		value  string
		want   bool
	}{{Answer{[]string{"cron", "crond"}, true}, " crond\n", true}, {Answer{[]string{"cron", "crond"}, true}, "CRON", false}, {Answer{[]string{"cron", "crond"}, false}, " CROND ", true}, {Answer{[]string{"École"}, false}, "éCOLE", true},
		{Answer{[]string{"Σ"}, false}, "ς", true}, {Answer{[]string{"cron"}, false}, "cr", false}} {
		if got := test.answer.Matches(test.value); got != test.want {
			t.Fatal(test, got)
		}
	}
}
func TestStepDeadlineAndPayloadContext(t *testing.T) {
	src, b := fixture(t)
	s := b.Checks[0].Steps[0]
	s.TimeoutSeconds = 10
	extensions := lc.Extensions{"org.example.owner": json.RawMessage(`{"id":1}`)}
	b.Extensions = lc.Extensions{"org.example.template": json.RawMessage(`true`)}
	src.Lab.Extensions = lc.Extensions{"org.example.lab": json.RawMessage(`"v"`)}
	s.GuestHosts = []string{"workstation"}
	s.Assets = []string{"images/services.yml"}
	inst := lc.Instance{Plan: lc.Plan{Source: src, Machines: []lc.PlannedMachine{{Name: "services"}, {Name: "workstation", Address: "10.0.0.4", HostPublicKey: "pinned"}}}}
	files, e := b.GuestFiles(src, inst, s, extensions)
	if e != nil {
		t.Fatal(e)
	}
	paths := []string{}
	for _, f := range files {
		paths = append(paths, f.Path)
		if f.Path == "labcentral/context.json" {
			var context GuestContext
			if e = json.Unmarshal(f.Content, &context); e != nil || context.Machine != "services" || string(context.OwnerExtensions["org.example.owner"]) != `{"id":1}` || len(context.LabExtensions) != 1 || len(context.TemplateExtensions) != 1 {
				t.Fatal(context, e)
			}
		}
	}
	if strings.Join(paths, ",") != "check-web.yml,template/images/services.yml,labcentral/hosts.json,labcentral/context.json" {
		t.Fatal(paths)
	}
	if Deadline([]Step{s, {}}) != 130*time.Second {
		t.Fatal("deadline ignores steps")
	}
}
func TestDisclosureAndPayloadCollisions(t *testing.T) {
	for name, change := range map[string]func(lc.Source, *Bundle){
		"guest hosts":        func(src lc.Source, b *Bundle) { b.Checks[0].Steps[0].GuestHosts = []string{"actor"} },
		"lab asset":          func(src lc.Source, b *Bundle) { b.Checks[0].Steps[0].Assets = []string{"lab.yaml"} },
		"private image":      func(src lc.Source, b *Bundle) { b.Checks[0].Steps[0].Assets = []string{"images/private.yml"} },
		"duplicate playbook": func(src lc.Source, b *Bundle) { b.Checks[0].Steps[0].Files = []string{"check-web.yml"} },
		"reserved file":      func(src lc.Source, b *Bundle) { b.Files = append(b.Files, lc.File{Path: "labcentral/hosts.json"}) },
		"directory collision": func(src lc.Source, b *Bundle) {
			b.Files = append(b.Files, lc.File{Path: "check-web.yml/vars"})
			b.Checks[0].Steps[0].Files = []string{"check-web.yml/vars"}
		},
		"empty reset validated": func(src lc.Source, b *Bundle) {
			b.Breaks = nil
			b.Reset = []Step{{Machine: "missing", Playbook: "reset.yml"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			src, b := fixture(t)
			b.Visibility = Visibility{Audience: "coaches", HiddenMachines: []string{"actor"}}
			b.Scenarios = nil
			hidden := src.Lab.Machines["services"]
			hidden.Image = "private"
			src.Lab.Machines["actor"] = hidden
			image := src.Lab.Images["services"]
			image.Build = &lc.ImageBuild{Playbook: "images/private.yml"}
			src.Lab.Images["private"] = image
			for n, m := range src.Lab.Machines {
				m.HostnamePeers = lc.HostnamePeers{Explicit: true, Names: []string{"services", "workstation"}}
				src.Lab.Machines[n] = m
			}
			for n, im := range src.Lab.Images {
				im.Build.Files = []string{im.Build.Playbook}
				src.Lab.Images[n] = im
			}
			src.Files = append(src.Files, lc.File{Path: "images/private.yml", Content: []byte("- hosts: localhost\n")})
			change(src, b)
			e := b.Validate(src)
			var problem *lc.Error
			if !errors.As(e, &problem) {
				t.Fatal("unsafe payload accepted", e)
			}
		})
	}
}

func TestWindowsPayloadCaseInsensitiveDestinations(t *testing.T) {
	src, b, err := LoadTemplate("../examples/windows-iis", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"CHECK.ps1", "LabCentral/hosts.json", "AUX.txt"} {
		copy := *b
		copy.Files = append(append([]lc.File{}, b.Files...), lc.File{Path: p, Content: []byte("data")})
		copy.Checks = append([]Check{}, b.Checks...)
		copy.Checks[0].Steps = append([]Step{}, b.Checks[0].Steps...)
		copy.Checks[0].Steps[0].Files = []string{p}
		if err = copy.Validate(src); err == nil {
			t.Fatal("Windows payload collision accepted", p)
		}
	}
}

func TestCoachOnlyScenariosRetainValidation(t *testing.T) {
	src, b, err := LoadTemplate("../examples/windows-iis", true)
	if err != nil {
		t.Fatal(err)
	}
	b.Visibility.Audience = "coaches"
	if err = b.Validate(src); err != nil {
		t.Fatal("coach-only scenarios rejected", err)
	}
	b.Scenarios[0].Variants[0].Compatible = false
	if err = b.Validate(src); err == nil {
		t.Fatal("uncertified coach variant accepted")
	}
	b.Scenarios[0].Variants[0].Compatible = true
	b.Reset = nil
	if err = b.Validate(src); err == nil {
		t.Fatal("coach scenarios without reset accepted")
	}
}
