package proxmox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	validationPlanV10 = "proxmox.validation-plan/v1.0"
	qemuPlanV10       = "proxmox.qemu-plan/v1.0"
	qemuStateV10      = "proxmox.qemu-state/v1.0"
	journalV10        = "proxmox.invocation-journal/v1.0"

	vmidClaimNamespace = "proxmox.qemu.vmid"
	vlanClaimNamespace = "proxmox.network.vlan"
)

var logicalIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)

type canonicalLabDocument struct {
	APIVersion        string                   `json:"apiVersion"`
	Metadata          canonicalLabMetadata     `json:"metadata"`
	Requirements      canonicalLabRequirements `json:"requirements,omitempty"`
	Resources         []canonicalResource      `json:"resources"`
	Inputs            json.RawMessage          `json:"inputs,omitempty"`
	ProviderOverrides json.RawMessage          `json:"providerOverrides,omitempty"`
}

type canonicalLabMetadata struct {
	Name string `json:"name"`
}

type canonicalLabRequirements struct {
	IsolationClass string            `json:"isolationClass,omitempty"`
	Capabilities   []string          `json:"capabilities,omitempty"`
	TargetSelector map[string]string `json:"targetSelector,omitempty"`
}

type canonicalResource struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	DependsOn []string        `json:"dependsOn,omitempty"`
	Spec      json.RawMessage `json:"spec"`
}

type canonicalNetworkSpec struct {
	Kind     string `json:"kind"`
	IPv4CIDR string `json:"ipv4Cidr"`
	IPv6CIDR string `json:"ipv6Cidr,omitempty"`
	MTU      uint16 `json:"mtu,omitempty"`
	IPAM     struct {
		Mode string `json:"mode"`
	} `json:"ipam"`
	Egress struct {
		Mode         string   `json:"mode"`
		Destinations []string `json:"destinations,omitempty"`
	} `json:"egress"`
}

type canonicalVMSpec struct {
	Image struct {
		Repository string `json:"repository"`
		Digest     string `json:"digest"`
		Ref        string `json:"ref,omitempty"`
	} `json:"image"`
	OSFamily string `json:"osFamily"`
	Compute  struct {
		CPUMillis    uint32 `json:"cpuMillis"`
		MemoryBytes  uint64 `json:"memoryBytes"`
		Architecture string `json:"architecture"`
	} `json:"compute"`
	Interfaces []struct {
		NetworkRef string `json:"networkRef"`
		IPv4       string `json:"ipv4,omitempty"`
		IPv6       string `json:"ipv6,omitempty"`
		MAC        string `json:"mac,omitempty"`
	} `json:"interfaces,omitempty"`
	Mounts []struct {
		VolumeRef string `json:"volumeRef"`
		Path      string `json:"path"`
		ReadOnly  bool   `json:"readOnly,omitempty"`
	} `json:"mounts,omitempty"`
	Provisioning *struct {
		Method      string `json:"method"`
		ArtifactRef string `json:"artifactRef"`
	} `json:"provisioning,omitempty"`
	Lifecycle struct {
		Snapshot bool   `json:"snapshot,omitempty"`
		Reset    string `json:"reset,omitempty"`
	} `json:"lifecycle,omitempty"`
}

type ProviderOverride struct {
	TemplateAlias string `json:"templateAlias,omitempty"`
	StorageAlias  string `json:"storageAlias,omitempty"`
	NetworkAlias  string `json:"networkAlias,omitempty"`
}

type ValidationPlan struct {
	SchemaVersion      string `json:"schemaVersion"`
	TargetID           string `json:"targetId"`
	ConfigFingerprint  string `json:"configFingerprint"`
	CanonicalLabDigest string `json:"canonicalLabDigest"`
	CapabilityDigest   string `json:"capabilityDigest"`
	InventoryDigest    string `json:"inventoryDigest"`
	PolicyDigest       string `json:"policyDigest"`
	TemplateAlias      string `json:"templateAlias"`
	StorageAlias       string `json:"storageAlias"`
	NetworkAlias       string `json:"networkAlias"`
}

type AllocationBinding struct {
	Namespace   string `json:"namespace"`
	Value       string `json:"value"`
	SourceRange string `json:"sourceRange"`
}

type PlanTemplate struct {
	Alias         string `json:"alias"`
	SourceNode    string `json:"sourceNode"`
	VMID          uint32 `json:"vmid"`
	ConfigDigest  string `json:"configDigest"`
	ContentDigest string `json:"contentDigest"`
}

