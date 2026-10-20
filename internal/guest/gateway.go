package guest

import (
	"fmt"
	"net/netip"
	"strings"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"go.yaml.in/yaml/v3"
)

// GatewayCloudInit contains no author programs. Forwarding is enabled only after
// the firewall loads successfully, on first boot and subsequent boots.
func GatewayCloudInit(m lc.PlannedMachine, egressCIDRs []string, labCIDRs ...string) (string, error) {
	raw, err := CloudInit(m)
	if err != nil {
		return "", err
	}
	var c map[string]any
	if err = yaml.Unmarshal([]byte(strings.TrimPrefix(raw, "#cloud-config\n")), &c); err != nil {
		return "", err
	}
	for _, cidr := range append(append([]string{}, egressCIDRs...), labCIDRs...) {
		p, err := netip.ParsePrefix(cidr)
		if err != nil || !p.Addr().Is4() || p != p.Masked() || !p.Addr().IsPrivate() {
			return "", fmt.Errorf("invalid gateway egress CIDR")
		}
	}
	rules := `flush ruleset
 table inet labcentral {
  set private4 { type ipv4_addr; flags interval; elements = { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 100.64.0.0/10 } }
  chain input { type filter hook input priority filter; policy drop;
   iifname "lo" accept
   ct state established,related accept
   udp sport 67 udp dport 68 accept
   icmp type echo-request accept
__LAB_INPUT__
   tcp dport 22 accept
  }
  chain forward { type filter hook forward priority filter; policy drop;
   fib daddr type local drop
   ct state established,related accept
   ip daddr @private4 drop
`
	if len(labCIDRs) == 0 {
		labCIDRs = []string{"10.77.0.0/24"}
	}
	drops := ""
	for _, cidr := range labCIDRs {
		drops += "   ip saddr " + cidr + " drop\n"
	}
	rules = strings.Replace(rules, "__LAB_INPUT__", strings.TrimSuffix(drops, "\n"), 1)
	for _, cidr := range egressCIDRs {
		rules += "   ip saddr " + cidr + " oifname \"__WAN__\" accept\n"
	}
	rules += "  }\n }\n table ip labcentral_nat {\n  chain postrouting { type nat hook postrouting priority srcnat; policy accept;\n"
	for _, cidr := range egressCIDRs {
		rules += "   ip saddr " + cidr + " oifname \"__WAN__\" masquerade\n"
	}
	rules += "  }\n }\n"
	files := c["write_files"].([]any)
	for _, f := range []writeFile{
		{"/etc/ssh/sshd_config.d/00-labcentral-gateway.conf", "AllowUsers lab\nAllowTcpForwarding local\nAllowAgentForwarding no\nPermitTunnel no\nX11Forwarding no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPermitRootLogin no\n", "0644"},
		{"/etc/labcentral-gateway.nft", rules, "0600"},
		{"/usr/local/sbin/labcentral-gateway", `#!/bin/sh
set -eu
wan=$(ip -4 route show default | awk 'NR == 1 {print $5}')
case "$wan" in ''|*[!a-zA-Z0-9_.:-]*) exit 1;; esac
sed "s/__WAN__/$wan/g" /etc/labcentral-gateway.nft > /run/labcentral-gateway.nft
nft -f /run/labcentral-gateway.nft
sysctl -w net.ipv4.ip_forward=1
`, "0700"},
		{"/etc/systemd/system/labcentral-gateway.service", "[Unit]\nAfter=network-online.target\nWants=network-online.target\n[Service]\nType=oneshot\nExecStart=/usr/local/sbin/labcentral-gateway\nRemainAfterExit=yes\n[Install]\nWantedBy=multi-user.target\n", "0644"},
	} {
		files = append(files, map[string]any{"path": f.Path, "content": f.Content, "permissions": f.Permissions})
	}
	c["write_files"] = files
	// Socket activation may not have started ssh.service yet. Its runtime
	// directory must exist before sshd can validate the pinned configuration.
	c["runcmd"] = []string{"systemctl daemon-reload", "systemctl enable --now labcentral-gateway.service", "install -d -m 0755 /run/sshd && sshd -t && systemctl restart ssh"}
	out, err := yaml.Marshal(c)
	return "#cloud-config\n" + string(out), err
}
