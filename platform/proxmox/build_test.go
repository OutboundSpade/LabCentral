package proxmox

import (
	"context"
	"encoding/json"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/build"
	"net/http"
	"testing"
)

func TestBuildPreservesPinnedBaseController(t *testing.T) {
	for _, controller := range []string{"virtio-scsi-pci", "virtio-scsi-single", "pvscsi", ""} {
		_, e := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				t.Fatal("base modified")
			}
			response(w, VMConfig{Template: 1, Digest: "pinned", SCSIController: controller})
		})
		cmd := lc.Command{Environment: e, Image: lc.PlannedImage{Base: lc.Base{ID: "2100", Identity: "pinned"}}}
		cfg := build.Config{Builders: []json.RawMessage{json.RawMessage(`{"type":"proxmox-clone","clone_vm_id":2100}`)}}
		if err := (&Provider{}).ConfigureBuild(context.Background(), cmd, &cfg); err != nil {
			t.Fatal(err)
		}
		var native map[string]any
		_ = json.Unmarshal(cfg.Builders[0], &native)
		want := controller
		if want == "" {
			want = "lsi"
		}
		if native["scsi_controller"] != want || native["clone_vm_id"] != float64(2100) {
			t.Fatal(native)
		}
		cmd.Image.Base.Identity = "changed"
		if err := (&Provider{}).ConfigureBuild(context.Background(), cmd, &cfg); err == nil {
			t.Fatal("unverified base configured")
		}
	}
}
