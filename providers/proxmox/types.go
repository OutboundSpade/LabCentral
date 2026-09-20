// Package proxmox implements target-scoped Proxmox VE discovery and the gated,
// fixed-node QEMU mutation subset.
package proxmox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/OutboundSpade/LabCentral/pkg/containervm"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const (
	PluginID      = "io.labcentral.provider.proxmox"
	PluginVersion = "0.1.0"

	maximumInventoryItems       = 4096
	maximumNestedInventoryItems = 8192
	maximumFieldBytes           = 512
)

var nodeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)
var repoIDPattern = regexp.MustCompile(`^[0-9A-Fa-f]{8,64}$`)
var aliasPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var storageIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var sha1Pattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var sha256Pattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var pveReleasePattern = regexp.MustCompile(`^9\.[0-9]+\.[0-9]+$`)
var templateDiskPattern = regexp.MustCompile(`^(scsi|virtio)[0-9]{1,2}$`)

const (
	targetConfigV11      = "proxmox.target-config/v1.1"
	targetConfigV15      = "proxmox.target-config/v1.5"
	targetConfigV14      = "proxmox.target-config/v1.4"
	targetConfigV13      = "proxmox.target-config/v1.3"
	targetConfigV12      = "proxmox.target-config/v1.2"
	mutationInventoryV10 = "proxmox.mutation-inventory/v1.0"
	supportedPVERange    = ">=9.0.0 <10.0.0"
)

type TargetConfig struct {
	ImageImport           *ImageImportProfile         `json:"imageImport,omitempty"`
	ContainerRuntime      *ContainerRuntimeProfile    `json:"containerRuntime,omitempty"`
	SchemaVersion         string                      `json:"schemaVersion,omitempty"`
	Endpoint              string                      `json:"endpoint"`
	CACertificatePEM      string                      `json:"caCertificatePEM,omitempty"`
	RequestTimeoutSeconds uint32                      `json:"requestTimeoutSeconds,omitempty"`
	MaxResponseBytes      int64                       `json:"maxResponseBytes,omitempty"`
	MutationEnabled       bool                        `json:"mutationEnabled,omitempty"`
	InstallationID        string                      `json:"installationID,omitempty"`
	SupportedPVERange     string                      `json:"supportedPVERange,omitempty"`
	DestinationNode       string                      `json:"destinationNode,omitempty"`
	Pool                  string                      `json:"pool,omitempty"`
	VMIDRanges            []IntegerRange              `json:"vmidRanges,omitempty"`
	Templates             []TemplateProfile           `json:"templates,omitempty"`
	Storages              []StorageProfile            `json:"storages,omitempty"`
	Networks              []NetworkProfile            `json:"networks,omitempty"`
	Limits                MutationLimits              `json:"limits,omitempty"`
	ManagementIsolation   ManagementIsolationEvidence `json:"managementIsolation,omitempty"`
	Qualification         TargetQualification         `json:"qualification,omitempty"`
	PermissionScopes      []PermissionScope           `json:"permissionScopes,omitempty"`
	JournalDirectory      string                      `json:"journalDirectory,omitempty"`
	ConsoleAccess         ConsoleAccessConfig         `json:"consoleAccess,omitempty"`
}

type ConsoleAccessConfig struct {
	Enabled                bool   `json:"enabled,omitempty"`
	MaximumDurationSeconds uint32 `json:"maximumDurationSeconds,omitempty"`
}

type IntegerRange struct {
	Start uint32 `json:"start"`
	End   uint32 `json:"end"`
}

type TemplateProfile struct {
	Alias                string                `json:"alias"`
	SourceNode           string                `json:"sourceNode"`
	VMID                 uint32                `json:"vmid"`
	ConfigDigest         string                `json:"configDigest"`
	ContentDigest        string                `json:"contentDigest"`
	Architecture         string                `json:"architecture"`
	CloudInitDrive       bool                  `json:"cloudInitDrive"`
	GuestAgent           bool                  `json:"guestAgent"`
	ForbidUnknownDevices bool                  `json:"forbidUnknownDevices"`
	Disks                []TemplateDiskProfile `json:"disks"`
}

