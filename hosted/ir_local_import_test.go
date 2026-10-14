package hosted

import (
	lc "github.com/OutboundSpade/LabCentral/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An explicitly supplied local template checkout is data-only validation; no
// author program runs on this host and no native acceptance is implied.
func TestLocalIncidentTemplatePrivacy(t *testing.T) {
	source := os.Getenv("LABCENTRAL_IR_TEMPLATE_CHECKOUT")
	if source == "" {
		t.Skip("set LABCENTRAL_IR_TEMPLATE_CHECKOUT for local template import")
	}
	root := filepath.Join(t.TempDir(), "lab")
	if err := os.CopyFS(root, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	bundle, err := extractInstructor(root)
	if err != nil {
		t.Fatal(err)
	}
	src, err := lc.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if bundle == nil || bundle.Validate(src) != nil || bundle.Visibility.Audience != "coaches" || !lc.Contains(bundle.Visibility.HiddenMachines, "support") {
		t.Fatal("incident metadata is not private", bundle)
	}
	for name, machine := range src.Lab.Machines {
		if name == "support" {
			continue
		}
		image := src.Lab.Images[machine.Image]
		for _, path := range image.BuildFiles() {
			if path == "lab.yaml" || path == "support.yml" || strings.HasPrefix(path, "instructor") {
				t.Fatal("private build material selected for student image", name, path)
			}
		}
	}
	var machines []lc.PlannedMachine
	for name, spec := range src.Lab.Machines {
		machines = append(machines, lc.PlannedMachine{Name: name, Spec: spec, Address: "10.77.0.10", HostPublicKey: "fixture-public-key"})
	}
	inst := lc.Instance{Plan: lc.Plan{Source: src, Machines: machines}}
	steps := append([]GuestPlaybook{}, bundle.Reset...)
	for _, x := range bundle.Breaks {
		steps = append(steps, x.Steps...)
	}
	for _, check := range bundle.Checks {
		steps = append(steps, check.Steps...)
	}
	for _, step := range steps {
		files, err := bundle.GuestFiles(src, inst, step, nil)
		if err != nil {
			t.Fatal(step, err)
		}
		if step.Machine == "support" {
			continue
		}
		for _, file := range files {
			if strings.HasPrefix(file.Path, "ir_") || file.Path == "test_ir_evidence.py" || file.Path == "guest-hosts.json" {
				t.Fatal("private file entered student guest", step, file.Path)
			}
		}
	}
	for _, file := range src.Files {
		if strings.Contains(file.Path, "instructor") {
			t.Fatal("private instructor source entered image source", file.Path)
		}
	}
}
