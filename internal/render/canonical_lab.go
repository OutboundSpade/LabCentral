package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

// CanonicalResource serializes one already-rendered logical resource with the
// same RFC 8785 rules used inside CanonicalLab. It is the provider RPC payload;
// provider-specific state is never added to these bytes.
func CanonicalResource(resource domain.LogicalResource) ([]byte, error) {
	value, err := canonicalResourceValue(resource)
	if err != nil {
		return nil, err
	}
	return Canonicalize(value)
}

func canonicalLabValue(author AuthorLab, graph domain.ResourceGraph, inputs resolvedInputs) (map[string]any, error) {
	resources := make([]any, len(graph.Resources))
	for index, resource := range graph.Resources {
		value, err := canonicalResourceValue(resource)
		if err != nil {
			return nil, err
		}
		resources[index] = value
	}
	result := map[string]any{
		"apiVersion": CanonicalFormatVersion,
		"metadata": map[string]any{
			"name": author.Metadata.Name,
		},
		"resources": resources,
	}
	requirements := make(map[string]any)
	if author.Spec.Requirements.IsolationClass != "" {
		requirements["isolationClass"] = string(author.Spec.Requirements.IsolationClass)
	}
	if len(author.Spec.Requirements.Capabilities) > 0 {
		capabilities := append([]string(nil), author.Spec.Requirements.Capabilities...)
		sort.Strings(capabilities)
		values := make([]any, len(capabilities))
		for index := range capabilities {
			values[index] = capabilities[index]
		}
		requirements["capabilities"] = values
	}
	if len(author.Spec.Requirements.TargetSelector) > 0 {
		selector := make(map[string]any, len(author.Spec.Requirements.TargetSelector))
		for key, value := range author.Spec.Requirements.TargetSelector {
			selector[key] = value
		}
		requirements["targetSelector"] = selector
	}
	if len(requirements) > 0 {
		result["requirements"] = requirements
	}
	if len(inputs.params) > 0 || len(inputs.secrets) > 0 {
		canonicalInputs := make(map[string]any, len(inputs.params)+len(inputs.secrets))
		for name, value := range inputs.params {
			converted, err := canonicalInputValue(value)
			if err != nil {
				return nil, renderError(StageCanonicalization, "invalid_resolved_input", "inputs."+name, "resolved input cannot be represented canonically", err)
			}
			canonicalInputs[name] = converted
		}
		for name, reference := range inputs.secrets {
			canonicalInputs[name] = map[string]any{
				"secretRef": map[string]any{
					"key":    reference.Key,
					"name":   reference.Name,
					"source": reference.Source,
				},
			}
		}
		result["inputs"] = canonicalInputs
	}
	if len(author.Spec.ProviderOverrides) > 0 {
		overrides := make(map[string]any, len(author.Spec.ProviderOverrides))
		for namespace, override := range author.Spec.ProviderOverrides {
			spec, err := DecodeJSON(override.Spec)
			if err != nil {
				return nil, renderError(StageCanonicalization, "invalid_provider_override", "spec.providerOverrides."+namespace, "provider override spec is not valid JSON", err)
			}
			overrides[namespace] = map[string]any{
				"apiVersion": override.APIVersion,
				"spec":       spec,
			}
		}
		result["providerOverrides"] = overrides
	}
	return result, nil
}

