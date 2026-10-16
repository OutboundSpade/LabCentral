package proxmox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/privatefile"
)

const UbuntuCloudURL = "https://cloud-images.ubuntu.com/releases/noble/release-20260926/ubuntu-24.04-server-cloudimg-amd64.img"
const UbuntuCloudSHA256 = "6a81c37564db9b1ee84e141922625e1d7c5b389b99bb3c572e0243607d5bb4d2"

// SeedISO creates platform-owned NoCloud data, using a local ISO utility only.
// Author source is never passed to this function or executed on the operator.
func SeedISO(ctx context.Context, dir, owner, userData string) (string, error) {
	binary, err := exec.LookPath("xorriso")
	if err != nil {
		return "", fmt.Errorf("setup needs xorriso: install it on the operator machine")
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	for name, data := range map[string]string{"user-data": userData, "meta-data": "instance-id: labcentral-" + owner + "\nlocal-hostname: labcentral-bootstrap\n", "network-config": "version: 2\nethernets:\n  uplink:\n    match: {name: 'en*'}\n    dhcp4: true\n    dhcp6: false\n    accept-ra: false\n"} {
		if err = privatefile.Write(filepath.Join(dir, name), []byte(data)); err != nil {
			return "", err
		}
	}
	iso := filepath.Join(dir, "seed.iso")
	cmd := exec.CommandContext(ctx, binary, "-as", "mkisofs", "-quiet", "-volid", "cidata", "-joliet", "-rock", "-output", iso, filepath.Join(dir, "user-data"), filepath.Join(dir, "meta-data"), filepath.Join(dir, "network-config"))
	if err = cmd.Run(); err != nil {
		return "", fmt.Errorf("create NoCloud seed ISO: %w", err)
	}
	if err = os.Chmod(iso, 0600); err != nil {
		return "", err
	}
	return iso, nil
}

// UploadISO sends only the generated platform seed, never host configuration or
// credentials. Multipart is buffered and bounded to keep redirects/retries off.
func (c *Client) UploadISO(ctx context.Context, storage, name, file string) error {
	return c.uploadISO(ctx, storage, name, file, nil)
}
func (c *Client) uploadISO(ctx context.Context, storage, name, file string, saveTask func(string) error) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if len(data) > 4<<20 {
		return fmt.Errorf("seed ISO exceeds bound")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err = writer.WriteField("content", "iso"); err != nil {
		return err
	}
	part, err := writer.CreateFormFile("filename", name)
	if err != nil {
		return err
	}
	if _, err = part.Write(data); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.URL+c.nodePath("/storage/"+url.PathEscape(storage)+"/upload"), &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.Token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("seed upload transport failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &HTTPError{res.StatusCode, "POST", "storage ISO upload"}
	}
	var envelope struct {
		Data string `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&envelope); err != nil {
		return err
	}
	if saveTask != nil {
		if err = saveTask(envelope.Data); err != nil {
			return err
		}
	}
	return c.Wait(ctx, envelope.Data)
}

type volume struct {
	ID      string `json:"volid"`
	Size    int64  `json:"size"`
	Format  string `json:"format"`
	Content string `json:"content"`
	CTime   int64  `json:"ctime"`
}

func (s *Setup) findVolume(ctx context.Context, id string) (volume, bool, error) {
	var list []volume
	if err := s.Client.Call(ctx, "GET", s.Client.nodePath("/storage/"+url.PathEscape(s.state.Plan.Environment.Proxmox.Storage)+"/content"), nil, &list); err != nil {
		return volume{}, false, err
	}
	for _, v := range list {
		if v.ID == id {
			return v, true, nil
		}
	}
	return volume{}, false, nil
}
func (s *Setup) saveVolume(ctx context.Context, key, id string) error {
	v, found, err := s.findVolume(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("created setup volume is missing")
	}
	return s.journal().Save(key, v)
}
func (s *Setup) verifyVolume(ctx context.Context, key, id string) error {
	var saved volume
	if err := s.journal().Load(key, &saved); err != nil {
		var task string
		if taskErr := s.journal().Load(strings.TrimSuffix(key, "-volume")+"-task", &task); taskErr != nil {
			return fmt.Errorf("volume ownership receipt unavailable; inspect setup task before retry: %w", err)
		}
		if err = s.Client.Wait(ctx, task); err != nil {
			return err
		}
		if err = s.saveVolume(ctx, key, id); err != nil {
			return err
		}
		if err = s.journal().Load(key, &saved); err != nil {
			return err
		}
	}
	actual, found, err := s.findVolume(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("setup volume missing")
	}
	if actual != saved {
		return fmt.Errorf("ownership_mismatch: setup volume metadata changed")
	}
	return nil
}
func (s *Setup) base(ctx context.Context) error {
	c, p := s.Client, s.state.Plan
	id := p.BaseVMID
	storage := p.Environment.Proxmox.Storage
	imageID, seedID := storage+":import/"+p.ImageName, storage+":iso/"+p.SeedName
	if err := s.step(ctx, "image", func() error {
		if _, found, err := s.findVolume(ctx, imageID); err != nil {
			return err
		} else if found {
			return fmt.Errorf("refusing to overwrite import image")
		}
		var task string
		if err := c.Call(ctx, "POST", c.nodePath("/storage/"+url.PathEscape(storage)+"/download-url"), map[string]any{"url": UbuntuCloudURL, "content": "import", "filename": p.ImageName, "checksum": UbuntuCloudSHA256, "checksum-algorithm": "sha256", "verify-certificates": 1}, &task); err != nil {
			return err
		}
		if err := s.journal().Save("image-task", task); err != nil {
			return err
		}
		if err := c.Wait(ctx, task); err != nil {
			return err
		}
		return s.saveVolume(ctx, "image-volume", imageID)
	}, func() error { return s.verifyVolume(ctx, "image-volume", imageID) }); err != nil {
		return err
	}
	if err := s.step(ctx, "seed", func() error {
		if _, found, err := s.findVolume(ctx, seedID); err != nil {
			return err
		} else if found {
			return fmt.Errorf("refusing to overwrite seed ISO")
		}
		dir, err := os.MkdirTemp(filepath.Dir(s.File), "labcentral-seed-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		makeSeed := s.seed
		if makeSeed == nil {
			makeSeed = SeedISO
		}
		iso, err := makeSeed(ctx, dir, p.Owner, "#cloud-config\npackage_update: true\npackages: [qemu-guest-agent, nftables]\nssh_pwauth: false\ndisable_root: true\nruncmd:\n  - systemctl add-wants multi-user.target qemu-guest-agent.service\n  - systemctl start qemu-guest-agent\n")
		if err != nil {
			return err
		}
		if err = c.uploadISO(ctx, storage, p.SeedName, iso, func(task string) error { return s.journal().Save("seed-task", task) }); err != nil {
			return err
		}
		return s.saveVolume(ctx, "seed-volume", seedID)
	}, func() error { return s.verifyVolume(ctx, "seed-volume", seedID) }); err != nil {
		return err
	}
	if err := s.step(ctx, "base", func() error {
		var inventory []struct {
			ID int `json:"vmid"`
		}
		if err := c.Call(ctx, "GET", "/cluster/resources?type=vm", nil, &inventory); err != nil {
			return err
		}
		for _, vm := range inventory {
			if vm.ID == id {
				return fmt.Errorf("base VMID became occupied; refusing to overwrite")
			}
		}
		var task string
		cfg := map[string]any{"vmid": id, "name": "lc-base-" + p.Owner[:12], "description": marker(p.Owner, "base"), "tags": "labcentral-v2;" + p.Owner, "pool": p.Environment.Proxmox.Pool, "cores": 1, "memory": 1024, "scsihw": "virtio-scsi-pci", "scsi0": storage + ":0,import-from=" + imageID, "ide2": seedID + ",media=cdrom", "serial0": "socket", "vga": "std", "boot": "order=scsi0", "agent": "enabled=1", "net0": "virtio,bridge=" + p.Environment.Proxmox.ManagementBridge}
		if err := c.Call(ctx, "POST", c.nodePath("/qemu"), cfg, &task); err != nil {
			return err
		}
		if err := s.journal().Save("base-task", task); err != nil {
			return err
		}
		return c.Wait(ctx, task)
	}, func() error {
		v, err := c.Config(ctx, id)
		if err != nil {
			return err
		}
		if err = checkOwner(v, p.Owner, "base"); err != nil {
			return err
		}
		if v.Lock != "" {
			return fmt.Errorf("base import still in progress; retry setup")
		}
		return nil
	}); err != nil {
		return err
	}
	if s.state.Steps["qualified"] != "done" {
		if err := c.Call(ctx, "PUT", c.VM(id, "/resize"), map[string]any{"disk": "scsi0", "size": "20G"}, nil); err != nil {
			return err
		}
		if err := c.Start(ctx, id); err != nil {
			return err
		}
		if err := c.WaitAgent(ctx, id); err != nil {
			return err
		}
		if err := c.GuestExec(ctx, id, []string{"/bin/sh", "-ec", cloudInitWait + "command -v nft; systemctl is-active qemu-guest-agent; dpkg-query -W qemu-guest-agent; test -e /dev/virtio-ports/org.qemu.guest_agent.0"}); err != nil {
			return fmt.Errorf("base bootstrap failed; check DHCP/Internet on management bridge: %w", err)
		}
		s.state.Steps["qualified"] = "done"
		if err := s.save(); err != nil {
			return err
		}
	}
	if s.state.Steps["reboot-verified"] != "done" {
		v, err := c.Config(ctx, id)
		if err != nil {
			return err
		}
		if err = checkOwner(v, p.Owner, "base"); err != nil {
			return err
		}
		var status struct {
			Status string `json:"status"`
		}
		if err = c.Call(ctx, "GET", c.VM(id, "/status/current"), nil, &status); err != nil {
			return err
		}
		if status.Status == "running" {
			if err = c.GuestExec(ctx, id, []string{"/bin/sync"}); err != nil {
				return err
			}
			var task string
			if err = c.Call(ctx, "POST", c.VM(id, "/status/shutdown"), map[string]any{"timeout": 60}, &task); err != nil {
				return err
			}
			if err = c.Wait(ctx, task); err != nil {
				return err
			}
		}
		if err = c.Start(ctx, id); err != nil {
			return err
		}
		if err = c.WaitAgent(ctx, id); err != nil {
			return fmt.Errorf("base guest agent did not survive restart: %w", err)
		}
		if err = c.GuestExec(ctx, id, []string{"/bin/sh", "-ec", cloudInitWait + "systemctl is-active qemu-guest-agent; test -L /etc/systemd/system/multi-user.target.wants/qemu-guest-agent.service"}); err != nil {
			return err
		}
		s.state.Steps["reboot-verified"] = "done"
		if err = s.save(); err != nil {
			return err
		}
	}

	if s.state.Steps["sanitized"] != "done" {
		// This platform program runs inside the bootstrap guest, then API stop waits
		// for shutdown. It removes the seed identity before a later clone can boot.
		if err := c.GuestExec(ctx, id, []string{"/bin/sh", "-ec", "set -eu; cloud-init clean --logs --seed; rm -f /etc/ssh/ssh_host_* /var/lib/dbus/machine-id /home/ubuntu/.ssh/authorized_keys; truncate -s 0 /etc/machine-id; rm -f /etc/netplan/50-cloud-init.yaml; sync"}); err != nil {
			return err
		}
		s.state.Steps["sanitized"] = "done"
		if err := s.save(); err != nil {
			return err
		}
	}
	if s.state.Steps["template"] != "done" {
		var v struct {
			Status string `json:"status"`
		}
		if err := c.Call(ctx, "GET", c.VM(id, "/status/current"), nil, &v); err != nil {
			return err
		}
		if v.Status == "running" {
			var task string
			if err := c.Call(ctx, "POST", c.VM(id, "/status/stop"), map[string]any{}, &task); err != nil {
				return err
			}
			if err := c.Wait(ctx, task); err != nil {
				return err
			}
		}
		vconfig, err := c.Config(ctx, id)
		if err != nil {
			return err
		}
		if err = checkOwner(vconfig, p.Owner, "base"); err != nil {
			return err
		}
		if vconfig.Template != 1 {
			if err = c.Call(ctx, "PUT", c.VM(id, "/config"), map[string]any{"ide2": storage + ":cloudinit", "ciuser": "ubuntu", "ciupgrade": 0, "ipconfig0": "ip=dhcp", "digest": vconfig.Digest}, nil); err != nil {
				return err
			}
			var task string
			if err = c.Call(ctx, "POST", c.VM(id, "/template"), map[string]any{}, &task); err != nil {
				return err
			}
			if err = c.Wait(ctx, task); err != nil {
				return err
			}
		}
		s.state.Steps["template"] = "done"
		if err = s.save(); err != nil {
			return err
		}
	}
	v, err := c.Config(ctx, id)
	if err != nil {
		return err
	}
	if err = checkOwner(v, p.Owner, "base"); err != nil {
		return err
	}
	if v.Template != 1 {
		return fmt.Errorf("base template conversion unconfirmed")
	}
	s.state.Plan.Environment.Bases["ubuntu-24.04"] = lc.Base{ID: strconv.Itoa(id), Identity: v.Digest, User: "ubuntu", DiskGiB: 20}
	return s.save()
}
