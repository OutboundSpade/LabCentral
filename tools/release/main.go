// Command release builds a deterministic native LabCentral distribution.
package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	dockerPluginID  = "io.labcentral.provider.docker"
	proxmoxPluginID = "io.labcentral.provider.proxmox"
	accessPluginID  = "io.labcentral.access.novnc"
	pluginVersion   = "0.1.0"
)

type config struct {
	outputDir string
	version   string
	goos      string
	goarch    string
	epoch     int64
	goBinary  string
}

type sourceInfo struct {
	Repository string `json:"repository,omitempty"`
	Revision   string `json:"revision"`
	Dirty      bool   `json:"dirty"`
}

type subject struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type module struct {
	Path    string
	Version string
	Sum     string
	Main    bool
	Replace *module
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	cfg, err := parseConfig(args)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.outputDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	source, err := inspectSource(ctx)
	if err != nil {
		return err
	}
	if source.Dirty {
		return errors.New("production release requires a clean Git worktree so source provenance matches the built binaries")
	}
	goVersion, err := commandOutput(ctx, cfg.goBinary, "version")
	if err != nil {
		return fmt.Errorf("read Go version: %w", err)
	}
	modules, err := listModules(ctx, cfg.goBinary)
	if err != nil {
		return err
	}

	baseName := fmt.Sprintf("labcentral-%s-%s-%s", cfg.version, cfg.goos, cfg.goarch)
	temporary, err := os.MkdirTemp("", "labcentral-release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	root := filepath.Join(temporary, baseName)
	for _, directory := range []string{"plugins", "deploy", "deploy/kubernetes", "docs", "docs/operations"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			return err
		}
	}
	proxmoxSourceDigest, err := gitArchiveDigest(ctx)
	if err != nil {
		return err
	}

	extension := ""
	if cfg.goos == "windows" {
		extension = ".exe"
	}
	labdName := "labd" + extension
	dockerProviderName := "lab-provider-docker" + extension
	proxmoxProviderName := "lab-provider-proxmox" + extension
	accessProviderName := "lab-access-novnc" + extension
	qualificationName := "lab-proxmox-qualification" + extension
	recoveryName := "labcentral-recovery" + extension
	if err := buildBinary(ctx, cfg, "./cmd/labd", filepath.Join(root, labdName), "-X=main.version="+cfg.version); err != nil {
		return err
	}
	if err := buildBinary(ctx, cfg, "./cmd/lab-provider-docker", filepath.Join(root, dockerProviderName), ""); err != nil {
		return err
	}
	if err := buildBinary(ctx, cfg, "./cmd/lab-provider-proxmox", filepath.Join(root, proxmoxProviderName), "-X=main.sourceTreeDigest="+proxmoxSourceDigest); err != nil {
		return err
	}
	if err := buildBinary(ctx, cfg, "./cmd/lab-access-novnc", filepath.Join(root, accessProviderName), ""); err != nil {
		return err
	}
	if err := buildBinary(ctx, cfg, "./tools/proxmox-qualification", filepath.Join(root, qualificationName), ""); err != nil {
		return err
	}
	if err := buildBinary(ctx, cfg, "./tools/recovery", filepath.Join(root, recoveryName), ""); err != nil {
		return err
	}
	labdDigest, err := fileSHA256(filepath.Join(root, labdName))
	if err != nil {
		return err
	}
	dockerProviderDigest, err := fileSHA256(filepath.Join(root, dockerProviderName))
	if err != nil {
		return err
	}
	proxmoxProviderDigest, err := fileSHA256(filepath.Join(root, proxmoxProviderName))
	if err != nil {
		return err
	}
	accessProviderDigest, err := fileSHA256(filepath.Join(root, accessProviderName))
	if err != nil {
		return err
	}
	qualificationDigest, err := fileSHA256(filepath.Join(root, qualificationName))
	if err != nil {
		return err
	}
	recoveryDigest, err := fileSHA256(filepath.Join(root, recoveryName))
	if err != nil {
		return err
	}
	for name, digest := range map[string]string{dockerProviderName: dockerProviderDigest, proxmoxProviderName: proxmoxProviderDigest, accessProviderName: accessProviderDigest} {
		if err := writeFile(filepath.Join(root, name+".sha256"), []byte(digest+"  "+name+"\n"), 0o644); err != nil {
			return err
		}
	}
	if err := writeFile(filepath.Join(root, proxmoxProviderName+".source.sha256"), []byte(proxmoxSourceDigest+"\n"), 0o644); err != nil {
		return err
	}

	created := time.Unix(cfg.epoch, 0).UTC()
	type pluginManifest struct {
		APIVersion           string   `json:"apiVersion"`
		ID                   string   `json:"id"`
		Version              string   `json:"version"`
		Executable           string   `json:"executable"`
		SHA256               string   `json:"sha256"`
		Categories           []string `json:"categories"`
		ProviderKinds        []string `json:"providerKinds,omitempty"`
		AccessKinds          []string `json:"accessKinds,omitempty"`
		AccessMethods        []string `json:"accessMethods,omitempty"`
		EndpointSchemas      []string `json:"endpointSchemas,omitempty"`
		ProtocolMajor        int      `json:"protocolMajor"`
		MinimumProtocolMinor int      `json:"minimumProtocolMinor"`
		MaximumProtocolMinor int      `json:"maximumProtocolMinor"`
		OperatingSystems     []string `json:"operatingSystems"`
		Architectures        []string `json:"architectures"`
		RequestedPermissions []string `json:"requestedPermissions"`
	}
	dockerManifest := pluginManifest{
		APIVersion: "labcentral.io/plugin-manifest/v1", ID: dockerPluginID, Version: pluginVersion,
		Executable: "../" + dockerProviderName, SHA256: dockerProviderDigest, Categories: []string{"provider"},
		ProviderKinds: []string{"docker"}, ProtocolMajor: 1, MinimumProtocolMinor: 0, MaximumProtocolMinor: 0,
		OperatingSystems: []string{cfg.goos}, Architectures: []string{cfg.goarch},
		RequestedPermissions: []string{"docker-engine-endpoint"},
	}
	if err := writeJSON(filepath.Join(root, "plugins", "docker.json"), dockerManifest); err != nil {
		return err
	}
	proxmoxManifest := pluginManifest{
		APIVersion: "labcentral.io/plugin-manifest/v1", ID: proxmoxPluginID, Version: pluginVersion,
		Executable: "../" + proxmoxProviderName, SHA256: proxmoxProviderDigest, Categories: []string{"provider"},
		ProviderKinds: []string{"proxmox"}, ProtocolMajor: 1, MinimumProtocolMinor: 0, MaximumProtocolMinor: 1,
		OperatingSystems: []string{cfg.goos}, Architectures: []string{cfg.goarch},
		RequestedPermissions: []string{"proxmox-api-endpoint", "qualification-evidence-read", "invocation-journal-write"},
	}
	if err := writeJSON(filepath.Join(root, "plugins", "proxmox.json"), proxmoxManifest); err != nil {
		return err
	}
	accessManifest := pluginManifest{
		APIVersion: "labcentral.io/plugin-manifest/v1", ID: accessPluginID, Version: pluginVersion,
		Executable: "../" + accessProviderName, SHA256: accessProviderDigest, Categories: []string{"access"},
		AccessKinds: []string{"browser-vnc"}, AccessMethods: []string{"vnc"}, EndpointSchemas: []string{"proxmox.vnc-endpoint/v1.0"},
		ProtocolMajor: 1, MinimumProtocolMinor: 2, MaximumProtocolMinor: 2,
		OperatingSystems: []string{cfg.goos}, Architectures: []string{cfg.goarch},
		RequestedPermissions: []string{"loopback-http-listener", "proxmox-vnc-tls-endpoint", "private-core-authorization-callback"},
	}
	if err := writeJSON(filepath.Join(root, "plugins", "access-novnc.json"), accessManifest); err != nil {
		return err
	}

	sbom := makeSBOM(cfg, created, modules, labdName, labdDigest, []subject{
		{Name: dockerProviderName, SHA256: dockerProviderDigest},
		{Name: proxmoxProviderName, SHA256: proxmoxProviderDigest},
		{Name: accessProviderName, SHA256: accessProviderDigest},
		{Name: qualificationName, SHA256: qualificationDigest},
		{Name: recoveryName, SHA256: recoveryDigest},
	})
	if err := writeJSON(filepath.Join(root, "sbom.cdx.json"), sbom); err != nil {
		return err
	}
	provenance := struct {
		FormatVersion int        `json:"formatVersion"`
		Created       string     `json:"created"`
		Release       string     `json:"release"`
		Source        sourceInfo `json:"source"`
		Builder       struct {
			GoVersion string `json:"goVersion"`
		} `json:"builder"`
		Target struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
			CGOEnabled   bool   `json:"cgoEnabled"`
		} `json:"target"`
		Subjects []subject `json:"subjects"`
	}{FormatVersion: 1, Created: created.Format(time.RFC3339), Release: cfg.version, Source: source}
	provenance.Builder.GoVersion = goVersion
	provenance.Target.OS = cfg.goos
	provenance.Target.Architecture = cfg.goarch
	provenance.Subjects = []subject{
		{Name: labdName, SHA256: labdDigest},
		{Name: dockerProviderName, SHA256: dockerProviderDigest},
		{Name: proxmoxProviderName, SHA256: proxmoxProviderDigest},
		{Name: accessProviderName, SHA256: accessProviderDigest},
		{Name: qualificationName, SHA256: qualificationDigest},
		{Name: recoveryName, SHA256: recoveryDigest},
	}
	if err := writeJSON(filepath.Join(root, "provenance.json"), provenance); err != nil {
		return err
	}

	readme := releaseReadme(cfg.version, cfg.goos, cfg.goarch, labdName, dockerProviderName, proxmoxProviderName, accessProviderName, recoveryName)
	if err := writeFile(filepath.Join(root, "README.md"), []byte(readme), 0o644); err != nil {
		return err
	}
	for sourcePath, destination := range map[string]string{
		"deploy/observability/prometheus-alerts.yaml":        "deploy/prometheus-alerts.yaml",
		"deploy/kubernetes/config.example.yaml":              "deploy/kubernetes/config.example.yaml",
		"deploy/kubernetes/disruption-budget.yaml":           "deploy/kubernetes/disruption-budget.yaml",
		"deploy/kubernetes/kustomization.yaml":               "deploy/kubernetes/kustomization.yaml",
		"deploy/kubernetes/namespace.yaml":                   "deploy/kubernetes/namespace.yaml",
		"deploy/kubernetes/network-policies.yaml":            "deploy/kubernetes/network-policies.yaml",
		"deploy/kubernetes/secrets.example.yaml":             "deploy/kubernetes/secrets.example.yaml",
		"deploy/kubernetes/service-account.yaml":             "deploy/kubernetes/service-account.yaml",
		"deploy/kubernetes/services.yaml":                    "deploy/kubernetes/services.yaml",
		"deploy/kubernetes/shared-storage.yaml":              "deploy/kubernetes/shared-storage.yaml",
		"deploy/kubernetes/statefulset.yaml":                 "deploy/kubernetes/statefulset.yaml",
		"deploy/proxmox/labcentral.service":                  "deploy/labcentral.service",
		"deploy/proxmox/labcentral-proxmox-provider.service": "deploy/labcentral-proxmox-provider.service",
		"deploy/proxmox/labcentral-novnc.service":            "deploy/labcentral-novnc.service",
		"deploy/proxmox/labcentral.sysusers":                 "deploy/labcentral.sysusers",
		"deploy/proxmox/provider-network.example.conf":       "deploy/provider-network.example.conf",
		"deploy/proxmox/environment.example":                 "deploy/environment.example",
		"deploy/proxmox/proxmox-target.json.example":         "deploy/proxmox-target.json.example",
		"deploy/proxmox/proxmox-credential.json.example":     "deploy/proxmox-credential.json.example",
		"deploy/proxmox/proxmox-trust.json.example":          "deploy/proxmox-trust.json.example",
		"docs/operations/proxmox-deployment.md":              "docs/proxmox-deployment.md",
		"docs/operations/kubernetes-proxmox.md":              "docs/kubernetes-proxmox.md",
		"docs/operations/observability.md":                   "docs/observability.md",
		"docs/operations/postgresql-control-plane.md":        "docs/postgresql-control-plane.md",
		"docs/operations/recovery.md":                        "docs/recovery.md",
		"docs/operations/release-verification.md":            "docs/operations/release-verification.md",
		"docs/compatibility.md":                              "docs/compatibility.md",
	} {
		if err := copyReleaseFile(sourcePath, filepath.Join(root, destination)); err != nil {
			return err
		}
	}
	files, err := inventory(root)
	if err != nil {
		return err
	}
	releaseMetadata := struct {
		FormatVersion int       `json:"formatVersion"`
		Name          string    `json:"name"`
		Version       string    `json:"version"`
		Created       string    `json:"created"`
		Target        string    `json:"target"`
		Files         []subject `json:"files"`
	}{1, "LabCentral", cfg.version, created.Format(time.RFC3339), cfg.goos + "/" + cfg.goarch, files}
	if err := writeJSON(filepath.Join(root, "release.json"), releaseMetadata); err != nil {
		return err
	}
	if err := writeChecksums(root); err != nil {
		return err
	}

	archivePath := filepath.Join(cfg.outputDir, baseName+".tar.gz")
	if err := writeArchiveAtomic(archivePath, root, baseName, created); err != nil {
		return err
	}
	archiveDigest, err := fileSHA256(archivePath)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(archivePath+".sha256", []byte(archiveDigest+"  "+filepath.Base(archivePath)+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\nsha256:%s\n", archivePath, archiveDigest)
	return nil
}

func parseConfig(args []string) (config, error) {
	var cfg config
	flags := flag.NewFlagSet("release", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&cfg.outputDir, "output", "dist", "output directory")
	flags.StringVar(&cfg.version, "version", "", "release version")
	flags.StringVar(&cfg.goos, "goos", runtime.GOOS, "target operating system")
	flags.StringVar(&cfg.goarch, "goarch", runtime.GOARCH, "target architecture")
	flags.StringVar(&cfg.goBinary, "go", "go", "Go command")
	epochText := strings.TrimSpace(os.Getenv("SOURCE_DATE_EPOCH"))
	flags.StringVar(&epochText, "source-date-epoch", epochText, "reproducible build timestamp")
	if err := flags.Parse(args); err != nil {
		return cfg, err
	}
	if flags.NArg() != 0 {
		return cfg, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	for name, value := range map[string]string{"version": cfg.version, "goos": cfg.goos, "goarch": cfg.goarch} {
		if !safeToken(value) {
			return cfg, fmt.Errorf("%s must match [A-Za-z0-9][A-Za-z0-9._-]{0,63}", name)
		}
	}
	if epochText == "" {
		value, err := commandOutput(context.Background(), "git", "show", "-s", "--format=%ct", "HEAD")
		if err != nil {
			return cfg, fmt.Errorf("derive SOURCE_DATE_EPOCH from HEAD: %w", err)
		}
		epochText = value
	}
	epoch, err := strconv.ParseInt(epochText, 10, 64)
	if err != nil || epoch < 0 {
		return cfg, fmt.Errorf("source-date-epoch must be a non-negative Unix timestamp")
	}
	cfg.epoch = epoch
	cfg.outputDir, err = filepath.Abs(cfg.outputDir)
	if err != nil {
		return cfg, fmt.Errorf("resolve output directory: %w", err)
	}
	return cfg, nil
}

func safeToken(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for index, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || (index > 0 && (r == '.' || r == '_' || r == '-')) {
			continue
		}
		return false
	}
	return true
}

func inspectSource(ctx context.Context) (sourceInfo, error) {
	revision, err := commandOutput(ctx, "git", "rev-parse", "HEAD")
	if err != nil {
		return sourceInfo{}, fmt.Errorf("read source revision: %w", err)
	}
	status, err := commandOutput(ctx, "git", "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return sourceInfo{}, fmt.Errorf("read source status: %w", err)
	}
	repository, _ := commandOutput(ctx, "git", "config", "--get", "remote.origin.url")
	return sourceInfo{Repository: sanitizeRemote(repository), Revision: revision, Dirty: status != ""}, nil
}

func sanitizeRemote(remote string) string {
	remote = strings.TrimSpace(remote)
	parsed, err := url.Parse(remote)
	if err == nil && parsed.Host != "" {
		parsed.User = nil
		return parsed.String()
	}
	if at := strings.Index(remote, "@"); at >= 0 && strings.Contains(remote[at+1:], ":") {
		return remote[at+1:]
	}
	return remote
}

func buildBinary(ctx context.Context, cfg config, pkg, destination, ldflags string) error {
	arguments := []string{"build", "-trimpath", "-buildvcs=false", "-ldflags", strings.TrimSpace("-buildid= -s -w " + ldflags), "-o", destination, pkg}
	command := exec.CommandContext(ctx, cfg.goBinary, arguments...)
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+cfg.goos, "GOARCH="+cfg.goarch, "SOURCE_DATE_EPOCH="+strconv.FormatInt(cfg.epoch, 10))
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build %s: %w: %s", pkg, err, strings.TrimSpace(string(output)))
	}
	if err := os.Chmod(destination, 0o755); err != nil {
		return err
	}
	return nil
}

