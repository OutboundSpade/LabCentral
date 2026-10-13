package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

type commandSpec struct {
	args, description, example string
	environment                bool
}

var commands = map[string]commandSpec{
	"init":                            {"DIRECTORY", "Create a portable lab source directory.", "labcentral init my-lab", false},
	"validate":                        {"DIRECTORY", "Validate source without starting a provider.", "labcentral validate my-lab", false},
	"plan":                            {"DIRECTORY", "Preview resources and image builds without provisioning.", "labcentral plan my-lab --environment aws.yaml", true},
	"build":                           {"DIRECTORY", "Build and cache the lab's images.", "labcentral build my-lab --environment aws.yaml", true},
	"deploy":                          {"DIRECTORY", "Build missing images and provision a lab instance.", "labcentral deploy my-lab --environment aws.yaml", true},
	"list":                            {"", "List retained instances from local state.", "labcentral list", false},
	"inspect":                         {"INSTANCE_ID", "Show a retained instance and its resources.", "labcentral inspect INSTANCE_ID", false},
	"access":                          {"INSTANCE_ID MACHINE ssh|desktop", "Open SSH or a temporary browser desktop.", "labcentral access INSTANCE_ID workstation desktop", false},
	"recover":                         {"INSTANCE_ID|build:IMAGE_KEY", "Reconcile the original attempt after an unknown outcome.", "labcentral recover INSTANCE_ID", false},
	"destroy":                         {"INSTANCE_ID", "Destroy a verified owned instance; retain its images.", "labcentral destroy INSTANCE_ID", false},
	"environment init":                {"FILE aws|proxmox", "Write an environment skeleton that needs operator configuration.", "labcentral environment init aws.yaml aws", false},
	"environment check":               {"", "Check the configured provider environment.", "labcentral environment check --environment aws.yaml", true},
	"environment fingerprint-plugins": {"DIRECTORY", "Fingerprint the pinned Packer plugins.", "labcentral environment fingerprint-plugins /path/to/plugins", false},
	"image list":                      {"", "List cached image artifacts.", "labcentral image list", false},
	"image builds":                    {"", "List pending image build intents for recovery.", "labcentral image builds", false},
	"image remove":                    {"IMAGE_KEY", "Remove a verified image when retained instances no longer reference it.", "labcentral image remove IMAGE_KEY", false},
	"image abandon":                   {"IMAGE_KEY", "Abandon an unpublished build after recovery confirms cleanup.", "labcentral image abandon IMAGE_KEY", false},
	"config init":                     {"[FILE]", "Write private CLI defaults; no credentials or automatic mutation approval.", "labcentral config init", false},
	"config show":                     {"", "Show resolved CLI paths and environment defaults.", "labcentral config show", false},
}

func help(out io.Writer, key string) error {
	if key == "" {
		fmt.Fprintln(out, `LabCentral v2 — portable lab operations
Usage: labcentral COMMAND [OPTIONS] [ARGUMENTS]

Get started:
  labcentral init my-lab
  labcentral environment init aws.yaml aws
  # Configure aws.yaml with your environment and pinned toolchain.
  labcentral plan my-lab --environment aws.yaml
  labcentral deploy my-lab --environment aws.yaml

Source:      init, validate, plan
Lifecycle:   build, deploy, list, inspect, access, recover, destroy
Environment: environment init|check|fingerprint-plugins
Images:      image list|builds|remove|abandon
Defaults:    config init|show

Use labcentral help COMMAND for usage and examples.
Options may precede or follow arguments. Use -- before a path beginning with '-'.
Common options: --config FILE --environment FILE --state DIR --provider FILE
                --inputs FILE --json --yes
Concurrency: --image-build-concurrency N --machine-concurrency N --cleanup-concurrency N
Mutations ask for confirmation; --yes explicitly approves automation.`)
		return nil
	}
	if key == "image" || key == "environment" || key == "config" {
		fmt.Fprintf(out, "Usage: labcentral %s SUBCOMMAND [OPTIONS] [ARGUMENTS]\n\n", key)
		for _, sub := range []string{"init", "check", "fingerprint-plugins", "list", "builds", "remove", "abandon", "show"} {
			if spec, ok := commands[key+" "+sub]; ok {
				fmt.Fprintf(out, "  %-20s %s\n", sub, spec.description)
			}
		}
		return nil
	}
	spec, ok := commands[key]
	if !ok {
		return unknownCommand(key)
	}
	fmt.Fprintf(out, "Usage: labcentral %s [OPTIONS] %s\n\n%s\n\nExample:\n  %s\n\n", key, spec.args, spec.description, spec.example)
	fmt.Fprintln(out, "Options: --config FILE --state DIR --provider FILE --json --yes\n         --environment FILE --inputs FILE (JSON string values)\nConcurrency: --image-build-concurrency N --machine-concurrency N --cleanup-concurrency N (1–32)\nFlags may appear before or after arguments. Use -- to end options.")
	if spec.environment {
		fmt.Fprintln(out, "An environment is required: pass --environment or save it with config init.")
	}
	return nil
}

