// Package labbundle verifies immutable multi-file template bundles without
// extracting or executing files. A gzip tar contains bundle.yaml plus exactly
// the regular files declared there. Single YAML documents remain compatible.
package labbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v4"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const MaxBytes = 8 << 20
const MaxFiles = 128

type File struct {
	Path      string `yaml:"path" json:"path"`
	Size      uint64 `yaml:"size" json:"size"`
	SHA256    string `yaml:"sha256" json:"sha256"`
	MediaType string `yaml:"mediaType" json:"mediaType"`
}
type Provenance struct {
	Source           string `yaml:"source" json:"source"`
	ResolvedIdentity string `yaml:"resolvedIdentity" json:"resolvedIdentity"`
}
type Manifest struct {
	Builds     *BuildMetadata `yaml:"builds,omitempty" json:"builds,omitempty"`
	APIVersion string         `yaml:"apiVersion" json:"apiVersion"`
	Kind       string         `yaml:"kind" json:"kind"`
	Entrypoint string         `yaml:"entrypoint" json:"entrypoint"`
	Files      []File         `yaml:"files" json:"files"`
	TotalBytes uint64         `yaml:"totalBytes,omitempty" json:"totalBytes,omitempty"`
	Provenance *Provenance    `yaml:"provenance,omitempty" json:"provenance,omitempty"`
}
type Bundle struct {
	Manifest Manifest
	Files    map[string][]byte
}

func Archive(data []byte) bool { return len(data) > 1 && data[0] == 0x1f && data[1] == 0x8b }
func Source(data []byte) ([]byte, uint32, error) {
	if len(data) == 0 || len(data) > MaxBytes {
		return nil, 0, fmt.Errorf("template size outside bounds")
	}
	if !Archive(data) {
		return data, 1, nil
	}
	bundle, err := Read(data)
	if err != nil {
		return nil, 0, err
	}
	return bundle.Files[bundle.Manifest.Entrypoint], uint32(len(bundle.Files)), nil
}
func ValidPath(name string) bool {
	if name == "" || len(name) > 512 || !utf8.ValidString(name) || !norm.NFC.IsNormalString(name) || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00:") || strings.Count(name, "/") > 16 {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "." || part == ".." || part == "" {
			return false
		}
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
func Read(data []byte) (Bundle, error) {
	result := Bundle{Files: map[string][]byte{}}
	if !Archive(data) || len(data) > MaxBytes {
		return result, fmt.Errorf("expected bounded gzip tar bundle")
	}
	compressed := bytes.NewReader(data)
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return result, err
	}
	defer gz.Close()
	gz.Multistream(false)
	expanded := io.LimitReader(gz, MaxBytes+1)
	archive, err := io.ReadAll(expanded)
	if err != nil || len(archive) > MaxBytes || len(archive) > len(data)*100 {
		return result, fmt.Errorf("bundle expanded size or compression ratio exceeds limit")
	}
	if compressed.Len() != 0 {
		return result, fmt.Errorf("trailing compressed bundle data")
	}
	tarBytes := bytes.NewReader(archive)
	tr := tar.NewReader(tarBytes)
	fold := cases.Fold()
	seen := map[string]bool{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return result, err
		}
		if (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || header.Linkname != "" || !ValidPath(header.Name) || len(header.PAXRecords) > 0 || len(header.Xattrs) > 0 {
			return result, fmt.Errorf("bundle requires clean regular files")
		}
		name := fold.String(header.Name)
		if seen[name] || len(seen) >= MaxFiles || header.Size < 0 || header.Size > MaxBytes {
			return result, fmt.Errorf("bundle path collision or file limit")
		}
		seen[name] = true
		body, err := io.ReadAll(io.LimitReader(tr, header.Size+1))
		if err != nil || int64(len(body)) != header.Size {
			return result, fmt.Errorf("incomplete bundle file")
		}
		result.Files[header.Name] = body
	}
	trailing, _ := io.ReadAll(tarBytes)
	for _, b := range trailing {
		if b != 0 {
			return result, fmt.Errorf("trailing tar data")
		}
	}
	manifestBytes, ok := result.Files["bundle.yaml"]
	if !ok || len(manifestBytes) > 64<<10 {
		return result, fmt.Errorf("bundle manifest missing or oversized")
	}
	// Reject alias expansion and nonstandard YAML tags before typed decoding.
	var tree yaml.Node
	if err := yaml.Unmarshal(manifestBytes, &tree); err != nil {
		return result, err
	}
	nodes := 0
	var check func(*yaml.Node, int) error
	check = func(n *yaml.Node, depth int) error {
		nodes++
		if nodes > 4096 || depth > 16 || n.Kind == yaml.AliasNode || n.Anchor != "" {
			return fmt.Errorf("bundle manifest complexity exceeds limit")
		}
		if n.Tag != "" && n.Tag != "!!map" && n.Tag != "!!seq" && n.Tag != "!!str" && n.Tag != "!!int" {
			return fmt.Errorf("invalid manifest tag")
		}
		for _, child := range n.Content {
			if err := check(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := check(&tree, 0); err != nil {
		return result, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.KnownFields(true)
	if err := decoder.Decode(&result.Manifest); err != nil {
		return result, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return result, fmt.Errorf("multiple manifest documents")
	}
	m := result.Manifest
	if m.APIVersion != "labcentral.bundle/v1alpha1" || m.Kind != "LabBundle" || !ValidPath(m.Entrypoint) || m.Entrypoint == "bundle.yaml" || len(m.Files) != len(result.Files)-1 {
		return result, fmt.Errorf("invalid bundle manifest")
	}
	declared := map[string]bool{}
	total := uint64(0)
	for _, f := range m.Files {
		body, ok := result.Files[f.Path]
		if !ok || f.Path == "bundle.yaml" || declared[f.Path] || f.Size != uint64(len(body)) || len(f.MediaType) == 0 || len(f.MediaType) > 128 {
			return result, fmt.Errorf("manifest file mismatch")
		}
		declared[f.Path] = true
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			return result, fmt.Errorf("bundle file digest mismatch")
		}
		total += f.Size
	}
	if !declared[m.Entrypoint] || (m.TotalBytes != 0 && m.TotalBytes != total) {
		return result, fmt.Errorf("bundle entrypoint or byte count mismatch")
	}
	return result, result.ValidateBuilds()
}
