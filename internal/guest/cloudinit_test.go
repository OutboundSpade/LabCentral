package guest

import (
	lc "github.com/OutboundSpade/LabCentral/v2"
	"strings"
	"testing"
)

func TestLocalhostOnlyAndSelectedHosts(t *testing.T) {
	m := lc.PlannedMachine{Name: "server", PublicKey: "public", HostPublicKey: "host", HostPrivateKey: "private"}
	text, e := CloudInit(m)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(text, "127.0.0.1 localhost") || !strings.Contains(text, "::1 localhost ip6-localhost") || strings.Contains(text, "10.77.") {
		t.Fatal(text)
	}
	m.Hosts = []lc.HostEntry{{Name: "peer", Address: "10.77.0.11"}}
	if !strings.Contains(Hosts(m), "10.77.0.11 peer\n") {
		t.Fatal(Hosts(m))
	}
}
