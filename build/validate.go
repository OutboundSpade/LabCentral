package build

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/OutboundSpade/LabCentral/v2/internal/privatefile"
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	"golang.org/x/crypto/ssh"
	"net/netip"
	"os"
	"path/filepath"
	"time"
)

func key(path string) ([]byte, string, error) {
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		_, k, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, "", err
		}
		p, err := ssh.MarshalPrivateKey(k, "")
		if err != nil {
			return nil, "", err
		}
		b = pem.EncodeToMemory(p)
		if err = privatefile.Write(path, b); err != nil {
			return nil, "", err
		}
	} else if e != nil {
		return nil, "", e
	}
	s, e := ssh.ParsePrivateKey(b)
	if e != nil {
		return nil, "", e
	}
	return b, string(ssh.MarshalAuthorizedKey(s.PublicKey())), nil
}

type validationRecord struct {
	ImageID    string
	Network    lc.Resource
	Machine    lc.Resource
	ChecksDone bool
	CheckError string
	Cleaned    bool
}

// ValidateNative boots an owned disposable machine from the captured native image.
func ValidateNative(ctx context.Context, root string, p lc.Provider, c lc.Command, image lc.Resource) (lc.Resource, error) {
	dir := filepath.Join(root, c.Owner, "validation")
	if e := os.MkdirAll(dir, 0700); e != nil {
		return image, e
	}
	priv, pub, e := key(filepath.Join(dir, "client"))
	if e != nil {
		return image, e
	}
	hp, hpub, e := key(filepath.Join(dir, "host"))
	if e != nil {
		return image, e
	}
	j := journal.Journal{Root: dir}
	receipt := validationRecord{ImageID: image.ID}
	if e = j.Load("receipt", &receipt); e != nil && !os.IsNotExist(e) {
		return image, e
	}
	if receipt.ImageID != image.ID {
		return image, fmt.Errorf("validation image identity changed")
	}
	if receipt.ChecksDone {
		return finishValidation(ctx, p, c, image, j, &receipt)
	}
	owner := c.Owner + "-validation"
	vlan := c.VLAN
	net, err := p.Execute(ctx, lc.Command{Action: "network.create", Operation: owner + "/network", Owner: owner, Environment: c.Environment, VLAN: vlan}, nil)
	if err != nil {
		return image, err
	}
	receipt.Network = net.Resource
	if e = j.Save("receipt", receipt); e != nil {
		return image, e
	}
	m := lc.PlannedMachine{Name: "validation", NativeImage: image.ID, CPU: 2, MemoryMiB: 4096, DiskGiB: c.Image.DiskGiB, InstanceType: c.Environment.Build.InstanceType, VMID: c.ValidationVMID, Address: "10.77.0.10", PublicKey: pub, HostPrivateKey: string(hp), HostPublicKey: hpub, Spec: lc.Machine{Image: c.Image.Name, CPU: 2, Memory: "4Gi", Disk: fmt.Sprintf("%dGi", c.Image.DiskGiB), Access: []string{"ssh"}}}
	if c.Image.Desktop {
		m.Spec.Access = append(m.Spec.Access, "desktop")
	}
	if c.Environment.Platform == "proxmox" {
		cidr, _, e := lc.ProxmoxSubnet(c.Environment.Proxmox, vlan)
		if e != nil {
			return image, e
		}
		m.Address = validationAddress(cidr)
	}
	cmd := lc.Command{Action: "machine.create", Operation: owner + "/machine", Owner: owner, Environment: c.Environment, Machine: m, Network: net.Resource, VLAN: vlan, ClientPrivateKey: priv}
	checkpoint := cmd
	checkpoint.ClientPrivateKey = nil
	checkpoint.Machine.HostPrivateKey = ""
	if e = j.Save("machine-command", checkpoint); e != nil {
		return image, e
	}
	machine, err := p.Execute(ctx, cmd, nil)
	if err != nil {
		return image, fmt.Errorf("validation requires recovery of %s: %w", owner, err)
	}
	receipt.Machine = machine.Resource
	if e = j.Save("receipt", receipt); e != nil {
		return image, e
	}
	client, err := remote.Dial(ctx, machine.Resource.Address, "lab", priv, hpub)
	if err == nil {
		command := "test -d /home/lab"
		if c.Image.Desktop {
			if c.Environment.Platform == "aws" {
				command = "systemctl is-active dcvserver >/dev/null && command -v dcv >/dev/null"
			} else {
				command = "systemctl is-active lightdm >/dev/null"
			}
		}
		_, err = remote.Run(ctx, client, command)
		client.Close()
	}
	receipt.ChecksDone = true
	if err != nil {
		receipt.CheckError = err.Error()
	}
	if e = j.Save("receipt", receipt); e != nil {
		return image, e
	}
	return finishValidation(ctx, p, c, image, j, &receipt)
}

func finishValidation(ctx context.Context, p lc.Provider, c lc.Command, image lc.Resource, j journal.Journal, receipt *validationRecord) (lc.Resource, error) {
	owner := c.Owner + "-validation"
	if !receipt.Cleaned {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
		defer cancel()
		for _, resource := range []lc.Resource{receipt.Machine, receipt.Network} {
			if resource.ID == "" {
				continue
			}
			r, err := p.Execute(cleanupCtx, lc.Command{Action: "resource.destroy", Operation: owner + "/destroy-" + resource.Kind, Owner: owner, Environment: c.Environment, Prior: resource}, nil)
			if err != nil || !r.Absent {
				return image, fmt.Errorf("validation %s cleanup unconfirmed: %v", resource.Kind, err)
			}
		}
		receipt.Cleaned = true
		if err := j.Save("receipt", receipt); err != nil {
			return image, err
		}
	}
	if receipt.CheckError != "" {
		return image, fmt.Errorf("image validation failed: %s", receipt.CheckError)
	}
	image.Complete = true
	return image, nil
}

func validationAddress(cidr string) string {
	p, _ := netip.ParsePrefix(cidr)
	a := p.Masked().Addr()
	for n := 0; n < 10; n++ {
		a = a.Next()
	}
	return a.String()
}
