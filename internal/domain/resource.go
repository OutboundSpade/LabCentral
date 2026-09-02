package domain

import (
	"fmt"
	"net"
	"net/netip"
	"path"
	"regexp"
	"strings"
)

type ResourceKind string

const (
	ResourceNetwork        ResourceKind = "Network"
	ResourceVolume         ResourceKind = "Volume"
	ResourceVM             ResourceKind = "VM"
	ResourceContainer      ResourceKind = "Container"
	ResourceAccessEndpoint ResourceKind = "AccessEndpoint"
	ResourceOutput         ResourceKind = "Output"
)

type CapabilityExpression struct {
	AllOf []CapabilityID
	AnyOf [][]CapabilityID
}

func (e CapabilityExpression) Validate() error {
	seen := make(map[CapabilityID]struct{})
	for _, capability := range e.AllOf {
		if _, err := NewCapabilityID(string(capability)); err != nil {
			return err
		}
		if _, exists := seen[capability]; exists {
			return fmt.Errorf("duplicate required capability %q", capability)
		}
		seen[capability] = struct{}{}
	}
	for index, alternatives := range e.AnyOf {
		if len(alternatives) == 0 {
			return fmt.Errorf("capability alternative group %d is empty", index)
		}
		groupSeen := make(map[CapabilityID]struct{})
		for _, capability := range alternatives {
			if _, err := NewCapabilityID(string(capability)); err != nil {
				return err
			}
			if _, exists := groupSeen[capability]; exists {
				return fmt.Errorf("duplicate capability %q in alternative group %d", capability, index)
			}
			groupSeen[capability] = struct{}{}
		}
	}
	return nil
}

type LogicalResource interface {
	ID() LogicalResourceID
	Kind() ResourceKind
	Dependencies() []LogicalResourceID
	RequiredCapabilities() CapabilityExpression
	logicalResourceSeal()
}

type ResourceSpec struct {
	Network        *NetworkSpec
	Volume         *VolumeSpec
	VM             *VMSpec
	Container      *ContainerSpec
	AccessEndpoint *AccessEndpointSpec
	Output         *OutputSpec
}

func (s ResourceSpec) Kind() (ResourceKind, error) {
	variants := []struct {
		present bool
		kind    ResourceKind
	}{
		{s.Network != nil, ResourceNetwork},
		{s.Volume != nil, ResourceVolume},
		{s.VM != nil, ResourceVM},
		{s.Container != nil, ResourceContainer},
		{s.AccessEndpoint != nil, ResourceAccessEndpoint},
		{s.Output != nil, ResourceOutput},
	}
	var selected ResourceKind
	count := 0
	for _, variant := range variants {
		if variant.present {
			selected = variant.kind
			count++
		}
	}
	if count != 1 {
		return "", fmt.Errorf("resource spec must contain exactly one variant, got %d", count)
	}
	return selected, nil
}

func (s ResourceSpec) Validate() error {
	kind, err := s.Kind()
	if err != nil {
		return err
	}
	switch kind {
	case ResourceNetwork:
		return s.Network.Validate()
	case ResourceVolume:
		return s.Volume.Validate()
	case ResourceVM:
		return s.VM.Validate()
	case ResourceContainer:
		return s.Container.Validate()
	case ResourceAccessEndpoint:
		return s.AccessEndpoint.Validate()
	case ResourceOutput:
		return s.Output.Validate()
	default:
		return fmt.Errorf("unsupported resource kind %q", kind)
	}
}

type NetworkKind string
type IPAMMode string
type EgressMode string

const (
	NetworkIsolated NetworkKind = "isolated"
	NetworkExternal NetworkKind = "external"

	IPAMNone            IPAMMode = "none"
	IPAMStatic          IPAMMode = "static"
	IPAMProviderManaged IPAMMode = "providerManaged"

	EgressDeny       EgressMode = "deny"
	EgressRestricted EgressMode = "restricted"
	EgressAllow      EgressMode = "allow"
)

type EgressPolicy struct {
	Mode         EgressMode
	Destinations []string
}

type NetworkSpec struct {
	Name     Name
	Kind     NetworkKind
	IPv4CIDR netip.Prefix
	IPv6CIDR netip.Prefix
	IPAM     IPAMMode
	MTU      uint16
	Egress   EgressPolicy
}

