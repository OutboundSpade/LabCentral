package novnc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	accessendpoint "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/accessendpoint"
)

func TestTailscaleProfileRejectsUnpinnedPeer(t *testing.T) {
	profile := TailscaleProfile{SchemaVersion: "labcentral.tailscale-vnc/v1", TargetID: "target.test", Hostname: "pve1.test.ts.net", Address: "100.100.100.1", NodeKey: "nodekey:" + strings.Repeat("a", 64)}
	endpoint := accessendpoint.ProxmoxVNC{TargetID: profile.TargetID, Host: profile.Hostname, ServerName: profile.Hostname, Port: 5900}
	var peer tailscalePeer
	peer.Node.Name = profile.Hostname + "."
	peer.Node.Key = profile.NodeKey
	peer.Node.Addresses = []string{profile.Address + "/32"}
	peer.Node.Online = true
	if profile.validate() != nil || !profile.matches(endpoint, peer) {
		t.Fatal("exact peer rejected")
	}
	for name, mutate := range map[string]func(*accessendpoint.ProxmoxVNC, *tailscalePeer){
		"target":      func(e *accessendpoint.ProxmoxVNC, p *tailscalePeer) { e.TargetID = "target.other" },
		"host":        func(e *accessendpoint.ProxmoxVNC, p *tailscalePeer) { e.Host = "other.test.ts.net" },
		"server name": func(e *accessendpoint.ProxmoxVNC, p *tailscalePeer) { e.ServerName = "other.test.ts.net" },
		"port":        func(e *accessendpoint.ProxmoxVNC, p *tailscalePeer) { e.Port = 8006 },
		"name":        func(e *accessendpoint.ProxmoxVNC, p *tailscalePeer) { p.Node.Name = "other.test.ts.net." },
		"key": func(e *accessendpoint.ProxmoxVNC, p *tailscalePeer) {
			p.Node.Key = "nodekey:" + strings.Repeat("b", 64)
		},
		"offline": func(e *accessendpoint.ProxmoxVNC, p *tailscalePeer) { p.Node.Online = false },
		"address": func(e *accessendpoint.ProxmoxVNC, p *tailscalePeer) { p.Node.Addresses = []string{"100.100.100.2/32"} },
	} {
		t.Run(name, func(t *testing.T) {
			e, p := endpoint, peer
			mutate(&e, &p)
			if profile.matches(e, p) {
				t.Fatal("foreign peer accepted")
			}
		})
	}
	for name, mutate := range map[string]func(*TailscaleProfile){
		"schema":         func(p *TailscaleProfile) { p.SchemaVersion = "v2" },
		"target":         func(p *TailscaleProfile) { p.TargetID = "" },
		"key":            func(p *TailscaleProfile) { p.NodeKey = "" },
		"public address": func(p *TailscaleProfile) { p.Address = "192.0.2.1" },
		"lan address":    func(p *TailscaleProfile) { p.Address = "192.168.1.1" },
	} {
		t.Run(name, func(t *testing.T) {
			p := profile
			mutate(&p)
			b, _ := json.Marshal(p)
			if _, err := ParseTailscaleProfile(b); err == nil {
				t.Fatal("incomplete profile accepted")
			}
		})
	}
	if _, err := ParseTailscaleProfile([]byte(`{"schemaVersion":"a","schemaVersion":"b"}`)); err == nil {
		t.Fatal("duplicate field accepted")
	}
}

func TestTailscaleIdentityOutageDoesNotDialOrdinaryTransport(t *testing.T) {
	p := TailscaleProfile{SchemaVersion: "labcentral.tailscale-vnc/v1", TargetID: "target.test", Hostname: "pve1.test.ts.net", Address: "100.100.100.1", NodeKey: "nodekey:" + strings.Repeat("a", 64)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if conn, err := p.connect(ctx, accessendpoint.ProxmoxVNC{}); err == nil || conn != nil {
		t.Fatal("identity outage did not fail closed")
	}
}
