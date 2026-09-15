package application

import (
	"strings"
	"testing"
)

func TestWorkspaceDisplayNames(t *testing.T) {
	for _, name := range []string{"security-basics", "Security basics", "Réseaux avancés", "実習", strings.Repeat("é", 128)} {
		if !ValidWorkspaceName(name) {
			t.Errorf("valid display name rejected: %q", name)
		}
	}
	for _, name := range []string{"", " spaces", "spaces ", "two\nlines", "control\x00", "\xff", strings.Repeat("é", 129)} {
		if ValidWorkspaceName(name) {
			t.Errorf("invalid display name accepted: %q", name)
		}
	}
}
