package proxmox

import (
	"context"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"time"
)

type firewallRule struct {
	Type   string `json:"type"`
	Action string `json:"action"`
	Enable int    `json:"enable"`
	Source string `json:"source,omitempty"`
	Dest   string `json:"dest,omitempty"`
	Proto  string `json:"proto,omitempty"`
	Dport  string `json:"dport,omitempty"`
}

func firewallRules(e lc.Environment, cidr string, internet bool, management ...string) []firewallRule {
	desired := []firewallRule{{Type: "in", Action: "ACCEPT", Enable: 1, Source: cidr}, {Type: "out", Action: "ACCEPT", Enable: 1, Dest: cidr}}
	for _, cidr := range e.OperatorCIDRs {
		desired = append(desired, firewallRule{Type: "in", Action: "ACCEPT", Enable: 1, Source: cidr, Proto: "tcp", Dport: "22"})
	}
	if !internet {
		return desired
	}
	blocked := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "100.64.0.0/10", "::/0"}
	blocked = append(blocked, e.OperatorCIDRs...)
	blocked = append(blocked, management...)
	if e.Proxmox.CIDR != "" {
		blocked = append(blocked, e.Proxmox.CIDR)
	}
	if u, err := url.Parse(e.Proxmox.URL); err == nil {
		if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
			blocked = append(blocked, netip.PrefixFrom(ip, ip.BitLen()).String())
		}
	}
	for _, dest := range blocked {
		desired = append(desired, firewallRule{Type: "out", Action: "DROP", Enable: 1, Dest: dest})
	}
	return append(desired, firewallRule{Type: "out", Action: "ACCEPT", Enable: 1, Dest: "0.0.0.0/0"})
}

func installFirewallRules(ctx context.Context, c *Client, vmid int, rules []firewallRule) error {
	// Proxmox POST prepends each rule. Reverse insertion preserves deny-before-
	// Internet-accept ordering in the effective ruleset. Keep the general accept
	// disabled until every preceding drop is installed, including during recovery.
	for n := len(rules) - 1; n >= 0; n-- {
		rule := rules[n]
		if rule.Type == "out" && rule.Action == "ACCEPT" && rule.Dest == "0.0.0.0/0" {
			rule.Enable = 0
		}
		if err := c.Call(ctx, "POST", c.VM(vmid, "/firewall/rules"), rule, nil); err != nil {
			return err
		}
	}
	for n, rule := range rules {
		if rule.Type == "out" && rule.Action == "ACCEPT" && rule.Dest == "0.0.0.0/0" {
			if err := c.Call(ctx, "PUT", c.VM(vmid, "/firewall/rules/"+strconv.Itoa(n)), map[string]int{"enable": 1}, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// Resolve management hostnames before changing the VM firewall. A failed lookup
// is unknown, so it cannot silently widen the Internet exception.
func managementCIDRs(ctx context.Context, e lc.Environment) ([]string, error) {
	u, err := url.Parse(e.Proxmox.URL)
	if err != nil {
		return nil, err
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		return []string{netip.PrefixFrom(ip, ip.BitLen()).String()}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", u.Hostname())
	if err != nil || len(addresses) == 0 {
		return nil, fmt.Errorf("resolve Proxmox management addresses: %w", err)
	}
	out := []string{}
	for _, ip := range addresses {
		out = append(out, netip.PrefixFrom(ip, ip.BitLen()).String())
	}
	return out, nil
}
