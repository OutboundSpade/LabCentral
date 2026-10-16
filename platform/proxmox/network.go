package proxmox

import (
	"encoding/json"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"net"
	"strconv"
	"strings"
)

func plannedNetwork(cmd lc.Command) (network, error) {
	if len(cmd.Networks) > 0 {
		for _, n := range cmd.Networks {
			cidr, gw, err := lc.ProxmoxSubnet(cmd.Environment.Proxmox, n.VLAN)
			if err != nil {
				return network{}, err
			}
			if cidr != n.CIDR || gw != n.Gateway {
				return network{}, fmt.Errorf("network differs from reserved VLAN allocation")
			}
		}
		n := cmd.Networks[0]
		return network{CIDR: n.CIDR, Gateway: n.Gateway, VLAN: n.VLAN, Segments: cmd.Networks}, nil
	}
	if len(cmd.Source.Lab.Networks) > 1 {
		return network{}, fmt.Errorf("multi-network lab requires a complete VLAN reservation")
	}
	cidr, gw, err := lc.ProxmoxSubnet(cmd.Environment.Proxmox, cmd.VLAN)
	if err != nil {
		return network{}, err
	}
	return network{CIDR: cidr, Gateway: gw, VLAN: cmd.VLAN}, nil
}
func machineFirewallRules(e lc.Environment, m lc.PlannedMachine, networks []lc.PlannedNetwork, management ...string) []firewallRule {
	desired := []firewallRule{}
	for j, nic := range m.NICs {
		iface := fmt.Sprintf("net%d", j)
		desired = append(desired, firewallRule{Type: "in", Action: "ACCEPT", Enable: 1, Source: nic.CIDR, Iface: iface}, firewallRule{Type: "out", Action: "ACCEPT", Enable: 1, Dest: nic.CIDR, Iface: iface})
		if j == 0 {
			for _, cidr := range e.OperatorCIDRs {
				desired = append(desired, firewallRule{Type: "in", Action: "ACCEPT", Enable: 1, Source: cidr, Dest: nic.Address, Proto: "tcp", Dport: "22", Iface: iface})
			}
		}
		for _, n := range networks {
			if n.Name != nic.Network {
				continue
			}
			for _, route := range n.Routes {
				if m.Spec.Forwarding && route.Router == m.Name {
					continue
				}
				desired = append(desired, firewallRule{Type: "in", Action: "ACCEPT", Enable: 1, Source: route.CIDR, Iface: iface}, firewallRule{Type: "out", Action: "ACCEPT", Enable: 1, Dest: route.CIDR, Iface: iface})
			}
		}
		internet := false
		for _, n := range networks {
			if n.Name == nic.Network {
				internet = n.Egress == "internet"
			}
		}
		if internet {
			// Retain Phase 2's deny-before-Internet ordering per NIC; management
			// allowances above apply only to the first NIC.
			tail := firewallRules(e, nic.CIDR, true, management...)
			for _, r := range tail {
				if r.Type != "out" || r.Dest == nic.CIDR {
					continue
				}
				r.Iface = iface
				if r.Action == "ACCEPT" && r.Dest == "0.0.0.0/0" {
					r.Source = nic.CIDR
				}
				desired = append(desired, r)
			}
		}
	}
	return desired
}

// Capture every cloned NIC so a base cannot silently retain an unplanned path.
func (v *VMConfig) UnmarshalJSON(raw []byte) error {
	type config VMConfig
	var decoded config
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	decoded.NICs = map[string]string{}
	decoded.Disks = map[string]string{}
	for key, value := range fields {
		if diskConfigKey(key) {
			var drive string
			if err := json.Unmarshal(value, &drive); err != nil {
				return err
			}
			decoded.Disks[key] = drive
		}
		if !strings.HasPrefix(key, "net") {
			continue
		}
		suffix := strings.TrimPrefix(key, "net")
		index, err := strconv.Atoi(suffix)
		if err != nil || index < 0 || index > 31 || strconv.Itoa(index) != suffix {
			continue
		}
		var nic string
		if err = json.Unmarshal(value, &nic); err != nil {
			return err
		}
		decoded.NICs[key] = nic
	}
	*v = VMConfig(decoded)
	return nil
}

// MACs binds guest network programs to the actual assigned NICs, independent of
// cloud-init's initial ethN or predictable ensN names.
func (v VMConfig) MACs(count int) ([]string, error) {
	macs := []string{}
	for j := 0; j < count; j++ {
		nic := v.NICs[fmt.Sprintf("net%d", j)]
		if j == 0 {
			nic = v.Net0
		}
		_, mac, ok := strings.Cut(strings.Split(nic, ",")[0], "=")
		if !ok {
			return nil, fmt.Errorf("planned NIC MAC is unavailable")
		}
		if _, err := net.ParseMAC(mac); err != nil {
			return nil, fmt.Errorf("invalid planned NIC MAC")
		}
		macs = append(macs, mac)
	}
	return macs, nil
}

func diskConfigKey(key string) bool {
	if key == "vmstate" {
		return true
	}
	for _, prefix := range []string{"scsi", "sata", "virtio", "ide", "efidisk", "tpmstate", "unused"} {
		if strings.HasPrefix(key, prefix) {
			suffix := strings.TrimPrefix(key, prefix)
			n, err := strconv.Atoi(suffix)
			return err == nil && n >= 0 && strconv.Itoa(n) == suffix
		}
	}
	return false
}
