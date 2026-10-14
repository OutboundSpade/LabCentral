package labcentral

// Capabilities describes the portable requests implemented by a platform.
type Capabilities struct {
	MaxNetworks, MaxNICs   int
	Egress, OS             []string
	AuthorCIDR, Forwarding bool
}

func PlatformCapabilities(platform string) (Capabilities, bool) {
	if platform != "aws" && platform != "proxmox" {
		return Capabilities{}, false
	}
	return Capabilities{MaxNetworks: 1, MaxNICs: 1, Egress: []string{"none"}, OS: []string{"linux"}}, true
}
func CheckCapabilities(l Lab, platform string) error {
	c, ok := PlatformCapabilities(platform)
	if !ok {
		return problem(Unsupported, "environment.platform", "unknown platform")
	}
	if l.Platforms != nil && !Contains(l.Platforms.Names, platform) {
		return problem(Unsupported, "platforms", "environment platform is not allowed")
	}
	for index, n := range sortedKeys(l.Networks) {
		f := "networks." + n
		v := l.Networks[n]
		if index >= c.MaxNetworks {
			return problem(Unsupported, f, "multiple networks are not implemented")
		}
		if v.CIDR != "" && !c.AuthorCIDR {
			return problem(Unsupported, f+".cidr", "author CIDRs are not implemented")
		}
		if v.Egress != "" && !Contains(c.Egress, v.Egress) {
			return problem(Unsupported, f+".egress", "egress mode is not implemented")
		}
	}
	for _, n := range sortedKeys(l.Images) {
		v := l.Images[n]
		if v.OS != "" && !Contains(c.OS, v.OS) {
			return problem(Unsupported, "images."+n+".os", "OS family is not implemented")
		}
	}
	for _, n := range sortedKeys(l.Machines) {
		v := l.Machines[n]
		f := "machines." + n
		if len(v.NICs) > c.MaxNICs {
			return problem(Unsupported, fmtPathIndex(f+".nics", c.MaxNICs), "multiple NICs are not implemented")
		}
		if v.Forwarding && !c.Forwarding {
			return problem(Unsupported, f+".forwarding", "forwarding is not implemented")
		}
	}
	return nil
}
