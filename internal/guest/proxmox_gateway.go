package guest

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"strings"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"go.yaml.in/yaml/v3"
)

// ProxmoxGatewayExec installs the shared gateway policy through the guest agent.
// The management NIC is the only uplink; no author program is involved.
func ProxmoxGatewayExec(m lc.PlannedMachine, e lc.Environment, networks []lc.PlannedNetwork, macs ...string) (string, error) {
	return ProxmoxGatewayExecOnNetwork(m, e, networks, "", macs...)
}
func ProxmoxGatewayExecOnNetwork(m lc.PlannedMachine, e lc.Environment, networks []lc.PlannedNetwork, managementCIDR string, macs ...string) (string, error) {
	m.NICs = nil // CloudInit's NIC bootstrap is specific to EC2.
	operatorCIDRs := make([]string, 0, len(e.OperatorCIDRs))
	for _, cidr := range e.OperatorCIDRs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil || !prefix.Addr().Is4() || prefix.Bits() < 24 {
			return "", fmt.Errorf("invalid operator CIDR")
		}
		operatorCIDRs = append(operatorCIDRs, prefix.Masked().String())
	}
	e.OperatorCIDRs = operatorCIDRs
	all, egress := []string{}, []string{}
	for _, n := range networks {
		all = append(all, n.CIDR)
		if n.Egress == "internet" {
			if !e.Proxmox.AllowEgress {
				return "", fmt.Errorf("gateway egress is disabled")
			}
			egress = append(egress, n.CIDR)
		}
	}
	raw, err := GatewayCloudInit(m, egress, all...)
	if err != nil {
		return "", err
	}
	var c struct {
		Files    []writeFile `yaml:"write_files"`
		Commands []string    `yaml:"runcmd"`
	}
	if err = yaml.Unmarshal([]byte(raw), &c); err != nil {
		return "", err
	}
	script := "set -eu; command -v nft >/dev/null; " + ProxmoxLoginPolicy
	write := func(path, content, mode string) {
		script += "printf '%s' '" + base64.StdEncoding.EncodeToString([]byte(content)) + "' | base64 -d > " + path + "; chmod " + mode + " " + path + "; "
	}
	write("/etc/ssh/ssh_host_ed25519_key", m.HostPrivateKey, "0600")
	write("/etc/ssh/ssh_host_ed25519_key.pub", m.HostPublicKey, "0644")
	for _, f := range c.Files {
		if f.Path == "/etc/labcentral-gateway.nft" {
			// Lab input is dropped before ICMP; SSH is admitted only from operator CIDRs.
			f.Content = strings.Replace(f.Content, "   icmp type echo-request accept\n", "", 1)
			ssh := ""
			for _, cidr := range e.OperatorCIDRs {
				ssh += "   iifname \"ens18\" ip saddr " + cidr + " tcp dport 22 accept\n"
			}
			f.Content = strings.Replace(f.Content, "   tcp dport 22 accept\n", ssh, 1)
			checks := ""
			for j, n := range networks {
				iface := fmt.Sprintf("ens%d", 19+j)
				checks += "   iifname \"" + iface + "\" ip saddr != " + n.CIDR + " drop\n"
				f.Content = strings.ReplaceAll(f.Content, "ip saddr "+n.CIDR+" oifname", "iifname \""+iface+"\" ip saddr "+n.CIDR+" oifname")
			}
			blocked := append([]string{}, e.OperatorCIDRs...)
			if managementCIDR != "" {
				blocked = append(blocked, managementCIDR)
			}
			for _, cidr := range blocked {
				prefix, err := netip.ParsePrefix(cidr)
				if err != nil || !prefix.Addr().Is4() || prefix != prefix.Masked() {
					return "", fmt.Errorf("invalid management CIDR")
				}
				checks += "   ip daddr " + cidr + " drop\n"
			}
			f.Content = strings.Replace(f.Content, "   fib daddr type local drop\n", "   fib daddr type local drop\n"+checks, 1)

		}
		write(f.Path, f.Content, f.Permissions)
	}
	script += ProxmoxHostnameAlias
	ethernet := map[string]any{}
	wan := map[string]any{"match": map[string]string{"name": "ens18"}, "dhcp4": true, "dhcp6": false, "accept-ra": false}
	if a := e.Proxmox.ManagementAddresses; a != nil {
		if m.Address == "" {
			return "", fmt.Errorf("static management address is missing")
		}
		wan["dhcp4"] = false
		wan["addresses"] = []string{m.Address + "/" + strings.Split(a.CIDR, "/")[1]}
		wan["routes"] = []map[string]string{{"to": "default", "via": a.Gateway}}
		wan["nameservers"] = map[string]any{"addresses": a.DNS}
	}
	if len(macs) > 0 {
		if len(macs) != len(networks)+1 {
			return "", fmt.Errorf("missing gateway NIC identities")
		}
		for _, mac := range macs {
			if _, err := net.ParseMAC(mac); err != nil {
				return "", err
			}
		}
		wan["match"] = map[string]string{"macaddress": macs[0]}
		wan["set-name"] = "ens18"
	}
	ethernet["wan"] = wan
	for j, n := range networks {
		ethernet[fmt.Sprintf("lab%d", j)] = map[string]any{"match": map[string]string{"name": fmt.Sprintf("ens%d", 19+j)}, "dhcp4": false, "dhcp6": false, "accept-ra": false, "addresses": []string{n.Gateway + "/24"}}
		if len(macs) > 0 {
			nic := ethernet[fmt.Sprintf("lab%d", j)].(map[string]any)
			nic["match"] = map[string]string{"macaddress": macs[j+1]}
			nic["set-name"] = fmt.Sprintf("ens%d", 19+j)
		}

	}
	rawNet, err := yaml.Marshal(map[string]any{"network": map[string]any{"version": 2, "ethernets": ethernet}})
	if err != nil {
		return "", err
	}
	write("/etc/cloud/cloud.cfg.d/99-labcentral-network.cfg", "network: {config: disabled}\n", "0644")
	write("/etc/netplan/99-labcentral.yaml", string(rawNet), "0600")
	// Close forwarding before changing any interface or policy, including recovery.
	script += "sysctl -w net.ipv4.ip_forward=0; rm -f /etc/netplan/50-cloud-init.yaml; netplan generate; netplan apply; "
	for _, command := range c.Commands {
		script += command + "; "
		if command == "systemctl enable --now labcentral-gateway.service" {
			script += "systemctl restart labcentral-gateway.service; "
		}
	}
	script += "systemctl is-active labcentral-gateway.service >/dev/null"
	return script, nil
}

// ProxmoxLoginPolicy prevents a private guest's SSH login from waiting for
// Ubuntu's Internet-backed release/news notices. It leaves package policy alone.
const ProxmoxLoginPolicy = `if test -f /etc/update-motd.d/91-release-upgrade; then chmod a-x /etc/update-motd.d/91-release-upgrade; fi; if test -f /etc/default/motd-news; then printf 'ENABLED=0\n' > /etc/default/motd-news; fi; `

// Proxmox cloud-init uses the native VM name as hostname. Keep it locally
// resolvable alongside portable lab names, including on networks without DNS.
const ProxmoxHostnameAlias = `printf '127.0.1.1 %s\n' "$(hostname)" >> /etc/hosts; `