type TemplateDiskProfile struct {
	Device    string `json:"device"`
	SizeBytes uint64 `json:"sizeBytes"`
	Boot      bool   `json:"boot,omitempty"`
}

type StorageProfile struct {
	Alias  string `json:"alias"`
	ID     string `json:"id"`
	Format string `json:"format"`
}

type NetworkProfile struct {
	Egress     *NetworkEgressProfile `json:"egress,omitempty"`
	Alias      string                `json:"alias"`
	Bridge     string                `json:"bridge"`
	VLANRanges []IntegerRange        `json:"vlanRanges"`
}

type MutationLimits struct {
	MaxCPUMillis   uint32 `json:"maxCPUMillis"`
	MaxMemoryBytes uint64 `json:"maxMemoryBytes"`
	MaxNICs        uint32 `json:"maxNICs"`
	MaxVMs         uint32 `json:"maxVMs"`
}

type ManagementIsolationEvidence struct {
	AttestationDigest        string `json:"attestationDigest"`
	ProbeDigest              string `json:"probeDigest"`
	IntendedSegmentReachable bool   `json:"intendedSegmentReachable"`
	ManagementReachable      bool   `json:"managementReachable"`
	CrossInstanceReachable   bool   `json:"crossInstanceReachable"`
}

type TargetQualification struct {
	PVERelease       string `json:"pveRelease"`
	CapabilityDigest string `json:"capabilityDigest"`
	InventoryDigest  string `json:"inventoryDigest"`
	PolicyDigest     string `json:"policyDigest"`
	EvidenceDigest   string `json:"evidenceDigest"`
	Passed           bool   `json:"passed"`
}

type PermissionScope struct {
	Path       string   `json:"path"`
	Privileges []string `json:"privileges"`
}

var forbiddenMutationPrivileges = map[string]struct{}{"VM.GuestAgent.Unrestricted": {}, "Sys.Modify": {}}

