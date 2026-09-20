package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"go.yaml.in/yaml/v3"
)

// Packer receives only a generated configuration. Both its QEMU child and all
// ansible-local controller code run inside the disposable outer VM.
func executeAnsible(ctx context.Context, root string, recipe *wire.BuildRecipe, source SourceContext, limits *wire.BuildLimits) (string, string, error) {
	var settings AnsibleSettings
	_ = strictJSON(recipe.Configuration.CanonicalJson, &settings)
	if _, err := os.Stat(filepath.Join(root, "context", settings.Playbook)); err != nil {
		return "", "", err
	}
	base := filepath.Join(root, "base.qcow2")
	if settings.BaseDependency != "" {
		for _, dep := range recipe.Dependencies {
			if dep.RecipeId == settings.BaseDependency {
				base = filepath.Join(root, "inputs", dep.Artifact.Digest)
			}
		}
	} else if err := downloadBase(ctx, settings.BaseURL, settings.BaseSHA256, base, limits.MaximumInputBytes); err != nil {
		return "", "", err
	}
	key := filepath.Join(root, "builder-key")
	if err := guestCommand(ctx, root, "/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key); err != nil {
		return "", "", err
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		return "", "", err
	}
	cloud := struct {
		Users           []any `yaml:"users"`
		SSHPasswordAuth bool  `yaml:"ssh_pwauth"`
		DisableRoot     bool  `yaml:"disable_root"`
	}{Users: []any{map[string]any{"name": settings.GuestUser, "sudo": "ALL=(ALL) NOPASSWD:ALL", "shell": "/bin/bash", "lock_passwd": true, "ssh_authorized_keys": []string{strings.TrimSpace(string(pub))}}}, DisableRoot: true}
	data, err := yaml.Marshal(cloud)
	if err != nil {
		return "", "", err
	}
	if err = os.WriteFile(filepath.Join(root, "user-data"), append([]byte("#cloud-config\n"), data...), 0600); err != nil {
		return "", "", err
	}
	if err = os.WriteFile(filepath.Join(root, "meta-data"), []byte("instance-id: labcentral-image-build\nlocal-hostname: labcentral-image-build\n"), 0600); err != nil {
		return "", "", err
	}
	seed := filepath.Join(root, "seed.iso")
	if err = guestCommand(ctx, root, "/usr/bin/cloud-localds", seed, filepath.Join(root, "user-data"), filepath.Join(root, "meta-data")); err != nil {
		return "", "", err
	}
	inputs := map[string]string{}
	for _, input := range source.Inputs {
		inputs[input.Name] = input.Value
	}
	data, _ = json.Marshal(inputs)
	if err = os.WriteFile(filepath.Join(root, "inputs.json"), data, 0600); err != nil {
		return "", "", err
	}
	// The pinned toolchain supplies a hash-locked offline wheelhouse and pip.pyz.
	// Target bases must have Python 3; no package-manager bootstrap runs unpinned.
	wrapper := "#!/bin/sh\nexport PYTHONPATH=/opt/labcentral-build/ansible\nexport HTTP_PROXY=" + guestProxy + " HTTPS_PROXY=" + guestProxy + " http_proxy=" + guestProxy + " https_proxy=" + guestProxy + "\nexec /usr/bin/python3 /opt/labcentral-build/ansible/bin/ansible-playbook \"$@\"\n"
	if err = os.WriteFile(filepath.Join(root, "ansible-playbook"), []byte(wrapper), 0700); err != nil {
		return "", "", err
	}
	outputDir := filepath.Join(root, "packer-output")
	config := map[string]any{
		"builders": []any{map[string]any{
			"type": "qemu", "name": "labcentral", "qemu_binary": "/usr/bin/qemu-system-x86_64", "accelerator": "tcg", "headless": true, "disk_image": true, "iso_url": base, "iso_checksum": "none", "format": "qcow2", "disk_size": strconv.FormatUint(uint64(settings.DiskGiB)*1024, 10), "memory": min(uint64(2048), limits.MemoryBytes/(2<<20)), "cpus": max(uint32(1), limits.CpuMillis/1000), "output_directory": outputDir, "vm_name": "image.qcow2", "ssh_username": settings.GuestUser, "ssh_private_key_file": key, "ssh_timeout": "20m", "shutdown_command": "sudo shutdown -P now", "shutdown_timeout": "5m",
			"qemuargs": [][]string{{"-cpu", "max"}, {"-netdev", "user,id=user.0,net=10.0.3.0/24,hostfwd=tcp:127.0.0.1:{{ .SSHHostPort }}-:22"}, {"-drive", "file=" + filepath.Join(outputDir, "image.qcow2") + ",format=qcow2,if=virtio,cache=writeback,index=0"}, {"-drive", "file=" + seed + ",format=raw,if=virtio,readonly=on,index=1"}},
		}},
		"provisioners": []any{
			map[string]any{"type": "file", "source": "/opt/labcentral/offline-ansible", "destination": "/tmp/offline-ansible"},
			map[string]any{"type": "file", "source": filepath.Join(root, "ansible-playbook"), "destination": "/tmp/labcentral-ansible"},
			map[string]any{"type": "file", "source": filepath.Join(root, "inputs.json"), "destination": "/tmp/labcentral-inputs.json"},
			map[string]any{"type": "shell", "inline": []string{"sudo mkdir -p /opt/labcentral-build/ansible", "sudo env PYTHONPATH=/tmp/offline-ansible/pip python3 -m pip install --no-index --require-hashes --find-links=/tmp/offline-ansible/wheels --target=/opt/labcentral-build/ansible -r /tmp/offline-ansible/requirements.txt", "chmod 700 /tmp/labcentral-ansible"}},
			map[string]any{"type": "ansible-local", "playbook_file": filepath.Join(root, "context", settings.Playbook), "playbook_dir": filepath.Join(root, "context"), "command": "/tmp/labcentral-ansible", "extra_arguments": []string{"--extra-vars=@/tmp/labcentral-inputs.json"}, "clean_staging_directory": true},
		},
	}
	data, err = json.Marshal(config)
	if err != nil {
		return "", "", err
	}
	configPath := filepath.Join(root, "packer.json")
	if err = os.WriteFile(configPath, data, 0600); err != nil {
		return "", "", err
	}
	if err = guestCommand(ctx, root, "/usr/local/bin/packer", "build", "-color=false", configPath); err != nil {
		return "", "", err
	}
	output := filepath.Join(outputDir, "image.qcow2")
	// Offline sanitization is still inside the disposable build guest. Author
	// disks are never parsed or mounted by the worker host.
	if err = guestCommand(ctx, root, "/usr/bin/virt-sysprep", "-a", output, "--operations", "customize,ssh-hostkeys,ssh-userdir,machine-id,logfiles,net-hwaddr,udev-persistent-net", "--no-logfile", "--delete", "/var/lib/cloud", "--delete", "/tmp/*", "--delete", "/opt/labcentral-build", "--delete", "/var/tmp/*"); err != nil {
		return "", "", fmt.Errorf("image identity cleanup failed")
	}
	// Verify the deletion postcondition inside the disposable VM. In particular,
	// sysprep ignores --delete when its customize operation is disabled.
	check := exec.CommandContext(ctx, "/usr/bin/guestfish", "--ro", "-a", output, "-i")
	check.Env = guestEnvironment()
	check.Stdin = strings.NewReader("exists /var/lib/cloud\nexists /opt/labcentral-build\nexists /etc/ssh/ssh_host_ed25519_key\nexists /etc/ssh/ssh_host_rsa_key\nexists /home/" + settings.GuestUser + "/.ssh\n")
	checked, err := check.Output()
	if err != nil || string(checked) != "false\nfalse\nfalse\nfalse\nfalse\n" {
		return "", "", fmt.Errorf("image cleanup verification failed")
	}
	// Convert in the guest to guarantee a standalone image without a backing path.
	standalone := filepath.Join(root, "result.qcow2")
	if err = guestCommand(ctx, root, "/usr/bin/qemu-img", "convert", "-f", "qcow2", "-O", "qcow2", output, standalone); err != nil {
		return "", "", err
	}
	return standalone, QcowMedia, nil
}
