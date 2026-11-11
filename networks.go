package labcentral

import (
	"fmt"
	"net/netip"
)

func effectiveNetworks(l Lab) map[string]Network {
	if len(l.Networks) == 0 {
		return map[string]Network{"lab": {}}
	}
	return l.Networks
}
func machineNICs(m Machine, names []string) []NIC {
	if len(m.NICs) == 0 {
		return []NIC{{Network: names[0]}}
	}
	return m.NICs
}
func offsetAddress(p netip.Prefix, offset int) string {
	a := p.Addr().As4()
	n := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	n += uint32(offset)
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}).String()
}

// AWSLayout validates before mutation. Each distinct /16 is a VPC CIDR block.
// The legacy default and single-block layouts remain unchanged.
func AWSLayout(l Lab) (networks []PlannedNetwork, vpc, edge string, err error) {
	vpc = "10.77.0.0/16"
	edge = "10.77.255.0/28"
	author := false
	for _, name := range sortedKeys(effectiveNetworks(l)) {
		if cidr := l.Networks[name].CIDR; cidr != "" {
			p, e := netip.ParsePrefix(cidr)
			if e != nil || !p.Addr().Is4() || !p.Addr().IsPrivate() || p.Bits() < 16 || p.Bits() > 28 || p != p.Masked() {
				return nil, "", "", problem(Invalid, "networks."+name+".cidr", "expected a canonical private IPv4 /16–/28")
			}
			if !author {
				vpc = netip.PrefixFrom(p.Addr(), 16).Masked().String()
				author = true
			}
		}
	}
	for i, name := range sortedKeys(effectiveNetworks(l)) {
		n := effectiveNetworks(l)[name]
		cidr := n.CIDR
		if cidr == "" {
			cidr = fmt.Sprintf("10.77.%d.0/24", i)
		}
		p, e := netip.ParsePrefix(cidr)
		if e != nil || p.Bits() < 16 {
			return nil, "", "", problem(Invalid, "networks."+name+".cidr", "invalid subnet CIDR")
		}
		for _, other := range networks {
			q, _ := netip.ParsePrefix(other.CIDR)
			if p.Overlaps(q) {
				return nil, "", "", problem(Invalid, "networks."+name+".cidr", "network CIDRs overlap")
			}
		}
		egress := n.Egress
		if egress == "" {
			egress = "none"
		}
		networks = append(networks, PlannedNetwork{Name: name, CIDR: cidr, Gateway: offsetAddress(p, 1), Egress: egress})
	}
	// AWS does not permit adding 172.31/16 or 10.0/16 as secondary blocks
	// to an existing VPC in their private range. Use them as primary if needed.
	for _, n := range networks {
		p := netip.MustParsePrefix(n.CIDR)
		b := netip.PrefixFrom(p.Addr(), 16).Masked().String()
		if b == "172.31.0.0/16" || b == "10.0.0.0/16" {
			vpc = b
		}
	}
	blocks := AWSVPCCIDRs(networks, vpc)
	if len(blocks) > 5 {
		return nil, "", "", problem(Unsupported, "networks", "at most five VPC IPv4 CIDR blocks are supported")
	}
	privateRange := func(cidr string) byte {
		return netip.MustParsePrefix(cidr).Addr().As4()[0]
	}
	for _, n := range networks {
		if privateRange(n.CIDR) != privateRange(vpc) {
			return nil, "", "", problem(Unsupported, "networks."+n.Name+".cidr", "AWS VPC CIDR blocks must use the same RFC1918 range; set every subnet CIDR explicitly when using 172.16/12 or 192.168/16")
		}
	}
	block, _ := netip.ParsePrefix(vpc)
	if author {
		edge = ""
		for off := 65520; off >= 0; off -= 16 {
			candidate := netip.PrefixFrom(netip.MustParseAddr(offsetAddress(block, off)), 28)
			free := true
			for _, n := range networks {
				if candidate.Overlaps(netip.MustParsePrefix(n.CIDR)) {
					free = false
					break
				}
			}
			if free {
				edge = candidate.String()
				break
			}
		}
		if edge == "" {
			return nil, "", "", problem(Unsupported, "networks", "no unused /28 for the edge subnet in the VPC primary /16")
		}
	}
	return
}

