package labcentral

import "testing"

func TestImageKeysBindOnlyBuildEnvironment(t *testing.T) {
	for _, platform := range []string{"aws", "proxmox"} {
		t.Run(platform, func(t *testing.T) {
			base := testEnvironment()
			if platform == "proxmox" {
				base = platformOptionsProxmoxEnvironment()
			}
			s := testSource(t)
			p, err := PlanLab(s, base, nil)
			if err != nil {
				t.Fatal(err)
			}
			key := p.Images[0].CacheKey
			for _, change := range []func(*Environment){
				func(e *Environment) {
					e.Build.Packer = "/other/packer"
					e.Build.PluginDirectory = "/other/plugins"
					e.Build.TimeoutSeconds = 900
					e.Build.DCVURL = "https://example.invalid/desktop"
					e.Build.DCVSHA256 = "desktop-only"
					e.Build.WindowsDCVURL = "https://example.invalid/windows.msi"
					e.Build.WindowsDCVSHA256 = "windows-only"
				},
				func(e *Environment) { e.Name = "other"; e.OperatorCIDRs = []string{"198.51.100.1/32"} },
				func(e *Environment) {
					e.AWS.AllowEgress = true
					e.AWS.Gateway.InstanceType = "t3.small"
					e.AWS.DesktopProfile = "new-license"
					e.AWS.Types[0].MaxNICs = 3
					e.AWS.Profile = "local-profile"
				},
				func(e *Environment) {
					e.Proxmox.AllowEgress = true
					e.Proxmox.CPUTypes = []string{"new-model"}
					e.Proxmox.TokenEnv = "OTHER_TOKEN"
					e.Proxmox.InsecureSkipTLSVerify = !e.Proxmox.InsecureSkipTLSVerify
				},
			} {
				e := base
				e.AWS.Types = append([]InstanceType{}, base.AWS.Types...)
				change(&e)
				updated, err := PlanLab(s, e, nil)
				if err != nil || updated.Images[0].CacheKey != key {
					t.Fatal("runtime environment changed image identity", err)
				}
			}
			for _, change := range []func(*Environment){
				func(e *Environment) { e.Build.ToolchainID = "new-toolchain" },
				func(e *Environment) {
					if platform == "aws" {
						e.Build.SubnetID = "other-build-subnet"
					} else {
						e.Build.Bridge = "other-build-bridge"
					}
				},
				func(e *Environment) {
					if platform == "aws" {
						e.AWS.Account = "999999999999"
					} else {
						e.Proxmox.Storage = "other-storage"
					}
				},
			} {
				e := base
				change(&e)
				updated, err := PlanLab(s, e, nil)
				if err != nil || updated.Images[0].CacheKey == key {
					t.Fatal("build environment omitted from image identity", err)
				}
			}
		})
	}
}
