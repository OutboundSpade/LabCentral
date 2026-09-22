package proxmox

import (
	"context"
	"encoding/json"
	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"testing"
)

func capacityFixture(t *testing.T) (TargetConfig, Inventory) {
	t.Helper()
	var c TargetConfig
	if err := json.Unmarshal(mutationTestTarget(t).ConfigEnvelope, &c); err != nil {
		t.Fatal(err)
	}
	c.SchemaVersion = targetConfigV16
	c.StorageCapacityFloors = []StorageCapacityFloor{{ID: "local-lvm", MinimumTotalBytes: 1 << 40}}
	i := mutationTestInventory()
	i.Storages[0].TotalBytes = 3 << 40
	return c, i
}

func TestStorageCapacityProjectionPreservesLegacyAndDiscovery(t *testing.T) {
	c, before := capacityFixture(t)
	a, err := before.MutationDigestForConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	if before.Storages[0].TotalBytes != 3<<40 {
		t.Fatal("projection changed discovered capacity")
	}
	after := mutationTestInventory()
	after.Storages[0].TotalBytes = (3 << 40) - 131072
	b, err := after.MutationDigestForConfig(c)
	if err != nil || a != b {
		t.Fatalf("ordinary filesystem capacity drift invalidated qualification: %v", err)
	}
	for _, version := range []string{targetConfigV11, targetConfigV12, targetConfigV13, targetConfigV14, targetConfigV15} {
		c.SchemaVersion, c.StorageCapacityFloors = version, nil
		old, err := before.MutationDigestForConfig(c)
		if err != nil {
			t.Fatal(err)
		}
		changed, err := after.MutationDigestForConfig(c)
		if err != nil || old == changed || old == a {
			t.Fatalf("legacy %s identity was changed or reused", version)
		}
		legacy, _ := before.MutationDigest()
		if version == targetConfigV14 || version == targetConfigV15 {
			legacy = digestDocument([]byte("proxmox.mutation-inventory/v1.1\x00" + legacy))
		}
		if old != legacy {
			t.Fatalf("legacy %s projection changed", version)
		}
	}
}

func TestStorageCapacityProjectionFailsClosed(t *testing.T) {
	for name, change := range map[string]func(*Inventory){
		"below minimum":    func(i *Inventory) { i.Storages[0].TotalBytes = (1 << 40) - 1 },
		"unknown capacity": func(i *Inventory) { i.Storages[0].TotalBytes = 0 },
		"missing storage":  func(i *Inventory) { i.Storages = nil },
		"wrong node":       func(i *Inventory) { i.Storages[0].Node = "other" },
		"malformed unconfigured storage": func(i *Inventory) {
			i.Storages = append(i.Storages, Storage{Node: "pve-a", ID: "other", Type: "dir", TotalBytes: -1})
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, i := capacityFixture(t)
			change(&i)
			if _, err := i.MutationDigestForConfig(c); err == nil {
				t.Fatal("unsafe capacity accepted")
			}
		})
	}
	for name, change := range map[string]func(*Inventory){
		"type":     func(i *Inventory) { i.Storages[0].Type = "dir" },
		"offline":  func(i *Inventory) { i.Storages[0].Active = false },
		"disabled": func(i *Inventory) { i.Storages[0].Enabled = false },
		"shared":   func(i *Inventory) { i.Storages[0].Shared = true },
		"format":   func(i *Inventory) { i.Storages[0].Formats = []string{"qcow2"} },
		"bridge":   func(i *Inventory) { i.Bridges[0].VLANAware = false },
		"template": func(i *Inventory) { i.Templates[0].Status = "running" },
	} {
		t.Run(name, func(t *testing.T) {
			c, i := capacityFixture(t)
			a, _ := i.MutationDigestForConfig(c)
			change(&i)
			b, err := i.MutationDigestForConfig(c)
			if err == nil && a == b {
				t.Fatal("structural drift ignored")
			}
		})
	}
}

