package instructor

import (
	lc "github.com/OutboundSpade/LabCentral/v2"
	"testing"
)

func TestLiveScoringConfigurationIsPrivateAndBounded(t *testing.T) {
	src := lc.Source{Lab: lc.Lab{Images: map[string]lc.Image{"linux": {Base: "ubuntu-24.04"}}, Machines: map[string]lc.Machine{"scorer": {Image: "linux", Access: []string{"ssh"}}}}}
	config := LiveScoring{Machine: "scorer", Port: 18080, Path: "/scoreboard.json"}
	if err := config.Validate(src, []string{"scorer"}); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"visible", "missing", "port", "host", "query", "fragment", "redirect", "stale", "windows", "appliance"} {
		t.Run(field, func(t *testing.T) {
			s := src
			c := config
			hidden := []string{"scorer"}
			switch field {
			case "visible":
				hidden = nil
			case "missing":
				c.Machine = "unknown"
			case "port":
				c.Port = 80
			case "host":
				c.Path = "https://example.org"
			case "query":
				c.Path = "/score?token=secret"
			case "fragment":
				c.Path = "/score#token"
			case "redirect":
				c.Path = "//169.254.169.254"
			case "stale":
				c.StaleAfterSeconds = 1
			case "windows":
				s.Lab.Images = map[string]lc.Image{"linux": {OS: "windows"}}
			case "appliance":
				s.Lab.Images = map[string]lc.Image{"linux": {Appliance: &lc.ApplianceImage{Kind: "vyos"}}}
			}
			if c.Validate(s, hidden) == nil {
				t.Fatal("invalid live scoring configuration accepted")
			}
		})
	}
	if _, err := Parse([]byte("version: 3\nvisibility: {audience: coaches, hiddenMachines: [scorer]}\nliveScoring: {machine: scorer, port: 18080, path: /scoreboard.json}\n")); err != nil {
		t.Fatal(err)
	}
}