type PlanDestination struct {
	Node          string `json:"node"`
	Pool          string `json:"pool,omitempty"`
	StorageAlias  string `json:"storageAlias"`
	StorageID     string `json:"storageId"`
	StorageFormat string `json:"storageFormat"`
	FullClone     bool   `json:"fullClone"`
}

type OwnershipMarker struct {
	InstallationID  string `json:"installationId"`
	TargetID        string `json:"targetId"`
	InstanceID      string `json:"instanceId"`
	LogicalResource string `json:"logicalResourceId"`
	Generation      uint64 `json:"generation"`
	Token           string `json:"token"`
}

type PlanHardware struct {
	Sockets   uint32 `json:"sockets"`
	Cores     uint32 `json:"cores"`
	CPULimit  string `json:"cpuLimit"`
	MemoryMiB uint64 `json:"memoryMiB"`
	CIUpgrade bool   `json:"ciUpgrade"`
	OnBoot    bool   `json:"onBoot"`
}

type PlanDisk struct {
	Device        string `json:"device"`
	SizeBytes     uint64 `json:"sizeBytes"`
	Boot          bool   `json:"boot,omitempty"`
	StorageID     string `json:"storageId"`
	StorageFormat string `json:"storageFormat"`
}

type NetworkBinding struct {
	LogicalResourceID string `json:"logicalResourceId"`
	ProfileAlias      string `json:"profileAlias"`
	Bridge            string `json:"bridge"`
	VLAN              uint32 `json:"vlan"`
	MAC               string `json:"mac,omitempty"`
	IPv4CIDR          string `json:"ipv4Cidr"`
	IPv4Address       string `json:"ipv4Address,omitempty"`
	Gateway           string `json:"gateway,omitempty"`
}

type PlanWarning struct {
	Code        string `json:"code"`
	SafeMessage string `json:"safeMessage"`
}

type QEMUPlan struct {
	SchemaVersion     string              `json:"schemaVersion"`
	Action            string              `json:"action"`
	ResourceKind      string              `json:"resourceKind"`
	TargetID          string              `json:"targetId"`
	ConfigFingerprint string              `json:"configFingerprint"`
	CapabilityDigest  string              `json:"capabilityDigest"`
	InventoryDigest   string              `json:"inventoryDigest"`
	PolicyDigest      string              `json:"policyDigest"`
	InputDigest       string              `json:"inputDigest"`
	InstanceID        string              `json:"instanceId"`
	LogicalResourceID string              `json:"logicalResourceId"`
	Generation        uint64              `json:"generation"`
	Allocations       []AllocationBinding `json:"allocations"`
	Template          *PlanTemplate       `json:"template,omitempty"`
	Destination       *PlanDestination    `json:"destination,omitempty"`
	Ownership         *OwnershipMarker    `json:"ownership,omitempty"`
	Hardware          *PlanHardware       `json:"hardware,omitempty"`
	Disks             []PlanDisk          `json:"disks,omitempty"`
	Networks          []NetworkBinding    `json:"networks,omitempty"`
	Steps             []string            `json:"steps"`
	Warnings          []PlanWarning       `json:"warnings"`
}

type QEMUProviderState struct {
	SchemaVersion     string           `json:"schemaVersion"`
	ResourceKind      string           `json:"resourceKind"`
	TargetID          string           `json:"targetId"`
	Node              string           `json:"node"`
	VMID              uint32           `json:"vmid,omitempty"`
	Ownership         *OwnershipMarker `json:"ownership,omitempty"`
	OwnershipDigest   string           `json:"ownershipDigest,omitempty"`
	LogicalResourceID string           `json:"logicalResourceId"`
	Generation        uint64           `json:"generation"`
	Volumes           []string         `json:"volumes,omitempty"`
	Networks          []NetworkBinding `json:"networks,omitempty"`
	ConfigDigest      string           `json:"configDigest,omitempty"`
	TaskUPID          string           `json:"taskUpid,omitempty"`
	Phase             string           `json:"phase"`
}

type JournalRecord struct {
	SchemaVersion       string         `json:"schemaVersion"`
	InvocationID        string         `json:"invocationId"`
	TargetID            string         `json:"targetId"`
	Action              string         `json:"action"`
	RequestDigest       string         `json:"requestDigest"`
	Node                string         `json:"node"`
	VMID                uint32         `json:"vmid,omitempty"`
	OwnershipDigest     string         `json:"ownershipDigest"`
	TaskUPID            string         `json:"taskUpid,omitempty"`
	Phase               string         `json:"phase"`
	LastSafeObservation string         `json:"lastSafeObservation,omitempty"`
	Result              *JournalResult `json:"result,omitempty"`
}

