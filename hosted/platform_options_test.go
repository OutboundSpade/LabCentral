package hosted

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"go.yaml.in/yaml/v3"
)

func TestTemplateMachineProjectionWithPlatformOptions(t *testing.T) {
	s, _, _ := stageFixture(t)
	ctx := context.Background()
	source := mustSource(t, s)
	for name, machine := range source.Lab.Machines {
		disabled := false
		machine.Platform = lc.MachinePlatform{AWS: &lc.AWSMachineOptions{InstanceType: "t3.large", CPUCredits: "standard"}, Proxmox: &lc.ProxmoxMachineOptions{CPUType: "host", NICModel: "e1000e", MemoryBalloon: &disabled}}
		source.Lab.Machines[name] = machine
	}
	raw, err := yaml.Marshal(source.Lab)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, file := range source.Files {
		content := file.Content
		if file.Path == "lab.yaml" {
			content = raw
		}
		path := filepath.Join(root, file.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	source, err = lc.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put(ctx, "source", "pinned", source); err != nil {
		t.Fatal(err)
	}
	s.env.Proxmox.CPUTypes = []string{"PRIVATE-OPERATOR-CPU-MODEL"}
	s.env.AWS.Types = append(s.env.AWS.Types, lc.InstanceType{Name: "PRIVATE-OPERATOR-INSTANCE-TYPE", CPU: 2, MemoryMiB: 8192})
	// Reopen the durable snapshot before exercising student projections.
	stateRoot := s.Store.Root
	if err := s.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s.Store, err = OpenStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	var retained lc.Source
	if err := s.Store.Get(ctx, "source", "pinned", &retained); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(source.Lab.Machines, retained.Lab.Machines) {
		t.Fatal("options lost during persistence")
	}
	for _, route := range []string{"/api/templates", "/api/templates/pinned"} {
		response := request(s, "GET", route, "", "", "")
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"name":"workstation"`) || !strings.Contains(response.Body.String(), `"memory":`) {
			t.Fatal("machine explorer failed", response.Code, response.Body.String())
		}
		for _, forbidden := range []string{"PRIVATE-OPERATOR", "cpuTypes", "instanceType", "cpuCredits", "memoryBalloon", "cpuType", "nicModel"} {
			if strings.Contains(response.Body.String(), forbidden) {
				t.Fatal("platform environment disclosed to student", forbidden)
			}
		}
	}
	response := request(s, "GET", "/api/admin", "", "", "")
	if response.Code != 403 {
		t.Fatal("options changed authorization", response.Code)
	}
}
