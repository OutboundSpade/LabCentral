package labbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v4"
)

const PortableVersion = "labcentral.bundle/v1beta1"
const SourcePackageVersion = "labcentral.source-package/v1"

// SourcePackage contains only source composition and logical build contracts.
// Local plugin installation IDs, target bindings and image bytes do not belong here.
type SourcePackage struct {
	APIVersion   string               `yaml:"apiVersion" json:"apiVersion"`
	Dependencies []SourceDependency   `yaml:"dependencies,omitempty" json:"dependencies,omitempty"`
	Components   []SourceImport       `yaml:"components,omitempty" json:"components,omitempty"`
	Recipes      []SourceImport       `yaml:"recipes,omitempty" json:"recipes,omitempty"`
	Builders     []BuilderRequirement `yaml:"builders,omitempty" json:"builders,omitempty"`
}
type SourceDependency struct {
	ID       string   `yaml:"id" json:"id"`
	Root     string   `yaml:"root" json:"root"`
	SHA256   string   `yaml:"sha256" json:"sha256"`
	Requires []string `yaml:"requires,omitempty" json:"requires,omitempty"`
}
type SourceImport struct {
	Name string `yaml:"name" json:"name"`
	Path string `yaml:"path" json:"path"`
}
type BuilderRequirement struct {
	ID     string `yaml:"id" json:"id"`
	Kind   string `yaml:"kind" json:"kind"`
	Schema string `yaml:"schema" json:"schema"`
}

var sourceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// SourceFile refuses binary/image/archive payloads in source-only packages.
// Text may contain scripts and encoded fixtures; no content is executed here.
func SourceFile(name string, body []byte) error {
	if !ValidPath(name) || !utf8.Valid(body) || bytes.ContainsRune(body, 0) {
		return fmt.Errorf("source file %q must be UTF-8 text without NUL", name)
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".qcow", ".qcow2", ".raw", ".img", ".vmdk", ".vhd", ".vhdx", ".vdi", ".iso", ".ova", ".oci", ".tar", ".gz", ".tgz", ".zip", ".xz", ".zst":
		return fmt.Errorf("image/archive file %q is not portable build source", name)
	}
	if path.Base(name) == "oci-layout" {
		return fmt.Errorf("OCI image layouts are not portable build source")
	}
	return nil
}

// DependencyDigest binds source-relative file names, metadata and bytes. The
// complete transitive closure is separately pinned by the package's lock graph.
func DependencyDigest(root string, files []File) (string, error) {
	if !ValidPath(root) {
		return "", fmt.Errorf("invalid dependency root")
	}
	selected := []File{}
	for _, file := range files {
		if strings.HasPrefix(file.Path, root+"/") {
			file.Path = strings.TrimPrefix(file.Path, root+"/")
			selected = append(selected, file)
		}
	}
	if len(selected) == 0 {
		return "", fmt.Errorf("dependency source is missing")
	}
	slices.SortFunc(selected, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	encoded, err := json.Marshal(selected)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("labcentral.source-dependency/v1\x00"), encoded...))
	return hex.EncodeToString(sum[:]), nil
}

