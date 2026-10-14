package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/access"
	"github.com/OutboundSpade/LabCentral/v2/build"
	"github.com/OutboundSpade/LabCentral/v2/instructor"
	"github.com/OutboundSpade/LabCentral/v2/plugin"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type options struct {
	root, env, provider, inputFile, configFile string
	json, yes, template                        bool
	engine                                     lc.EngineOptions
}

func defaults() options {
	root := os.Getenv("XDG_STATE_HOME")
	if root == "" {
		h, _ := os.UserHomeDir()
		root = filepath.Join(h, ".local", "state")
	}
	exe, _ := os.Executable()
	return options{root: filepath.Join(root, "labcentral", "v2"), provider: filepath.Join(filepath.Dir(exe), "labcentral-provider")}
}
func Run(ctx context.Context, args []string, in *os.File, out, errOut io.Writer) error {
	if len(args) == 0 {
		return help(out, "")
	}
	flags, positionals, err := interspersed(args)
	if err != nil {
		return err
	}
	helpRequested := wantsHelp(flags)
	if len(positionals) == 0 {
		if helpRequested {
			return help(out, "")
		}
		return fmt.Errorf("a command is required; run 'labcentral help'")
	}
	if positionals[0] == "help" {
		return help(out, strings.Join(positionals[1:], " "))
	}
	command, sub := positionals[0], ""
	args = positionals[1:]
	key := command
	if command == "environment" || command == "image" || command == "config" {
		if len(args) == 0 {
			if helpRequested {
				return help(out, key)
			}
			return fmt.Errorf("%s requires a subcommand; run 'labcentral help %s'", command, command)
		}
		sub, args = args[0], args[1:]
		key += " " + sub
	}
	spec, known := commands[key]
	if !known {
		return unknownCommand(key)
	}
	if helpRequested {
		return help(out, key)
	}
	o := defaults()
	o.configFile = defaultConfigPath()
	if key != "config init" {
		if err := loadDefaults(flags, &o); err != nil {
			return err
		}
	}
	f := flag.NewFlagSet(key, flag.ContinueOnError)
	f.SetOutput(errOut)
	if JSONOutput(flags) {
		f.SetOutput(io.Discard)
	}
	f.Usage = func() { _ = help(errOut, key) }
	f.StringVar(&o.configFile, "config", o.configFile, "CLI defaults JSON file")
	f.StringVar(&o.root, "state", o.root, "private local state directory")
	f.StringVar(&o.env, "environment", o.env, "environment YAML")
	f.StringVar(&o.provider, "provider", o.provider, "platform executable")
	f.StringVar(&o.inputFile, "inputs", "", "input values JSON file")
	limits := o.engine.Effective()
	f.IntVar(&o.engine.MaxImageBuildConcurrency, "image-build-concurrency", limits.MaxImageBuildConcurrency, "parallel image builds per operation (1–32)")
	f.IntVar(&o.engine.MaxMachineConcurrency, "machine-concurrency", limits.MaxMachineConcurrency, "parallel machine creates per instance (1–32)")
	f.IntVar(&o.engine.MaxCleanupConcurrency, "cleanup-concurrency", limits.MaxCleanupConcurrency, "parallel machine deletes per instance (1–32)")
	f.BoolVar(&o.template, "template", false, "require hosted template README")
	f.BoolVar(&o.json, "json", false, "versioned JSON output")
	f.BoolVar(&o.yes, "yes", false, "confirm mutations without prompting")
	f.BoolVar(&helpRequested, "help", false, "show command help")
	f.BoolVar(&helpRequested, "h", false, "show command help")
	if err := f.Parse(append(append(flags, "--"), args...)); err != nil {
		return err
	}
	if err := o.engine.Validate(); err != nil {
		return err
	}
	args = f.Args()
	labels := strings.Fields(spec.args)
	minimum := len(labels)
	if minimum > 0 && strings.HasPrefix(labels[0], "[") {
		minimum = 0
	}
	if len(args) < minimum {
		return usageError(key, "missing "+labels[len(args)])
	}
	if len(args) > len(labels) {
		return usageError(key, "too many arguments")
	}
	if spec.environment && o.env == "" {
		return usageError(key, "--environment is required; pass a file or save an absolute path in your CLI config")
	}
	if command == "access" && args[2] != "ssh" && args[2] != "desktop" {
		return usageError(key, "access method must be ssh or desktop")
	}
	emit := func(v any) error {
		if !o.json && readable(out, v) {
			return nil
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if o.json {
			return enc.Encode(struct {
				Version int `json:"version"`
				Result  any `json:"result"`
			}{2, v})
		}
		return enc.Encode(v)
	}
	arg := func(n int) (string, error) {
		if len(args) <= n {
			return "", fmt.Errorf("missing argument %d", n+1)
		}
		return args[n], nil
	}
	if command == "config" {
		if sub == "init" {
			path := o.configFile
			if len(args) > 0 {
				path = args[0]
			}
			if err := initConfig(path); err != nil {
				return err
			}
			return emit("Created CLI defaults at " + path + ". Set absolute environment, state or provider paths; command flags override them.")
		}
		return emit(struct {
			ConfigFile  string           `json:"configFile"`
			State       string           `json:"state"`
			Environment string           `json:"environment"`
			Provider    string           `json:"provider"`
			Engine      lc.EngineOptions `json:"engine"`
		}{o.configFile, o.root, o.env, o.provider, o.engine.Effective()})
	}
	if command == "init" {
		dir, err := arg(0)
		if err != nil {
			return err
		}
		if err := initialize(dir); err != nil {
			return err
		}
		return emit("Created " + dir + ". Run 'labcentral validate " + dir + "' to check your source.")
	}
	if command == "environment" && sub == "init" {
		file, err := arg(0)
		if err != nil {
			return err
		}
		platform, err := arg(1)
		if err != nil {
			return err
		}
		if err := initEnvironment(file, platform); err != nil {
			return err
		}
		return emit("Created " + file + ". Fill in your environment and pinned toolchain before running 'labcentral environment check --environment " + file + "'.")
	}
	if command == "environment" && sub == "fingerprint-plugins" {
		dir, err := arg(0)
		if err != nil {
			return err
		}
		fp, err := build.FingerprintPlugins(dir)
		if err != nil {
			return err
		}
		return emit(struct {
			SHA256 string `json:"sha256"`
		}{fp})
	}
	var plan lc.Plan
	if command == "validate" || command == "plan" || command == "build" || command == "deploy" {
		dir, err := arg(0)
		if err != nil {
			return err
		}
		source, _, err := instructor.LoadTemplate(dir, o.template)
		if err != nil {
			return err
		}
		if command == "validate" {
			if !o.json {
				return emit("Valid source: " + source.Lab.Name + "\nDigest: " + source.Digest)
			}
			return emit(struct {
				Name   string `json:"name"`
				Digest string `json:"sourceDigest"`
			}{source.Lab.Name, source.Digest})
		}
		if o.env == "" {
			return fmt.Errorf("--environment is required")
		}
		env, err := lc.LoadEnvironment(o.env)
		if err != nil {
			return err
		}
		values := map[string]string{}
		if o.inputFile != "" {
			b, err := os.ReadFile(o.inputFile)
			if err != nil {
				return err
			}
			if err = json.Unmarshal(b, &values); err != nil {
				return err
			}
		}
		plan, err = lc.PlanLab(source, env, values)
		if err != nil {
			return err
		}
		if command == "plan" {
			return emit(plan)
		}
		if !o.yes {
			fmt.Fprintf(errOut, "%s %s on %s: %d machines, %d image definitions.\n", command, plan.Source.Lab.Name, env.Name, len(plan.Machines), len(plan.Images))
			for _, m := range plan.Machines {
				fmt.Fprintf(errOut, "  %s: %s, %d CPU, %d MiB, %d GiB\n", m.Name, m.InstanceType, m.CPU, m.MemoryMiB, m.DiskGiB)
			}
			for _, im := range plan.Images {
				if im.Playbook != "" {
					fmt.Fprintf(errOut, "  image %s: build %s (reuse only if cached artifact verifies)\n", im.Name, im.Playbook)
				}
			}
			for _, notice := range plan.Notices {
				fmt.Fprintln(errOut, notice)
			}
			if err = confirm(in, errOut); err != nil {
				return err
			}
		}
	}
	// Read-only local queries don't start the platform process.
	var provider lc.Provider = &offline{}
	var process *plugin.Process
	remote := !(command == "list" || command == "inspect" || (command == "image" && (sub == "list" || sub == "builds")))
	if remote {
		var err error
		process, err = plugin.Start(ctx, o.provider, filepath.Join(o.root, "providers"))
		if err != nil {
			return err
		}
		defer process.Close()
		provider = process
	}
	observe := func(event lc.Event) {
		if o.json {
			_ = json.NewEncoder(errOut).Encode(event)
		} else {
			fmt.Fprintf(errOut, "%s %s %s\n", event.Stage, event.Resource, event.Message)
		}
	}
	engine, err := lc.OpenWithOptions(o.root, provider, observe, o.engine)
	if err != nil {
		return err
	}
	defer engine.Close()
	switch command {
	case "list":
		r, err := engine.List(ctx)
		if err != nil {
			return err
		}
		return emit(r)
	case "inspect":
		id, err := arg(0)
		if err != nil {
			return err
		}
		r, err := engine.Inspect(ctx, id)
		if err != nil {
			return err
		}
		return emit(r)
	case "build":
		r, err := engine.Build(ctx, plan)
		if err != nil {
			return err
		}
		return emit(r)
	case "deploy":
		r, err := engine.Deploy(ctx, plan)
		if err != nil {
			if r.ID != "" && !o.json {
				fmt.Fprintf(errOut, "Instance: %s\nResume this attempt: labcentral recover %s --state %q\n", r.ID, r.ID, o.root)
			}
			return err
		}
		return emit(r)
	case "recover":
		id, err := arg(0)
		if err != nil {
			return err
		}
		if !o.yes {
			fmt.Fprintf(errOut, "%s %s (state: %s)\n", key, id, o.root)
			if err = confirm(in, errOut); err != nil {
				return err
			}
		}
		if strings.HasPrefix(id, "build:") {
			r, err := engine.RecoverBuild(ctx, strings.TrimPrefix(id, "build:"))
			if err != nil {
				return err
			}
			return emit(r)
		}
		r, err := engine.Recover(ctx, id)
		if err != nil {
			return err
		}
		return emit(r)
	case "destroy":
		id, err := arg(0)
		if err != nil {
			return err
		}
		if !o.yes {
			fmt.Fprintf(errOut, "%s %s (state: %s)\n", key, id, o.root)
			if err = confirm(in, errOut); err != nil {
				return err
			}
		}
		return engine.Destroy(ctx, id)
	case "environment":
		if sub != "check" {
			return fmt.Errorf("unknown environment command")
		}
		env, err := lc.LoadEnvironment(o.env)
		if err != nil {
			return err
		}
		if err = engine.Check(ctx, env); err != nil {
			return err
		}
		return emit("environment checks passed; live isolation probes remain separate")
	case "image":
		if sub == "builds" {
			r, err := engine.Builds(ctx)
			if err != nil {
				return err
			}
			return emit(r)
		}
		if sub == "list" {
			r, err := engine.Images(ctx)
			if err != nil {
				return err
			}
			return emit(r)
		}
		if sub == "remove" || sub == "abandon" {
			key, err := arg(0)
			if err != nil {
				return err
			}
			if !o.yes {
				fmt.Fprintf(errOut, "%s %s (state: %s)\n", "image "+sub, key, o.root)
				if err = confirm(in, errOut); err != nil {
					return err
				}
			}
			if sub == "abandon" {
				return engine.AbandonBuild(ctx, key)
			}
			return engine.RemoveImage(ctx, key)
		}
		return fmt.Errorf("unknown image command")
	case "access":
		instance, err := arg(0)
		if err != nil {
			return err
		}
		machine, err := arg(1)
		if err != nil {
			return err
		}
		method, err := arg(2)
		if err != nil {
			return err
		}
		if method == "ssh" {
			code, err := access.Shell(ctx, engine, instance, machine, in, out, errOut)
			if err != nil {
				return err
			}
			if code != 0 {
				return &ExitError{code}
			}
			return nil
		}
		if method != "desktop" {
			return fmt.Errorf("access method must be ssh or desktop")
		}
		info, err := engine.OpenDesktop(ctx, instance, machine)
		if err != nil {
			return err
		}
		if err = emit(info); err != nil {
			return err
		}
		fmt.Fprintln(errOut, "Open the URL in your browser. Keep this command running; Ctrl-C closes access.")
		<-ctx.Done()
		return engine.CloseAccess(context.Background(), info.ID)
	}
	return fmt.Errorf("unknown command %q", command)
}

type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("remote command exited %d", e.Code) }