func (config TargetConfig) validateMutation() error {
	if !config.MutationEnabled {
		return nil
	}
	if config.SchemaVersion != targetConfigV11 && config.SchemaVersion != targetConfigV12 && config.SchemaVersion != targetConfigV13 && config.SchemaVersion != targetConfigV14 && config.SchemaVersion != targetConfigV15 {
		return fmt.Errorf("mutation requires schemaVersion %q or %q", targetConfigV11, targetConfigV12)
	}
	if err := config.validateImageImport(); err != nil {
		return err
	}
	if runtime := config.ContainerRuntime; runtime != nil {
		if (config.SchemaVersion != targetConfigV13 && config.SchemaVersion != targetConfigV14 && config.SchemaVersion != targetConfigV15) || config.Pool == "" || runtime.SchemaVersion != containervm.Version || len(runtime.Images) == 0 || len(runtime.Images) > 64 {
			return fmt.Errorf("invalid container runtime profile")
		}
		if _, ok := findTemplateProfile(config, runtime.TemplateAlias); !ok {
			return fmt.Errorf("runtime template unavailable")
		}
		seen := map[string]bool{}
		for _, image := range runtime.Images {
			if !containervm.ValidImage(image) || seen[image] {
				return fmt.Errorf("invalid runtime image")
			}
			seen[image] = true
		}
	}

	if config.ConsoleAccess.Enabled {
		if (config.SchemaVersion != targetConfigV12 && config.SchemaVersion != targetConfigV13 && config.SchemaVersion != targetConfigV14 && config.SchemaVersion != targetConfigV15) || config.ConsoleAccess.MaximumDurationSeconds < 60 || config.ConsoleAccess.MaximumDurationSeconds > 3600 {
			return fmt.Errorf("console access requires schemaVersion %q and a maximum duration between 60 and 3600 seconds", targetConfigV12)
		}
	} else if config.ConsoleAccess.MaximumDurationSeconds != 0 {
		return fmt.Errorf("console access duration requires console access to be enabled")
	}
	if config.SupportedPVERange != supportedPVERange {
		return fmt.Errorf("supportedPVERange must be %q", supportedPVERange)
	}
	if !logicalIDPattern.MatchString(config.InstallationID) {
		return fmt.Errorf("installationID is invalid")
	}
	if !nodeNamePattern.MatchString(config.DestinationNode) || config.Pool != "" && !storageIDPattern.MatchString(config.Pool) {
		return fmt.Errorf("destination node or pool is invalid")
	}
	if err := validateDisjointRanges(config.VMIDRanges, 100, 999999999); err != nil {
		return fmt.Errorf("vmidRanges: %w", err)
	}
	if len(config.Templates) == 0 || len(config.Templates) > 64 {
		return fmt.Errorf("templates must contain between 1 and 64 entries")
	}
	seenAliases := make(map[string]struct{})
	seenTemplates := make(map[string]struct{})
	for _, profile := range config.Templates {
		if !aliasPattern.MatchString(profile.Alias) || !nodeNamePattern.MatchString(profile.SourceNode) || profile.SourceNode != config.DestinationNode || profile.VMID < 100 || !sha1Pattern.MatchString(profile.ConfigDigest) || !sha256Pattern.MatchString(profile.ContentDigest) || profile.Architecture != "amd64" || !profile.CloudInitDrive || !profile.GuestAgent || !profile.ForbidUnknownDevices {
			return fmt.Errorf("template profile is outside the supported fixed-node Linux subset")
		}
		if len(profile.Disks) == 0 || len(profile.Disks) > 16 {
			return fmt.Errorf("template profile must declare between 1 and 16 cloned disks")
		}
		seenDisks := make(map[string]struct{})
		bootDisks := 0
		for _, disk := range profile.Disks {
			if !templateDiskPattern.MatchString(disk.Device) || disk.SizeBytes == 0 {
				return fmt.Errorf("template disk profile is invalid")
			}
			if _, exists := seenDisks[disk.Device]; exists {
				return fmt.Errorf("duplicate template disk %q", disk.Device)
			}
			seenDisks[disk.Device] = struct{}{}
			if disk.Boot {
				bootDisks++
			}
		}
		if bootDisks != 1 {
			return fmt.Errorf("template profile must declare exactly one boot disk")
		}
		if _, exists := seenAliases[profile.Alias]; exists {
			return fmt.Errorf("duplicate profile alias %q", profile.Alias)
		}
		seenAliases[profile.Alias] = struct{}{}
		identity := fmt.Sprintf("%s/%d", profile.SourceNode, profile.VMID)
		if _, exists := seenTemplates[identity]; exists {
			return fmt.Errorf("duplicate template identity %q", identity)
		}
		seenTemplates[identity] = struct{}{}
	}
	if len(config.Storages) == 0 || len(config.Storages) > 16 {
		return fmt.Errorf("storages must contain between 1 and 16 profiles")
	}
	seenStorageIDs := make(map[string]struct{})
	for _, storage := range config.Storages {
		if !aliasPattern.MatchString(storage.Alias) || !storageIDPattern.MatchString(storage.ID) || (storage.Format != "qcow2" && storage.Format != "raw") {
			return fmt.Errorf("storage profile is invalid")
		}
		if _, exists := seenAliases[storage.Alias]; exists {
			return fmt.Errorf("duplicate profile alias %q", storage.Alias)
		}
		if _, exists := seenStorageIDs[storage.ID]; exists {
			return fmt.Errorf("duplicate storage ID %q", storage.ID)
		}
		seenAliases[storage.Alias] = struct{}{}
		seenStorageIDs[storage.ID] = struct{}{}
	}
	if len(config.Networks) == 0 || len(config.Networks) > 16 {
		return fmt.Errorf("networks must contain between 1 and 16 profiles")
	}
	seenBridges := make(map[string]struct{})
	for _, network := range config.Networks {
		if !aliasPattern.MatchString(network.Alias) || !storageIDPattern.MatchString(network.Bridge) {
			return fmt.Errorf("network profile is invalid")
		}
		if _, exists := seenAliases[network.Alias]; exists {
			return fmt.Errorf("duplicate profile alias %q", network.Alias)
		}
		if _, exists := seenBridges[network.Bridge]; exists && config.SchemaVersion != targetConfigV15 {
			return fmt.Errorf("duplicate network bridge %q", network.Bridge)
		}
		if err := validateDisjointRanges(network.VLANRanges, 1, 4094); err != nil {
			return fmt.Errorf("network vlanRanges: %w", err)
		}
		if err := validateNetworkEgress(config.SchemaVersion, network); err != nil {
			return err
		}
		seenAliases[network.Alias] = struct{}{}
		seenBridges[network.Bridge] = struct{}{}
	}
	if config.SchemaVersion == targetConfigV15 {
		for i, a := range config.Networks {
			for _, b := range config.Networks[i+1:] {
				if a.Bridge == b.Bridge && a.Egress == nil && b.Egress == nil {
					return fmt.Errorf("duplicate denied network bridge")
				}
				for _, ar := range a.VLANRanges {
					for _, br := range b.VLANRanges {
						if ar.Start <= br.End && br.Start <= ar.End {
							return fmt.Errorf("network profiles have overlapping VLAN allocations")
						}
					}
				}
			}
		}
	}
	if config.Limits.MaxCPUMillis == 0 || config.Limits.MaxCPUMillis > 128000 || config.Limits.MaxMemoryBytes < 16<<20 || config.Limits.MaxNICs == 0 || config.Limits.MaxNICs > 16 || config.Limits.MaxVMs == 0 {
		return fmt.Errorf("mutation limits are invalid")
	}
	if !sha256Pattern.MatchString(config.ManagementIsolation.AttestationDigest) || !sha256Pattern.MatchString(config.ManagementIsolation.ProbeDigest) || !config.ManagementIsolation.IntendedSegmentReachable || config.ManagementIsolation.ManagementReachable || config.ManagementIsolation.CrossInstanceReachable {
		return fmt.Errorf("management isolation evidence is invalid")
	}
	if !config.Qualification.Passed || !pveReleasePattern.MatchString(config.Qualification.PVERelease) || !sha256Pattern.MatchString(config.Qualification.CapabilityDigest) || !sha256Pattern.MatchString(config.Qualification.InventoryDigest) || !sha256Pattern.MatchString(config.Qualification.PolicyDigest) || !sha256Pattern.MatchString(config.Qualification.EvidenceDigest) {
		return fmt.Errorf("exact-target qualification evidence is invalid")
	}
	if err := validatePermissionScopes(config); err != nil {
		return err
	}
	if !filepath.IsAbs(config.JournalDirectory) || filepath.Clean(config.JournalDirectory) != config.JournalDirectory || config.JournalDirectory == "/" {
		return fmt.Errorf("journalDirectory must be an absolute clean non-root path")
	}
	return nil
}

