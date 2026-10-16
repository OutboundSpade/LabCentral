// Package build generates fixed Packer configurations and runs native image builds.
package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/guest"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const AmazonVersion = "1.8.1"
const ProxmoxVersion = "1.2.3"
const AnsiblePluginVersion = "1.1.4"

type Config struct {
	Builders     []json.RawMessage `json:"builders"`
	Provisioners []json.RawMessage `json:"provisioners"`
}
type Runner struct {
	Configure func(context.Context, lc.Command, *Config) error
	Provider  lc.Provider
	Prepare   func(context.Context, lc.Command, string) error
	Cleanup   func(context.Context, lc.Command) error
	Root      string
	Find      func(context.Context, lc.Command) (lc.Resource, bool, error)
	Validate  func(context.Context, lc.Command, lc.Resource) (lc.Resource, error)
}

func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func FingerprintPlugins(dir string) (string, error) {
	var entries []struct {
		Path   string
		SHA256 string
	}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		i, e := d.Info()
		if e != nil {
			return e
		}
		if !i.Mode().IsRegular() {
			return fmt.Errorf("plugin directory contains nonregular files")
		}
		h, e := hashFile(p)
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(dir, p)
		entries = append(entries, struct {
			Path   string
			SHA256 string
		}{rel, h})
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("plugin directory is empty")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	b, _ := json.Marshal(entries)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func hashFile(p string) (string, error) {
	f, e := os.Open(p)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CheckToolchain verifies installed bytes without invoking Packer or author code.
func CheckToolchain(b lc.BuildConfig) error {
	pin, err := hashFile(b.Packer)
	if err != nil || pin != b.PackerSHA256 {
		return fmt.Errorf("Packer executable missing or checksum mismatch; install the pinned version and rerun environment setup to pin it")
	}
	fp, err := FingerprintPlugins(b.PluginDirectory)
	if err != nil || fp != b.ToolchainID {
		return fmt.Errorf("Packer plugin directory missing or fingerprint mismatch; install pinned plugins and rerun environment setup")
	}
	return nil
}
func safePin(s string) bool { return regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(s) }
func Generate(c lc.Command, dir string) (Config, error) {
	if err := lc.CheckCommandPlatformOptions(c); err != nil {
		return Config{}, err
	}
	b := c.Environment.Build
	if c.Environment.Platform == "aws" && c.Image.BuildInstanceType != "" {
		b.InstanceType = c.Image.BuildInstanceType
	}
	im := c.Image
	var cfg Config
	if im.Base.OS != "windows" && !safePin(b.AnsibleVersion) {
		return cfg, fmt.Errorf("Ansible must have an exact numeric version")
	}
	if im.BuildProgram() == "" {
		return cfg, fmt.Errorf("build requires a playbook")
	}
	if !regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(c.Owner) {
		return cfg, fmt.Errorf("invalid build identity")
	}
	tags := map[string]string{"labcentral:version": "2", "labcentral:owner": c.Owner, "labcentral:role": im.Name}
	if c.Environment.Platform == "aws" {
		if b.SubnetID == "" || b.SecurityGroupID == "" || b.InstanceType == "" {
			return cfg, fmt.Errorf("AWS builds require an operator-owned build subnet, restricted security group and instance type")
		}
		builder := struct {
			SkipCredentialValidation bool              `json:"skip_credential_validation"`
			Type                     string            `json:"type"`
			Region                   string            `json:"region"`
			Profile                  string            `json:"profile,omitempty"`
			Source                   string            `json:"source_ami"`
			Instance                 string            `json:"instance_type"`
			User                     string            `json:"ssh_username"`
			AMI                      string            `json:"ami_name"`
			Subnet                   string            `json:"subnet_id"`
			SG                       string            `json:"security_group_id"`
			Public                   bool              `json:"associate_public_ip_address"`
			Key                      string            `json:"ssh_keypair_name"`
			PrivateKey               string            `json:"ssh_private_key_file"`
			Tags                     map[string]string `json:"tags"`
			RunTags                  map[string]string `json:"run_tags"`
			RunVolumeTags            map[string]string `json:"run_volume_tags"`
			SnapshotTags             map[string]string `json:"snapshot_tags"`
			Volumes                  []struct {
				Device    string `json:"device_name"`
				Size      int    `json:"volume_size"`
				Delete    bool   `json:"delete_on_termination"`
				Encrypted bool   `json:"encrypted"`
				Type      string `json:"volume_type"`
			} `json:"launch_block_device_mappings"`
			Metadata struct {
				Tokens string `json:"http_tokens"`
			} `json:"metadata_options"`
		}{SkipCredentialValidation: true, Type: "amazon-ebs", Region: c.Environment.AWS.Region, Profile: c.Environment.AWS.Profile, Source: im.Base.ID, Instance: b.InstanceType, User: im.Base.User, AMI: "labcentral-" + c.Owner, Subnet: b.SubnetID, SG: b.SecurityGroupID, Public: true, Key: "labcentral-" + c.Owner, PrivateKey: filepath.Join(dir, "build.key"), Tags: tags, RunTags: tags, SnapshotTags: tags, RunVolumeTags: tags}
		builder.Metadata.Tokens = "required"
		builder.Volumes = append(builder.Volumes, struct {
			Device    string `json:"device_name"`
			Size      int    `json:"volume_size"`
			Delete    bool   `json:"delete_on_termination"`
			Encrypted bool   `json:"encrypted"`
			Type      string `json:"volume_type"`
		}{"/dev/sda1", im.DiskGiB, true, true, "gp3"})
		builderJSON := raw(builder)
		if im.Base.OS == "windows" {
			_, pub, err := key(filepath.Join(dir, "build.key"))
			if err != nil {
				return cfg, err
			}
			ud, err := guest.WindowsBuildUserData(pub, c.Environment.OperatorCIDRs)
			if err != nil {
				return cfg, err
			}
			var w map[string]any
			if err = json.Unmarshal(builderJSON, &w); err != nil {
				return cfg, err
			}
			delete(w, "ssh_keypair_name") // Windows EC2 key pairs cannot carry Ed25519; user data installs the SSH key.
			w["communicator"] = "ssh"
			w["ssh_username"] = "Administrator"
			w["ssh_timeout"] = "20m"
			w["ssh_file_transfer_method"] = "sftp"
			w["user_data"] = ud
			w["disable_stop_instance"] = true
			w["windows_password_timeout"] = "20m"
			builderJSON = raw(w)
		}
		cfg.Builders = append(cfg.Builders, builderJSON)
	} else {
		p := c.Environment.Proxmox
		baseID, err := strconv.Atoi(im.Base.ID)
		if err != nil {
			return cfg, err
		}
		if b.Bridge == "" {
			return cfg, fmt.Errorf("Proxmox builds require an operator-owned build bridge")
		}
		builder := struct {
			Type         string `json:"type"`
			Insecure     bool   `json:"insecure_skip_tls_verify"`
			URL          string `json:"proxmox_url"`
			Username     string `json:"username"`
			Token        string `json:"token,omitempty"`
			Node         string `json:"node"`
			Clone        int    `json:"clone_vm_id"`
			ID           int    `json:"vm_id"`
			Name         string `json:"vm_name"`
			Tags         string `json:"tags"`
			Template     string `json:"template_name"`
			Description  string `json:"template_description"`
			Pool         string `json:"pool"`
			Full         bool   `json:"full_clone"`
			Memory       int    `json:"memory"`
			Cores        int    `json:"cores"`
			User         string `json:"ssh_username"`
			CloudInit    bool   `json:"cloud_init"`
			CloudStorage string `json:"cloud_init_storage_pool"`
			NICs         []struct {
				Model  string `json:"model"`
				Bridge string `json:"bridge"`
				VLAN   string `json:"vlan_tag,omitempty"`
			} `json:"network_adapters"`
		}{Type: "proxmox-clone", Insecure: p.InsecureSkipTLSVerify, URL: strings.TrimRight(p.URL, "/") + "/api2/json", Username: p.TokenID, Node: p.Node, Clone: baseID, ID: c.Machine.VMID, Name: "lc-" + c.Owner, Tags: "labcentral-v2;" + c.Owner, Template: "lc-" + c.Owner, Description: "labcentral:v2:" + c.Owner + ":" + im.Name, Pool: p.Pool, Full: true, Memory: 4096, Cores: 2, User: im.Base.User, CloudInit: true, CloudStorage: p.Storage}
		vlan := ""
		if b.VLAN > 0 {
			vlan = fmt.Sprint(b.VLAN)
		}
		builder.NICs = append(builder.NICs, struct {
			Model  string `json:"model"`
			Bridge string `json:"bridge"`
			VLAN   string `json:"vlan_tag,omitempty"`
		}{"virtio", b.Bridge, vlan})
		cfg.Builders = append(cfg.Builders, raw(builder))
	}
	if im.Base.OS == "windows" {
		return windowsProvisioners(c, dir, cfg)
	}
	bootstrap := []string{"set -eu", "sudo env DEBIAN_FRONTEND=noninteractive apt-get update", "sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y python3-venv qemu-guest-agent ca-certificates curl", "sudo python3 -m venv /opt/labcentral-ansible", "sudo /opt/labcentral-ansible/bin/pip install --disable-pip-version-check 'ansible-core==" + b.AnsibleVersion + "'", "sudo systemctl enable qemu-guest-agent"}
	cfg.Provisioners = append(cfg.Provisioners, raw(struct {
		Type   string   `json:"type"`
		Inline []string `json:"inline"`
	}{"shell", bootstrap}))
	labExtensions, imageExtensions := c.Source.Lab.Extensions, c.Source.Lab.Images[c.Image.Name].Extensions
	if labExtensions == nil {
		labExtensions = lc.Extensions{}
	}
	if imageExtensions == nil {
		imageExtensions = lc.Extensions{}
	}
	vars := map[string]json.RawMessage{"labcentral_extensions": raw(map[string]lc.Extensions{"lab": labExtensions, "image": imageExtensions})}
	for _, v := range c.Inputs {
		switch v.Type {
		case "string":
			vars[v.Name] = raw(v.Value)
		case "integer":
			n, err := strconv.ParseInt(v.Value, 10, 64)
			if err != nil {
				return cfg, err
			}
			vars[v.Name] = raw(n)
		case "boolean":
			b, err := strconv.ParseBool(v.Value)
			if err != nil {
				return cfg, err
			}
			vars[v.Name] = raw(b)
		default:
			return cfg, fmt.Errorf("unsupported input type")
		}
	}
	vb, _ := json.Marshal(vars)
	if err := os.WriteFile(filepath.Join(dir, "inputs.json"), vb, 0600); err != nil {
		return cfg, err
	}
	cfg.Provisioners = append(cfg.Provisioners, raw(struct {
		Type        string `json:"type"`
		Source      string `json:"source"`
		Destination string `json:"destination"`
	}{"file", filepath.Join(dir, "inputs.json"), "/tmp/labcentral-inputs.json"}))
	cfg.Provisioners = append(cfg.Provisioners, raw(struct {
		Type      string   `json:"type"`
		Command   string   `json:"command"`
		Playbook  string   `json:"playbook_file"`
		Directory string   `json:"playbook_dir"`
		Arguments []string `json:"extra_arguments"`
		Staging   string   `json:"staging_directory"`
		Clean     bool     `json:"clean_staging_directory"`
	}{"ansible-local", "sudo /opt/labcentral-ansible/bin/ansible-playbook", filepath.Join(dir, "source", im.Playbook), filepath.Join(dir, "source"), []string{"--extra-vars=@/tmp/labcentral-inputs.json"}, "/tmp/labcentral-source", true}))
	if im.Desktop {
		desktop := []string{"set -eu", "sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends xfce4 lightdm dbus-x11 xorg", "printf '%s\\n' 'exec startxfce4' | sudo tee /etc/skel/.xsession >/dev/null"}
		if c.Environment.Platform == "proxmox" {
			desktop = append(desktop, "sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends lightdm-gtk-greeter", "sudo mkdir -p /etc/lightdm/lightdm.conf.d", "printf '%s\\n' '[Seat:*]' 'greeter-session=lightdm-gtk-greeter' 'user-session=xfce' | sudo tee /etc/lightdm/lightdm.conf.d/50-labcentral.conf >/dev/null", "sudo systemctl enable lightdm", "sudo systemctl set-default graphical.target")
		}
		if c.Environment.Platform == "aws" {
			if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(b.DCVSHA256) || !strings.HasPrefix(b.DCVURL, "https://") || strings.ContainsAny(b.DCVURL, "'\n\r ") {
				return cfg, fmt.Errorf("AWS desktop build requires a pinned HTTPS DCV archive")
			}
			desktop = append(desktop, "curl --fail --proto '=https' --tlsv1.2 --max-time 300 '"+b.DCVURL+"' -o /tmp/dcv.tgz", "printf '%s\\n' '"+b.DCVSHA256+"  /tmp/dcv.tgz' | sha256sum -c -", "mkdir /tmp/dcv-package; tar xzf /tmp/dcv.tgz -C /tmp/dcv-package --strip-components=1", "sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y /tmp/dcv-package/*dcv-server*.deb /tmp/dcv-package/*dcv-web-viewer*.deb /tmp/dcv-package/*xdcv*.deb", "printf '%s\\n' '[security]' 'authentication=\"system\"' | sudo tee /etc/dcv/dcv.conf >/dev/null", "sudo systemctl enable dcvserver", "rm -rf /tmp/dcv-package /tmp/dcv.tgz")
		}
		cfg.Provisioners = append(cfg.Provisioners, raw(struct {
			Type   string   `json:"type"`
			Inline []string `json:"inline"`
		}{"shell", desktop}))
	}
	cfg.Provisioners = append(cfg.Provisioners, raw(struct {
		Type   string   `json:"type"`
		Inline []string `json:"inline"`
	}{"shell", []string{"set -eu", "sudo rm -f /tmp/labcentral-inputs.json", "sudo rm -rf /tmp/labcentral-source", "sudo find /root /home -type f -name authorized_keys -delete", "sudo rm -f /etc/ssh/ssh_host_*", "if systemctl list-unit-files dcvserver.service --no-legend | grep -q dcvserver; then sudo systemctl stop dcvserver; sudo rm -f /etc/dcv/dcv.pem /etc/dcv/dcv.key /var/lib/dcv/.config/NICE/dcv/private/dcv.pem /var/lib/dcv/.config/NICE/dcv/private/dcv.key; fi", "sudo cloud-init clean --logs --machine-id", "sudo sync"}}))
	return cfg, nil
}
func (r *Runner) Run(ctx context.Context, c lc.Command, o lc.Observer) (lc.Result, error) {
	if r.Find == nil || r.Validate == nil || r.Cleanup == nil {
		return lc.Result{}, fmt.Errorf("native build reconciliation and validation are required")
	}
	artifact, found, err := r.Find(ctx, c)
	if err != nil {
		return lc.Result{}, err
	}
	dir := filepath.Join(r.Root, c.Owner)
	if !found {
		if c.Recovery {
			if err = r.Cleanup(ctx, c); err != nil {
				return lc.Result{}, err
			}
			// Absence is a positive reconciliation result, not an inference from
			// a failed Packer run. Callers may replace this attempt only after
			// abandonment confirms all remaining cleanup.
			return lc.Result{Absent: true}, nil
		}
		if err = stageImageSource(c, dir); err != nil {
			return lc.Result{}, err
		}
		pin, err := hashFile(c.Environment.Build.Packer)
		if err != nil || pin != c.Environment.Build.PackerSHA256 {
			return lc.Result{}, fmt.Errorf("Packer executable checksum mismatch")
		}
		fp, err := FingerprintPlugins(c.Environment.Build.PluginDirectory)
		if err != nil || fp != c.Environment.Build.ToolchainID {
			return lc.Result{}, fmt.Errorf("Packer plugin directory fingerprint mismatch")
		}

		cfg, err := Generate(c, dir)
		if err != nil {
			return lc.Result{}, err
		}
		if r.Configure != nil {
			if err = r.Configure(ctx, c, &cfg); err != nil {
				return lc.Result{}, err
			}
		}
		if r.Prepare != nil {
			_, pub, err := key(filepath.Join(dir, "build.key"))
			if err != nil {
				return lc.Result{}, err
			}
			if err = r.Prepare(ctx, c, pub); err != nil {
				return lc.Result{}, err
			}
		}
		b, _ := json.MarshalIndent(cfg, "", "  ")
		path := filepath.Join(dir, "build.json")
		if err = os.WriteFile(path, b, 0600); err != nil {
			return lc.Result{}, err
		}
		ctx, cancel := context.WithTimeout(ctx, time.Duration(c.Environment.Build.TimeoutSeconds)*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, c.Environment.Build.Packer, "build", "-color=false", "-on-error=cleanup", path)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PACKER_PLUGIN_PATH="+c.Environment.Build.PluginDirectory, "PACKER_NO_COLOR=1")
		if c.Environment.Platform == "proxmox" {
			cmd.Env = append(cmd.Env, "PROXMOX_TOKEN="+os.Getenv(c.Environment.Proxmox.TokenEnv))
			if c.Environment.Proxmox.CAFile != "" {
				cmd.Env = append(cmd.Env, "SSL_CERT_FILE="+c.Environment.Proxmox.CAFile)
			}
		}
		stopWithGroup(cmd)
		cmd.WaitDelay = 10 * time.Second
		log, err := os.OpenFile(filepath.Join(dir, "packer.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return lc.Result{}, err
		}
		limited := &limitWriter{W: log, N: 8 << 20}
		cmd.Stdout = limited
		cmd.Stderr = limited
		if o != nil {
			o(lc.Event{Version: 2, Stage: "packer", Resource: c.Image.Name, Message: "native image build started"})
		}
		err = cmd.Run()
		log.Close()
		if err != nil {
			return lc.Result{}, fmt.Errorf("Packer build failed; see protected local log and recover native resources: %w", err)
		}
		artifact, found, err = r.Find(ctx, c)
		if err != nil {
			return lc.Result{}, err
		}
		if !found {
			return lc.Result{}, fmt.Errorf("native image was not found after Packer completed")
		}
	}
	validated, err := r.Validate(ctx, c, artifact)
	if err != nil {
		return lc.Result{}, err
	}
	if err = r.Cleanup(ctx, c); err != nil {
		return lc.Result{}, fmt.Errorf("build cleanup unconfirmed: %w", err)
	}
	validated.Complete = true
	return lc.Result{Resource: validated}, nil
}

type limitWriter struct {
	W io.Writer
	N int64
}

func (w *limitWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w.N > 0 {
		b := p
		if int64(len(b)) > w.N {
			b = b[:w.N]
		}
		k, e := w.W.Write(b)
		w.N -= int64(k)
		if e != nil {
			return k, e
		}
	}
	return n, nil
}

func stageImageSource(c lc.Command, dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "source"), 0700); err != nil {
		return err
	}
	for _, f := range c.Source.Files {
		selected := c.Source.Lab.Images[c.Image.Name].BuildFiles()
		if len(selected) > 0 && !lc.Contains(selected, f.Path) {
			continue
		}
		p := filepath.Join(dir, "source", filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(p, f.Content, 0600); err != nil {
			return err
		}
	}
	return nil
}