func canonicalInputValue(value any) (any, error) {
	switch typed := value.(type) {
	case nil, bool, string, int64, float64:
		return typed, nil
	case []string:
		return stringsToAny(typed), nil
	case []any:
		result := make([]any, len(typed))
		for index := range typed {
			converted, err := canonicalInputValue(typed[index])
			if err != nil {
				return nil, err
			}
			result[index] = converted
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported resolved input type %T", value)
	}
}

func canonicalResourceValue(resource domain.LogicalResource) (map[string]any, error) {
	result := map[string]any{
		"id":   string(resource.ID()),
		"kind": string(resource.Kind()),
	}
	if dependencies := resource.Dependencies(); len(dependencies) > 0 {
		values := make([]any, len(dependencies))
		for index := range dependencies {
			values[index] = string(dependencies[index])
		}
		result["dependsOn"] = values
	}
	switch typed := resource.(type) {
	case domain.Network:
		result["spec"] = canonicalNetworkSpec(typed.Spec)
	case domain.Volume:
		result["spec"] = canonicalVolumeSpec(typed.Spec)
	case domain.VM:
		result["spec"] = canonicalVMSpec(typed.Spec)
	case domain.Container:
		result["spec"] = canonicalContainerSpec(typed.Spec)
	case domain.AccessEndpoint:
		result["spec"] = canonicalAccessSpec(typed.Spec)
	case domain.Output:
		result["spec"] = canonicalOutputSpec(typed.Spec)
	default:
		return nil, renderError(StageCanonicalization, "unsupported_resource", string(resource.ID()), fmt.Sprintf("canonicalizer does not support %T", resource), nil)
	}
	return result, nil
}

func canonicalNetworkSpec(spec domain.NetworkSpec) map[string]any {
	result := map[string]any{
		"egress": map[string]any{"mode": string(spec.Egress.Mode)},
		"ipam":   map[string]any{"mode": string(spec.IPAM)},
		"kind":   string(spec.Kind),
	}
	if spec.IPv4CIDR.IsValid() {
		result["ipv4Cidr"] = spec.IPv4CIDR.String()
	}
	if spec.IPv6CIDR.IsValid() {
		result["ipv6Cidr"] = spec.IPv6CIDR.String()
	}
	if spec.MTU != 0 {
		result["mtu"] = uint64(spec.MTU)
	}
	if len(spec.Egress.Destinations) > 0 {
		destinations := make([]any, len(spec.Egress.Destinations))
		for index := range spec.Egress.Destinations {
			destinations[index] = spec.Egress.Destinations[index]
		}
		result["egress"].(map[string]any)["destinations"] = destinations
	}
	return result
}

func canonicalVolumeSpec(spec domain.VolumeSpec) map[string]any {
	return map[string]any{
		"persistence": string(spec.Persistence),
		"sizeBytes":   uint64(spec.SizeBytes),
		"snapshot":    spec.SnapshotReady,
	}
}

func canonicalCompute(spec domain.ComputeRequirements) map[string]any {
	return map[string]any{
		"architecture": string(spec.Architecture),
		"cpuMillis":    uint64(spec.CPUMillis),
		"memoryBytes":  uint64(spec.MemoryBytes),
	}
}

func canonicalImage(reference domain.ImageRef) map[string]any {
	value := string(reference)
	separator := strings.LastIndex(value, "@sha256:")
	if separator < 0 {
		return map[string]any{"ref": value}
	}
	return map[string]any{
		"digest":     value[separator+1:],
		"repository": value[:separator],
	}
}

func canonicalInterfaces(interfaces []domain.NetworkInterface) []any {
	result := make([]any, len(interfaces))
	for index, networkInterface := range interfaces {
		value := map[string]any{"networkRef": string(networkInterface.NetworkID)}
		if networkInterface.IPv4.IsValid() {
			value["ipv4"] = networkInterface.IPv4.String()
		}
		if networkInterface.IPv6.IsValid() {
			value["ipv6"] = networkInterface.IPv6.String()
		}
		if networkInterface.MAC != "" {
			value["mac"] = networkInterface.MAC
		}
		result[index] = value
	}
	return result
}

func canonicalMounts(mounts []domain.Mount) []any {
	result := make([]any, len(mounts))
	for index, mount := range mounts {
		value := map[string]any{
			"path":      mount.Path,
			"volumeRef": string(mount.VolumeID),
		}
		if mount.ReadOnly {
			value["readOnly"] = true
		}
		result[index] = value
	}
	return result
}

func canonicalVMSpec(spec domain.VMSpec) map[string]any {
	result := map[string]any{
		"compute":  canonicalCompute(spec.Compute),
		"image":    canonicalImage(spec.Image),
		"osFamily": string(spec.OSFamily),
	}
	if len(spec.Interfaces) > 0 {
		result["interfaces"] = canonicalInterfaces(spec.Interfaces)
	}
	if len(spec.Mounts) > 0 {
		result["mounts"] = canonicalMounts(spec.Mounts)
	}
	if spec.Provisioning != nil {
		result["provisioning"] = map[string]any{
			"artifactRef": string(spec.Provisioning.ArtifactRef),
			"method":      string(spec.Provisioning.Method),
		}
	}
	if spec.Lifecycle.Snapshot || spec.Lifecycle.Reset != "" {
		lifecycle := make(map[string]any)
		if spec.Lifecycle.Snapshot {
			lifecycle["snapshot"] = true
		}
		if spec.Lifecycle.Reset != "" {
			lifecycle["reset"] = string(spec.Lifecycle.Reset)
		}
		result["lifecycle"] = lifecycle
	}
	return result
}

func canonicalContainerSpec(spec domain.ContainerSpec) map[string]any {
	result := map[string]any{
		"compute":   canonicalCompute(spec.Compute),
		"image":     canonicalImage(spec.Image),
		"isolation": string(spec.Isolation),
	}
	if len(spec.RealizationPreference) > 0 {
		values := make([]any, len(spec.RealizationPreference))
		for index := range spec.RealizationPreference {
			values[index] = string(spec.RealizationPreference[index])
		}
		result["realizationPreference"] = values
	}
	if len(spec.Interfaces) > 0 {
		result["interfaces"] = canonicalInterfaces(spec.Interfaces)
	}
	if len(spec.Mounts) > 0 {
		result["mounts"] = canonicalMounts(spec.Mounts)
	}
	if len(spec.Environment) > 0 {
		environment := make(map[string]any, len(spec.Environment))
		for _, variable := range spec.Environment {
			if variable.Value.Literal != nil {
				environment[variable.Name] = map[string]any{"literal": *variable.Value.Literal}
			} else {
				environment[variable.Name] = map[string]any{
					"secretRef": map[string]any{
						"key":    variable.Value.Secret.Key,
						"name":   variable.Value.Secret.Name,
						"source": variable.Value.Secret.Source,
					},
				}
			}
		}
		result["environment"] = environment
	}
	if len(spec.Command) > 0 {
		result["command"] = stringsToAny(spec.Command)
	}
	if len(spec.Args) > 0 {
		result["args"] = stringsToAny(spec.Args)
	}
	if len(spec.PublishedPorts) > 0 {
		ports := make([]any, len(spec.PublishedPorts))
		for index, port := range spec.PublishedPorts {
			value := map[string]any{
				"exposure": string(port.Exposure),
				"name":     string(port.Name),
				"port":     uint64(port.Port),
				"protocol": string(port.Protocol),
			}
			if port.ExternalPort != 0 {
				value["externalPort"] = uint64(port.ExternalPort)
			}
			ports[index] = value
		}
		result["publishedPorts"] = ports
	}
	return result
}

func canonicalAccessSpec(spec domain.AccessEndpointSpec) map[string]any {
	methods := make([]string, len(spec.Methods))
	for index := range spec.Methods {
		methods[index] = string(spec.Methods[index])
	}
	sort.Strings(methods)
	return map[string]any{
		"methods":   stringsToAny(methods),
		"targetRef": string(spec.TargetID),
	}
}

func canonicalOutputSpec(spec domain.OutputSpec) map[string]any {
	return map[string]any{
		"sensitive": spec.Sensitive,
		"type":      string(spec.Type),
		"valueRef":  spec.Expression,
	}
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for index := range values {
		result[index] = values[index]
	}
	return result
}
