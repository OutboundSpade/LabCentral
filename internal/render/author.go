package render

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

type AuthorLab struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`
	Metadata   AuthorMetadata `json:"metadata"`
	Spec       AuthorLabSpec  `json:"spec"`
}

type AuthorMetadata struct {
	Name        string            `json:"name"`
	Title       string            `json:"title,omitempty"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type AuthorLabSpec struct {
	Parameters        []AuthorParameter                 `json:"parameters,omitempty"`
	Components        []AuthorComponent                 `json:"components,omitempty"`
	Uses              []AuthorComponentUse              `json:"uses,omitempty"`
	Requirements      AuthorRequirements                `json:"requirements,omitempty"`
	Resources         AuthorResources                   `json:"resources"`
	Outputs           []AuthorOutput                    `json:"outputs,omitempty"`
	ProviderOverrides map[string]AuthorProviderOverride `json:"providerOverrides,omitempty"`
}

type AuthorParameter struct {
	Name        string            `json:"name"`
	Type        domain.InputKind  `json:"type"`
	Description string            `json:"description,omitempty"`
	Required    bool              `json:"required,omitempty"`
	Default     json.RawMessage   `json:"default,omitempty"`
	Enum        []json.RawMessage `json:"enum,omitempty"`
	Min         *float64          `json:"min,omitempty"`
	Max         *float64          `json:"max,omitempty"`
	Pattern     string            `json:"pattern,omitempty"`
}

type AuthorRequirements struct {
	IsolationClass domain.IsolationClass `json:"isolationClass,omitempty"`
	Capabilities   []string              `json:"capabilities,omitempty"`
	TargetSelector map[string]string     `json:"targetSelector,omitempty"`
}

type AuthorForEach struct {
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
	In    string `json:"in"`
}

