package access

import (
	"os/exec"
	"strings"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
)

// Runs the generated account scripts in bash against stubbed account tools.
func runAccountScript(t *testing.T, gecos, script string) (string, error) {
	t.Helper()
	stubs := `id(){ test -n "$GECOS"; }; getent(){ printf 'u:x:1:1:%s:/home/u:/bin/bash\n' "$GECOS"; }; sudo(){ echo "sudo $*"; }; GECOS=` + "'" + gecos + "'; "
	out, err := exec.Command("bash", "-c", stubs+script).CombinedOutput()
	return string(out), err
}

func TestDesktopAccountComment(t *testing.T) {
	c := lc.Command{Owner: "owner", Operation: "op"}
	marker, comment := desktopOwner(c)
	if !strings.HasPrefix(comment, "Student,,,,") || !strings.HasSuffix(comment, marker) {
		t.Fatal(comment)
	}
	script := desktopAccountScript("labcentral-x", "pw", c)
	for _, tc := range []struct {
		name, gecos, want string
		ok                bool
	}{
		{"new account shows a friendly name", "", "useradd --create-home --shell /bin/bash --comment " + comment, true},
		{"retained account in the old format is converted", marker, "usermod --comment " + comment, true},
		{"current account is reused", comment, "chpasswd", true},
		{"foreign account is refused", "Someone Else", "", false},
	} {
		out, err := runAccountScript(t, tc.gecos, script)
		if (err == nil) != tc.ok || !strings.Contains(out, tc.want) {
			t.Fatalf("%s: err=%v out=%q", tc.name, err, out)
		}
	}
	for _, gecos := range []string{marker, comment} {
		if _, err := runAccountScript(t, gecos, desktopOwned("labcentral-x", c)); err != nil {
			t.Fatal("cleanup refused an owned account", gecos)
		}
	}
	if _, err := runAccountScript(t, "Someone Else", desktopOwned("labcentral-x", c)); err == nil {
		t.Fatal("cleanup accepted a foreign account")
	}
}

func TestDesktopPasswordIsExactChpasswdInput(t *testing.T) {
	script := desktopAccountScript("labcentral-test", "Abc123", lc.Command{Owner: "owner", Operation: "op"})
	stubs := `id(){ return 1; }; sudo(){ if test "$1" = chpasswd; then cat; fi; }; `
	out, err := exec.Command("bash", "-c", stubs+script).CombinedOutput()
	if err != nil || string(out) != "labcentral-test:Abc123\n" {
		t.Fatalf("chpasswd input=%q err=%v", out, err)
	}
}