func (s NetworkSpec) Validate() error {
	if _, err := NewName(string(s.Name)); err != nil {
		return err
	}
	if s.Kind != NetworkIsolated && s.Kind != NetworkExternal {
		return fmt.Errorf("invalid network kind %q", s.Kind)
	}
	if s.IPAM != IPAMNone && s.IPAM != IPAMStatic && s.IPAM != IPAMProviderManaged {
		return fmt.Errorf("invalid IPAM mode %q", s.IPAM)
	}
	if s.IPv4CIDR.IsValid() && !s.IPv4CIDR.Addr().Is4() {
		return fmt.Errorf("IPv4 CIDR contains a non-IPv4 address")
	}
	if s.IPv6CIDR.IsValid() && !s.IPv6CIDR.Addr().Is6() {
		return fmt.Errorf("IPv6 CIDR contains a non-IPv6 address")
	}
	if s.MTU != 0 && (s.MTU < 576 || s.MTU > 9216) {
		return fmt.Errorf("MTU must be between 576 and 9216")
	}
	if s.Egress.Mode != EgressDeny && s.Egress.Mode != EgressRestricted && s.Egress.Mode != EgressAllow {
		return fmt.Errorf("invalid egress mode %q", s.Egress.Mode)
	}
	for _, destination := range s.Egress.Destinations {
		if destination == "" || strings.TrimSpace(destination) != destination {
			return fmt.Errorf("egress destination must be non-empty and trimmed")
		}
	}
	return nil
}

type VolumePersistence string

const (
	VolumeEphemeral  VolumePersistence = "ephemeral"
	VolumePersistent VolumePersistence = "persistent"
)

type VolumeSpec struct {
	Name          Name
	SizeBytes     ByteQuantity
	Persistence   VolumePersistence
	SnapshotReady bool
}

func (s VolumeSpec) Validate() error {
	if _, err := NewName(string(s.Name)); err != nil {
		return err
	}
	if s.SizeBytes == 0 {
		return fmt.Errorf("volume size must be greater than zero")
	}
	if s.Persistence != VolumeEphemeral && s.Persistence != VolumePersistent {
		return fmt.Errorf("invalid volume persistence %q", s.Persistence)
	}
	return nil
}

type Architecture string

const (
	ArchitectureAMD64 Architecture = "amd64"
	ArchitectureARM64 Architecture = "arm64"
)

type ComputeRequirements struct {
	CPUMillis    uint32
	MemoryBytes  ByteQuantity
	Architecture Architecture
}

func (c ComputeRequirements) Validate() error {
	if c.CPUMillis == 0 || c.CPUMillis > 128_000 {
		return fmt.Errorf("CPU must be between 1 and 128000 millicores")
	}
	if c.MemoryBytes == 0 {
		return fmt.Errorf("memory must be greater than zero")
	}
	if c.Architecture != ArchitectureAMD64 && c.Architecture != ArchitectureARM64 {
		return fmt.Errorf("invalid architecture %q", c.Architecture)
	}
	return nil
}

type NetworkInterface struct {
	NetworkID LogicalResourceID
	IPv4      netip.Addr
	IPv6      netip.Addr
	MAC       string
}

func (i NetworkInterface) Validate() error {
	if err := validateLogicalResourceID(string(i.NetworkID)); err != nil {
		return err
	}
	if i.IPv4.IsValid() && !i.IPv4.Is4() {
		return fmt.Errorf("IPv4 field contains a non-IPv4 address")
	}
	if i.IPv6.IsValid() && !i.IPv6.Is6() {
		return fmt.Errorf("IPv6 field contains a non-IPv6 address")
	}
	if i.MAC != "" {
		parsed, err := net.ParseMAC(i.MAC)
		if err != nil || len(parsed) != 6 {
			return fmt.Errorf("invalid 48-bit MAC address")
		}
	}
	return nil
}

type Mount struct {
	VolumeID LogicalResourceID
	Path     string
	ReadOnly bool
}

func (m Mount) Validate() error {
	if err := validateLogicalResourceID(string(m.VolumeID)); err != nil {
		return err
	}
	if !strings.HasPrefix(m.Path, "/") || path.Clean(m.Path) != m.Path {
		return fmt.Errorf("mount path must be an absolute clean guest path")
	}
	return nil
}

type ProvisioningMethod string

const (
	ProvisioningCloudInit ProvisioningMethod = "cloudInit"
	ProvisioningIgnition  ProvisioningMethod = "ignition"
	ProvisioningOCI       ProvisioningMethod = "oci"
)

type ProvisioningSpec struct {
	Method      ProvisioningMethod
	ArtifactRef ArtifactRef
}