type AuthorCommon struct {
	Name      string            `json:"name"`
	When      string            `json:"when,omitempty"`
	DependsOn []string          `json:"dependsOn,omitempty"`
	ForEach   *AuthorForEach    `json:"forEach,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

type AuthorComponent struct {
	Name       string            `json:"name"`
	Parameters []AuthorParameter `json:"parameters,omitempty"`
	Resources  AuthorResources   `json:"resources"`
}

type AuthorComponentUse struct {
	Name         string                     `json:"name"`
	ComponentRef string                     `json:"componentRef"`
	Arguments    map[string]json.RawMessage `json:"arguments,omitempty"`
	When         string                     `json:"when,omitempty"`
	ForEach      *AuthorForEach             `json:"forEach,omitempty"`
}

type AuthorResources struct {
	Networks        []AuthorNetwork        `json:"networks,omitempty"`
	Volumes         []AuthorVolume         `json:"volumes,omitempty"`
	VMs             []AuthorVM             `json:"vms,omitempty"`
	Containers      []AuthorContainer      `json:"containers,omitempty"`
	AccessEndpoints []AuthorAccessEndpoint `json:"accessEndpoints,omitempty"`
}

type AuthorEgress struct {
	Mode         domain.EgressMode `json:"mode"`
	Destinations []string          `json:"destinations,omitempty"`
}

type AuthorNetwork struct {
	AuthorCommon
	Kind     domain.NetworkKind `json:"kind,omitempty"`
	IPv4CIDR string             `json:"ipv4Cidr,omitempty"`
	IPv6CIDR string             `json:"ipv6Cidr,omitempty"`
	IPAM     domain.IPAMMode    `json:"ipam,omitempty"`
	MTU      uint16             `json:"mtu,omitempty"`
	Egress   AuthorEgress       `json:"egress,omitempty"`
}

type AuthorVolume struct {
	AuthorCommon
	Size        string                   `json:"size"`
	Persistence domain.VolumePersistence `json:"persistence,omitempty"`
	Snapshot    bool                     `json:"snapshot,omitempty"`
}

type AuthorCompute struct {
	CPU          float64             `json:"cpu"`
	Memory       string              `json:"memory"`
	Architecture domain.Architecture `json:"architecture,omitempty"`
}

type AuthorInterface struct {
	NetworkRef string `json:"networkRef"`
	IPv4       string `json:"ipv4,omitempty"`
	IPv6       string `json:"ipv6,omitempty"`
	MAC        string `json:"mac,omitempty"`
	When       string `json:"when,omitempty"`
}

type AuthorMount struct {
	VolumeRef string `json:"volumeRef"`
	Path      string `json:"path"`
	ReadOnly  bool   `json:"readOnly,omitempty"`
}

type AuthorProvisioning struct {
	Method      domain.ProvisioningMethod `json:"method"`
	ArtifactRef string                    `json:"artifactRef,omitempty"`
}

type AuthorLifecycle struct {
	Snapshot bool   `json:"snapshot,omitempty"`
	Reset    string `json:"reset,omitempty"`
}

type AuthorVM struct {
	AuthorCommon
	ImageRef     string              `json:"imageRef"`
	OSFamily     domain.OSFamily     `json:"osFamily,omitempty"`
	Compute      AuthorCompute       `json:"compute"`
	Interfaces   []AuthorInterface   `json:"interfaces,omitempty"`
	Mounts       []AuthorMount       `json:"mounts,omitempty"`
	Provisioning *AuthorProvisioning `json:"provisioning,omitempty"`
	Lifecycle    AuthorLifecycle     `json:"lifecycle,omitempty"`
}

type AuthorPublishedPort struct {
	Name         string                 `json:"name"`
	Port         uint16                 `json:"port"`
	Protocol     domain.NetworkProtocol `json:"protocol,omitempty"`
	Exposure     domain.PortExposure    `json:"exposure"`
	ExternalPort uint16                 `json:"externalPort,omitempty"`
}

type AuthorContainer struct {
	AuthorCommon
	Image                 string                        `json:"image"`
	Compute               AuthorCompute                 `json:"compute"`
	Isolation             domain.IsolationClass         `json:"isolation,omitempty"`
	RealizationPreference []domain.ContainerRealization `json:"realizationPreference,omitempty"`
	Interfaces            []AuthorInterface             `json:"interfaces,omitempty"`
	Mounts                []AuthorMount                 `json:"mounts,omitempty"`
	Environment           map[string]json.RawMessage    `json:"environment,omitempty"`
	Command               []string                      `json:"command,omitempty"`
	Args                  []string                      `json:"args,omitempty"`
	PublishedPorts        []AuthorPublishedPort         `json:"publishedPorts,omitempty"`
}

type AuthorAccessEndpoint struct {
	AuthorCommon
	TargetRef string                `json:"targetRef"`
	Methods   []domain.AccessMethod `json:"methods"`
}

type AuthorOutput struct {
	Name      string            `json:"name"`
	Type      domain.OutputType `json:"type"`
	Value     string            `json:"value"`
	Sensitive bool              `json:"sensitive,omitempty"`
}

type AuthorProviderOverride struct {
	APIVersion string          `json:"apiVersion"`
	Spec       json.RawMessage `json:"spec"`
}

func decodeAuthorLab(document any) (AuthorLab, error) {
	encoded, err := json.Marshal(document)
	if err != nil {
		return AuthorLab{}, renderError(StageSchema, "author_decode_failed", "", "validated author document could not be encoded", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var lab AuthorLab
	if err := decoder.Decode(&lab); err != nil {
		return AuthorLab{}, renderError(StageSchema, "author_decode_failed", "", "validated author document does not map to typed author structures", err)
	}
	return lab, nil
}

func decodeEnvironmentValue(raw json.RawMessage) (domain.EnvironmentValue, error) {
	var literal string
	if err := json.Unmarshal(raw, &literal); err == nil {
		return domain.EnvironmentValue{Literal: &literal}, nil
	}
	var secretEnvelope struct {
		SecretRef domain.SecretRef `json:"secretRef"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&secretEnvelope); err != nil {
		return domain.EnvironmentValue{}, fmt.Errorf("decode environment value: %w", err)
	}
	value := domain.EnvironmentValue{Secret: &secretEnvelope.SecretRef}
	if err := value.Validate(); err != nil {
		return domain.EnvironmentValue{}, err
	}
	return value, nil
}
