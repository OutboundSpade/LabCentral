package labcentral

// Capabilities describes the portable requests implemented by a platform.
type Capabilities struct {
	MaxNetworks, MaxNICs         int
	Egress, OS                   []string
	AuthorCIDR, Forwarding       bool
	ImageOptions, MachineOptions []string
}

func PlatformCapabilities(platform string) (Capabilities, bool) {
	if platform != "aws" && platform != "proxmox" {
		return Capabilities{}, false
	}
	c := Capabilities{MaxNetworks: 1, MaxNICs: 1, Egress: []string{"none", "internet"}, OS: []string{"linux"}}
	if platform == "aws" {
		c.ImageOptions = []string{"buildInstanceType"}
		c.MachineOptions = []string{"instanceType", "volume", "volume.type", "volume.iops", "volume.throughput", "cpuCredits"}
	} else {
		c.MachineOptions = []string{"cpuType", "nicModel", "memoryBalloon"}
	}
	return c, true
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
		var options any = v.Platform.AWS
		if platform == "proxmox" {
			options = v.Platform.Proxmox
		}
		if err := checkImplementedOptions(options, c.ImageOptions, "images."+n+".platform."+platform); err != nil {
			return err
		}
		if v.OS != "" && !Contains(c.OS, v.OS) {
			return problem(Unsupported, "images."+n+".os", "OS family is not implemented")
		}
	}
	for _, n := range sortedKeys(l.Machines) {
		v := l.Machines[n]
		f := "machines." + n
		var options any = v.Platform.AWS
		if platform == "proxmox" {
			options = v.Platform.Proxmox
		}
		if err := checkImplementedOptions(options, c.MachineOptions, f+".platform."+platform); err != nil {
			return err
		}
		if len(v.NICs) > c.MaxNICs {
			return problem(Unsupported, fmtPathIndex(f+".nics", c.MaxNICs), "multiple NICs are not implemented")
		}
		if v.Forwarding && !c.Forwarding {
			return problem(Unsupported, f+".forwarding", "forwarding is not implemented")
		}
	}
	return nil
}

// InternetAccess reports the author request, independently of operator permission.
func InternetAccess(l Lab) bool {
	for _, n := range l.Networks {
		if n.Egress == "internet" {
			return true
		}
	}
	return false
}

// CheckEgress repeats environment policy before any native work.
func CheckEgress(l Lab, e Environment) error {
	for _, name := range sortedKeys(l.Networks) {
		if l.Networks[name].Egress != "internet" {
			continue
		}
		enabled := e.AWS.AllowEgress
		if e.Platform == "proxmox" {
			enabled = e.Proxmox.AllowEgress
		}
		if !enabled {
			return problem(Unsupported, "networks."+name+".egress", "internet requires environment."+e.Platform+".allowEgress=true")
		}
		if e.Platform == "proxmox" && e.Proxmox.Gateway == "" {
			return problem(Invalid, "environment.proxmox.gateway", "internet egress requires an operator VLAN gateway with NAT")
		}
	}
	return nil
}