func (p ProvisioningSpec) Validate() error {
	if p.Method != ProvisioningCloudInit && p.Method != ProvisioningIgnition && p.Method != ProvisioningOCI {
		return fmt.Errorf("invalid provisioning method %q", p.Method)
	}
	if _, err := NewArtifactRef(string(p.ArtifactRef)); err != nil {
		return err
	}
	return nil
}

type OSFamily string

const (
	OSLinux   OSFamily = "linux"
	OSWindows OSFamily = "windows"
	OSOther   OSFamily = "other"
)

type VMSpec struct {
	Name         Name
	Image        ImageRef
	OSFamily     OSFamily
	Compute      ComputeRequirements
	Interfaces   []NetworkInterface
	Mounts       []Mount
	Provisioning *ProvisioningSpec
	Lifecycle    WorkloadLifecycle
}

type ResetMode string

const (
	ResetRecreate ResetMode = "recreate"
	ResetSnapshot ResetMode = "snapshot"
)

type WorkloadLifecycle struct {
	Snapshot bool
	Reset    ResetMode
}

func (l WorkloadLifecycle) Validate() error {
	if l.Reset != "" && l.Reset != ResetRecreate && l.Reset != ResetSnapshot {
		return fmt.Errorf("invalid reset mode %q", l.Reset)
	}
	if l.Reset == ResetSnapshot && !l.Snapshot {
		return fmt.Errorf("snapshot reset requires snapshot lifecycle support")
	}
	return nil
}

func (s VMSpec) Validate() error {
	if _, err := NewName(string(s.Name)); err != nil {
		return err
	}
	if _, err := NewImageRef(string(s.Image)); err != nil {
		return err
	}
	if s.OSFamily != OSLinux && s.OSFamily != OSWindows && s.OSFamily != OSOther {
		return fmt.Errorf("invalid OS family %q", s.OSFamily)
	}
	if err := s.Compute.Validate(); err != nil {
		return err
	}
	for _, networkInterface := range s.Interfaces {
		if err := networkInterface.Validate(); err != nil {
			return err
		}
	}
	for _, mount := range s.Mounts {
		if err := mount.Validate(); err != nil {
			return err
		}
	}
	if s.Provisioning != nil {
		if err := s.Provisioning.Validate(); err != nil {
			return err
		}
	}
	if err := s.Lifecycle.Validate(); err != nil {
		return err
	}
	return nil
}

type IsolationClass string
type ContainerRealization string

const (
	IsolationProcess            IsolationClass = "process"
	IsolationSandboxedContainer IsolationClass = "sandboxedContainer"
	IsolationVirtualMachine     IsolationClass = "virtualMachine"

	ContainerNative    ContainerRealization = "native"
	ContainerSandboxed ContainerRealization = "sandboxed"
	ContainerViaVM     ContainerRealization = "viaVM"
)

type SecretRef struct {
	Source string
	Name   string
	Key    string
}

func (r SecretRef) Validate() error {
	for field, value := range map[string]string{"source": r.Source, "name": r.Name, "key": r.Key} {
		if value == "" || strings.TrimSpace(value) != value || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("secret reference %s must be non-empty and trimmed", field)
		}
	}
	return nil
}

type EnvironmentValue struct {
	Literal *string
	Secret  *SecretRef
}

func (v EnvironmentValue) Validate() error {
	if (v.Literal == nil) == (v.Secret == nil) {
		return fmt.Errorf("environment value must contain exactly one of literal or secret reference")
	}
	if v.Secret != nil {
		return v.Secret.Validate()
	}
	return nil
}

