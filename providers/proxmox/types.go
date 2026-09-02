// Package proxmox implements target-scoped Proxmox VE discovery. This first
// slice is deliberately read-only and does not implement provider mutations.
package proxmox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

type TargetConfig struct {
	Endpoint              string `json:"endpoint"`
	CACertificatePEM      string `json:"caCertificatePEM,omitempty"`
	RequestTimeoutSeconds uint32 `json:"requestTimeoutSeconds,omitempty"`
	MaxResponseBytes      int64  `json:"maxResponseBytes,omitempty"`
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
