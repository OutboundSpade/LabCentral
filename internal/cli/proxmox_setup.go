package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/build"
	"github.com/OutboundSpade/LabCentral/v2/platform/proxmox"
	"golang.org/x/term"
)

func setupProxmox(ctx context.Context, action, file string, opts proxmox.SetupOptions, o options, in *os.File, out io.Writer) error {
	var err error
	opts.Build, err = setupBuild()
	if err != nil {
		return err
	}
	s, err := proxmox.OpenSetup(file, opts)
	if err != nil {
		return err
	}
	if action == "teardown" {
		fmt.Fprintln(out, "Remove the environment's owned base, seed/import files, internal bridge, pool, user, token and roles. Labs and built images must be removed first.")
		if !o.yes {
			if err = confirm(in, out); err != nil {
				return err
			}
		}
		if err = s.Teardown(ctx); err != nil {
			return err
		}
		fmt.Fprintln(out, "Proxmox setup resources removed; private receipt retained for confirmed cleanup.")
		return nil
	}
	plan, err := s.Plan(ctx, opts)
	if err != nil {
		return err
	}
	bridge := plan.Environment.Build.Bridge
	plan.Environment.Build = opts.Build
	plan.Environment.Build.Bridge = bridge
	for _, change := range plan.Changes {
		fmt.Fprintln(out, "- "+change)
	}
	if !o.yes {
		if err = confirm(in, out); err != nil {
			return err
		}
	}
	if err = s.Apply(ctx, plan); err != nil {
		return err
	}
	absolute, _ := filepath.Abs(file)
	quotedToken := "'" + strings.ReplaceAll(absolute+".token", "'", "'\\''") + "'"
	fmt.Fprintf(out, "Environment: %s\nToken secret: %s.token (0600). Export it without logging:\n  export LABCENTRAL_PROXMOX_TOKEN=\"$(cat %s)\"\n", absolute, absolute, quotedToken)
	// Show a newly issued secret once, only on an interactive terminal. Logs and
	// JSON/piped output receive only the protected file path.
	if f, ok := out.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		secret, err := s.TokenSecretOnce()
		if err != nil {
			return err
		}
		if secret != "" {
			fmt.Fprintf(out, "Token secret (keep private): %s\n", strings.TrimSpace(secret))
		}
	}
	if build.CheckToolchain(plan.Environment.Build) != nil {
		fmt.Fprintln(out, "Install pinned Packer/plugins as documented, then rerun setup to pin the installed tools.")
	}
	quotedEnvironment := "'" + strings.ReplaceAll(absolute, "'", "'\\''") + "'"
	fmt.Fprintf(out, "Next: labcentral environment check --environment %s\nOperator must reach gateway/build DHCP addresses on the management bridge.\n", quotedEnvironment)
	return nil
}
func setupBuild() (lc.BuildConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return lc.BuildConfig{}, err
	}
	b := lc.BuildConfig{Packer: filepath.Join(home, ".local", "bin", "packer"), PluginDirectory: filepath.Join(home, ".local", "share", "labcentral", "packer-plugins"), PackerSHA256: strings.Repeat("0", 64), ToolchainID: strings.Repeat("0", 64), AnsibleVersion: "2.19.3", TimeoutSeconds: 3600}
	if directory := os.Getenv("PACKER_PLUGIN_PATH"); directory != "" {
		b.PluginDirectory = directory
	}
	if binary, err := exec.LookPath("packer"); err == nil {
		absolute, err := filepath.Abs(binary)
		if err != nil {
			return b, err
		}
		b.Packer = absolute
	}
	if bytes, err := os.ReadFile(b.Packer); err == nil {
		hash := sha256.Sum256(bytes)
		b.PackerSHA256 = fmt.Sprintf("%x", hash)
	}
	if fingerprint, err := build.FingerprintPlugins(b.PluginDirectory); err == nil {
		b.ToolchainID = fingerprint
	}
	return b, nil
}
