// Package vm implements builders whose only author-code execution boundary is
// an individually bounded disposable QEMU guest on a dedicated builder host.
package vm

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
	"github.com/distribution/reference"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

const AnsibleSchema = "labcentral.ansible.v1"
const DockerfileSchema = "labcentral.dockerfile.v1"
const QcowMedia = "application/x-qemu-disk"
const OCIMedia = "application/vnd.oci.image.layout.v1.tar"

// Recipes deliberately do not accept Packer HCL, CLI switches, host paths,
// registry credentials, provisioner plugins, or a custom Dockerfile frontend.
type AnsibleSettings struct {
	BaseURL        string `json:"baseURL,omitempty"`
	BaseSHA256     string `json:"baseSHA256,omitempty"`
	BaseDependency string `json:"baseDependency,omitempty"`
	Playbook       string `json:"playbook"`
	DiskGiB        uint32 `json:"diskGiB"`
	GuestUser      string `json:"guestUser"`
}
type DockerfileSettings struct {
	Dockerfile string `json:"dockerfile"`
	Target     string `json:"target,omitempty"`
}
type SourceFile struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
}
type SourceContext struct {
	Version uint32                 `json:"version"`
	Files   []SourceFile           `json:"files"`
	Inputs  []labbundle.BuildInput `json:"inputs"`
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var guestUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func strictJSON(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("invalid typed configuration: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("trailing configuration")
	}
	return nil
}
func digestValid(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && value == strings.ToLower(value)
}
func sourcePath(value string) bool {
	return len(value) > 0 && len(value) <= 240 && (value != "." && value != ".." && !strings.HasPrefix(value, "../") && !strings.HasPrefix(value, "/")) && path.Clean(value) == value && !strings.ContainsAny(value, "\\\x00\r\n")
}
func ParseContext(data []byte) (SourceContext, error) {
	var out SourceContext
	if len(data) > 12<<20 {
		return out, fmt.Errorf("context too large")
	}
	if err := strictJSON(data, &out); err != nil {
		return out, err
	}
	if out.Version != 1 || len(out.Files) == 0 || len(out.Files) > 64 || len(out.Inputs) > 64 {
		return out, fmt.Errorf("invalid context bounds")
	}
	seen := map[string]bool{}
	total := 0
	for _, file := range out.Files {
		if !sourcePath(file.Path) || seen[file.Path] {
			return out, fmt.Errorf("invalid context path")
		}
		seen[file.Path] = true
		total += len(file.Data)
		if total > 8<<20 {
			return out, fmt.Errorf("expanded context too large")
		}
	}
	seen = map[string]bool{}
	for _, input := range out.Inputs {
		if !safeName.MatchString(input.Name) || seen[input.Name] || len(input.Value) > 4096 || strings.ContainsRune(input.Value, 0) {
			return out, fmt.Errorf("invalid declared input")
		}
		seen[input.Name] = true
	}
	return out, nil
}
func ValidateRecipe(recipe *wire.BuildRecipe, limits *wire.BuildLimits) error {
	if recipe == nil || recipe.Architecture != "amd64" || !digestValid(recipe.Digest) || recipe.Configuration.GetSchema().GetMajor() != 1 || len(recipe.Configuration.CanonicalJson) > 64<<10 {
		return fmt.Errorf("invalid Linux amd64 recipe")
	}
	if limits == nil || limits.TimeoutSeconds < 30 || limits.TimeoutSeconds > 7200 || limits.CpuMillis < 1000 || limits.CpuMillis > 16000 || limits.MemoryBytes < 512<<20 || limits.MemoryBytes > 32<<30 || limits.MaximumInputBytes == 0 || limits.MaximumInputBytes > 32<<30 || limits.MaximumOutputBytes == 0 || limits.MaximumOutputBytes > 32<<30 || len(limits.AllowedEgress) > 64 {
		return fmt.Errorf("invalid build limits")
	}
	for _, host := range limits.AllowedEgress {
		if !validEgressHost(host) {
			return fmt.Errorf("invalid egress host")
		}
	}
	if len(recipe.Dependencies) > 8 || len(recipe.BaseImageDigests) > 32 || len(recipe.Inputs) != 1 {
		return fmt.Errorf("invalid recipe input bounds")
	}
	if recipe.Inputs[0].GetSize() == 0 || recipe.Inputs[0].GetSize() > 12<<20 || !digestValid(recipe.Inputs[0].GetDigest()) || recipe.Inputs[0].GetMediaType() != "application/vnd.labcentral.build-context+json" {
		return fmt.Errorf("invalid source context")
	}
	deps := map[string]*wire.ArtifactHandle{}
	for _, dep := range recipe.Dependencies {
		if dep == nil || !safeName.MatchString(dep.RecipeId) || deps[dep.RecipeId] != nil || !digestValid(dep.Artifact.GetDigest()) || dep.Artifact.GetSize() == 0 {
			return fmt.Errorf("invalid dependency")
		}
		deps[dep.RecipeId] = dep.Artifact
	}
	for _, digest := range recipe.BaseImageDigests {
		if !digestValid(strings.TrimPrefix(digest, "sha256:")) {
			return fmt.Errorf("invalid base digest")
		}
	}
	switch recipe.Configuration.Schema.Name {
	case AnsibleSchema:
		if limits.MemoryBytes < 2<<30 {
			return fmt.Errorf("Ansible requires at least 2 GiB guest memory")
		}
		var config AnsibleSettings
		if err := strictJSON(recipe.Configuration.CanonicalJson, &config); err != nil {
			return err
		}
		if !sourcePath(config.Playbook) || config.DiskGiB < 2 || config.DiskGiB > 64 || (!guestUser.MatchString(config.GuestUser) || config.GuestUser == "root") {
			return fmt.Errorf("invalid Ansible settings")
		}
		if config.BaseDependency != "" {
			if config.BaseURL != "" || config.BaseSHA256 != "" || deps[config.BaseDependency] == nil || deps[config.BaseDependency].MediaType != QcowMedia {
				return fmt.Errorf("invalid VM base dependency")
			}
		} else {
			u, err := url.Parse(config.BaseURL)
			if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || (u.Port() != "" && u.Port() != "443") || !slices.Contains(limits.AllowedEgress, u.Hostname()) || !digestValid(config.BaseSHA256) || !declaresDigest(recipe.BaseImageDigests, config.BaseSHA256) {
				return fmt.Errorf("HTTPS base must be allowlisted and digest pinned")
			}
		}
		if len(deps) > 1 || (len(deps) == 1 && config.BaseDependency == "") {
			return fmt.Errorf("Ansible accepts only its declared base dependency")
		}
	case DockerfileSchema:
		var config DockerfileSettings
		if err := strictJSON(recipe.Configuration.CanonicalJson, &config); err != nil {
			return err
		}
		if !sourcePath(config.Dockerfile) || (config.Target != "" && !safeName.MatchString(config.Target)) {
			return fmt.Errorf("invalid Dockerfile settings")
		}
		for _, dep := range deps {
			if dep.MediaType != OCIMedia {
				return fmt.Errorf("Dockerfile requires OCI dependencies")
			}
		}
	default:
		return fmt.Errorf("unsupported builder schema")
	}
	return nil
}

// ValidateDockerfile uses the same parser as the pinned builder. In particular,
// alternate FROM/COPY/RUN-mount image sources cannot evade base pinning.
func ValidateDockerfile(data []byte, baseDigests []string, dependencies []string) error {
	if _, _, _, found := parser.DetectSyntax(data); found {
		return fmt.Errorf("custom Dockerfile frontends are not supported")
	}
	parsed, err := parser.Parse(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("invalid Dockerfile: %w", err)
	}
	stages, _, err := instructions.Parse(parsed.AST, nil)
	if err != nil {
		return err
	}
	if len(stages) == 0 || len(stages) > 64 {
		return fmt.Errorf("invalid Dockerfile stage count")
	}
	prior := map[string]bool{}
	image := func(value string) error {
		if value == "scratch" || prior[strings.ToLower(value)] || slices.Contains(dependencies, value) {
			return nil
		}
		ref, err := reference.ParseNormalizedNamed(value)
		if err != nil {
			return fmt.Errorf("image source must be a literal pinned reference")
		}
		pinned, ok := ref.(reference.Digested)
		if !ok || pinned.Digest().Algorithm() != "sha256" || !declaresDigest(baseDigests, pinned.Digest().String()) {
			return fmt.Errorf("image source is not declared and digest pinned")
		}
		return nil
	}
	for i, stage := range stages {
		if stage.Platform != "" && stage.Platform != "linux/amd64" {
			return fmt.Errorf("only literal linux/amd64 stages supported")
		}
		if err = image(stage.BaseName); err != nil {
			return err
		}
		for _, command := range stage.Commands {
			switch c := command.(type) {
			case *instructions.CopyCommand:
				if c.From != "" {
					if err = image(c.From); err != nil {
						return err
					}
				}
			case *instructions.RunCommand:
				for _, m := range instructions.GetMounts(c) {
					if m.From != "" {
						if err = image(m.From); err != nil {
							return err
						}
					}
				}
			case *instructions.AddCommand:
				for _, src := range c.SourcePaths {
					if strings.Contains(src, "://") || strings.HasPrefix(src, "git@") {
						return fmt.Errorf("vendor remote ADD sources into the source package")
					}
				}
			}
		}
		prior[strconv.Itoa(i)] = true
		if stage.Name != "" {
			if slices.Contains(dependencies, stage.Name) {
				return fmt.Errorf("stage shadows dependency")
			}
			prior[strings.ToLower(stage.Name)] = true
		}
	}
	return nil
}

func declaresDigest(declared []string, value string) bool {
	value = strings.TrimPrefix(value, "sha256:")
	for _, digest := range declared {
		if strings.TrimPrefix(digest, "sha256:") == value {
			return true
		}
	}
	return false
}