type JournalResult struct {
	State                 *QEMUProviderState `json:"state,omitempty"`
	Observed              *QEMUObservedState `json:"observed,omitempty"`
	Error                 *JournalError      `json:"error,omitempty"`
	CompensationAttempted bool               `json:"compensationAttempted,omitempty"`
	CompensationComplete  bool               `json:"compensationComplete,omitempty"`
}

type JournalError struct {
	Code        string `json:"code"`
	Class       string `json:"class"`
	SafeMessage string `json:"safeMessage"`
	Retryable   bool   `json:"retryable,omitempty"`
}

type QEMUObservedState struct {
	SchemaVersion string           `json:"schemaVersion"`
	ResourceKind  string           `json:"resourceKind"`
	Node          string           `json:"node"`
	VMID          uint32           `json:"vmid,omitempty"`
	PowerState    string           `json:"powerState,omitempty"`
	GuestReady    bool             `json:"guestReady,omitempty"`
	Destroyed     bool             `json:"destroyed,omitempty"`
	ConfigDigest  string           `json:"configDigest,omitempty"`
	Networks      []NetworkBinding `json:"networks,omitempty"`
	Leftovers     []string         `json:"leftovers,omitempty"`
}

type journalEnvelope struct {
	Record   JournalRecord `json:"record"`
	Checksum string        `json:"checksum"`
}