func listModules(ctx context.Context, goBinary string) ([]module, error) {
	command := exec.CommandContext(ctx, goBinary, "list", "-m", "-json", "all")
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("list Go modules: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	var modules []module
	for {
		var item module
		if err := decoder.Decode(&item); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode Go module list: %w", err)
		}
		if !item.Main {
			modules = append(modules, item)
		}
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Path < modules[j].Path })
	return modules, nil
}

func makeSBOM(cfg config, created time.Time, modules []module, labdName, labdDigest string, applications []subject) any {
	type hash struct {
		Algorithm string `json:"alg"`
		Content   string `json:"content"`
	}
	type property struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	type component struct {
		Type       string     `json:"type"`
		Name       string     `json:"name"`
		Version    string     `json:"version,omitempty"`
		PURL       string     `json:"purl,omitempty"`
		Hashes     []hash     `json:"hashes,omitempty"`
		Properties []property `json:"properties,omitempty"`
	}
	components := make([]component, 0, len(applications)+len(modules))
	for _, application := range applications {
		components = append(components, component{Type: "application", Name: application.Name, Version: pluginVersion, Hashes: []hash{{"SHA-256", application.SHA256}}})
	}
	for _, dependency := range modules {
		path, version, sum := dependency.Path, dependency.Version, dependency.Sum
		properties := []property(nil)
		if dependency.Replace != nil {
			properties = append(properties, property{"labcentral:go:replaces", dependency.Replace.Path + "@" + dependency.Replace.Version})
			if dependency.Replace.Version != "" {
				version, sum = dependency.Replace.Version, dependency.Replace.Sum
			}
		}
		if sum != "" {
			properties = append(properties, property{"labcentral:go:module-checksum", sum})
		}
		components = append(components, component{Type: "library", Name: path, Version: version, PURL: goPURL(path, version), Properties: properties})
	}
	return struct {
		BOMFormat   string `json:"bomFormat"`
		SpecVersion string `json:"specVersion"`
		Version     int    `json:"version"`
		Metadata    struct {
			Timestamp string    `json:"timestamp"`
			Component component `json:"component"`
		} `json:"metadata"`
		Components []component `json:"components"`
	}{
		BOMFormat: "CycloneDX", SpecVersion: "1.6", Version: 1,
		Metadata: struct {
			Timestamp string    `json:"timestamp"`
			Component component `json:"component"`
		}{created.Format(time.RFC3339), component{Type: "application", Name: labdName, Version: cfg.version, Hashes: []hash{{"SHA-256", labdDigest}}}},
		Components: components,
	}
}

