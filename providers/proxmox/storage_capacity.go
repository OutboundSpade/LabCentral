package proxmox

import "fmt"

// StorageCapacityFloor is reviewed target policy, not a reservation or a promise
// of free space. The target configuration signature binds each minimum.
type StorageCapacityFloor struct {
	ID                string `json:"id"`
	MinimumTotalBytes int64  `json:"minimumTotalBytes"`
}

func (config TargetConfig) validateStorageCapacityFloors() error {
	if config.SchemaVersion != targetConfigV16 {
		if len(config.StorageCapacityFloors) != 0 {
			return fmt.Errorf("storage capacity floors require target-config v1.6")
		}
		return nil
	}
	expected := map[string]bool{}
	for _, storage := range config.Storages {
		expected[storage.ID] = true
	}
	if config.ImageImport != nil {
		expected[config.ImageImport.StagingStorage] = true
	}
	if len(expected) == 0 || len(config.StorageCapacityFloors) != len(expected) || len(expected) > 17 {
		return fmt.Errorf("storage capacity floors must exactly cover configured disk and staging storages")
	}
	for _, floor := range config.StorageCapacityFloors {
		if !storageIDPattern.MatchString(floor.ID) || !expected[floor.ID] || floor.MinimumTotalBytes <= 0 {
			return fmt.Errorf("invalid, duplicate or unconfigured storage capacity floor")
		}
		delete(expected, floor.ID)
	}
	return nil
}

// v1.6 excludes volatile filesystem totals from infrastructure identity while
// refusing capacity below the reviewed minimum on every fresh digest check.
// Copy storage records before projection so discovery retains real capacity.
func (inventory Inventory) capacityBoundMutationDigest(config TargetConfig) (string, error) {
	if err := config.validateStorageCapacityFloors(); err != nil {
		return "", err
	}
	for _, floor := range config.StorageCapacityFloors {
		found := false
		for _, storage := range inventory.Storages {
			if storage.Node == config.DestinationNode && storage.ID == floor.ID {
				found = storage.TotalBytes >= floor.MinimumTotalBytes
				break
			}
		}
		if !found {
			return "", fmt.Errorf("storage %q is missing or below its reviewed capacity minimum", floor.ID)
		}
	}
	inventory.Storages = append([]Storage(nil), inventory.Storages...)
	for i := range inventory.Storages {
		inventory.Storages[i].TotalBytes = 0
	}
	digest, err := inventory.MutationDigest()
	if err != nil {
		return "", err
	}
	return digestDocument([]byte("proxmox.mutation-inventory/v1.2\x00" + digest)), nil
}
