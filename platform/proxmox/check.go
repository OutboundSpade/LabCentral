package proxmox

import (
	"context"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type bridge struct {
	Active    int    `json:"active"`
	Address6  string `json:"address6"`
	CIDR6     string `json:"cidr6"`
	Gateway6  string `json:"gateway6"`
	Interface string `json:"iface"`
	Type      string `json:"type"`
	VLANAware int    `json:"bridge_vlan_aware"`
	Ports     string `json:"bridge_ports"`
	Address   string `json:"address"`
	CIDR      string `json:"cidr"`
	Gateway   string `json:"gateway"`
	Method    string `json:"method"`
	Comments  string `json:"comments"`
	Pending   any    `json:"pending"`
}

func (c *Client) nodePath(suffix string) string { return "/nodes/" + url.PathEscape(c.Node) + suffix }
func checkLabBridge(ctx context.Context, c *Client, name string) error {
	return checkBridgeDefinition(ctx, c, name, false)
}
func checkBridgeDefinition(ctx context.Context, c *Client, name string, allowPending bool) error {
	var bridges []bridge
	var changes string
	if err := c.call(ctx, "GET", c.nodePath("/network"), nil, &bridges, &changes); err != nil {
		return err
	}
	if !allowPending && strings.TrimSpace(changes) != "" {
		return fmt.Errorf("pending network changes exist; apply or revert them before checking labBridge isolation")
	}
	for _, b := range bridges {
		if b.Interface == name {
			if b.Type != "bridge" || b.VLANAware != 1 || strings.TrimSpace(b.Ports) != "" || b.Address != "" || b.CIDR != "" || b.Gateway != "" || b.Address6 != "" || b.CIDR6 != "" || b.Gateway6 != "" || !allowPending && b.Active != 1 {
				return fmt.Errorf("proxmox.labBridge must be active and VLAN-aware, have no bridge_ports and no host address or gateway; use environment setup to create an internal bridge")
			}
			return nil
		}
	}
	return fmt.Errorf("proxmox.labBridge is missing; run environment setup proxmox")
}
func checkStorage(ctx context.Context, c *Client, name string) error {
	var s struct {
		Content string `json:"content"`
		Active  int    `json:"active"`
		Enabled int    `json:"enabled"`
	}
	if err := c.Call(ctx, "GET", c.nodePath("/storage/"+url.PathEscape(name)+"/status"), nil, &s); err != nil {
		return err
	}
	if !lc.Contains(strings.Split(s.Content, ","), "images") || s.Active != 1 || s.Enabled != 1 {
		return fmt.Errorf("proxmox.storage must be active, enabled and support images; choose image storage or rerun setup")
	}
	return nil
}

func checkOperatorBridges(ctx context.Context, c *Client, e lc.Environment) error {
	var bridges []bridge
	if err := c.Call(ctx, "GET", c.nodePath("/network"), nil, &bridges); err != nil {
		return err
	}
	for field, name := range map[string]string{"proxmox.managementBridge": e.Proxmox.ManagementBridge, "build.bridge": e.Build.Bridge} {
		if name == "" {
			continue
		}
		found := false
		for _, b := range bridges {
			if b.Interface == name && b.Active == 1 && (b.Type == "bridge" || b.Type == "OVSBridge") {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%s must name an active bridge; choose the management/build network with DHCP, Internet and operator SSH reachability", field)
		}
	}
	return nil
}

var runtimePrivileges = []string{"VM.Allocate", "VM.Audit", "VM.Clone", "VM.Config.CDROM", "VM.Config.CPU", "VM.Config.Cloudinit", "VM.Config.Disk", "VM.Config.HWType", "VM.Config.Memory", "VM.Config.Network", "VM.Config.Options", "VM.Console", "VM.PowerMgmt", "VM.GuestAgent.Audit", "VM.GuestAgent.Unrestricted"}

func guestAgentEnabled(value string) bool {
	for _, part := range strings.Split(value, ",") {
		if part == "1" || part == "enabled=1" {
			return true
		}
	}
	return false
}

func checkPermissions(ctx context.Context, c *Client, e lc.Environment) error {
	var permissions map[string]map[string]int
	if err := c.Call(ctx, "GET", "/access/permissions", nil, &permissions); err != nil {
		return fmt.Errorf("read token privileges: %w", err)
	}
	paths := map[string][]string{"/pool/" + e.Proxmox.Pool: runtimePrivileges, "/storage/" + e.Proxmox.Storage: {"Datastore.Audit", "Datastore.AllocateSpace"}, "/nodes/" + c.Node: {"Sys.Audit"}}
	for _, name := range []string{e.Proxmox.LabBridge, e.Proxmox.ManagementBridge, e.Build.Bridge} {
		if name != "" {
			paths["/sdn/zones/localnetwork/"+name] = []string{"SDN.Use"}
		}
	}
	for path, required := range paths {
		for _, priv := range required {
			ok := false
			for parent, grants := range permissions {
				if (path == parent || strings.HasPrefix(path, parent+"/") || parent == "/") && grants[priv] == 1 {
					ok = true
				}
			}
			if !ok {
				return fmt.Errorf("Proxmox token lacks %s on %s; rerun setup ACLs for both user and privilege-separated token", priv, path)
			}
		}
	}
	return nil
}
func checkRanges(ctx context.Context, c *Client, e lc.Environment) error {
	var list []struct {
		ID   int    `json:"vmid"`
		Node string `json:"node"`
		Type string `json:"type"`
	}
	if err := c.Call(ctx, "GET", "/cluster/resources?type=vm", nil, &list); err != nil {
		return err
	}
	occupied := 0
	usedVLANs := map[int]bool{}
	for _, vm := range list {
		reserved := vm.ID >= e.Proxmox.VMIDStart && vm.ID <= e.Proxmox.VMIDEnd
		if vm.Node != c.Node {
			if reserved {
				return fmt.Errorf("VMID range is occupied on another node; choose an exclusive range")
			}
			continue
		}
		var cfg VMConfig
		if vm.Type == "lxc" {
			var raw map[string]any
			if err := c.Call(ctx, "GET", c.nodePath("/lxc/"+strconv.Itoa(vm.ID)+"/config"), nil, &raw); err != nil {
				return err
			}
			cfg.NICs = map[string]string{}
			for key, value := range raw {
				if _, err := strconv.Atoi(strings.TrimPrefix(key, "net")); strings.HasPrefix(key, "net") && err == nil {
					cfg.NICs[key], _ = value.(string)
				}
			}
		} else {
			var err error
			cfg, err = c.Config(ctx, vm.ID)
			if err != nil {
				return fmt.Errorf("inspect VMID/VLAN allocations: %w", err)
			}
		}
		owned := vm.Type != "lxc" && strings.HasPrefix(cfg.Description, "labcentral:v2:")
		tags := strings.Split(cfg.Tags, ";")
		if vm.Type != "lxc" && lc.Contains(tags, "labcentral-v2") {
			for _, tag := range tags {
				owned = owned || regexp.MustCompile(`^build-[a-f0-9]{24}$`).MatchString(tag)
			}
		}
		if reserved {
			occupied++
			if !owned {
				return fmt.Errorf("VMID range contains an unowned guest; choose an exclusive range")
			}
		}
		for _, nic := range cfg.NICs {
			fields := map[string]string{}
			for _, part := range strings.Split(nic, ",") {
				key, value, _ := strings.Cut(part, "=")
				fields[key] = value
			}
			if fields["bridge"] != e.Proxmox.LabBridge {
				continue
			}
			if !reserved || !owned {
				return fmt.Errorf("labBridge is used by a guest outside the owned VMID range; remove the foreign connection before provisioning")
			}
			tag, err := strconv.Atoi(fields["tag"])
			if fields["trunks"] != "" || err != nil || tag < e.Proxmox.VLANStart || tag > e.Proxmox.VLANEnd {
				return fmt.Errorf("labBridge NICs must use access tags within the exclusive VLAN range; inspect modified guest networking")
			}
			usedVLANs[tag] = true
		}
	}
	if occupied >= e.Proxmox.VMIDEnd-e.Proxmox.VMIDStart+1 {
		return fmt.Errorf("VMID range is full; destroy unused labs or expand the exclusive range")
	}
	if len(usedVLANs) >= e.Proxmox.VLANEnd-e.Proxmox.VLANStart+1 {
		return fmt.Errorf("VLAN range is full; destroy unused labs or expand the exclusive range")
	}
	return nil
}

func requireVMAudit(ctx context.Context, c *Client, id int) error {
	return requireAudit(ctx, c, "/vms/"+strconv.Itoa(id), "VM.Audit")
}
func requireAudit(ctx context.Context, c *Client, path, privilege string) error {
	var permissions map[string]map[string]int
	if err := c.Call(ctx, "GET", "/access/permissions", nil, &permissions); err != nil {
		return err
	}
	for parent, grants := range permissions {
		if (path == parent || strings.HasPrefix(path, parent+"/") || parent == "/") && grants[privilege] == 1 {
			return nil
		}
	}
	return fmt.Errorf("recovery_required: cannot prove native absence without %s on %s; rerun environment setup to restore read-only inventory visibility", privilege, path)
}
