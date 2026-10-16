package guest

import (
	"encoding/base64"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"go.yaml.in/yaml/v3"
	"net"
	"strings"
)

// Netplan renders only platform-owned addressing and routing. AWS substitutes
// MACs obtained by device index from IMDS; Proxmox uses its stable PCI NIC names.
func Netplan(m lc.PlannedMachine, platform string, macs ...string) (string, error) {
	ethernets := map[string]any{}
	for j, nic := range m.NICs {
		iface := map[string]any{"dhcp4": false, "dhcp6": false, "accept-ra": false, "addresses": []string{nic.Address + "/" + strings.Split(nic.CIDR, "/")[1]}}
		if platform == "aws" {
			iface["match"] = map[string]string{"macaddress": fmt.Sprintf("MAC_%d", j)}
		} else {
			iface["match"] = map[string]string{"name": fmt.Sprintf("ens%d", 18+j)}
			if len(macs) > 0 {
				if len(macs) != len(m.NICs) {
					return "", fmt.Errorf("missing Proxmox NIC identities")
				}
				if _, err := net.ParseMAC(macs[j]); err != nil {
					return "", err
				}
				iface["match"] = map[string]string{"macaddress": macs[j]}
				iface["set-name"] = fmt.Sprintf("ens%d", 18+j)
			}
		}
		routes := []map[string]any{}
		if nic.Default && nic.Gateway != "" {
			routes = append(routes, map[string]any{"to": "0.0.0.0/0", "via": nic.Gateway})
		}
		for _, r := range nic.Routes {
			routes = append(routes, map[string]any{"to": r.CIDR, "via": r.Via})
		}
		if platform == "aws" && j == 0 {
			routes = append(routes, map[string]any{"to": "169.254.169.254/32", "scope": "link"})
			if m.Resolver != "" {
				iface["nameservers"] = map[string]any{"addresses": []string{m.Resolver}}
			}
		}
		if platform == "proxmox" && nic.Default {
			// LAN DNS is deliberately unreachable from a lab; egress uses public DNS.
			iface["nameservers"] = map[string]any{"addresses": []string{"1.1.1.1", "9.9.9.9"}}
		}
		if len(routes) > 0 {
			iface["routes"] = routes
		}
		ethernets[fmt.Sprintf("nic%d", j)] = iface
	}
	raw, err := yaml.Marshal(map[string]any{"network": map[string]any{"version": 2, "ethernets": ethernets}})
	return string(raw), err
}

const AWSNetworkScript = `#!/usr/bin/python3
import pathlib, urllib.request
base = 'http://169.254.169.254/latest/'
token = urllib.request.urlopen(urllib.request.Request(base+'api/token', data=b'', headers={'X-aws-ec2-metadata-token-ttl-seconds':'60'}, method='PUT'), timeout=10).read().decode()
def metadata(path):
    return urllib.request.urlopen(urllib.request.Request(base+'meta-data/'+path, headers={'X-aws-ec2-metadata-token':token}), timeout=10).read().decode()
text = pathlib.Path('/etc/labcentral-network.yaml').read_text()
for mac in metadata('network/interfaces/macs/').splitlines():
    index = int(metadata('network/interfaces/macs/'+mac+'device-number'))
    text = text.replace('MAC_'+str(index), mac.rstrip('/'))
if 'MAC_' in text:
    raise RuntimeError('missing planned EC2 NIC')
path = pathlib.Path('/etc/netplan/99-labcentral.yaml')
path.write_text(text)
path.chmod(0o600)
`

// NetworkExec writes, applies, and checks the runtime network through the guest
// agent. Any failed address/default/static-route assertion leaves readiness false.
func NetworkExec(m lc.PlannedMachine, macs ...string) (string, error) {
	raw, err := Netplan(m, "proxmox", macs...)
	if err != nil {
		return "", err
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(raw))
	script := "umask 077; mkdir -p /etc/netplan; printf '%s' '" + encoded + "' | base64 -d > /run/labcentral-network.yaml; " + preserveProxmoxDNS(m) + "printf 'network: {config: disabled}\\n' > /etc/cloud/cloud.cfg.d/99-labcentral-network.cfg; rm -f /etc/netplan/50-cloud-init.yaml; "
	if m.Spec.Forwarding {
		script += "printf 'net.ipv4.ip_forward=1\\n' > /etc/sysctl.d/90-labcentral-router.conf; sysctl -p /etc/sysctl.d/90-labcentral-router.conf; "
	} else {
		script += "printf 'net.ipv4.ip_forward=0\\n' > /etc/sysctl.d/90-labcentral-router.conf; sysctl -p /etc/sysctl.d/90-labcentral-router.conf; "
	}
	script += "netplan generate; netplan apply; "
	for j, nic := range m.NICs {
		iface := fmt.Sprintf("ens%d", 18+j)
		script += "ip -4 -o addr show dev " + iface + " | grep -F ' " + nic.Address + "/" + strings.Split(nic.CIDR, "/")[1] + " '; "
		for _, r := range nic.Routes {
			script += "ip -4 route show " + r.CIDR + " | grep -F 'via " + r.Via + " dev " + iface + "'; "
		}
		if nic.Default {
			script += "ip -4 route show default | grep -F 'via " + nic.Gateway + " dev " + iface + "'; "
		}
	}
	script += "test $(ip -4 route show default | wc -l) -eq "
	defaults := 0
	for _, nic := range m.NICs {
		if nic.Default {
			defaults++
		}
	}
	script += fmt.Sprintf("%d", defaults)
	return script, nil
}

// Keep operator/base DNS through first boot and recovery while replacing runtime
// addresses/routes. DNS follows the selected default NIC, or management without one.
func preserveProxmoxDNS(m lc.PlannedMachine) string {
	index := 0
	for j, nic := range m.NICs {
		if nic.Default {
			index = j
			break
		}
	}
	return fmt.Sprintf(`python3 - <<'LC_NETWORK_DNS'
import pathlib, yaml
new = pathlib.Path('/run/labcentral-network.yaml')
config = yaml.safe_load(new.read_text())
for name in ('99-labcentral.yaml', '50-cloud-init.yaml'):
    old = pathlib.Path('/etc/netplan') / name
    if not old.exists():
        continue
    previous = yaml.safe_load(old.read_text()) or {}
    dns = next((nic.get('nameservers') for nic in previous.get('network', {}).get('ethernets', {}).values() if nic.get('nameservers')), None)
    if dns and not config['network']['ethernets']['nic%d'].get('nameservers'):
        config['network']['ethernets']['nic%d']['nameservers'] = dns
        break
path = pathlib.Path('/etc/netplan/99-labcentral.yaml')
path.write_text(yaml.safe_dump(config))
path.chmod(0o600)
LC_NETWORK_DNS
`, index, index)
}