type EnvironmentVariable struct {
	Name  string
	Value EnvironmentValue
}

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,255}$`)

func (v EnvironmentVariable) Validate() error {
	if !environmentNamePattern.MatchString(v.Name) {
		return fmt.Errorf("invalid environment variable name %q", v.Name)
	}
	return v.Value.Validate()
}

type NetworkProtocol string
type PortExposure string

const (
	ProtocolTCP NetworkProtocol = "tcp"
	ProtocolUDP NetworkProtocol = "udp"

	ExposureHost    PortExposure = "host"
	ExposurePublic  PortExposure = "public"
	ExposureProxied PortExposure = "proxied"
)

type PublishedPort struct {
	Name         Name
	Port         uint16
	Protocol     NetworkProtocol
	Exposure     PortExposure
	ExternalPort uint16
}

func (p PublishedPort) Validate() error {
	if _, err := NewName(string(p.Name)); err != nil {
		return err
	}
	if p.Port == 0 {
		return fmt.Errorf("published port must be greater than zero")
	}
	if p.Protocol != ProtocolTCP && p.Protocol != ProtocolUDP {
		return fmt.Errorf("invalid network protocol %q", p.Protocol)
	}
	if p.Exposure != ExposureHost && p.Exposure != ExposurePublic && p.Exposure != ExposureProxied {
		return fmt.Errorf("invalid port exposure %q", p.Exposure)
	}
	return nil
}

type ContainerSpec struct {
	Name                  Name
	Image                 ImageRef
	Compute               ComputeRequirements
	Isolation             IsolationClass
	RealizationPreference []ContainerRealization
	Interfaces            []NetworkInterface
	Mounts                []Mount
	Environment           []EnvironmentVariable
	Command               []string
	Args                  []string
	PublishedPorts        []PublishedPort
}

func (s ContainerSpec) Validate() error {
	if _, err := NewName(string(s.Name)); err != nil {
		return err
	}
	if _, err := NewImageRef(string(s.Image)); err != nil {
		return err
	}
	if err := s.Compute.Validate(); err != nil {
		return err
	}
	if s.Isolation != IsolationProcess && s.Isolation != IsolationSandboxedContainer && s.Isolation != IsolationVirtualMachine {
		return fmt.Errorf("invalid isolation class %q", s.Isolation)
	}
	realizations := make(map[ContainerRealization]struct{})
	for _, realization := range s.RealizationPreference {
		if realization != ContainerNative && realization != ContainerSandboxed && realization != ContainerViaVM {
			return fmt.Errorf("invalid container realization %q", realization)
		}
		if _, exists := realizations[realization]; exists {
			return fmt.Errorf("duplicate container realization %q", realization)
		}
		realizations[realization] = struct{}{}
	}
	for _, networkInterface := range s.Interfaces {
		if err := networkInterface.Validate(); err != nil {
			return err
		}
	}
	for _, mount := range s.Mounts {
		if err := mount.Validate(); err != nil {
			return err
		}
	}
	for _, variable := range s.Environment {
		if err := variable.Validate(); err != nil {
			return err
		}
	}
	for _, publishedPort := range s.PublishedPorts {
		if err := publishedPort.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type AccessMethod string

const (
	AccessSSH             AccessMethod = "ssh"
	AccessBrowserTerminal AccessMethod = "browserTerminal"
	AccessVNC             AccessMethod = "vnc"
	AccessSPICE           AccessMethod = "spice"
	AccessSerial          AccessMethod = "serial"
	AccessRDP             AccessMethod = "rdp"
	AccessContainerExec   AccessMethod = "containerExec"
	AccessKubernetesExec  AccessMethod = "kubernetesExec"
	AccessProviderNative  AccessMethod = "providerNative"
)

type AccessEndpointSpec struct {
	Name     Name
	TargetID LogicalResourceID
	Methods  []AccessMethod
}

func (s AccessEndpointSpec) Validate() error {
	if _, err := NewName(string(s.Name)); err != nil {
		return err
	}
	if err := validateLogicalResourceID(string(s.TargetID)); err != nil {
		return err
	}
	if len(s.Methods) == 0 {
		return fmt.Errorf("access endpoint must include at least one method")
	}
	seen := make(map[AccessMethod]struct{})
	for _, method := range s.Methods {
		switch method {
		case AccessSSH, AccessBrowserTerminal, AccessVNC, AccessSPICE, AccessSerial, AccessRDP, AccessContainerExec, AccessKubernetesExec, AccessProviderNative:
		default:
			return fmt.Errorf("invalid access method %q", method)
		}
		if _, exists := seen[method]; exists {
			return fmt.Errorf("duplicate access method %q", method)
		}
		seen[method] = struct{}{}
	}
	return nil
}

type OutputType string

const (
	OutputString  OutputType = "string"
	OutputInteger OutputType = "integer"
	OutputBoolean OutputType = "boolean"
	OutputURL     OutputType = "url"
	OutputIP      OutputType = "ip"
)

type OutputSpec struct {
	Name       Name
	Type       OutputType
	Expression string
	Sensitive  bool
}

func (s OutputSpec) Validate() error {
	if _, err := NewName(string(s.Name)); err != nil {
		return err
	}
	switch s.Type {
	case OutputString, OutputInteger, OutputBoolean, OutputURL, OutputIP:
	default:
		return fmt.Errorf("invalid output type %q", s.Type)
	}
	if s.Expression == "" || len(s.Expression) > 1024 {
		return fmt.Errorf("output expression must be 1-1024 characters")
	}
	return nil
}

type resourceBase struct {
	id           LogicalResourceID
	dependencies []LogicalResourceID
	required     CapabilityExpression
}

func (r resourceBase) ID() LogicalResourceID { return r.id }

func (r resourceBase) Dependencies() []LogicalResourceID {
	return append([]LogicalResourceID(nil), r.dependencies...)
}

func (r resourceBase) RequiredCapabilities() CapabilityExpression {
	copyOf := CapabilityExpression{AllOf: append([]CapabilityID(nil), r.required.AllOf...)}
	copyOf.AnyOf = make([][]CapabilityID, len(r.required.AnyOf))
	for index := range r.required.AnyOf {
		copyOf.AnyOf[index] = append([]CapabilityID(nil), r.required.AnyOf[index]...)
	}
	return copyOf
}

type Network struct {
	resourceBase
	Spec NetworkSpec
}

func (Network) Kind() ResourceKind   { return ResourceNetwork }
func (Network) logicalResourceSeal() {}

type Volume struct {
	resourceBase
	Spec VolumeSpec
}

func (Volume) Kind() ResourceKind   { return ResourceVolume }
func (Volume) logicalResourceSeal() {}

type VM struct {
	resourceBase
	Spec VMSpec
}

func (VM) Kind() ResourceKind   { return ResourceVM }
func (VM) logicalResourceSeal() {}

type Container struct {
	resourceBase
	Spec ContainerSpec
}

func (Container) Kind() ResourceKind   { return ResourceContainer }
func (Container) logicalResourceSeal() {}

type AccessEndpoint struct {
	resourceBase
	Spec AccessEndpointSpec
}

func (AccessEndpoint) Kind() ResourceKind   { return ResourceAccessEndpoint }
func (AccessEndpoint) logicalResourceSeal() {}

type Output struct {
	resourceBase
	Spec OutputSpec
}

func (Output) Kind() ResourceKind   { return ResourceOutput }
func (Output) logicalResourceSeal() {}

func NewLogicalResource(
	id LogicalResourceID,
	dependencies []LogicalResourceID,
	required CapabilityExpression,
	spec ResourceSpec,
) (LogicalResource, error) {
	if err := validateLogicalResourceID(string(id)); err != nil {
		return nil, err
	}
	if err := required.Validate(); err != nil {
		return nil, err
	}
	seenDependencies := make(map[LogicalResourceID]struct{})
	for _, dependency := range dependencies {
		if err := validateLogicalResourceID(string(dependency)); err != nil {
			return nil, err
		}
		if dependency == id {
			return nil, fmt.Errorf("resource cannot depend on itself")
		}
		if _, exists := seenDependencies[dependency]; exists {
			return nil, fmt.Errorf("duplicate dependency %q", dependency)
		}
		seenDependencies[dependency] = struct{}{}
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	kind, err := spec.Kind()
	if err != nil {
		return nil, err
	}
	base := resourceBase{
		id:           id,
		dependencies: append([]LogicalResourceID(nil), dependencies...),
		required: CapabilityExpression{
			AllOf: append([]CapabilityID(nil), required.AllOf...),
			AnyOf: cloneCapabilityAlternatives(required.AnyOf),
		},
	}
	switch kind {
	case ResourceNetwork:
		return Network{resourceBase: base, Spec: *spec.Network}, nil
	case ResourceVolume:
		return Volume{resourceBase: base, Spec: *spec.Volume}, nil
	case ResourceVM:
		return VM{resourceBase: base, Spec: *spec.VM}, nil
	case ResourceContainer:
		return Container{resourceBase: base, Spec: *spec.Container}, nil
	case ResourceAccessEndpoint:
		return AccessEndpoint{resourceBase: base, Spec: *spec.AccessEndpoint}, nil
	case ResourceOutput:
		return Output{resourceBase: base, Spec: *spec.Output}, nil
	default:
		return nil, fmt.Errorf("unsupported resource kind %q", kind)
	}
}

func cloneCapabilityAlternatives(source [][]CapabilityID) [][]CapabilityID {
	result := make([][]CapabilityID, len(source))
	for index := range source {
		result[index] = append([]CapabilityID(nil), source[index]...)
	}
	return result
}
