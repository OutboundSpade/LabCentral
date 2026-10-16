package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/guest"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
)

func (p *Provider) gateway(ctx context.Context, c *Client, cmd lc.Command) (lc.Result, error) {
	m, e := cmd.Machine, cmd.Environment
	base := e.Bases[e.Proxmox.EffectiveGatewayBase()]
	if m.Name != "gateway" || m.NativeImage != base.ID || m.CPU != 1 || m.MemoryMiB < 512 || m.DiskGiB < base.DiskGiB {
		return lc.Result{}, fmt.Errorf("gateway differs from environment binding")
	}
	var n network
	if err := json.Unmarshal(cmd.Network.Data, &n); err != nil {
		return lc.Result{}, err
	}
	networks := n.Segments
	if len(networks) == 0 {
		networks = []lc.PlannedNetwork{{Name: "lab", CIDR: n.CIDR, Gateway: n.Gateway, VLAN: n.VLAN, Egress: "none"}}
	}
	if err := checkLabBridge(ctx, c, e.Proxmox.LabBridge); err != nil {
		return lc.Result{}, err
	}
	baseID, err := strconv.Atoi(base.ID)
	if err != nil {
		return lc.Result{}, err
	}
	_, err = journal.Do(ctx, p.Journal, cmd.Operation+"/clone", func() (int, error) {
		v, err := c.Config(ctx, baseID)
		if err != nil {
			return 0, err
		}
		if v.Template != 1 || v.Digest != base.Identity {
			return 0, fmt.Errorf("gateway base template identity changed; verify the environment binding before provisioning")
		}
		var task string
		err = c.Call(ctx, "POST", c.VM(baseID, "/clone"), map[string]any{"newid": m.VMID, "name": "lc-" + cmd.Owner[:12] + "-gateway", "description": marker(cmd.Owner, "gateway"), "full": 1, "storage": e.Proxmox.Storage, "pool": e.Proxmox.Pool}, &task)
		if err != nil {
			return 0, err
		}
		if err = c.Wait(ctx, task); err != nil {
			return 0, err
		}
		return m.VMID, nil
	}, func() (int, bool, error) {
		v, err := c.Config(ctx, m.VMID)
		if err != nil {
			return 0, false, err
		}
		if err = checkOwner(v, cmd.Owner, "gateway"); err != nil {
			return 0, false, err
		}
		if v.Lock != "" {
			return 0, false, fmt.Errorf("gateway clone is still locked; retry recovery")
		}
		return m.VMID, true, nil
	})
	if err != nil {
		return lc.Result{}, err
	}
	v, err := c.Config(ctx, m.VMID)
	if err != nil {
		return lc.Result{}, err
	}
	if err = checkOwner(v, cmd.Owner, "gateway"); err != nil {
		return lc.Result{}, err
	}
	ip := "ip=dhcp"
	if a := e.Proxmox.ManagementAddresses; a != nil {
		ip = "ip=" + m.Address + "/" + strings.Split(a.CIDR, "/")[1] + ",gw=" + a.Gateway
	}
	cfg := map[string]any{"cores": 1, "memory": m.MemoryMiB, "balloon": 0, "net0": gatewayNIC(v.Net0, e.Proxmox.ManagementBridge, 0), "ipconfig0": ip, "ciuser": "lab", "sshkeys": strings.ReplaceAll(url.QueryEscape(m.PublicKey), "+", "%20"), "ciupgrade": 0, "agent": "enabled=1", "digest": v.Digest}
	for j, n := range networks {
		cfg[fmt.Sprintf("net%d", j+1)] = gatewayNIC(v.NICs[fmt.Sprintf("net%d", j+1)], e.Proxmox.LabBridge, n.VLAN)
		cfg[fmt.Sprintf("ipconfig%d", j+1)] = "ip=" + n.Gateway + "/24"
	}
	removed := []string{}
	for key := range v.NICs {
		index, _ := strconv.Atoi(strings.TrimPrefix(key, "net"))
		if index > len(networks) {
			removed = append(removed, key, "ipconfig"+strconv.Itoa(index))
		}
	}
	if len(removed) > 0 {
		cfg["delete"] = strings.Join(removed, ",")
	}
	if err = c.Call(ctx, "PUT", c.VM(m.VMID, "/config"), cfg, nil); err != nil {
		return lc.Result{}, err
	}
	if err = c.Start(ctx, m.VMID); err != nil {
		return lc.Result{}, err
	}
	if err = c.WaitAgent(ctx, m.VMID); err != nil {
		return lc.Result{}, err
	}
	current, err := c.Config(ctx, m.VMID)
	if err != nil {
		return lc.Result{}, err
	}
	macs, err := current.MACs(len(networks) + 1)
	if err != nil {
		return lc.Result{}, err
	}
	_, managementCIDR, err := c.managementNetwork(ctx, m.VMID)
	if err != nil {
		return lc.Result{}, err
	}
	script, err := guest.ProxmoxGatewayExecOnNetwork(m, e, networks, managementCIDR, macs...)
	if err != nil {
		return lc.Result{}, err
	}
	if err = c.GuestExec(ctx, m.VMID, []string{"/bin/sh", "-ec", cloudInitWait + script}); err != nil {
		return lc.Result{}, err
	}
	result, err := p.findGateway(ctx, c, cmd)
	if err != nil {
		return result, err
	}
	if err = remote.Wait(ctx, result.Resource.Address, "lab", cmd.ClientPrivateKey, m.HostPublicKey); err != nil {
		return result, err
	}
	result.Resource.Complete = true
	return result, nil
}

