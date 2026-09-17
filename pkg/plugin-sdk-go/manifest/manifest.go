// Package manifest defines the independently distributed plugin installation contract.
package manifest

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

var identityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

const Version = "labcentral.io/plugin-manifest/v1"

type Manifest struct {
	APIVersion           string          `json:"apiVersion"`
	ID                   string          `json:"id"`
	Version              string          `json:"version"`
	Executable           string          `json:"executable"`
	SHA256               string          `json:"sha256"`
	Categories           []string        `json:"categories"`
	SourceKinds          []string        `json:"sourceKinds,omitempty"`
	BuilderKinds         []string        `json:"builderKinds,omitempty"`
	ConfigurationSchema  json.RawMessage `json:"configurationSchema,omitempty"`
	Browser              *BrowserPackage `json:"browser,omitempty"`
	ProviderKinds        []string        `json:"providerKinds,omitempty"`
	AccessKinds          []string        `json:"accessKinds,omitempty"`
	AccessMethods        []string        `json:"accessMethods,omitempty"`
	EndpointSchemas      []string        `json:"endpointSchemas,omitempty"`
	ProtocolMajor        uint32          `json:"protocolMajor"`
	MinimumProtocolMinor uint32          `json:"minimumProtocolMinor"`
	MaximumProtocolMinor uint32          `json:"maximumProtocolMinor"`
	OperatingSystems     []string        `json:"operatingSystems"`
	Architectures        []string        `json:"architectures"`
	RequestedPermissions []string        `json:"requestedPermissions"`
}

// Decode rejects duplicate keys, unknown fields, trailing values and deeply
// nested documents. It is shared by the manifest and installation readers.
func Decode(b []byte, destination any) error {
	if len(b) == 0 || len(b) > 1<<20 {
		return fmt.Errorf("configuration must contain 1 byte to 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return fmt.Errorf("configuration nesting exceeds limit")
		}
		t, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				name, ok := k.(string)
				if !ok || seen[name] {
					return fmt.Errorf("duplicate or invalid configuration key")
				}
				seen[name] = true
				if e := value(depth + 1); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e := value(depth + 1); e != nil {
					return e
				}
			}
		default:
			return fmt.Errorf("unexpected configuration delimiter")
		}
		_, e = d.Token()
		return e
	}
	if e := value(0); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return fmt.Errorf("trailing configuration data")
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(destination)
}
func ProtectedFile(path string, max int64) ([]byte, error) {
	before, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0022 != 0 {
		return nil, fmt.Errorf("configuration must be a non-writable regular file")
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	after, e := f.Stat()
	if e != nil || !os.SameFile(before, after) || after.Mode() != before.Mode() {
		return nil, fmt.Errorf("configuration identity changed")
	}
	b, e := io.ReadAll(io.LimitReader(f, max+1))
	if e != nil || int64(len(b)) > max {
		return nil, fmt.Errorf("configuration exceeds size limit")
	}
	return b, nil
}
func Within(root, path string) (string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(os.PathSeparator) {
		return "", fmt.Errorf("plugin root must be absolute and clean")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path = filepath.Clean(path)
	rel, e := filepath.Rel(root, path)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("plugin path escapes installation root")
	}
	resolved, e := filepath.EvalSymlinks(path)
	if e != nil || resolved != path {
		return "", fmt.Errorf("plugin paths must not contain symbolic links")
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return "", fmt.Errorf("plugin parent directory is writable or invalid")
		}
		if dir == root {
			break
		}
		if dir == filepath.Dir(dir) {
			return "", fmt.Errorf("plugin path has no protected root")
		}
	}
	return path, nil
}
func Load(root, path string, granted []string) (Manifest, string, error) {
	var m Manifest
	info, e := os.Lstat(root)
	if e != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return m, "", fmt.Errorf("plugin root must be a protected directory")
	}
	path, e = Within(root, path)
	if e != nil {
		return m, "", e
	}
	b, e := ProtectedFile(path, 1<<20)
	if e != nil {
		return m, "", e
	}
	if e = Decode(b, &m); e != nil {
		return m, "", e
	}
	browser := m.APIVersion == ExtensionVersion && len(m.Categories) == 1 && m.Categories[0] == "browser"
	digestText := m.SHA256
	if browser {
		digestText = strings.Repeat("0", 64)
	}
	digest, e := hex.DecodeString(digestText)
	if e != nil || len(digest) != 32 || m.SHA256 != strings.ToLower(m.SHA256) || (m.APIVersion != Version && m.APIVersion != ExtensionVersion) || !identityPattern.MatchString(m.ID) || !identityPattern.MatchString(m.Version) || m.ProtocolMajor != 1 || m.MinimumProtocolMinor > m.MaximumProtocolMinor || !slices.Contains(m.OperatingSystems, runtime.GOOS) || !slices.Contains(m.Architectures, runtime.GOARCH) {
		return m, "", fmt.Errorf("plugin manifest identity or compatibility is invalid")
	}
	for _, list := range [][]string{m.Categories, m.SourceKinds, m.BuilderKinds, m.ProviderKinds, m.AccessKinds, m.AccessMethods, m.EndpointSchemas, m.OperatingSystems, m.Architectures, granted} {
		if len(list) > 64 {
			return m, "", fmt.Errorf("manifest list exceeds limit")
		}
		seen := map[string]bool{}
		for _, v := range list {
			if !identityPattern.MatchString(v) || seen[v] {
				return m, "", fmt.Errorf("manifest list contains invalid or duplicate values")
			}
			seen[v] = true
		}
	}
	if !browser && (m.Executable == "" || filepath.IsAbs(m.Executable)) {
		return m, "", fmt.Errorf("manifest executable must be relative")
	}
	if len(m.RequestedPermissions) > 64 || len(granted) > 64 {
		return m, "", fmt.Errorf("too many plugin permissions")
	}
	seen := map[string]bool{}
	for _, permission := range m.RequestedPermissions {
		if permission == "" || len(permission) > 128 || seen[permission] || !slices.Contains(granted, permission) {
			return m, "", fmt.Errorf("plugin requests an invalid or ungranted permission")
		}
		seen[permission] = true
	}
	if m.APIVersion == Version && (len(m.SourceKinds) > 0 || len(m.BuilderKinds) > 0 || m.Browser != nil || len(m.ConfigurationSchema) > 0) {
		return m, "", fmt.Errorf("new contributions require manifest v2")
	}
	if m.APIVersion == ExtensionVersion {
		if err := validateExtension(m); err != nil {
			return m, "", err
		}
	}
	if browser {
		return m, "", nil
	}
	executable, e := Within(root, filepath.Join(filepath.Dir(path), m.Executable))
	if e != nil {
		return m, "", e
	}
	return m, executable, nil
}
