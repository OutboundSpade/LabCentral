package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/build"
)

// ConfigureBuild retains the pinned base's disk controller. The clone plugin's
// default is LSI, which can make a virtio cloud image drop into initramfs.
func (p *Provider) ConfigureBuild(ctx context.Context, cmd lc.Command, cfg *build.Config) error {
	c, err := NewClient(cmd.Environment)
	if err != nil {
		return err
	}
	id, err := strconv.Atoi(cmd.Image.Base.ID)
	if err != nil {
		return err
	}
	v, err := c.Config(ctx, id)
	if err != nil {
		return err
	}
	if v.Template != 1 || v.Digest != cmd.Image.Base.Identity {
		return fmt.Errorf("base template identity changed before build")
	}
	controller := v.SCSIController
	if controller == "" {
		controller = "lsi"
	} // Proxmox's effective default.
	switch controller {
	case "lsi", "lsi53c810", "virtio-scsi-pci", "virtio-scsi-single", "megasas", "pvscsi":
	default:
		return fmt.Errorf("unsupported base SCSI controller")
	}
	if len(cfg.Builders) != 1 {
		return fmt.Errorf("expected one native builder")
	}
	var native map[string]any
	if err = json.Unmarshal(cfg.Builders[0], &native); err != nil {
		return err
	}
	native["scsi_controller"] = controller
	cfg.Builders[0], err = json.Marshal(native)
	return err
}
