package proxmox

import (
	"fmt"
	"net/netip"
)

// NetworkEgressProfile describes an operator-provisioned gateway outside authored
// guests. Policy and measured probe identities are bound by exact-target signed
// qualification. The provider never creates or changes the gateway or firewall.
type NetworkEgressProfile struct {
	Mode                   string `json:"mode"`
	IPv4CIDR               string `json:"ipv4Cidr"`
	Gateway                string `json:"gateway"`
	PolicyDigest           string `json:"policyDigest"`
	ProbeDigest            string `json:"probeDigest"`
	InternetReachable      bool   `json:"internetReachable"`
	ManagementReachable    bool   `json:"managementReachable"`
	CrossInstanceReachable bool   `json:"crossInstanceReachable"`
}

func validateNetworkEgress(version string, profile NetworkProfile) error {
	e := profile.Egress
	if e == nil {
		return nil
	}
	prefix, err := netip.ParsePrefix(e.IPv4CIDR)
	gateway, gatewayErr := netip.ParseAddr(e.Gateway)
	if version != targetConfigV15 || e.Mode != "allow" || err != nil || gatewayErr != nil || !prefix.Addr().Is4() || prefix != prefix.Masked() || !prefix.Addr().IsPrivate() || prefix.Bits() < 16 || prefix.Bits() > 29 || !usableIPv4(prefix, gateway) || !sha256Pattern.MatchString(e.PolicyDigest) || !sha256Pattern.MatchString(e.ProbeDigest) || !e.InternetReachable || e.ManagementReachable || e.CrossInstanceReachable {
		return fmt.Errorf("egress requires a v1.5 private IPv4 gateway profile and qualified Internet, management and cross-instance probes")
	}
	return nil
}

func usableIPv4(prefix netip.Prefix, address netip.Addr) bool {
	if !address.Is4() || !prefix.IsValid() || !prefix.Addr().Is4() || !prefix.Contains(address) || address == prefix.Masked().Addr() {
		return false
	}
	a := address.As4()
	value := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	hostMask := uint32(0xffffffff) >> prefix.Bits()
	return value&hostMask != hostMask
}

func validateNetworkProfileSpec(profile NetworkProfile, spec canonicalNetworkSpec) error {
	if err := validateNetworkSpec(spec); err != nil {
		return err
	}
	if profile.Egress == nil {
		if spec.Egress.Mode != "deny" {
			return fmt.Errorf("selected network profile does not provide qualified Internet egress")
		}
		return nil
	}
	if spec.Egress.Mode != profile.Egress.Mode || spec.IPv4CIDR != profile.Egress.IPv4CIDR {
		return fmt.Errorf("network egress mode and IPv4 CIDR must match the selected qualified gateway profile")
	}
	return nil
}

func networkProfileGateway(profile NetworkProfile) string {
	if profile.Egress == nil {
		return ""
	}
	return profile.Egress.Gateway
}

func networkBindingMatchesProfile(profile NetworkProfile, binding NetworkBinding) bool {
	if binding.Bridge != profile.Bridge || !valueInRanges(binding.VLAN, profile.VLANRanges) || binding.Gateway != networkProfileGateway(profile) {
		return false
	}
	if profile.Egress == nil {
		return true
	}
	if binding.IPv4CIDR != profile.Egress.IPv4CIDR {
		return false
	}
	if binding.IPv4Address != "" {
		prefix, _ := netip.ParsePrefix(binding.IPv4CIDR)
		address, err := netip.ParseAddr(binding.IPv4Address)
		if err != nil || !usableIPv4(prefix, address) || binding.IPv4Address == binding.Gateway {
			return false
		}
	}
	return true
}