func validatePermissionScopes(config TargetConfig) error {
	scopes := config.PermissionScopes
	if len(scopes) == 0 || len(scopes) > 32 {
		return fmt.Errorf("permissionScopes must contain between 1 and 32 entries")
	}
	expected := expectedPermissionScopes(config)
	if len(scopes) != len(expected) {
		return fmt.Errorf("permissionScopes do not exactly cover configured target objects")
	}
	seenPaths := make(map[string]struct{})
	for _, scope := range scopes {
		if len(scope.Path) == 0 || len(scope.Path) > 512 || scope.Path == "/" || !strings.HasPrefix(scope.Path, "/") || strings.Contains(scope.Path, "..") || len(scope.Privileges) == 0 || len(scope.Privileges) > 32 {
			return fmt.Errorf("permission scope is invalid")
		}
		if _, exists := seenPaths[scope.Path]; exists {
			return fmt.Errorf("duplicate permission scope %q", scope.Path)
		}
		seenPaths[scope.Path] = struct{}{}
		expectedPrivileges, expectedPath := expected[scope.Path]
		if !expectedPath || len(scope.Privileges) != len(expectedPrivileges) {
			return fmt.Errorf("permission scope %q is not an exact configured target scope", scope.Path)
		}
		seenPrivileges := make(map[string]struct{})
		for _, privilege := range scope.Privileges {
			if !validField(privilege) {
				return fmt.Errorf("permission scope privilege is invalid")
			}
			if _, forbidden := forbiddenMutationPrivileges[privilege]; forbidden && !(privilege == "VM.GuestAgent.Unrestricted" && config.ContainerRuntime != nil && config.Pool != "" && scope.Path == "/pool/"+config.Pool) {
				return fmt.Errorf("permission scope contains forbidden broad privilege %q", privilege)
			}
			if _, duplicate := seenPrivileges[privilege]; duplicate {
				return fmt.Errorf("permission scope contains duplicate privilege %q", privilege)
			}
			seenPrivileges[privilege] = struct{}{}
			if !expectedPrivileges[privilege] {
				return fmt.Errorf("permission scope %q contains unexpected privilege %q", scope.Path, privilege)
			}
		}
	}
	for path := range expected {
		if _, exists := seenPaths[path]; !exists {
			return fmt.Errorf("permissionScopes omit configured scope %q", path)
		}
	}
	return nil
}

