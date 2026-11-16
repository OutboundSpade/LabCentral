package instructor

import (
	lc "github.com/OutboundSpade/LabCentral/v2"
	"regexp"
	"strings"
)

func (s LiveScoring) Validate(src lc.Source, hidden []string) error {
	m, ok := src.Lab.Machines[s.Machine]
	image := src.Lab.Images[m.Image]
	if !ok || !lc.Contains(m.Access, "ssh") || lc.OSFamily(image.OS) != "linux" || image.Appliance != nil || !lc.Contains(hidden, s.Machine) {
		return invalid("liveScoring.machine", "requires a hidden Linux scorer with SSH access")
	}
	if s.Port < 1024 || s.Port > 65535 {
		return invalid("liveScoring.port", "expected 1024 through 65535")
	}
	if len(s.Path) > 256 || strings.HasPrefix(s.Path, "//") || !regexp.MustCompile(`^/[a-zA-Z0-9_/-]+[a-zA-Z0-9_.-]*$`).MatchString(s.Path) {
		return invalid("liveScoring.path", "expected an absolute endpoint path without queries, fragments or URL authority")
	}
	if s.StaleAfterSeconds != 0 && (s.StaleAfterSeconds < 30 || s.StaleAfterSeconds > 900) {
		return invalid("liveScoring.staleAfterSeconds", "expected 30 through 900 seconds")
	}
	return nil
}

func (s LiveScoring) StaleAfter() int {
	if s.StaleAfterSeconds == 0 {
		return 180
	}
	return s.StaleAfterSeconds
}
