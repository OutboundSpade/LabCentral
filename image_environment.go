package labcentral

// Image identity excludes runtime policy, credentials and unrelated base bindings.
// This is a one-time key revision; prior images remain removable by saved keys.
type imageEnvironment struct {
	Platform, Account, Region, URL, Node, Storage string
	Toolchain                                     imageToolchain
	Networking                                    imageBuildNetwork
}
type imageToolchain struct {
	PackerSHA256, ToolchainID, AnsibleVersion, DCVURL, DCVSHA256 string
}
type imageBuildNetwork struct {
	SubnetID, SecurityGroupID, InstanceType, Bridge, Gateway string
	VLAN                                                     int
}

func buildEnvironment(e Environment) imageEnvironment {
	b := e.Build
	x := imageEnvironment{Platform: e.Platform, Toolchain: imageToolchain{PackerSHA256: b.PackerSHA256, ToolchainID: b.ToolchainID, AnsibleVersion: b.AnsibleVersion, DCVURL: b.DCVURL, DCVSHA256: b.DCVSHA256}}
	if e.Platform == "aws" {
		x.Account, x.Region = e.AWS.Account, e.AWS.Region
		x.Networking = imageBuildNetwork{SubnetID: b.SubnetID, SecurityGroupID: b.SecurityGroupID, InstanceType: b.InstanceType}
	} else {
		x.URL, x.Node, x.Storage = e.Proxmox.URL, e.Proxmox.Node, e.Proxmox.Storage
		x.Networking = imageBuildNetwork{Bridge: b.Bridge, VLAN: b.VLAN, Gateway: b.Gateway}
	}
	return x
}