const cloudInitWait = `rc=0; cloud-init status --wait >/dev/null || rc=$?; { [ "$rc" -eq 0 ] || [ "$rc" -eq 2 ]; } || exit 1; `

func (c *Client) Start(ctx context.Context, id int) error {
	var s struct {
		Status string `json:"status"`
	}
	if err := c.Call(ctx, "GET", c.VM(id, "/status/current"), nil, &s); err != nil {
		return err
	}
	if s.Status == "running" {
		return nil
	}
	var task string
	if err := c.Call(ctx, "POST", c.VM(id, "/status/start"), map[string]any{}, &task); err != nil {
		return err
	}
	return c.Wait(ctx, task)
}
func (c *Client) WaitAgent(ctx context.Context, id int) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("guest agent unavailable: check base installation and agent=1: %w", err)
		}
		if c.Call(ctx, "POST", c.VM(id, "/agent/ping"), map[string]any{}, nil) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
}
func (p *Provider) findGateway(ctx context.Context, c *Client, cmd lc.Command) (lc.Result, error) {
	v, err := c.Config(ctx, cmd.Machine.VMID)
	if err != nil {
		return lc.Result{}, err
	}
	if err = checkOwner(v, cmd.Owner, "gateway"); err != nil {
		return lc.Result{}, err
	}
	if v.Lock != "" {
		return lc.Result{}, fmt.Errorf("gateway mutation still in progress; retry recovery")
	}
	if cmd.Action == "discover" {
		data, _ := json.Marshal(lc.GatewayIdentity{HostPublicKey: cmd.Machine.HostPublicKey})
		return lc.Result{Resource: lc.Resource{Kind: "gateway", Name: "gateway", ID: strconv.Itoa(cmd.Machine.VMID), Owner: cmd.Owner, Data: data}}, nil
	}
	address, err := c.ManagementAddress(ctx, cmd.Machine.VMID)
	if err != nil {
		return lc.Result{}, err
	}
	if cmd.Machine.Address != "" && address != cmd.Machine.Address {
		return lc.Result{}, fmt.Errorf("gateway management address differs from reservation")
	}
	data, _ := json.Marshal(lc.GatewayIdentity{HostPublicKey: cmd.Machine.HostPublicKey})
	return lc.Result{Resource: lc.Resource{Kind: "gateway", Name: "gateway", ID: strconv.Itoa(cmd.Machine.VMID), Owner: cmd.Owner, Address: address, Data: data}}, nil
}
func (c *Client) ManagementAddress(ctx context.Context, id int) (string, error) {
	address, _, err := c.managementNetwork(ctx, id)
	return address, err
}
func (c *Client) managementNetwork(ctx context.Context, id int) (string, string, error) {
	var result struct {
		Result []struct {
			Hardware string `json:"hardware-address"`
			IPs      []struct {
				Address string `json:"ip-address"`
				Type    string `json:"ip-address-type"`
				Prefix  int    `json:"prefix"`
			} `json:"ip-addresses"`
		} `json:"result"`
	}
	v, err := c.Config(ctx, id)
	if err != nil {
		return "", "", err
	}
	head := strings.Split(v.Net0, ",")[0]
	_, mac, ok := strings.Cut(head, "=")
	if !ok {
		return "", "", fmt.Errorf("management NIC MAC unavailable")
	}
	if err = c.Call(ctx, "GET", c.VM(id, "/agent/network-get-interfaces"), nil, &result); err != nil {
		return "", "", err
	}
	addresses := []string{}
	cidrs := []string{}
	for _, nic := range result.Result {
		if !strings.EqualFold(mac, nic.Hardware) {
			continue
		}
		for _, ip := range nic.IPs {
			a, err := netip.ParseAddr(ip.Address)
			if err == nil && a.Is4() && !a.IsLoopback() && !a.IsLinkLocalUnicast() {
				addresses = append(addresses, a.String())
				if ip.Prefix < 1 || ip.Prefix > 32 {
					return "", "", fmt.Errorf("invalid management prefix")
				}
				cidrs = append(cidrs, netip.PrefixFrom(a, ip.Prefix).Masked().String())
			}
		}
	}
	if len(addresses) != 1 {
		return "", "", fmt.Errorf("gateway management NIC needs exactly one IPv4 address; check DHCP or managementAddresses")
	}
	return addresses[0], cidrs[0], nil
}

func gatewayNIC(original, bridge string, vlan int) string {
	nic := "virtio"
	if _, mac, ok := strings.Cut(strings.Split(original, ",")[0], "="); ok && mac != "" {
		nic += "=" + mac
	}
	nic += ",bridge=" + bridge
	if vlan != 0 {
		nic += fmt.Sprintf(",tag=%d", vlan)
	}
	return nic
}