func (b *Bundle) validatePortable() error {
	m := &b.Manifest
	if m.APIVersion != PortableVersion {
		if m.Package != nil {
			return fmt.Errorf("source package requires portable bundle version")
		}
		return nil
	}
	p := m.Package
	if p == nil || p.APIVersion != SourcePackageVersion || len(p.Dependencies) > 32 || len(p.Components) > 64 || len(p.Recipes) > 8 || len(p.Builders) > 16 {
		return fmt.Errorf("invalid source package")
	}
	for name, body := range b.Files {
		if err := SourceFile(name, body); err != nil {
			return err
		}
	}
	deps := map[string]SourceDependency{}
	roots := []string{}
	for _, d := range p.Dependencies {
		if !sourceName.MatchString(d.ID) || deps[d.ID].ID != "" || len(d.Requires) > 32 {
			return fmt.Errorf("invalid dependency identity")
		}
		digest, err := DependencyDigest(d.Root, m.Files)
		if err != nil || digest != d.SHA256 {
			return fmt.Errorf("dependency %q does not match its locked source", d.ID)
		}
		for _, root := range roots {
			if d.Root == root || strings.HasPrefix(d.Root, root+"/") || strings.HasPrefix(root, d.Root+"/") {
				return fmt.Errorf("overlapping dependency roots")
			}
		}
		roots = append(roots, d.Root)
		deps[d.ID] = d
	}
	state := map[string]uint8{}
	var visit func(string) error
	visit = func(id string) error {
		d, ok := deps[id]
		if !ok {
			return fmt.Errorf("missing source dependency %q", id)
		}
		if state[id] == 1 {
			return fmt.Errorf("source dependency cycle")
		}
		if state[id] == 2 {
			return nil
		}
		state[id] = 1
		seen := map[string]bool{}
		for _, child := range d.Requires {
			if seen[child] {
				return fmt.Errorf("duplicate dependency edge")
			}
			seen[child] = true
			if err := visit(child); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range deps {
		if err := visit(id); err != nil {
			return err
		}
	}
	requirements := map[string]BuilderRequirement{}
	for _, r := range p.Builders {
		if !buildName.MatchString(r.ID) || !buildName.MatchString(r.Kind) || !buildName.MatchString(r.Schema) || requirements[r.ID].ID != "" {
			return fmt.Errorf("invalid logical builder requirement")
		}
		requirements[r.ID] = r
	}
	for _, imports := range [][]SourceImport{p.Components, p.Recipes} {
		names := map[string]bool{}
		for _, imp := range imports {
			if !sourceName.MatchString(imp.Name) || names[imp.Name] || !ValidPath(imp.Path) || imp.Path == m.Entrypoint || imp.Path == "bundle.yaml" {
				return fmt.Errorf("invalid or duplicate source import")
			}
			if _, ok := b.Files[imp.Path]; !ok {
				return fmt.Errorf("imported source missing")
			}
			names[imp.Name] = true
		}
	}
	// Keep the immutable manifest unchanged while resolving imported recipes.
	if m.Builds != nil {
		copy := *m.Builds
		copy.Recipes = slices.Clone(m.Builds.Recipes)
		b.ResolvedBuilds = &copy
	}
	// Imported recipe documents are closed JSON data, not executable templates.
	for _, imp := range p.Recipes {
		var recipe BuildRecipe
		decoder := json.NewDecoder(bytes.NewReader(b.Files[imp.Path]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&recipe); err != nil {
			return fmt.Errorf("invalid recipe %q: %w", imp.Name, err)
		}
		if decoder.Decode(new(any)) != io.EOF {
			return fmt.Errorf("trailing recipe data")
		}
		recipe.ID = imp.Name
		recipe.ContextRoot = path.Dir(imp.Path)
		for i, p := range recipe.Context {
			if !ValidPath(p) {
				return fmt.Errorf("invalid recipe context path")
			}
			recipe.Context[i] = path.Join(path.Dir(imp.Path), p)
		}
		if b.ResolvedBuilds == nil {
			b.ResolvedBuilds = &BuildMetadata{APIVersion: "labcentral.build/v2"}
		}
		b.ResolvedBuilds.Recipes = append(b.ResolvedBuilds.Recipes, recipe)
	}
	if b.Builds() != nil {
		for _, recipe := range b.Builds().Recipes {
			requirement, ok := requirements[recipe.Builder]
			if !ok || requirement.Schema != recipe.Schema {
				return fmt.Errorf("recipe %q has no matching logical builder requirement", recipe.ID)
			}
		}
	}
	return nil
}

// Pack creates deterministic manifest and archive bytes from an explicit source
// file set. Existing dependency locks are verified, never silently refreshed.
func Pack(manifest Manifest, files map[string][]byte) ([]byte, error) {
	if _, ok := files["bundle.yaml"]; ok {
		return nil, fmt.Errorf("manifest is supplied separately")
	}
	if len(files) == 0 || len(files) >= MaxFiles {
		return nil, fmt.Errorf("source file count outside bounds")
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	manifest.Files = nil
	manifest.TotalBytes = 0
	for _, name := range names {
		body := files[name]
		if !ValidPath(name) {
			return nil, fmt.Errorf("invalid file path")
		}
		if manifest.APIVersion == PortableVersion {
			if err := SourceFile(name, body); err != nil {
				return nil, err
			}
		}
		sum := sha256.Sum256(body)
		manifest.Files = append(manifest.Files, File{Path: name, Size: uint64(len(body)), SHA256: hex.EncodeToString(sum[:]), MediaType: "text/plain"})
		manifest.TotalBytes += uint64(len(body))
		if manifest.TotalBytes > MaxBytes {
			return nil, fmt.Errorf("source exceeds size limit")
		}
	}
	mb, err := yaml.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	// Stored deflate avoids rejecting valid highly repetitive source through the
	// reader's fixed decompression-ratio limit and gives stable cross-run output.
	gz, err := gzip.NewWriterLevel(&buffer, gzip.NoCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(gz)
	write := func(name string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err = write("bundle.yaml", mb); err != nil {
		return nil, err
	}
	for _, name := range names {
		if err = write(name, files[name]); err != nil {
			return nil, err
		}
	}
	if err = tw.Close(); err != nil {
		return nil, err
	}
	if err = gz.Close(); err != nil {
		return nil, err
	}
	output := buffer.Bytes()
	if _, err = Read(output); err != nil {
		return nil, err
	}
	return output, nil
}

// LockSources is an explicit authoring operation. Publication and Pack only
// verify existing locks; they never change a pinned dependency automatically.
func LockSources(manifest Manifest, files map[string][]byte) (Manifest, error) {
	if manifest.APIVersion != PortableVersion || manifest.Package == nil {
		return manifest, fmt.Errorf("locking requires a portable source package")
	}
	manifest.Files = nil
	manifest.TotalBytes = 0
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		body := files[name]
		if name == "bundle.yaml" {
			return manifest, fmt.Errorf("manifest cannot index itself")
		}
		if err := SourceFile(name, body); err != nil {
			return manifest, err
		}
		sum := sha256.Sum256(body)
		manifest.Files = append(manifest.Files, File{Path: name, Size: uint64(len(body)), SHA256: hex.EncodeToString(sum[:]), MediaType: "text/plain"})
		manifest.TotalBytes += uint64(len(body))
	}
	// Copy before updating so callers retaining old metadata keep its identity.
	copy := *manifest.Package
	copy.Dependencies = slices.Clone(copy.Dependencies)
	manifest.Package = &copy
	for i := range copy.Dependencies {
		digest, err := DependencyDigest(copy.Dependencies[i].Root, manifest.Files)
		if err != nil {
			return manifest, err
		}
		copy.Dependencies[i].SHA256 = digest
	}
	if _, err := Pack(manifest, files); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func ExportSource(source []byte) ([]byte, error) {
	m := Manifest{APIVersion: PortableVersion, Kind: "LabBundle", Entrypoint: "lab.yaml", Package: &SourcePackage{APIVersion: SourcePackageVersion}}
	files := map[string][]byte{"lab.yaml": source}
	if Archive(source) {
		bundle, err := Read(source)
		if err != nil {
			return nil, err
		}
		if bundle.Manifest.Package != nil {
			return slices.Clone(source), nil
		}
		if bundle.Manifest.Builds != nil {
			return nil, fmt.Errorf("legacy build recipes use local bindings; publish a source package with logical builder requirements before export")
		}
		m.Entrypoint = bundle.Manifest.Entrypoint
		files = bundle.Files
		delete(files, "bundle.yaml")
	}
	return Pack(m, files)
}
