package labcentral

import (
	"fmt"
	"io/fs"
	"net/netip"
	"strings"
)

func (i Image) ApplianceKind() string {
	if i.Appliance == nil {
		return ""
	}
	return i.Appliance.Kind
}
func HasAppliances(l Lab) bool {
	for _, image := range l.Images {
		if image.Appliance != nil {
			return true
		}
	}
	return false
}

// ApplianceCommands accepts configuration only. Authentication, host identity,
// shell commands and host-side execution are deliberately outside this contract.
func ApplianceCommands(kind string, raw []byte) ([]string, error) {
	if kind != "vyos" && kind != "panos" {
		return nil, fmt.Errorf("unknown native appliance")
	}
	if len(raw) > 12<<10 {
		return nil, fmt.Errorf("appliance configuration exceeds 12 KiB")
	}
	prefixes := []string{"firewall ", "nat ", "policy ", "zone-policy ", "interfaces ethernet "}
	if kind == "panos" {
		prefixes = []string{"zone ", "rulebase ", "address ", "address-group ", "service ", "service-group ", "tag "}
	}
	var commands []string
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		control := strings.ContainsFunc(line, func(r rune) bool { return r < 32 || r == 127 })
		if len(commands) >= 128 || len(line) > 1024 || control || strings.ContainsAny(line, ";&|`$\\<>") || strings.Count(line, "'")%2 != 0 || strings.Count(line, `"`)%2 != 0 {
			return nil, fmt.Errorf("invalid or excessive appliance configuration")
		}
		body, ok := strings.CutPrefix(line, "set ")
		if !ok {
			body, ok = strings.CutPrefix(line, "delete ")
		}
		if !ok || strings.Contains(strings.ToLower(line), "labcentral") {
			return nil, fmt.Errorf("only vendor set/delete configuration outside the reserved namespace is allowed")
		}
		allowed := false
		for _, prefix := range prefixes {
			allowed = allowed || strings.HasPrefix(body, prefix)
		}
		if kind == "vyos" && strings.HasPrefix(body, "interfaces ") && !strings.Contains(body, " firewall ") {
			allowed = false
		}
		if !allowed {
			return nil, fmt.Errorf("configuration must contain network, firewall or NAT policy; management and identity are platform owned")
		}
		commands = append(commands, line)
	}
	return commands, nil
}

func ValidateAppliances(s Source) error {
	l := s.Lab
	for _, name := range sortedKeys(l.Images) {
		i := l.Images[name]
		if i.Appliance == nil {
			continue
		}
		field := "images." + name + ".appliance"
		if !Contains([]string{"vyos", "panos"}, i.Appliance.Kind) {
			return problem(Invalid, field+".kind", "expected vyos or panos")
		}
		if OSFamily(i.OS) != "linux" || i.Build != nil {
			return problem(Invalid, field, "appliances require a native Linux-classified AMI base and cannot use image builds")
		}
		if path := i.Appliance.Configuration; path != "" {
			if !fs.ValidPath(path) {
				return problem(Invalid, field+".configuration", "expected a source file")
			}
			found := false
			for _, f := range s.Files {
				if f.Path == path {
					found = true
					if _, err := ApplianceCommands(i.Appliance.Kind, f.Content); err != nil {
						return problem(Invalid, field+".configuration", err.Error())
					}
				}
			}
			if !found {
				return problem(Invalid, field+".configuration", "configuration file is missing from source")
			}
		}
	}
	for _, name := range sortedKeys(l.Machines) {
		m := l.Machines[name]
		kind := l.Images[m.Image].ApplianceKind()
		field := "machines." + name
		management, data := 0, 0
		for j, nic := range m.NICs {
			if nic.Management {
				management++
				if kind == "" || j != 0 {
					return problem(Invalid, field+".nics", "only an appliance's first NIC may be management-only")
				}
			} else {
				data++
			}
		}
		if kind == "" {
			continue
		}
		if !m.Forwarding || data < 2 || management > 1 {
			return problem(Invalid, field, "appliances require forwarding and at least two data NICs")
		}
		if kind == "panos" && management != 1 {
			return problem(Invalid, field+".nics", "PAN-OS requires a separate first management NIC")
		}
		if kind == "panos" {
			memory, _ := SizeMiB(m.Memory)
			if m.CPU < 2 || memory < 8192 {
				return problem(Invalid, field, "PAN-OS requires at least two vCPUs and 8 GiB; the selected AMI/license may require more")
			}
		}
		for _, access := range m.Access {
			if access != "ssh" {
				return problem(Invalid, field+".access", "appliances expose SSH terminals; use a lab workstation browser for their HTTPS management UI")
			}
		}
	}
	for _, name := range sortedKeys(l.Networks) {
		n := l.Networks[name]
		seen := map[string]bool{}
		if len(n.Routes) > 16 {
			return problem(Invalid, "networks."+name+".routes", "at most 16 explicit routes per network")
		}
		checkRouter := func(router string) error {
			m, ok := l.Machines[router]
			if !ok || !m.Forwarding {
				return problem(Invalid, "networks."+name, "route requires a declared forwarding machine")
			}
			for _, nic := range m.NICs {
				if nic.Network == name && !nic.Management {
					return nil
				}
			}
			return problem(Invalid, "networks."+name, "route must target a data NIC on this network")
		}
		if n.DefaultRouter != "" {
			if n.Egress == "internet" {
				return problem(Invalid, "networks."+name, "defaultRouter and direct internet egress are mutually exclusive")
			}
			if err := checkRouter(n.DefaultRouter); err != nil {
				return err
			}
		}
		for _, route := range n.Routes {
			p, err := netip.ParsePrefix(route.CIDR)
			if err != nil || !p.Addr().Is4() || !p.Addr().IsPrivate() || p != p.Masked() || p.Bits() < 16 || seen[route.CIDR] {
				return problem(Invalid, "networks."+name+".routes", "expected unique canonical private IPv4 /16 through /32 routes")
			}
			seen[route.CIDR] = true
			for _, other := range l.Networks {
				if other.CIDR != "" {
					q, err := netip.ParsePrefix(other.CIDR)
					if err == nil && p.Overlaps(q) {
						return problem(Invalid, "networks."+name+".routes", "explicit routes must not overlap managed subnets")
					}
				}
			}
			if err := checkRouter(route.Router); err != nil {
				return err
			}
		}
	}
	return nil
}