func digestDocument(contents []byte) string {
	digest := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func marshalDigest(document any) ([]byte, string, error) {
	contents, err := json.Marshal(document)
	if err != nil {
		return nil, "", err
	}
	return contents, digestDocument(contents), nil
}

func (plan QEMUPlan) validate() error {
	if plan.SchemaVersion != qemuPlanV10 || plan.Action != "ensure" || (plan.ResourceKind != "Network" && plan.ResourceKind != "VM") {
		return fmt.Errorf("invalid plan identity")
	}
	if plan.TargetID == "" || !sha256Pattern.MatchString(plan.ConfigFingerprint) || !sha256Pattern.MatchString(plan.CapabilityDigest) || !sha256Pattern.MatchString(plan.InventoryDigest) || !sha256Pattern.MatchString(plan.PolicyDigest) || !sha256Pattern.MatchString(plan.InputDigest) || plan.InstanceID == "" || !logicalIDPattern.MatchString(plan.LogicalResourceID) || plan.Generation == 0 {
		return fmt.Errorf("invalid plan binding")
	}
	if len(plan.Allocations) == 0 || len(plan.Allocations) > 17 {
		return fmt.Errorf("invalid plan allocations")
	}
	if len(plan.Warnings) > 32 {
		return fmt.Errorf("too many plan warnings")
	}
	for _, warning := range plan.Warnings {
		if !validField(warning.Code) || !validField(warning.SafeMessage) {
			return fmt.Errorf("plan warning is invalid")
		}
	}
	seenClaims := make(map[string]struct{})
	vlanValues := make(map[uint32]bool)
	vmidClaims, vlanClaims := 0, 0
	for _, claim := range plan.Allocations {
		if (claim.Namespace != vmidClaimNamespace && claim.Namespace != vlanClaimNamespace) || claim.Value == "" || claim.SourceRange == "" {
			return fmt.Errorf("invalid allocation binding")
		}
		value, err := strconv.ParseUint(claim.Value, 10, 32)
		if err != nil || (len(claim.Value) > 1 && strings.HasPrefix(claim.Value, "0")) || !allocationInSourceRange(uint32(value), claim.SourceRange) {
			return fmt.Errorf("allocation binding value or source range is invalid")
		}
		if claim.Namespace == vmidClaimNamespace {
			vmidClaims++
			if value < 100 {
				return fmt.Errorf("VMID allocation is invalid")
			}
		} else {
			vlanClaims++
			if value < 1 || value > 4094 {
				return fmt.Errorf("VLAN allocation is invalid")
			}
			vlanValues[uint32(value)] = true
		}
		key := claim.Namespace + "\x00" + claim.Value
		if _, exists := seenClaims[key]; exists {
			return fmt.Errorf("duplicate allocation binding")
		}
		seenClaims[key] = struct{}{}
	}
	if plan.ResourceKind == "Network" {
		if vmidClaims != 0 || vlanClaims != 1 || plan.Template != nil || plan.Destination != nil || plan.Ownership != nil || plan.Hardware != nil || len(plan.Disks) != 0 || len(plan.Networks) != 1 || len(plan.Steps) != 1 || plan.Steps[0] != "bind-network" {
			return fmt.Errorf("network plan contains VM fields")
		}
		if !vlanValues[plan.Networks[0].VLAN] {
			return fmt.Errorf("network binding is not backed by its VLAN claim")
		}
		return validateNetworkBinding(plan.Networks[0])
	}
	if vmidClaims != 1 || vlanClaims > len(plan.Networks) || plan.Template == nil || plan.Destination == nil || plan.Ownership == nil || plan.Hardware == nil || !plan.Destination.FullClone || len(plan.Disks) == 0 || len(plan.Networks) > 16 {
		return fmt.Errorf("VM plan omitted required fields")
	}
	if !aliasPattern.MatchString(plan.Template.Alias) || !nodeNamePattern.MatchString(plan.Template.SourceNode) || plan.Template.VMID < 100 || !sha1Pattern.MatchString(plan.Template.ConfigDigest) || !sha256Pattern.MatchString(plan.Template.ContentDigest) || !nodeNamePattern.MatchString(plan.Destination.Node) || !aliasPattern.MatchString(plan.Destination.StorageAlias) || !storageIDPattern.MatchString(plan.Destination.StorageID) || (plan.Destination.StorageFormat != "raw" && plan.Destination.StorageFormat != "qcow2") {
		return fmt.Errorf("VM plan template or destination is invalid")
	}
	if plan.Ownership.TargetID != plan.TargetID || plan.Ownership.InstanceID != plan.InstanceID || plan.Ownership.LogicalResource != plan.LogicalResourceID || plan.Ownership.Generation != plan.Generation || plan.Ownership.InstallationID == "" || plan.Ownership.Token == "" || len(plan.Ownership.Token) > 512 {
		return fmt.Errorf("VM plan ownership binding is invalid")
	}
	if plan.Hardware.Sockets != 1 || plan.Hardware.Cores == 0 || plan.Hardware.Cores > 128 || plan.Hardware.CPULimit == "" || plan.Hardware.MemoryMiB < 16 || plan.Hardware.CIUpgrade || plan.Hardware.OnBoot {
		return fmt.Errorf("VM plan hardware is invalid")
	}
	seenDisks := make(map[string]struct{})
	for _, disk := range plan.Disks {
		if !diskFieldPattern.MatchString(disk.Device) || disk.SizeBytes == 0 || !storageIDPattern.MatchString(disk.StorageID) || (disk.StorageFormat != "raw" && disk.StorageFormat != "qcow2") {
			return fmt.Errorf("VM plan disk is invalid")
		}
		if _, duplicate := seenDisks[disk.Device]; duplicate {
			return fmt.Errorf("VM plan disk is duplicated")
		}
		seenDisks[disk.Device] = struct{}{}
	}
	expectedSteps := []string{"preflight", "clone", "wait-clone", "configure", "verify-config", "start", "wait-start", "guest-ready"}
	if strings.Join(plan.Steps, "\x00") != strings.Join(expectedSteps, "\x00") {
		return fmt.Errorf("VM plan contains an invalid step sequence")
	}
	boundVLANs := make(map[uint32]bool)
	for _, binding := range plan.Networks {
		if err := validateNetworkBinding(binding); err != nil {
			return err
		}
		if binding.MAC == "" || binding.IPv4Address == "" {
			return fmt.Errorf("VM network binding is incomplete")
		}
		if !vlanValues[binding.VLAN] {
			return fmt.Errorf("VM network binding is not backed by a VLAN claim")
		}
		boundVLANs[binding.VLAN] = true
	}
	if len(boundVLANs) != len(vlanValues) {
		return fmt.Errorf("VM plan contains an unused VLAN claim")
	}
	return nil
}

func allocationInSourceRange(value uint32, source string) bool {
	parts := strings.Split(source, "-")
	if len(parts) != 2 {
		return false
	}
	start, startErr := strconv.ParseUint(parts[0], 10, 32)
	end, endErr := strconv.ParseUint(parts[1], 10, 32)
	return startErr == nil && endErr == nil && start <= end && uint64(value) >= start && uint64(value) <= end
}

func validateNetworkBinding(binding NetworkBinding) error {
	prefix, err := netip.ParsePrefix(binding.IPv4CIDR)
	if err != nil || !prefix.Addr().Is4() || binding.VLAN < 1 || binding.VLAN > 4094 || !storageIDPattern.MatchString(binding.Bridge) || !aliasPattern.MatchString(binding.ProfileAlias) || !logicalIDPattern.MatchString(binding.LogicalResourceID) {
		return fmt.Errorf("invalid network binding")
	}
	if binding.IPv4Address != "" {
		address, parseErr := netip.ParseAddr(binding.IPv4Address)
		if parseErr != nil || !prefix.Contains(address) {
			return fmt.Errorf("invalid network address binding")
		}
	}
	if binding.MAC != "" {
		mac, err := net.ParseMAC(binding.MAC)
		if err != nil || len(mac) != 6 || mac[0]&0x01 != 0 || mac[0]&0x02 == 0 {
			return fmt.Errorf("invalid network MAC binding")
		}
	}
	return nil
}

func (state QEMUProviderState) validate() error {
	if state.SchemaVersion != qemuStateV10 || (state.ResourceKind != "Network" && state.ResourceKind != "VM") || state.TargetID == "" || !nodeNamePattern.MatchString(state.Node) || !logicalIDPattern.MatchString(state.LogicalResourceID) || state.Generation == 0 {
		return fmt.Errorf("provider state identity is invalid")
	}
	for _, binding := range state.Networks {
		if err := validateNetworkBinding(binding); err != nil {
			return err
		}
	}
	if state.ResourceKind == "Network" {
		if state.VMID != 0 || state.Ownership != nil || state.OwnershipDigest != "" || len(state.Volumes) != 0 || len(state.Networks) != 1 || state.ConfigDigest != "" || state.TaskUPID != "" || state.Phase != "ready" {
			return fmt.Errorf("network provider state is invalid")
		}
		return nil
	}
	if state.VMID < 100 || state.Ownership == nil || state.Ownership.InstallationID == "" || state.Ownership.TargetID != state.TargetID || state.Ownership.LogicalResource != state.LogicalResourceID || state.Ownership.Generation != state.Generation || state.Ownership.Token == "" || len(state.Volumes) == 0 || len(state.Networks) > 16 || !sha1Pattern.MatchString(state.ConfigDigest) {
		return fmt.Errorf("VM provider state is invalid")
	}
	marker, _ := json.Marshal(state.Ownership)
	if state.OwnershipDigest != digestDocument(marker) {
		return fmt.Errorf("VM ownership digest is invalid")
	}
	seenVolumes := make(map[string]bool)
	for _, volume := range state.Volumes {
		if volume == "" || len(volume) > maximumFieldBytes || strings.ContainsAny(volume, "\r\n\x00") {
			return fmt.Errorf("VM volume identity is invalid")
		}
		if seenVolumes[volume] {
			return fmt.Errorf("VM volume identity is duplicated")
		}
		seenVolumes[volume] = true
	}
	if state.TaskUPID != "" {
		upid, err := ParseUPID(state.TaskUPID)
		if err != nil || upid.validateObject(state.Node, state.VMID, "qmclone", "qmstart", "qmshutdown", "qmsuspend", "qmresume", "qmdestroy") != nil {
			return fmt.Errorf("VM task identity is invalid")
		}
	}
	switch state.Phase {
	case "ready", "running", "stopped", "paused":
		return nil
	default:
		return fmt.Errorf("VM state phase is invalid")
	}
}

func (state QEMUObservedState) validate() error {
	if state.SchemaVersion != qemuStateV10 || (state.ResourceKind != "Network" && state.ResourceKind != "VM") || !nodeNamePattern.MatchString(state.Node) || state.VMID != 0 && state.VMID < 100 || len(state.Networks) > 16 || len(state.Leftovers) > 16 {
		return fmt.Errorf("observed state identity is invalid")
	}
	if state.ResourceKind == "Network" && state.VMID != 0 || state.ResourceKind == "VM" && state.VMID < 100 {
		return fmt.Errorf("observed resource identity is invalid")
	}
	if state.ConfigDigest != "" && !sha1Pattern.MatchString(state.ConfigDigest) {
		return fmt.Errorf("observed config digest is invalid")
	}
	if state.PowerState != "" && state.PowerState != "running" && state.PowerState != "stopped" && state.PowerState != "paused" {
		return fmt.Errorf("observed power state is invalid")
	}
	for _, binding := range state.Networks {
		if err := validateNetworkBinding(binding); err != nil {
			return err
		}
	}
	for _, leftover := range state.Leftovers {
		if leftover == "" || len(leftover) > maximumFieldBytes || strings.ContainsAny(leftover, "\r\n\x00") {
			return fmt.Errorf("observed leftover is invalid")
		}
	}
	return nil
}

func normalizeAllocations(values []AllocationBinding) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].Namespace != values[j].Namespace {
			return values[i].Namespace < values[j].Namespace
		}
		return values[i].Value < values[j].Value
	})
}