// AWSVPCCIDRs returns the primary block followed by distinct secondary /16s in
// network order. Callers supply the validated layout returned by AWSLayout.
func AWSVPCCIDRs(networks []PlannedNetwork, primary string) []string {
	blocks := []string{primary}
	for _, n := range networks {
		p := netip.MustParsePrefix(n.CIDR)
		b := netip.PrefixFrom(p.Addr(), 16).Masked().String()
		if !Contains(blocks, b) {
			blocks = append(blocks, b)
		}
	}
	return blocks
}

// PlanNetworks is deterministic. Proxmox previews the range start; deployment
// supplies atomically reserved VLANs and recomputes the same topology and offsets.
func PlanNetworks(p *Plan, vlans []int) error {
	l, e := p.Source.Lab, p.Environment
	names := sortedKeys(effectiveNetworks(l))
	p.Networks = nil
	p.SecondaryVPCCIDRs = nil
	if e.Platform == "aws" {
		var err error
		p.Networks, p.VPCCIDR, p.EdgeCIDR, err = AWSLayout(l)
		if err != nil {
			return err
		}
		blocks := AWSVPCCIDRs(p.Networks, p.VPCCIDR)
		if len(blocks) > 1 {
			p.SecondaryVPCCIDRs = blocks[1:]
		}
	} else {
		if e.Proxmox.VLANEnd-e.Proxmox.VLANStart+1 < len(names) {
			return problem(Unsupported, "environment.proxmox.vlanEnd", "VLAN range cannot reserve all lab networks")
		}
		for j, name := range names {
			vlan := e.Proxmox.VLANStart + j
			if len(vlans) > 0 {
				if j >= len(vlans) {
					return problem(Invalid, "networks", "incomplete VLAN reservation")
				}
				vlan = vlans[j]
			}
			cidr, gw, err := ProxmoxSubnet(e.Proxmox, vlan)
			if err != nil {
				return err
			}
			eg := effectiveNetworks(l)[name].Egress
			if eg == "" {
				eg = "none"
			}
			p.Networks = append(p.Networks, PlannedNetwork{Name: name, CIDR: cidr, Gateway: gw, VLAN: vlan, Egress: eg})
		}
	}
	indexes := map[string]int{}
	used := map[string]map[int]bool{}
	next := map[string]int{}
	for j, n := range p.Networks {
		indexes[n.Name] = j
		used[n.Name] = map[int]bool{}
		next[n.Name] = 11
		if e.Platform == "proxmox" && n.Gateway != "" {
			used[n.Name][int(netip.MustParseAddr(n.Gateway).As4()[3])] = true
		}
	}
	for _, m := range p.Machines {
		for j, nic := range machineNICs(m.Spec, names) {
			if used[nic.Network] == nil {
				return problem(Invalid, fmtPathIndex("machines."+m.Name+".nics", j)+".network", "unknown network")
			}
			if nic.Address != nil {
				used[nic.Network][*nic.Address] = true
			}
		}
	}
	for j := range p.Machines {
		m := &p.Machines[j]
		m.NICs = nil
		hasDefault := false
		for k, nic := range machineNICs(m.Spec, names) {
			n := p.Networks[indexes[nic.Network]]
			prefix := netip.MustParsePrefix(n.CIDR)
			offset := 0
			if nic.Address != nil {
				offset = *nic.Address
			} else {
				offset = next[nic.Network]
				for used[nic.Network][offset] {
					offset++
				}
				next[nic.Network] = offset + 1
				used[nic.Network][offset] = true
			}
			max := 254
			if usable := (1 << (32 - prefix.Bits())) - 2; usable < max {
				max = usable
			}
			if offset > max {
				return problem(Invalid, fmtPathIndex("machines."+m.Name+".nics", k)+".address", "network has no usable address for this NIC")
			}
			def := !hasDefault && (n.Egress == "internet" || (l.Networks[n.Name].DefaultRouter != "" && l.Networks[n.Name].DefaultRouter != m.Name))
			hasDefault = hasDefault || def
			m.NICs = append(m.NICs, PlannedNIC{Network: n.Name, CIDR: n.CIDR, Address: offsetAddress(prefix, offset), VLAN: n.VLAN, Gateway: n.Gateway, Default: def, Management: nic.Management})
		}
		m.Address = m.NICs[0].Address
	}
	// A bipartite forest allows a router with three NICs without treating its
	// internal connections as a cycle. Two routers sharing two segments is ambiguous.
	adj := map[string][]string{}
	pair := map[string]string{}
	for _, m := range p.Machines {
		if !m.Spec.Forwarding {
			continue
		}
		for a, nic := range m.NICs {
			if nic.Management {
				continue
			}
			for _, other := range m.NICs[a+1:] {
				if other.Management {
					continue
				}
				x, y := nic.Network, other.Network
				if x > y {
					x, y = y, x
				}
				key := x + "/" + y
				if pair[key] != "" {
					return problem(Invalid, "machines."+m.Name+".forwarding", "ambiguous routers joining networks "+x+" and "+y)
				}
				pair[key] = m.Name
			}
			router := "r:" + m.Name
			network := "n:" + nic.Network
			adj[router] = append(adj[router], network)
			adj[network] = append(adj[network], router)
		}
	}
	visited := map[string]bool{}
	var cycle func(string, string) bool
	cycle = func(node, parent string) bool {
		if visited[node] {
			return true
		}
		visited[node] = true
		for _, v := range adj[node] {
			if v != parent && cycle(v, node) {
				return true
			}
		}
		return false
	}
	for _, node := range sortedKeys(adj) {
		if !visited[node] && cycle(node, "") {
			return problem(Invalid, "networks", "router topology contains a cycle")
		}
	}
	machines := map[string]PlannedMachine{}
	for _, m := range p.Machines {
		machines[m.Name] = m
	}
	for j := range p.Networks {
		n := &p.Networks[j]
		var walk func(string, string, string)
		walk = func(node, parent, first string) {
			for _, v := range adj[node] {
				if v == parent {
					continue
				}
				hop := first
				if hop == "" {
					hop = v[2:]
				}
				if v[:2] == "n:" {
					dst := p.Networks[indexes[v[2:]]]
					router := machines[hop]
					for k, nic := range router.NICs {
						if nic.Network == n.Name {
							n.Routes = append(n.Routes, PlannedRoute{Network: dst.Name, CIDR: dst.CIDR, Via: nic.Address, Router: hop, RouterNIC: k})
							break
						}
					}
				}
				walk(v, node, hop)
			}
		}
		walk("n:"+n.Name, "", "")
		// Sort destinations independently of traversal/router names.
		ordered := []PlannedRoute{}
		for _, name := range names {
			for _, r := range n.Routes {
				if r.Network == name {
					ordered = append(ordered, r)
				}
			}
		}
		n.Routes = ordered
		requests := append([]NetworkRoute(nil), l.Networks[n.Name].Routes...)
		if router := l.Networks[n.Name].DefaultRouter; router != "" {
			requests = append(requests, NetworkRoute{CIDR: "0.0.0.0/0", Router: router})
		}
		for _, requested := range requests {
			if requested.CIDR != "0.0.0.0/0" {
				prefix, err := netip.ParsePrefix(requested.CIDR)
				if err != nil {
					return problem(Invalid, "networks."+n.Name+".routes", "invalid explicit route")
				}
				for _, other := range p.Networks {
					if prefix.Overlaps(netip.MustParsePrefix(other.CIDR)) {
						return problem(Invalid, "networks."+n.Name+".routes", "explicit routes must not overlap allocated subnets")
					}
				}
			}
			router, ok := machines[requested.Router]
			if !ok || !router.Spec.Forwarding {
				return problem(Invalid, "networks."+n.Name+".routes", "unknown forwarding router")
			}
			found := false
			for k, nic := range router.NICs {
				if nic.Network == n.Name && !nic.Management {
					n.Routes = append(n.Routes, PlannedRoute{Network: "route:" + requested.CIDR, CIDR: requested.CIDR, Via: nic.Address, Router: router.Name, RouterNIC: k})
					found = true
					break
				}
			}
			if !found {
				return problem(Invalid, "networks."+n.Name+".routes", "router has no data NIC on network")
			}
		}
	}
	for j := range p.Machines {
		m := &p.Machines[j]
		connected := map[string]bool{}
		for _, nic := range m.NICs {
			connected[nic.Network] = true
		}
		for k := range m.NICs {
			if m.NICs[k].Management {
				continue
			}
			for _, r := range p.Networks[indexes[m.NICs[k].Network]].Routes {
				if r.CIDR == "0.0.0.0/0" {
					if m.NICs[k].Default && r.Router != m.Name {
						m.NICs[k].Gateway = r.Via
					}
					if m.Appliance != "panos" {
						continue
					}
				}
				if r.Router != m.Name && !connected[r.Network] {
					m.NICs[k].Routes = append(m.NICs[k].Routes, r)
					connected[r.Network] = true
				}
			}
		}
		if e.Platform == "aws" {
			block := netip.MustParsePrefix(p.VPCCIDR)
			m.Resolver = offsetAddress(block, 2)
			first := &m.NICs[0]
			managementGateway := p.Networks[indexes[first.Network]].Gateway
			first.Routes = append(first.Routes, PlannedRoute{CIDR: p.EdgeCIDR, Via: managementGateway})
			if !netip.MustParsePrefix(first.CIDR).Contains(netip.MustParseAddr(m.Resolver)) {
				first.Routes = append(first.Routes, PlannedRoute{CIDR: m.Resolver + "/32", Via: managementGateway})
			}
			if m.Appliance == "" {
				for _, peer := range p.Machines {
					if peer.Appliance == "panos" && len(peer.NICs) > 0 {
						mgmt := peer.NICs[0]
						if mgmt.Network != first.Network {
							duplicate := false
							for _, r := range first.Routes {
								duplicate = duplicate || r.CIDR == mgmt.CIDR
							}
							if !duplicate {
								first.Routes = append(first.Routes, PlannedRoute{CIDR: mgmt.CIDR, Via: managementGateway})
							}
						}
					}
				}
			}
		}

		m.Hosts = []HostEntry{}
		for _, peer := range p.Machines {
			if m.Spec.HostnamePeers.Explicit && !Contains(m.Spec.HostnamePeers.Names, peer.Name) {
				continue
			}
			address := peer.Address
			found := false
			for _, nic := range m.NICs {
				for _, pn := range peer.NICs {
					if pn.Network == nic.Network {
						address = pn.Address
						found = true
						break
					}
				}
				if found {
					break
				}
			}
			m.Hosts = append(m.Hosts, HostEntry{Name: peer.Name, Address: address})
		}
	}
	return nil
}

func SortedMachineNames(l Lab) []string { return sortedKeys(l.Machines) }

// CheckAWSNICLimits uses the same instance selection as planning. Zero/absent
// environment limits are resolved by the adapter's DescribeInstanceTypes check.
func CheckAWSNICLimits(l Lab, e Environment, limits map[string]int) error {
	for _, name := range sortedKeys(l.Machines) {
		spec := l.Machines[name]
		selected, err := selectAWSInstanceType(spec, e.AWS, "machines."+name)
		if err != nil {
			return err
		}
		limit := selected.MaxNICs
		if actual, ok := limits[selected.Name]; ok {
			limit = actual
		}
		if limit == 0 {
			continue
		}
		count := len(spec.NICs)
		if count == 0 {
			count = 1
		}
		if count > limit {
			return problem(Unsupported, fmtPathIndex("machines."+name+".nics", limit), "NIC count exceeds instance type ENI limit")
		}
	}
	return nil
}