type offline struct{}

func (*offline) Execute(context.Context, lc.Command, lc.Observer) (lc.Result, error) {
	return lc.Result{}, fmt.Errorf("provider unavailable for local-only operation")
}
func confirm(in io.Reader, out io.Writer) error {
	fmt.Fprint(out, "Continue? [y/N] ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil {
		return fmt.Errorf("confirmation required; use --yes for automation")
	}
	if strings.TrimSpace(strings.ToLower(line)) != "y" {
		return fmt.Errorf("cancelled")
	}
	return nil
}
func writeNew(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(b)
	return err
}
func initialize(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("destination exists")
	}
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0755); err != nil {
		return err
	}
	if err := writeNew(filepath.Join(dir, "lab.yaml"), []byte("version: 3\nname: hello-lab\nimages:\n  server:\n    base: ubuntu-24.04\n    build:\n      playbook: images/server.yml\nmachines:\n  server:\n    image: server\n    cpu: 2\n    memory: 2Gi\n    disk: 20Gi\n    access: [ssh]\n")); err != nil {
		return err
	}
	return writeNew(filepath.Join(dir, "images", "server.yml"), []byte("- name: Prepare server\n  hosts: localhost\n  connection: local\n  become: true\n  tasks:\n    - name: Install nginx\n      ansible.builtin.apt:\n        name: nginx\n        state: present\n        update_cache: true\n"))
}
func initEnvironment(path, platform string) error {
	common := "version: 2\nname: example\nplatform: " + platform + "\noperatorCIDRs: [203.0.113.10/32]\nbases:\n  ubuntu-24.04:\n    id: REPLACE\n    identity: REPLACE\n    user: ubuntu\n    diskGiB: 8\nbuild:\n  packer: /absolute/path/to/packer\n  packerSHA256: REPLACE\n  pluginDirectory: /absolute/path/to/packer-plugins\n  toolchainID: REPLACE\n  ansibleVersion: 2.19.3\n  timeoutSeconds: 3600\n"
	switch platform {
	case "aws":
		common += "  subnetID: subnet-REPLACE\n  securityGroupID: sg-REPLACE\n  instanceType: t3.medium\naws:\n  account: '000000000000'\n  region: us-east-1\n  zone: us-east-1a\n  types:\n    - {name: t3.medium, cpu: 2, memoryMiB: 4096}\n"
	case "proxmox":
		common += "  bridge: vmbr-build\nproxmox:\n  url: https://pve.example.invalid:8006\n  tokenID: labcentral@pve!v2\n  tokenEnv: LABCENTRAL_PROXMOX_TOKEN\n  node: pve\n  storage: local-lvm\n  pool: labcentral-v2\n  bridge: vmbr-labs\n  vmidStart: 2000\n  vmidEnd: 2999\n  vlanStart: 100\n  vlanEnd: 199\n  cidr: 10.80.0.0/16\n  gateway: 10.80.0.1\n  verifiedIsolation: false\n"
	default:
		return fmt.Errorf("expected aws or proxmox")
	}
	return writeNew(path, []byte(common))
}