func goPURL(path, version string) string {
	if version == "" {
		return ""
	}
	return "pkg:golang/" + strings.ReplaceAll(path, "%", "%25") + "@" + url.PathEscape(version)
}

func releaseReadme(version, goos, goarch, labdName, dockerProviderName, proxmoxProviderName, accessProviderName, recoveryName string) string {
	return fmt.Sprintf(`# LabCentral %s (%s/%s)

This archive contains the trusted-laptop Docker distribution and the
qualification-gated Proxmox distribution with its isolated noVNC access
process. Keep %s, %s, %s, %s, %s, and their packaged checksum and
source-provenance metadata together. Local child-process mode verifies
and snapshots provider executables before launch. The native production units
instead isolate the official processes under three OS identities; archive
checksums and signed exact-build qualification bind the installed files. The
recovery utility creates and verifies signed age-encrypted PostgreSQL/shared
state or native SQLite/provider-state recovery sets and performs offline,
reference-aware filesystem artifact maintenance; follow docs/recovery.md.

For the local Docker example, run:

    ./%s --listen 127.0.0.1:8080 --data-dir ./labcentral-data --auth-mode=none

The Docker provider uses DOCKER_HOST, or the platform's default Docker Engine
endpoint when it is unset. Access to a Docker socket is effectively host-level
control. This command explicitly selects no authentication; LabCentral refuses non-loopback
listeners by default, and is not a sandbox for hostile workloads. For Proxmox,
follow docs/proxmox-deployment.md. For the two-replica Kubernetes topology,
follow docs/kubernetes-proxmox.md and replace every fail-closed deployment
placeholder. Production VM mutation remains closed unless
signed exact-target evidence matches the installed provider build and fresh
live target inventory.

Before trusting this archive, verify its external Sigstore bundle and GitHub
provenance attestation with the exact workflow-and-tag identity in
docs/operations/release-verification.md. Then verify every packaged file with:

    sha256sum -c SHA256SUMS

The CycloneDX inventory is sbom.cdx.json. provenance.json records source and
build facts but is not a signature or a SLSA attestation. Read
docs/compatibility.md before installing, upgrading, or rolling back.
`, version, goos, goarch, labdName, dockerProviderName, proxmoxProviderName, accessProviderName, recoveryName, labdName)
}

