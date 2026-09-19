package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
	"golang.org/x/text/cases"
	"mime"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

const ExtensionVersion = "labcentral.io/plugin-manifest/v2"

type BrowserAsset struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type BrowserContribution struct {
	ID          string   `json:"id"`
	RouteAlias  string   `json:"routeAlias,omitempty"`
	Slot        string   `json:"slot"`
	Title       string   `json:"title"`
	Entrypoint  string   `json:"entrypoint"`
	Permissions []string `json:"permissions"`
}

const BrowserHostVersion = 4

type BrowserPackage struct {
	MinimumHostVersion uint32                `json:"minimumHostVersion,omitempty"`
	APIVersion         string                `json:"apiVersion"`
	Assets             []BrowserAsset        `json:"assets"`
	Contributions      []BrowserContribution `json:"contributions"`
}
type AssetSnapshot struct {
	Content   []byte
	MediaType string
	Digest    string
}

// SnapshotBrowser reads and verifies all assets once. HTTP handlers serve only
// this immutable snapshot, never the operator's mutable installation directory.
func SnapshotBrowser(root, manifestPath string, m Manifest) (map[string]AssetSnapshot, error) {
	if m.Browser == nil || m.Browser.APIVersion != "labcentral.browser/v1" || m.Browser.MinimumHostVersion > BrowserHostVersion || len(m.Browser.Assets) == 0 || len(m.Browser.Assets) > 256 || len(m.Browser.Contributions) == 0 || len(m.Browser.Contributions) > 32 {
		return nil, fmt.Errorf("invalid browser package")
	}
	manifestFile, err := Within(root, manifestPath)
	if err != nil {
		return nil, err
	}
	assets := make(map[string]AssetSnapshot)
	total := 0
	folded := map[string]bool{}
	fold := cases.Fold()
	for _, asset := range m.Browser.Assets {
		if !labbundle.ValidPath(asset.Path) || path.Clean(asset.Path) != asset.Path || strings.ContainsAny(asset.Path, "\\?#%") || strings.HasPrefix(asset.Path, "/") || strings.HasPrefix(asset.Path, "../") {
			return nil, fmt.Errorf("invalid browser asset path")
		}
		if folded[fold.String(asset.Path)] {
			return nil, fmt.Errorf("duplicate browser asset")
		}
		folded[fold.String(asset.Path)] = true
		file, err := Within(root, filepath.Join(filepath.Dir(manifestFile), asset.Path))
		if err != nil {
			return nil, err
		}
		data, err := ProtectedFile(file, 8<<20)
		if err != nil {
			return nil, err
		}
		total += len(data)
		if total > 32<<20 {
			return nil, fmt.Errorf("browser package exceeds 32 MiB")
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != asset.SHA256 {
			return nil, fmt.Errorf("browser asset digest mismatch")
		}
		media := mime.TypeByExtension(path.Ext(asset.Path))
		if media == "" {
			return nil, fmt.Errorf("unsupported browser asset type")
		}
		assets[asset.Path] = AssetSnapshot{Content: data, MediaType: media, Digest: asset.SHA256}
	}
	ids := map[string]bool{}
	for _, c := range m.Browser.Contributions {
		if !identityPattern.MatchString(c.ID) || ids[c.ID] || c.Title == "" || len(c.Title) > 128 || !slices.Contains([]string{"lab-panel", "resource-action", "access-view", "plugin-settings", "page"}, c.Slot) {
			return nil, fmt.Errorf("invalid browser contribution")
		}
		if c.RouteAlias != "" && (m.Browser.MinimumHostVersion < 3 || c.Slot != "page" || !slices.Contains([]string{"templates", "teams", "classroom"}, c.RouteAlias)) {
			return nil, fmt.Errorf("invalid browser route alias")
		}
		ids[c.ID] = true
		if asset, ok := assets[c.Entrypoint]; !ok || !strings.HasPrefix(asset.MediaType, "text/html") {
			return nil, fmt.Errorf("browser entrypoint is not verified HTML")
		}
		seen := map[string]bool{}
		for _, permission := range c.Permissions {
			if !slices.Contains(m.RequestedPermissions, permission) || seen[permission] || !slices.Contains([]string{"page.presentation", "context.read", "topology.read", "status.read", "access.create", "access.stream", "navigation.lab", "templates.read", "templates.write", "builds.read", "builds.create", "images.manage", "sources.read", "sources.resolve", "labs.read", "labs.create", "labs.manage", "members.read", "groups.read", "groups.enroll", "groups.manage"}, permission) {
				return nil, fmt.Errorf("invalid browser permission")
			}
			seen[permission] = true
		}
	}
	return assets, nil
}

func validateExtension(m Manifest) error {
	if len(m.Categories) != 1 || !slices.Contains([]string{"provider", "access", "source", "builder", "browser"}, m.Categories[0]) {
		return fmt.Errorf("manifest must declare exactly one category")
	}
	if err := ValidateConfigurationSchema(m.ConfigurationSchema); err != nil {
		return err
	}
	switch m.Categories[0] {
	case "provider":
		if len(m.ProviderKinds) == 0 {
			return fmt.Errorf("provider kinds required")
		}
	case "access":
		if len(m.AccessKinds) == 0 || len(m.AccessMethods) == 0 || len(m.EndpointSchemas) == 0 {
			return fmt.Errorf("access contract declarations required")
		}
	case "source":
		if len(m.SourceKinds) == 0 {
			return fmt.Errorf("source kinds required")
		}
	case "builder":
		if len(m.BuilderKinds) == 0 {
			return fmt.Errorf("builder kinds required")
		}
	case "browser":
		if m.Browser == nil || m.Executable != "" {
			return fmt.Errorf("browser package cannot declare an executable")
		}
	}
	if m.Categories[0] != "browser" && m.Browser != nil {
		return fmt.Errorf("browser assets require a separate contribution manifest")
	}
	return nil
}
