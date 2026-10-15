package labcentral

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// OSFamily applies the portable default without changing serialized Linux identities.
func OSFamily(os string) string {
	if os == "" {
		return "linux"
	}
	return os
}
func (i Image) BuildProgram() string {
	if i.Build == nil {
		return ""
	}
	if i.Build.Script != "" {
		return i.Build.Script
	}
	return i.Build.Playbook
}
func (i Image) BuildScript() string {
	if i.Build == nil {
		return ""
	}
	return i.Build.Script
}
func (i PlannedImage) BuildProgram() string {
	if i.Script != "" {
		return i.Script
	}
	return i.Playbook
}
func (m PlannedMachine) SSHUser() string {
	if m.OS == "windows" {
		return "Administrator"
	}
	return "lab"
}

// CheckWindowsCommand protects standalone validation and provider requests as well
// as plans; the native adapters must never bootstrap an unvalidated Windows shape.
func CheckWindowsCommand(c Command) error {
	if c.Machine.OS == "windows" {
		f := "machines." + c.Machine.Name
		if c.Environment.Platform != "aws" {
			return problem(Unsupported, f+".os", "Windows guests are unsupported on Proxmox")
		}
		if c.Machine.Spec.Forwarding {
			return problem(Unsupported, f+".forwarding", "Windows forwarding is unsupported; routers must use Linux")
		}
		if len(c.Machine.NICs) > 1 || len(c.Machine.Spec.NICs) > 1 {
			return problem(Unsupported, f+".nics", "Windows supports at most one NIC (MaxWindowsNICs = 1)")
		}
		if len(c.Machine.Name) > 15 {
			return problem(Unsupported, f, "Windows computer names must be at most 15 characters")
		}
		if c.Machine.MemoryMiB < 4096 {
			return problem(Unsupported, f+".memory", "Windows requires at least 4 GiB of memory")
		}
		for _, t := range c.Environment.AWS.Types {
			if t.Name == c.Machine.InstanceType && t.MemoryMiB < 4096 {
				return problem(Unsupported, f+".platform.aws.instanceType", "Windows instance types require at least 4 GiB of memory")
			}
		}
	}
	if c.Image.Base.OS == "windows" {
		if c.Environment.Platform != "aws" {
			return problem(Unsupported, "images."+c.Image.Name+".os", "Windows guests are unsupported on Proxmox")
		}
		if c.Image.BuildProgram() != "" {
			if c.Image.Desktop {
				if err := ValidateWindowsDCV(c.Environment.Build); err != nil {
					return err
				}
			}
			name := c.Image.BuildInstanceType
			if name == "" {
				name = c.Environment.Build.InstanceType
			}
			if err := WindowsBuildCapacity(c.Environment, name, "images."+c.Image.Name+".platform.aws.buildInstanceType"); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateWindowsDCV requires operator pins only for Windows desktop builds.
func ValidateWindowsDCV(b BuildConfig) error {
	u, err := url.Parse(b.WindowsDCVURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || strings.ContainsAny(b.WindowsDCVURL, "'\n\r \t") {
		return problem(Invalid, "environment.build.windowsDCVURL", "a pinned HTTPS Windows DCV MSI URL without credentials or whitespace is required")
	}
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(b.WindowsDCVSHA256) {
		return problem(Invalid, "environment.build.windowsDCVSHA256", "a lowercase SHA-256 for the Windows DCV MSI is required")
	}
	return nil
}

func WindowsBuildCapacity(e Environment, name, field string) error {
	for _, t := range e.AWS.Types {
		if t.Name == name {
			if t.MemoryMiB >= 4096 {
				return nil
			}
			return problem(Unsupported, field, "Windows build and qualification instance types require at least 4 GiB of memory")
		}
	}
	return problem(Unsupported, field, "Windows build instance type must have capacity declared in environment.aws.types")
}

// WindowsFilePaths rejects source selections which cannot retain their identity
// on a Windows filesystem. It performs no provider work or host filesystem access.
func WindowsFilePaths(paths []string) error {
	seen := map[string]bool{}
	for _, path := range paths {
		lower := strings.ToLower(path)
		for existing := range seen {
			if existing == lower || strings.HasPrefix(existing, lower+"/") || strings.HasPrefix(lower, existing+"/") {
				return fmt.Errorf("Windows paths collide: %s", path)
			}
		}
		seen[lower] = true
		for _, part := range strings.Split(lower, "/") {
			base, _, _ := strings.Cut(part, ".")
			if strings.HasSuffix(part, ".") || Contains([]string{"con", "prn", "aux", "nul", "com1", "com2", "com3", "com4", "com5", "com6", "com7", "com8", "com9", "lpt1", "lpt2", "lpt3", "lpt4", "lpt5", "lpt6", "lpt7", "lpt8", "lpt9"}, base) {
				return fmt.Errorf("Windows reserved path: %s", path)
			}
		}
	}
	return nil
}