func usageError(key, message string) error {
	spec := commands[key]
	return fmt.Errorf("%s\nUsage: labcentral %s [OPTIONS] %s\nRun 'labcentral help %s' for an example", message, key, spec.args, key)
}

func unknownCommand(key string) error {
	best, score := "", 4
	for candidate := range commands {
		d := distance(key, candidate)
		if d < score || (d == score && candidate < best) {
			best, score = candidate, d
		}
	}
	if score <= 2 {
		return fmt.Errorf("unknown command %q. Did you mean %q? Run 'labcentral help'", key, best)
	}
	return fmt.Errorf("unknown command %q. Run 'labcentral help' for available commands", key)
}

func distance(a, b string) int {
	row := make([]int, len(b)+1)
	for i := range row {
		row[i] = i
	}
	for i := range a {
		previous := row[0]
		row[0] = i + 1
		for j := range b {
			old := row[j+1]
			cost := 0
			if a[i] != b[j] {
				cost = 1
			}
			row[j+1] = min(row[j]+1, row[j+1]+1, previous+cost)
			previous = old
		}
	}
	return row[len(b)]
}

// Reorder only known options. Keep '--' literal positional arguments intact.
// Unknown flags fail before any source, state, or provider is opened.
func interspersed(args []string) (flags, positionals []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			positionals = append(positionals, a)
			continue
		}
		name, _, equal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		switch name {
		case "json", "yes", "h", "help":
			flags = append(flags, a)
		case "config", "state", "environment", "provider", "inputs", "image-build-concurrency", "machine-concurrency", "cleanup-concurrency":
			flags = append(flags, a)
			if !equal {
				if i+1 == len(args) {
					return nil, nil, fmt.Errorf("--%s requires a value", name)
				}
				i++
				flags = append(flags, args[i])
			}
		default:
			return nil, nil, fmt.Errorf("unknown option %q; run 'labcentral help'", a)
		}
	}
	return flags, positionals, nil
}

func takesValue(name string) bool {
	switch name {
	case "config", "state", "environment", "provider", "inputs", "image-build-concurrency", "machine-concurrency", "cleanup-concurrency":
		return true
	}
	return false
}

// JSONOutput uses option semantics even when command validation fails. A flag
// value or a literal argument after '--' cannot switch the error format.
func JSONOutput(args []string) bool {
	value := false
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		name, raw, equal := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if !strings.HasPrefix(args[i], "-") {
			continue
		}
		if takesValue(name) && !equal {
			i++
			continue
		}
		if name == "json" {
			if !equal {
				value = true
			} else if parsed, err := strconv.ParseBool(raw); err == nil {
				value = parsed
			}
		}
	}
	return value
}

func wantsHelp(flags []string) bool {
	value := false
	for i := 0; i < len(flags); i++ {
		name, raw, equal := strings.Cut(strings.TrimLeft(flags[i], "-"), "=")
		if takesValue(name) && !equal {
			i++
			continue
		}
		if name == "h" || name == "help" {
			if !equal {
				value = true
			} else if parsed, err := strconv.ParseBool(raw); err == nil {
				value = parsed
			}
		}
	}
	return value
}