func TestStorageCapacityPolicyRequiresExactVersionedCoverage(t *testing.T) {
	for name, change := range map[string]func(*TargetConfig){
		"old version": func(c *TargetConfig) { c.SchemaVersion = targetConfigV15 },
		"missing":     func(c *TargetConfig) { c.StorageCapacityFloors = nil },
		"zero":        func(c *TargetConfig) { c.StorageCapacityFloors[0].MinimumTotalBytes = 0 },
		"negative":    func(c *TargetConfig) { c.StorageCapacityFloors[0].MinimumTotalBytes = -1 },
		"foreign":     func(c *TargetConfig) { c.StorageCapacityFloors[0].ID = "other" },
		"duplicate": func(c *TargetConfig) {
			c.ImageImport = &ImageImportProfile{StagingStorage: "stage"}
			c.StorageCapacityFloors = append(c.StorageCapacityFloors, c.StorageCapacityFloors[0])
		},
		"missing staging": func(c *TargetConfig) { c.ImageImport = &ImageImportProfile{StagingStorage: "stage"} },
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := capacityFixture(t)
			change(&c)
			if c.validateStorageCapacityFloors() == nil {
				t.Fatal("invalid floor policy accepted")
			}
		})
	}
	c, i := capacityFixture(t)
	c.Qualification.InventoryDigest, _ = i.MutationDigestForConfig(c)
	if err := c.validateMutation(); err != nil {
		t.Fatal(err)
	}
	c.ImageImport = &ImageImportProfile{StagingStorage: "stage"}
	c.StorageCapacityFloors = append(c.StorageCapacityFloors, StorageCapacityFloor{ID: "stage", MinimumTotalBytes: 1 << 30})
	if err := c.validateStorageCapacityFloors(); err != nil {
		t.Fatal(err)
	}
	if _, err := i.MutationDigestForConfig(c); err == nil {
		t.Fatal("missing staging storage accepted")
	}
	i.Storages = append(i.Storages, Storage{Node: "pve-a", ID: "stage", Type: "dir", TotalBytes: 1 << 30})
	if _, err := i.MutationDigestForConfig(c); err != nil {
		t.Fatal(err)
	}
}

// Exercise the actual import preflight, including changed totals between plan
// and execution, rather than testing only the digest helper.
type capacityImageAPI struct {
	*fakeImageAPI
	inventory Inventory
}

func (f *capacityImageAPI) Discover(context.Context) (Inventory, error) { return f.inventory, nil }
func TestImageImportRechecksStorageCapacity(t *testing.T) {
	for _, below := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary drift", true: "below minimum"}[below], func(t *testing.T) {
			_, fake, request, data := imageFixture(t)
			config := fake.config
			config.SchemaVersion = targetConfigV16
			config.ImageImport.CPUModel = "host"
			config.StorageCapacityFloors = []StorageCapacityFloor{{ID: "local-lvm", MinimumTotalBytes: 1 << 40}, {ID: "local", MinimumTotalBytes: 1 << 30}}
			inventory := mutationTestInventory()
			inventory.Storages[0].TotalBytes = 3 << 40
			inventory.Storages = append(inventory.Storages, Storage{Node: "pve-a", ID: "local", Type: "dir", TotalBytes: 4 << 30, Active: true, Enabled: true, Content: []string{"iso"}})
			config.Qualification.InventoryDigest, _ = inventory.MutationDigestForConfig(config)
			fake.config = config
			request.Target.ConfigEnvelope, _ = json.Marshal(config)
			client := &capacityImageAPI{fakeImageAPI: fake, inventory: inventory}
			provider := newT0MutationService(ClientFactoryFunc(func([]byte, []byte) (DiscoveryClient, error) { return client, nil }))
			service, err := NewImageService(provider, config.JournalDirectory)
			if err != nil {
				t.Fatal(err)
			}
			planned, err := service.Plan(context.Background(), &wire.ImageImportServicePlanRequest{Input: request})
			if err != nil {
				t.Fatal(err)
			}
			request.ReservedPlan = planned.Plan
			uploadImageFixture(t, service, request, data)
			for i := range client.inventory.Storages {
				if client.inventory.Storages[i].ID == "local-lvm" {
					client.inventory.Storages[i].TotalBytes -= 131072
					if below {
						client.inventory.Storages[i].TotalBytes = (1 << 40) - 1
					}
				}
			}
			result, err := service.Import(context.Background(), request)
			if below {
				if err == nil || fake.create != 0 || fake.upload != 0 {
					t.Fatal("below-floor import performed provider writes")
				}
				return
			}
			if err != nil || result.State != "succeeded" {
				t.Fatalf("valid capacity drift rejected: %v %+v", err, result)
			}
			if fake.create != 1 {
				t.Fatal("image not imported")
			}
		})
	}
}
