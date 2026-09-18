package proxmox

import "fmt"

// ImageImportProfile is local operator policy, never portable source content.
// Its exact bytes remain bound by signed infrastructure qualification.
type ImageImportProfile struct {
	StagingStorage       string         `json:"stagingStorage"`
	StorageAlias         string         `json:"storageAlias"`
	VMIDRanges           []IntegerRange `json:"vmidRanges"`
	MaximumArtifactBytes uint64         `json:"maximumArtifactBytes"`
	MaximumDiskBytes     uint64         `json:"maximumDiskBytes"`
}

func (config TargetConfig) validateImageImport() error {
	p := config.ImageImport
	if p == nil {
		return nil
	}
	if config.SchemaVersion != targetConfigV14 || config.Pool == "" || !storageIDPattern.MatchString(p.StagingStorage) || p.MaximumArtifactBytes < 1<<20 || p.MaximumArtifactBytes > 32<<30 || p.MaximumDiskBytes < 1<<30 || p.MaximumDiskBytes > 128<<30 {
		return fmt.Errorf("image import requires bounded v1.4 policy and an ownership pool")
	}
	if _, ok := findStorageProfile(config, p.StorageAlias); !ok {
		return fmt.Errorf("image import storage profile unavailable")
	}
	if err := validateDisjointRanges(p.VMIDRanges, 100, 999999999); err != nil {
		return fmt.Errorf("image import VMID ranges: %w", err)
	}
	for _, r := range p.VMIDRanges {
		for _, t := range config.Templates {
			if t.VMID >= r.Start && t.VMID <= r.End {
				return fmt.Errorf("image import range contains a static template")
			}
		}
	}
	return nil
}

// MutationDigestForConfig preserves every old inventory identity. The explicit
// v1.4 projection binds only configured static templates: imported images are
// validated individually against their immutable provider evidence instead.
func (inventory Inventory) MutationDigestForConfig(config TargetConfig) (string, error) {
	if config.SchemaVersion != targetConfigV14 {
		return inventory.MutationDigest()
	}
	// Normalize the full inventory before filtering, so malformed discovery data
	// cannot disappear from qualification merely because it is not configured.
	if err := inventory.normalize(); err != nil {
		return "", err
	}
	filtered := make([]Template, 0, len(config.Templates))
	for _, t := range inventory.Templates {
		for _, p := range config.Templates {
			if t.Node == p.SourceNode && t.VMID == int64(p.VMID) {
				filtered = append(filtered, t)
				break
			}
		}
	}
	inventory.Templates = filtered
	digest, err := inventory.MutationDigest()
	if err != nil {
		return "", err
	}
	return digestDocument([]byte("proxmox.mutation-inventory/v1.1\x00" + digest)), nil
}
