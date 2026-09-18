package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/pkg/containervm"
	"github.com/OutboundSpade/LabCentral/pkg/ocilayout"
	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/proto"
)

const runtimeBase = "/opt/labcentral/container-runtime.qcow2"

// This fixed recipe runs only in the disposable outer guest. Podman expands OCI
// layers in Packer's inner guest; neither worker nor provider hosts parse disks.
func executeContainerRuntime(ctx context.Context, root string, recipe *wire.BuildRecipe, source SourceContext, limits *wire.BuildLimits) (string, string, error) {
	var settings containervm.RuntimeBuildSettings
	if err := strictJSON(recipe.Configuration.CanonicalJson, &settings); err != nil {
		return "", "", err
	}
	if len(source.Inputs) != 0 || len(source.Files) != 1 || source.Files[0].Path != "runtime.json" || string(source.Files[0].Data) != "{}" {
		return "", "", fmt.Errorf("runtime packager accepts only its fixed source context")
	}
	if err := verifyPinned(PinnedFile{Path: runtimeBase, SHA256: settings.Policy.BaseSHA256}); err != nil {
		return "", "", err
	}
	input := filepath.Join(root, "inputs", recipe.Dependencies[0].Artifact.Digest)
	file, err := os.Open(input)
	if err != nil {
		return "", "", err
	}
	identity, err := ocilayout.Inspect(file, recipe.Dependencies[0].Artifact.Size)
	file.Close()
	if err != nil || identity != settings.Image {
		return "", "", fmt.Errorf("OCI source identity mismatch")
	}
	if err = os.Link(input, filepath.Join(root, "context", "image.oci.tar")); err != nil {
		return "", "", err
	}
	if err = os.Link(runtimeBase, filepath.Join(root, "inputs", settings.Policy.BaseSHA256)); err != nil {
		return "", "", err
	}
	environment := map[string]string{"HOME": "/home/labworkload", "XDG_RUNTIME_DIR": "/run/user/1001"}
	command := func(args ...string) map[string]any {
		return map[string]any{"ansible.builtin.command": map[string]any{"argv": append([]string{"/usr/sbin/runuser", "-u", "labworkload", "--", "/usr/bin/podman"}, args...)}, "environment": environment}
	}
	tasks := []any{
		map[string]any{"ansible.builtin.stat": map[string]any{"path": containervm.Entrypoint, "checksum_algorithm": "sha256"}, "register": "runtime_agent"},
		map[string]any{"ansible.builtin.assert": map[string]any{"that": []string{"runtime_agent.stat.checksum == '" + settings.Policy.AgentSHA256 + "'"}}},
		map[string]any{"ansible.builtin.file": map[string]any{"path": "/run/user/1001", "state": "directory", "owner": "labworkload", "group": "labworkload", "mode": "0700"}},
		map[string]any{"ansible.builtin.copy": map[string]any{"src": "image.oci.tar", "dest": "/var/tmp/labcentral-runtime.oci.tar", "owner": "labworkload", "group": "labworkload", "mode": "0400"}},
		command("load", "--input", "/var/tmp/labcentral-runtime.oci.tar"),
		command("image", "exists", settings.Image.ConfigDigest),
	}
	for _, path := range []string{"/var/tmp/labcentral-runtime.oci.tar", "/home/labworkload/.config/containers/auth.json", "/run/user/1001/containers/auth.json", "/root/.config/containers/auth.json"} {
		tasks = append(tasks, map[string]any{"ansible.builtin.file": map[string]any{"path": path, "state": "absent"}})
	}
	playbook, err := yaml.Marshal([]any{map[string]any{"hosts": "all", "become": true, "gather_facts": false, "tasks": tasks}})
	if err != nil {
		return "", "", err
	}
	if err = os.WriteFile(filepath.Join(root, "context", "runtime.yml"), playbook, 0600); err != nil {
		return "", "", err
	}
	inner := proto.Clone(recipe).(*wire.BuildRecipe)
	inner.Configuration.Schema.Name = AnsibleSchema
	inner.Configuration.CanonicalJson, _ = json.Marshal(AnsibleSettings{BaseDependency: "runtime-base", Playbook: "runtime.yml", DiskGiB: settings.Policy.DiskGiB, GuestUser: "debian"})
	inner.Dependencies = []*wire.BuildDependency{{RecipeId: "runtime-base", Artifact: &wire.ArtifactHandle{Digest: settings.Policy.BaseSHA256, MediaType: QcowMedia}}}
	return executeAnsible(ctx, root, inner, SourceContext{Version: 1}, limits)
}
