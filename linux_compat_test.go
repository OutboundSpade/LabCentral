package labcentral

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// These exact plans were captured on the parent Linux implementation before adding Windows.
func TestLinuxPlansGolden(t *testing.T) {
	for _, desktop := range []bool{false, true} {
		name := "server"
		s := testSource(t)
		e := testEnvironment()
		if desktop {
			name = "desktop"
			raw := strings.Replace(sample, "base: ubuntu-24.04", "base: ubuntu-24.04\n    build: {playbook: build.yml, files: [build.yml]}", 1)
			raw = strings.Replace(raw, "[ssh]", "[ssh, desktop]", 1)
			var err error
			s, err = sourceYAML(t, []byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			e = conformanceEnvironment()
		}
		p, err := PlanLab(s, e, nil)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.MarshalIndent(p, "", "  ")
		data = append(data, '\n')
		path := "testdata/linux-" + name + "-plan.json"
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != string(want) {
			t.Fatal("Linux plan or image identity changed: " + name)
		}
	}
}
