package labcentral

import (
	"encoding/json"
	"strings"
	"testing"
)

const windowsLab = `version: 3
name: win
images:
  base:
    os: windows
    base: windows-2022
    build: {script: build.ps1, files: [build.ps1]}
machines:
  server:
    image: base
    cpu: 2
    memory: 4Gi
    disk: 30Gi
    access: [ssh, desktop]
`

func windowsPlan(t *testing.T) Plan {
	t.Helper()
	s, err := sourceYAML(t, []byte(windowsLab))
	if err != nil {
		t.Fatal(err)
	}
	e := windowsConformanceEnvironment(s, conformanceEnvironment())
	p, err := PlanLab(s, e, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestWindowsPlanningAndBaseBinding(t *testing.T) {
	p := windowsPlan(t)
	if p.Machines[0].OS != "windows" || p.Machines[0].SSHUser() != "Administrator" || p.Images[0].Script != "build.ps1" || p.Images[0].Playbook != "" {
		t.Fatal("Windows execution identity lost")
	}
	for _, tc := range []struct {
		name, field string
		change      func(*Environment)
	}{
		{"os", "images.base.base", func(e *Environment) {
			b := e.Bases["windows-2022"]
			b.OS = ""
			b.User = "ubuntu"
			e.Bases["windows-2022"] = b
		}},
		{"user", "environment.bases.windows-2022.user", func(e *Environment) { b := e.Bases["windows-2022"]; b.User = "admin"; e.Bases["windows-2022"] = b }},
		{"disk", "environment.bases.windows-2022.diskGiB", func(e *Environment) { b := e.Bases["windows-2022"]; b.DiskGiB = 29; e.Bases["windows-2022"] = b }},
		{"ami", "environment.bases.windows-2022.id", func(e *Environment) { b := e.Bases["windows-2022"]; b.ID = "ssm:latest"; e.Bases["windows-2022"] = b }},
		{"build-memory", "images.base.platform.aws.buildInstanceType", func(e *Environment) {
			e.Build.InstanceType = "t3.micro"
			e.AWS.Types = append(e.AWS.Types, InstanceType{Name: "t3.micro", CPU: 2, MemoryMiB: 1024})
		}},
		{"gateway", "environment.aws.gateway.base", func(e *Environment) { e.AWS.Gateway.Base = "windows-2022" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var e Environment
			b, _ := json.Marshal(p.Environment)
			_ = json.Unmarshal(b, &e)
			tc.change(&e)
			_, err := PlanLab(p.Source, e, nil)
			code := Invalid
			if tc.name == "build-memory" {
				code = Unsupported
			}
			requireField(t, err, code, tc.field)
		})
	}
}
func TestWindowsKeysBindScriptsAndFiles(t *testing.T) {
	p := windowsPlan(t)
	key := p.Images[0].CacheKey
	src := p.Source
	src.Files = append([]File(nil), src.Files...)
	src.Files = append(src.Files, File{Path: "README.md", Content: []byte("unrelated")})
	src.Digest = digest(src.Files)
	next, err := PlanLab(src, p.Environment, nil)
	if err != nil || next.Images[0].CacheKey != key {
		t.Fatal("unselected file changed image identity", err)
	}
	for i := range src.Files {
		if src.Files[i].Path == "build.ps1" {
			src.Files[i].Content = []byte("exit 1")
		}
	}
	src.Digest = digest(src.Files)
	next, err = PlanLab(src, p.Environment, nil)
	if err != nil || next.Images[0].CacheKey == key {
		t.Fatal("script bytes did not change image identity", err)
	}
	// Linux's desktop pin must not become a Windows dependency.
	p.Environment.Build.DCVURL = "https://unused.invalid/linux.tgz"
	p.Environment.Build.DCVSHA256 = strings.Repeat("a", 64)
	next, err = PlanLab(p.Source, p.Environment, nil)
	if err != nil || next.Images[0].CacheKey != key {
		t.Fatal("Linux desktop pin affected Windows key", err)
	}
}
func TestStandaloneWindowsRequestsCannotBypassCapabilities(t *testing.T) {
	p := windowsPlan(t)
	c := Command{Environment: p.Environment, Machine: p.Machines[0], Image: p.Images[0]}
	c.Machine.MemoryMiB = 1024
	requireField(t, CheckWindowsCommand(c), Unsupported, "machines.server.memory")
	c.Machine = p.Machines[0]
	c.Environment.Platform = "proxmox"
	requireField(t, CheckWindowsCommand(c), Unsupported, "machines.server.os")
}

func TestWindowsFileSelectionsPreserveFilesystemIdentity(t *testing.T) {
	for _, paths := range [][]string{{"check.ps1", "CHECK.ps1"}, {"Folder", "folder/check.ps1"}, {"AUX.txt"}, {"trailing."}, {"files/COM1.ps1"}} {
		if err := WindowsFilePaths(paths); err == nil {
			t.Fatal("ambiguous Windows files accepted", paths)
		}
	}
	if err := WindowsFilePaths([]string{"check.ps1", "files/settings.json"}); err != nil {
		t.Fatal(err)
	}
}
