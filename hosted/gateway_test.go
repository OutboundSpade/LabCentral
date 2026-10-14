package hosted

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	"github.com/OutboundSpade/LabCentral/v2/internal/sshtest"
)

func TestGuestJobReusesPinnedGatewayAcrossProgramsAndMachines(t *testing.T) {
	machine := sshtest.New(t, nil, func(string) string { return "fixture" })
	gateway := sshtest.New(t, func(dest string) string {
		if dest == "10.77.0.11:22" || dest == "10.77.0.12:22" {
			return machine.Address
		}
		return ""
	}, nil)
	key, _ := sshtest.Key(t)
	data, _ := json.Marshal(lc.GatewayIdentity{HostPublicKey: gateway.HostKey, ENI: "eni-owned"})
	inst := lc.Instance{ID: "owner", Resources: []lc.Resource{{Kind: "gateway", ID: "gateway", Owner: "owner", Address: gateway.Address, Complete: true, Data: data}}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ctx, closeJob := guestJobContext(ctx)
	defer closeJob()
	cache, _ := guestCacheFor(ctx)
	for range 2 {
		for _, address := range []string{"10.77.0.11", "10.77.0.12"} {
			c, err := cache.clientFor(ctx, inst, address, address, key, machine.HostKey)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = remote.Run(ctx, c, "guest program fixture"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if gateway.Connections.Load() != 1 || gateway.Channels.Load() != 2 || machine.Connections.Load() != 2 {
		t.Fatal("job redialed gateway or machine", gateway.Connections.Load(), gateway.Channels.Load(), machine.Connections.Load())
	}
	inst.Resources[0].Address = "192.0.2.99"
	if _, err := cache.clientFor(ctx, inst, "10.77.0.11", "10.77.0.11", key, machine.HostKey); err == nil {
		t.Fatal("gateway identity changed during job")
	}
}

func TestGuestJobCannotSwitchBetweenDirectAndGatewayPaths(t *testing.T) {
	cache, _ := guestCacheFor(context.Background())
	// Record a prior gateway identity without performing any network I/O.
	cache.gateways = map[string]string{"owner": "previous gateway identity"}
	if _, err := cache.clientFor(context.Background(), lc.Instance{ID: "owner"}, "machine", "192.0.2.1", nil, ""); err == nil {
		t.Fatal("removed gateway selected direct access")
	}
}