func expectedPermissionScopes(config TargetConfig) map[string]map[string]bool {
	result := make(map[string]map[string]bool)
	add := func(path string, privileges ...string) {
		if result[path] == nil {
			result[path] = make(map[string]bool)
		}
		for _, privilege := range privileges {
			result[path][privilege] = true
		}
	}
	for _, profile := range config.Templates {
		add(fmt.Sprintf("/vms/%d", profile.VMID), "VM.Audit", "VM.Clone")
	}
	destinationPath := "/vms"
	if config.Pool != "" {
		destinationPath = "/pool/" + config.Pool
	}
	add(destinationPath, "VM.Allocate", "VM.Audit", "VM.Config.CPU", "VM.Config.Cloudinit", "VM.Config.Memory", "VM.Config.Network", "VM.Config.Options", "VM.GuestAgent.Audit", "VM.PowerMgmt")
	if config.ImageImport != nil {
		add(destinationPath, "VM.Clone", "VM.Config.Disk", "VM.Config.HWType")
		add("/storage/"+config.ImageImport.StagingStorage, "Datastore.Allocate", "Datastore.AllocateTemplate", "Datastore.Audit")
	}
	if config.ContainerRuntime != nil {
		add(destinationPath, "VM.GuestAgent.Unrestricted")
	}
	if config.ConsoleAccess.Enabled {
		add(destinationPath, "VM.Console")
	}
	for _, profile := range config.Storages {
		add("/storage/"+profile.ID, "Datastore.AllocateSpace", "Datastore.Audit")
	}
	for _, profile := range config.Networks {
		add("/sdn/zones/localnetwork/"+profile.Bridge, "SDN.Use")
	}
	add("/nodes/"+config.DestinationNode, "Sys.Audit")
	return result
}

func validateDisjointRanges(ranges []IntegerRange, minimum, maximum uint32) error {
	if len(ranges) == 0 || len(ranges) > 64 {
		return fmt.Errorf("must contain between 1 and 64 ranges")
	}
	ordered := append([]IntegerRange(nil), ranges...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })
	for index, value := range ordered {
		if value.Start < minimum || value.End > maximum || value.Start > value.End {
			return fmt.Errorf("range is outside %d-%d", minimum, maximum)
		}
		if index > 0 && ordered[index-1].End >= value.Start {
			return fmt.Errorf("ranges overlap")
		}
	}
	return nil
}

type APITokenCredentials struct {
	TokenID     string `json:"tokenID"`
	TokenSecret string `json:"tokenSecret"`
}

type Version struct {
	Release string `json:"release"`
	Version string `json:"version"`
	RepoID  string `json:"repoID"`
	Console string `json:"console,omitempty"`
}

type Node struct {
	Name            string  `json:"name"`
	Status          string  `json:"status"`
	MaxCPU          int64   `json:"maxCPU,omitempty"`
	CPUUsage        float64 `json:"cpuUsage,omitempty"`
	MaxMemoryBytes  int64   `json:"maxMemoryBytes,omitempty"`
	UsedMemoryBytes int64   `json:"usedMemoryBytes,omitempty"`
	UptimeSeconds   int64   `json:"uptimeSeconds,omitempty"`
	QEMUAPI         bool    `json:"qemuAPI"`
}

type Storage struct {
	Node           string   `json:"node"`
	ID             string   `json:"id"`
	Type           string   `json:"type"`
	Content        []string `json:"content"`
	Formats        []string `json:"formats,omitempty"`
	DefaultFormat  string   `json:"defaultFormat,omitempty"`
	Active         bool     `json:"active"`
	Enabled        bool     `json:"enabled"`
	Shared         bool     `json:"shared"`
	TotalBytes     int64    `json:"totalBytes,omitempty"`
	UsedBytes      int64    `json:"usedBytes,omitempty"`
	AvailableBytes int64    `json:"availableBytes,omitempty"`
}

