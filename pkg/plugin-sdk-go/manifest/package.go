package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
)

// Package indexes independently configured contributions. Loading a package
// verifies metadata only: each backend still requires its own installation,
// process identity, granted permissions and target/project bindings.
type Package struct {
	APIVersion    string                `json:"apiVersion"`
	ID            string                `json:"id"`
	Version       string                `json:"version"`
	Contributions []PackageContribution `json:"contributions"`
}
type PackageContribution struct {
	ID       string `json:"id"`
	Manifest string `json:"manifest"`
	SHA256   string `json:"sha256"`
}

func LoadPackage(root, name string) (Package, error) {
	var p Package
	file, err := Within(root, name)
	if err != nil {
		return p, err
	}
	data, err := ProtectedFile(file, 1<<20)
	if err != nil {
		return p, err
	}
	if err = Decode(data, &p); err != nil {
		return p, err
	}
	if p.APIVersion != "labcentral.io/extension-package/v1" || !identityPattern.MatchString(p.ID) || !identityPattern.MatchString(p.Version) || len(p.Contributions) == 0 || len(p.Contributions) > 32 {
		return p, fmt.Errorf("invalid extension package")
	}
	seen := map[string]bool{}
	for _, c := range p.Contributions {
		if !identityPattern.MatchString(c.ID) || seen[c.ID] || c.Manifest == "" || filepath.IsAbs(c.Manifest) {
			return p, fmt.Errorf("invalid package contribution")
		}
		seen[c.ID] = true
		path, err := Within(root, filepath.Join(filepath.Dir(file), c.Manifest))
		if err != nil {
			return p, err
		}
		data, err := ProtectedFile(path, 1<<20)
		if err != nil {
			return p, err
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != c.SHA256 {
			return p, fmt.Errorf("contribution manifest digest mismatch")
		}
	}
	return p, nil
}

// ContributionManifest resolves an installation selection against the verified
// package. This does not merge backend identities or permissions.
func ContributionManifest(root, packagePath, contribution string) (string, error) {
	p, err := LoadPackage(root, packagePath)
	if err != nil {
		return "", err
	}
	file, err := Within(root, packagePath)
	if err != nil {
		return "", err
	}
	for _, c := range p.Contributions {
		if c.ID == contribution {
			return Within(root, filepath.Join(filepath.Dir(file), c.Manifest))
		}
	}
	return "", fmt.Errorf("package contribution not found")
}
