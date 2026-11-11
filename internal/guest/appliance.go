package guest

import (
	"fmt"
	"strings"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"go.yaml.in/yaml/v3"
)

func ApplianceUserData(m lc.PlannedMachine, owner string, commands []string) (string, error) {
	if m.Appliance == "panos" {
		return "type=dhcp-client\nhostname=" + m.Name + "\ndhcp-send-hostname=yes\ndhcp-send-client-id=yes\ndhcp-accept-server-hostname=no\ndhcp-accept-server-domain=no\n", nil
	}
	if m.Appliance != "vyos" {
		return "", fmt.Errorf("unknown native appliance")
	}
	pub := strings.Fields(m.PublicKey)
	if len(pub) < 2 {
		return "", fmt.Errorf("appliance client key is required")
	}
	config := append([]string(nil), commands...)
	config = append(config, "set system host-name '"+m.Name+"'", "set system login user vyos authentication public-keys labcentral type '"+pub[0]+"'", "set system login user vyos authentication public-keys labcentral key '"+pub[1]+"'", "set service ssh port '22'", "set service ssh listen-address '"+m.Address+"'")
	seen := map[string]bool{}
	for j, nic := range m.NICs {
		config = append(config, fmt.Sprintf("delete interfaces ethernet eth%d address", j), fmt.Sprintf("set interfaces ethernet eth%d address '%s/%s'", j, nic.Address, strings.Split(nic.CIDR, "/")[1]))
		routes := append([]lc.PlannedRoute(nil), nic.Routes...)
		if nic.Default {
			routes = append(routes, lc.PlannedRoute{CIDR: "0.0.0.0/0", Via: nic.Gateway})
		}
		for _, r := range routes {
			if !seen[r.CIDR] {
				config = append(config, "set protocols static route '"+r.CIDR+"' next-hop '"+r.Via+"'")
				seen[r.CIDR] = true
			}
		}
	}
	// This platform-owned guest hook publishes public host identities to the
	// authenticated EC2 console; it never sends a private key or credential.
	hook := "#!/bin/sh\nfor key in /etc/ssh/ssh_host_*_key.pub; do\n [ -r \"$key\" ] && printf 'LABCENTRAL_HOST_KEY " + owner + " %s\\n' \"$(cat \"$key\")\" > /dev/console\ndone\n"
	raw, err := yaml.Marshal(map[string]any{"vyos_config_commands": config, "write_files": []map[string]any{{"path": "/opt/vyatta/etc/config/scripts/vyos-postconfig-bootup.script", "permissions": "0755", "owner": "root:root", "content": hook}}})
	return "#cloud-config\n" + string(raw), err
}

func PANOSNetworkCommands(m lc.PlannedMachine) []string {
	var out []string
	seen := map[string]bool{}
	routeID := 0
	for j, nic := range m.NICs {
		if nic.Management {
			continue
		}
		iface := fmt.Sprintf("ethernet1/%d", j)
		out = append(out, "set network interface ethernet "+iface+" layer3 ip "+nic.Address+"/"+strings.Split(nic.CIDR, "/")[1], "set network virtual-router labcentral interface "+iface)
		routes := append([]lc.PlannedRoute(nil), nic.Routes...)
		if nic.Default {
			routes = append(routes, lc.PlannedRoute{CIDR: "0.0.0.0/0", Via: nic.Gateway})
		}
		for _, r := range routes {
			if seen[r.CIDR] {
				continue
			}
			seen[r.CIDR] = true
			prefix := fmt.Sprintf("set network virtual-router labcentral routing-table ip static-route lc-route-%d ", routeID)
			routeID++
			out = append(out, prefix+"destination "+r.CIDR, prefix+"interface "+iface, prefix+"nexthop ip-address "+r.Via)
		}
	}
	return out
}
