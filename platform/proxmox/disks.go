package proxmox

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	lc "github.com/OutboundSpade/LabCentral/v2"
)

type diskReceipt struct {
	Owner   string
	VMID    int
	Volumes []string
}
type storageVolume struct {
	ID      string `json:"volid"`
	VMID    int    `json:"vmid"`
	Content string `json:"content"`
}

func diskKey(owner string, id int) string { return owner + "/disks/" + strconv.Itoa(id) }
func diskPath(c *Client, storage string) string {
	return "/nodes/" + url.PathEscape(c.Node) + "/storage/" + url.PathEscape(storage) + "/content"
}
func listVolumes(ctx context.Context, c *Client, storage string) ([]storageVolume, error) {
	if storage == "" {
		return nil, fmt.Errorf("recovery_required: storage binding missing; disk absence cannot be verified")
	}
	if err := requireAudit(ctx, c, "/storage/"+storage, "Datastore.Audit"); err != nil {
		return nil, err
	}
	var volumes []storageVolume
	err := c.Call(ctx, "GET", diskPath(c, storage)+"?content=images", nil, &volumes)
	return volumes, err
}
func (p *Provider) diskReceipt(cmd lc.Command, id int) (diskReceipt, error) {
	var receipt diskReceipt
	err := p.Journal.Load(diskKey(cmd.Owner, id), &receipt)
	if os.IsNotExist(err) {
		return diskReceipt{Owner: cmd.Owner, VMID: id}, nil
	}
	if err != nil {
		return receipt, err
	}
	if receipt.Owner != cmd.Owner || receipt.VMID != id {
		return receipt, fmt.Errorf("ownership_mismatch: disk cleanup receipt")
	}
	return receipt, nil
}

// Remember native volume IDs before VM deletion can erase their configuration.
// Native storage VMID ownership, not a matching disk name, authorizes cleanup.
func (p *Provider) rememberDisks(ctx context.Context, c *Client, cmd lc.Command, id int, v VMConfig) error {
	receipt, err := p.diskReceipt(cmd, id)
	if err != nil {
		return err
	}
	volumes, err := listVolumes(ctx, c, cmd.Environment.Proxmox.Storage)
	if err != nil {
		return err
	}
	native := map[string]storageVolume{}
	for _, volume := range volumes {
		native[volume.ID] = volume
	}
	known := map[string]bool{}
	for _, volume := range receipt.Volumes {
		known[volume] = true
	}
	for _, drive := range v.Disks {
		volume, _, _ := strings.Cut(drive, ",")
		if volume == "" || volume == "none" {
			continue
		}
		// Native VM destruction skips ISO media; never adopt or delete operator ISOs.
		if strings.Contains(drive, ",media=cdrom") && strings.Contains(volume, ":iso/") {
			continue
		}
		storage, _, ok := strings.Cut(volume, ":")
		if !ok || storage != cmd.Environment.Proxmox.Storage {
			return fmt.Errorf("ownership_mismatch: VM disk is outside the configured storage; VM retained")
		}
		if found, exists := native[volume]; exists && (found.VMID != id || found.Content != "images") {
			return fmt.Errorf("ownership_mismatch: attached disk is not owned by this VM; VM retained")
		}
		known[volume] = true
	}
	for _, volume := range volumes {
		if volume.VMID == id && !known[volume.ID] {
			return fmt.Errorf("recovery_required: unrecorded storage volume for reserved VMID; inspect native storage before cleanup")
		}
	}
	receipt.Volumes = nil
	for volume := range known {
		receipt.Volumes = append(receipt.Volumes, volume)
	}
	sort.Strings(receipt.Volumes)
	return p.Journal.Save(diskKey(cmd.Owner, id), receipt)
}

// Proxmox can finish VM destruction with only warnings about failed disk frees.
// Retain the claim until every recorded disk is confirmed absent. A retry deletes
// only an exact recorded volume with the same native storage owner.
func (p *Provider) cleanupDisks(ctx context.Context, c *Client, cmd lc.Command, id int) error {
	receipt, err := p.diskReceipt(cmd, id)
	if err != nil {
		return err
	}
	volumes, err := listVolumes(ctx, c, cmd.Environment.Proxmox.Storage)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, volume := range receipt.Volumes {
		known[volume] = true
	}
	for _, child := range cmd.Prior.Children {
		if child.Kind == "volume" {
			known[child.ID] = true
		}
	}
	var remaining []storageVolume
	for _, volume := range volumes {
		if volume.VMID == id && !known[volume.ID] {
			return fmt.Errorf("recovery_required: VM absent but an unrecorded storage volume remains; allocation retained for investigation")
		}
		if known[volume.ID] {
			if volume.VMID != id || volume.Content != "images" {
				return fmt.Errorf("ownership_mismatch: recorded disk has a different native owner; allocation retained")
			}
			remaining = append(remaining, volume)
		}
	}
	for _, volume := range remaining {
		// A foreign replacement at the reserved VMID must not lose its attached disk.
		var inventory []struct {
			VMID int `json:"vmid"`
		}
		if err = c.Call(ctx, "GET", "/cluster/resources?type=vm", nil, &inventory); err != nil {
			return err
		}
		for _, vm := range inventory {
			if vm.VMID == id {
				return fmt.Errorf("recovery_required: VMID reappeared during disk cleanup; inspect ownership")
			}
		}
		var task string
		if err = c.Call(ctx, "DELETE", diskPath(c, cmd.Environment.Proxmox.Storage)+"/"+url.PathEscape(volume.ID), nil, &task); err != nil {
			return fmt.Errorf("recovery_required: recorded disk %s remains after VM removal; allocation retained; an administrator may need to delete the verified volume with Datastore.Allocate: %w", volume.ID, err)
		}
		if task != "" {
			if err = c.Wait(ctx, task); err != nil {
				return err
			}
		}
	}
	if len(remaining) > 0 {
		volumes, err = listVolumes(ctx, c, cmd.Environment.Proxmox.Storage)
		if err != nil {
			return err
		}
		for _, volume := range volumes {
			if known[volume.ID] {
				return fmt.Errorf("recovery_required: disk deletion unconfirmed; allocation retained")
			}
		}
	}
	return nil
}