func copyReleaseFile(source, destination string) error {
	contents, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read release asset %s: %w", source, err)
	}
	return writeFile(destination, contents, 0o644)
}

func gitArchiveDigest(ctx context.Context) (string, error) {
	output, err := exec.CommandContext(ctx, "git", "archive", "--format=tar", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("compute Proxmox source-tree digest: %w", err)
	}
	digest := sha256.Sum256(output)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func inventory(root string) ([]subject, error) {
	var result []subject
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		digest, err := fileSHA256(path)
		if err != nil {
			return err
		}
		result = append(result, subject{Name: filepath.ToSlash(relative), SHA256: digest})
		return nil
	})
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, err
}

func writeChecksums(root string) error {
	files, err := inventory(root)
	if err != nil {
		return err
	}
	var output strings.Builder
	for _, file := range files {
		fmt.Fprintf(&output, "%s  %s\n", file.SHA256, file.Name)
	}
	return writeFile(filepath.Join(root, "SHA256SUMS"), []byte(output.String()), 0o644)
}

func writeJSON(path string, value any) error {
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(contents, '\n'), 0o644)
}

func writeFile(path string, contents []byte, mode fs.FileMode) error {
	if err := os.WriteFile(path, contents, mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func writeFileAtomic(path string, contents []byte, mode fs.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".release-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func writeArchiveAtomic(path, root, baseName string, timestamp time.Time) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".archive-*.tar.gz")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := writeArchive(temporary, root, baseName, timestamp); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temporaryPath, 0o644); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func writeArchive(destination io.Writer, root, baseName string, timestamp time.Time) error {
	gzipWriter, err := gzip.NewWriterLevel(destination, gzip.BestCompression)
	if err != nil {
		return err
	}
	gzipWriter.Header.ModTime = timestamp
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)

	entries := []string{"."}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, relative)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(entries)
	for _, relative := range entries {
		path := root
		if relative != "." {
			path = filepath.Join(root, relative)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("archive entry %s is not a regular file or directory", relative)
		}
		name := baseName
		if relative != "." {
			name += "/" + filepath.ToSlash(relative)
		}
		mode := int64(0o644)
		typeflag := byte(tar.TypeReg)
		if info.IsDir() {
			name += "/"
			mode = 0o755
			typeflag = tar.TypeDir
		} else if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		header := &tar.Header{
			Name: name, Mode: mode, Size: info.Size(), ModTime: timestamp,
			AccessTime: time.Time{}, ChangeTime: time.Time{}, Typeflag: typeflag,
			Uid: 0, Gid: 0, Uname: "root", Gname: "root", Format: tar.FormatUSTAR,
		}
		if info.IsDir() {
			header.Size = 0
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tarWriter, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	return gzipWriter.Close()
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, bufio.NewReader(file)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func commandOutput(ctx context.Context, name string, arguments ...string) (string, error) {
	output, err := exec.CommandContext(ctx, name, arguments...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}