type Bridge struct {
	Node      string   `json:"node"`
	Name      string   `json:"name"`
	Active    bool     `json:"active"`
	Autostart bool     `json:"autostart"`
	VLANAware bool     `json:"vlanAware"`
	Ports     []string `json:"ports,omitempty"`
	CIDR      string   `json:"cidr,omitempty"`
	CIDR6     string   `json:"cidr6,omitempty"`
}

type Template struct {
	Node   string `json:"node"`
	VMID   int64  `json:"vmID"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type DiscoveryIssue struct {
	Node  string `json:"node"`
	Scope string `json:"scope"`
	Code  string `json:"code"`
}

type Inventory struct {
	Version   Version          `json:"version"`
	Nodes     []Node           `json:"nodes"`
	Storages  []Storage        `json:"storages"`
	Bridges   []Bridge         `json:"bridges"`
	Templates []Template       `json:"templates"`
	Issues    []DiscoveryIssue `json:"issues,omitempty"`
}

// mutationInventoryDocument contains only target facts that must remain stable
// between qualification, planning, and mutation preflight. Point-in-time load,
// uptime, and free-space counters remain in the full discovery inventory but
// cannot safely fence a multi-request operation on a live target.
type mutationInventoryDocument struct {
	SchemaVersion string                   `json:"schemaVersion"`
	Version       Version                  `json:"version"`
	Nodes         []mutationInventoryNode  `json:"nodes"`
	Storages      []mutationInventoryStore `json:"storages"`
	Bridges       []Bridge                 `json:"bridges"`
	Templates     []Template               `json:"templates"`
	Issues        []DiscoveryIssue         `json:"issues,omitempty"`
}

type mutationInventoryNode struct {
	Name           string `json:"name"`
	Status         string `json:"status"`
	MaxCPU         int64  `json:"maxCPU,omitempty"`
	MaxMemoryBytes int64  `json:"maxMemoryBytes,omitempty"`
	QEMUAPI        bool   `json:"qemuAPI"`
}

type mutationInventoryStore struct {
	Node          string   `json:"node"`
	ID            string   `json:"id"`
	Type          string   `json:"type"`
	Content       []string `json:"content"`
	Formats       []string `json:"formats,omitempty"`
	DefaultFormat string   `json:"defaultFormat,omitempty"`
	Active        bool     `json:"active"`
	Enabled       bool     `json:"enabled"`
	Shared        bool     `json:"shared"`
	TotalBytes    int64    `json:"totalBytes,omitempty"`
}

func (inventory *Inventory) normalize() error {
	if inventory.Nodes == nil {
		inventory.Nodes = []Node{}
	}
	if inventory.Storages == nil {
		inventory.Storages = []Storage{}
	}
	if inventory.Bridges == nil {
		inventory.Bridges = []Bridge{}
	}
	if inventory.Templates == nil {
		inventory.Templates = []Template{}
	}
	inventory.Version.Release = strings.TrimSpace(inventory.Version.Release)
	inventory.Version.Version = strings.TrimSpace(inventory.Version.Version)
	inventory.Version.RepoID = strings.TrimSpace(inventory.Version.RepoID)
	inventory.Version.Console = strings.TrimSpace(inventory.Version.Console)
	if inventory.Version.Version == "" || inventory.Version.Release == "" || inventory.Version.RepoID == "" {
		return fmt.Errorf("Proxmox version response omitted required fields")
	}
	if !validField(inventory.Version.Release) || !validField(inventory.Version.Version) || !repoIDPattern.MatchString(inventory.Version.RepoID) || !validOptionalField(inventory.Version.Console) {
		return fmt.Errorf("Proxmox version response contained an invalid field")
	}
	if len(inventory.Nodes) > maximumNodes || len(inventory.Nodes)+len(inventory.Storages)+len(inventory.Bridges)+len(inventory.Templates)+len(inventory.Issues) > maximumInventoryItems {
		return fmt.Errorf("Proxmox inventory exceeded an item limit")
	}
	seenNodes := make(map[string]struct{}, len(inventory.Nodes))
	for index := range inventory.Nodes {
		node := &inventory.Nodes[index]
		node.Name = strings.TrimSpace(node.Name)
		node.Status = strings.TrimSpace(node.Status)
		if !nodeNamePattern.MatchString(node.Name) || !validField(node.Status) || node.MaxCPU < 0 || node.CPUUsage < 0 || node.MaxMemoryBytes < 0 || node.UsedMemoryBytes < 0 || node.UptimeSeconds < 0 {
			return fmt.Errorf("Proxmox node response contained an invalid node field")
		}
		if _, duplicate := seenNodes[node.Name]; duplicate {
			return fmt.Errorf("Proxmox node response contained duplicate node %q", node.Name)
		}
		seenNodes[node.Name] = struct{}{}
	}
	seenStorages := make(map[string]struct{}, len(inventory.Storages))
	for index := range inventory.Storages {
		storage := &inventory.Storages[index]
		storage.Node = strings.TrimSpace(storage.Node)
		storage.ID = strings.TrimSpace(storage.ID)
		storage.Type = strings.TrimSpace(storage.Type)
		storage.DefaultFormat = strings.TrimSpace(storage.DefaultFormat)
		if _, exists := seenNodes[storage.Node]; !exists || !validField(storage.ID) || !validField(storage.Type) || !validOptionalField(storage.DefaultFormat) || storage.TotalBytes < 0 || storage.UsedBytes < 0 || storage.AvailableBytes < 0 {
			return fmt.Errorf("Proxmox storage response contained an invalid field")
		}
		key := storage.Node + "\x00" + storage.ID
		if _, duplicate := seenStorages[key]; duplicate {
			return fmt.Errorf("Proxmox storage response contained a duplicate storage")
		}
		seenStorages[key] = struct{}{}
		storage.Content = sortedUnique(storage.Content)
		storage.Formats = sortedUnique(storage.Formats)
		if !validFields(storage.Content) || !validFields(storage.Formats) {
			return fmt.Errorf("Proxmox storage response exceeded a field limit")
		}
	}
	seenBridges := make(map[string]struct{}, len(inventory.Bridges))
	for index := range inventory.Bridges {
		bridge := &inventory.Bridges[index]
		bridge.Node = strings.TrimSpace(bridge.Node)
		bridge.Name = strings.TrimSpace(bridge.Name)
		bridge.CIDR = strings.TrimSpace(bridge.CIDR)
		bridge.CIDR6 = strings.TrimSpace(bridge.CIDR6)
		bridge.Ports = sortedUnique(bridge.Ports)
		if _, exists := seenNodes[bridge.Node]; !exists || !validField(bridge.Name) || !validOptionalField(bridge.CIDR) || !validOptionalField(bridge.CIDR6) || !validFields(bridge.Ports) {
			return fmt.Errorf("Proxmox network response contained an invalid field")
		}
		key := bridge.Node + "\x00" + bridge.Name
		if _, duplicate := seenBridges[key]; duplicate {
			return fmt.Errorf("Proxmox network response contained a duplicate bridge")
		}
		seenBridges[key] = struct{}{}
	}
	seenTemplates := make(map[string]struct{}, len(inventory.Templates))
	for index := range inventory.Templates {
		template := &inventory.Templates[index]
		template.Node = strings.TrimSpace(template.Node)
		template.Name = strings.TrimSpace(template.Name)
		template.Status = strings.TrimSpace(template.Status)
		if _, exists := seenNodes[template.Node]; !exists || template.VMID <= 0 || !validOptionalField(template.Name) || !validField(template.Status) {
			return fmt.Errorf("Proxmox QEMU response contained an invalid field")
		}
		key := fmt.Sprintf("%s\x00%d", template.Node, template.VMID)
		if _, duplicate := seenTemplates[key]; duplicate {
			return fmt.Errorf("Proxmox QEMU response contained a duplicate template")
		}
		seenTemplates[key] = struct{}{}
	}
	for index := range inventory.Issues {
		issue := &inventory.Issues[index]
		issue.Node = strings.TrimSpace(issue.Node)
		issue.Scope = strings.TrimSpace(issue.Scope)
		issue.Code = strings.TrimSpace(issue.Code)
		if _, exists := seenNodes[issue.Node]; !exists || !validField(issue.Scope) || !validField(issue.Code) {
			return fmt.Errorf("Proxmox discovery issue contained an invalid field")
		}
	}
	if nestedInventoryItemCount(*inventory) > maximumNestedInventoryItems {
		return fmt.Errorf("Proxmox inventory exceeded a nested item limit")
	}
	sort.Slice(inventory.Nodes, func(i, j int) bool { return inventory.Nodes[i].Name < inventory.Nodes[j].Name })
	sort.Slice(inventory.Storages, func(i, j int) bool {
		if inventory.Storages[i].Node != inventory.Storages[j].Node {
			return inventory.Storages[i].Node < inventory.Storages[j].Node
		}
		return inventory.Storages[i].ID < inventory.Storages[j].ID
	})
	sort.Slice(inventory.Bridges, func(i, j int) bool {
		if inventory.Bridges[i].Node != inventory.Bridges[j].Node {
			return inventory.Bridges[i].Node < inventory.Bridges[j].Node
		}
		return inventory.Bridges[i].Name < inventory.Bridges[j].Name
	})
	sort.Slice(inventory.Templates, func(i, j int) bool {
		if inventory.Templates[i].Node != inventory.Templates[j].Node {
			return inventory.Templates[i].Node < inventory.Templates[j].Node
		}
		return inventory.Templates[i].VMID < inventory.Templates[j].VMID
	})
	sort.Slice(inventory.Issues, func(i, j int) bool {
		if inventory.Issues[i].Node != inventory.Issues[j].Node {
			return inventory.Issues[i].Node < inventory.Issues[j].Node
		}
		if inventory.Issues[i].Scope != inventory.Issues[j].Scope {
			return inventory.Issues[i].Scope < inventory.Issues[j].Scope
		}
		return inventory.Issues[i].Code < inventory.Issues[j].Code
	})
	return nil
}

func nestedInventoryItemCount(inventory Inventory) int {
	count := 0
	for _, storage := range inventory.Storages {
		count += len(storage.Content) + len(storage.Formats)
	}
	for _, bridge := range inventory.Bridges {
		count += len(bridge.Ports)
	}
	return count
}

func validField(value string) bool {
	if value == "" || len(value) > maximumFieldBytes {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validOptionalField(value string) bool {
	return value == "" || validField(value)
}

func validFields(values []string) bool {
	for _, value := range values {
		if !validField(value) {
			return false
		}
	}
	return true
}

func (inventory *Inventory) Digest() (string, error) {
	if err := inventory.normalize(); err != nil {
		return "", err
	}
	contents, err := json.Marshal(inventory)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// MutationDigest returns a deterministic digest of the versioned,
// mutation-relevant inventory projection. It deliberately excludes volatile
// utilization counters that would otherwise make every real preflight stale.
func (inventory *Inventory) MutationDigest() (string, error) {
	if err := inventory.normalize(); err != nil {
		return "", err
	}
	document := mutationInventoryDocument{
		SchemaVersion: mutationInventoryV10,
		Version:       inventory.Version,
		Bridges:       append([]Bridge(nil), inventory.Bridges...),
		Templates:     append([]Template(nil), inventory.Templates...),
		Issues:        append([]DiscoveryIssue(nil), inventory.Issues...),
	}
	for _, node := range inventory.Nodes {
		document.Nodes = append(document.Nodes, mutationInventoryNode{
			Name: node.Name, Status: node.Status, MaxCPU: node.MaxCPU,
			MaxMemoryBytes: node.MaxMemoryBytes, QEMUAPI: node.QEMUAPI,
		})
	}
	for _, storage := range inventory.Storages {
		document.Storages = append(document.Storages, mutationInventoryStore{
			Node: storage.Node, ID: storage.ID, Type: storage.Type,
			Content: append([]string(nil), storage.Content...), Formats: append([]string(nil), storage.Formats...),
			DefaultFormat: storage.DefaultFormat, Active: storage.Active, Enabled: storage.Enabled,
			Shared: storage.Shared, TotalBytes: storage.TotalBytes,
		})
	}
	contents, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func sortedUnique(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
