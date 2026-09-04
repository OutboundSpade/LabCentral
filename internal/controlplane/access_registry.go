package controlplane

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
)

const (
	AccessAuthorizationFeature = "access-authorization-recheck-v1"
	AccessProtocolMinor        = uint32(2)
)

var accessMethodPattern = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,63}$`)

type AccessBinding struct {
	PluginID        string
	PluginVersion   string
	PluginInstance  string
	AccessKind      string
	Audience        string
	Access          pluginv1.AccessServiceClient
	ProtocolMajor   uint32
	ProtocolMinor   uint32
	Features        []string
	Methods         []string
	EndpointSchemas []*pluginv1.SchemaVersion
}

type AccessRegistry struct {
	bindings []accessBinding
}

type accessBinding struct {
	AccessBinding
	features map[string]bool
	methods  map[string]bool
	schemas  map[string]uint32
}

func NewAccessRegistry(bindings []AccessBinding) (*AccessRegistry, error) {
	if len(bindings) == 0 || len(bindings) > 16 {
		return nil, fmt.Errorf("access registry must contain between 1 and 16 plugins")
	}
	registry := &AccessRegistry{bindings: make([]accessBinding, 0, len(bindings))}
	seenInstances := make(map[string]bool, len(bindings))
	for _, input := range bindings {
		if input.PluginID == "" || input.PluginVersion == "" || input.PluginInstance == "" ||
			!providerKindPattern.MatchString(input.AccessKind) || input.Access == nil ||
			input.ProtocolMajor != 1 || input.ProtocolMinor < AccessProtocolMinor ||
			len(input.Audience) == 0 || len(input.Audience) > 512 || strings.ContainsAny(input.Audience, "\r\n\t ") {
			return nil, fmt.Errorf("access plugin binding is invalid")
		}
		if seenInstances[input.PluginInstance] {
			return nil, fmt.Errorf("access plugin instance %s is registered more than once", input.PluginInstance)
		}
		seenInstances[input.PluginInstance] = true
		binding := accessBinding{AccessBinding: input, features: make(map[string]bool), methods: make(map[string]bool), schemas: make(map[string]uint32)}
		for _, feature := range input.Features {
			if !providerKindPattern.MatchString(feature) || binding.features[feature] {
				return nil, fmt.Errorf("access plugin feature set is invalid")
			}
			binding.features[feature] = true
		}
		if !binding.features[AccessAuthorizationFeature] {
			return nil, fmt.Errorf("access plugin is missing %s", AccessAuthorizationFeature)
		}
		for _, method := range input.Methods {
			if !accessMethodPattern.MatchString(method) || binding.methods[method] {
				return nil, fmt.Errorf("access plugin method set is invalid")
			}
			binding.methods[method] = true
		}
		if len(binding.methods) == 0 {
			return nil, fmt.Errorf("access plugin must advertise at least one method")
		}
		for _, schema := range input.EndpointSchemas {
			if schema == nil || !providerKindPattern.MatchString(schema.GetName()) || schema.GetMajor() == 0 {
				return nil, fmt.Errorf("access plugin endpoint schema set is invalid")
			}
			key := endpointSchemaKey(schema.GetName(), schema.GetMajor())
			if _, duplicate := binding.schemas[key]; duplicate {
				return nil, fmt.Errorf("access plugin endpoint schema set is invalid")
			}
			binding.schemas[key] = schema.GetMinor()
		}
		if len(binding.schemas) == 0 {
			return nil, fmt.Errorf("access plugin must advertise at least one endpoint schema")
		}
		registry.bindings = append(registry.bindings, binding)
	}
	sort.Slice(registry.bindings, func(i, j int) bool { return registry.bindings[i].PluginInstance < registry.bindings[j].PluginInstance })
	return registry, nil
}

func (registry *AccessRegistry) Select(method string) (AccessBinding, error) {
	if registry == nil || !accessMethodPattern.MatchString(method) {
		return AccessBinding{}, ErrAccessUnsupported
	}
	for _, binding := range registry.bindings {
		if binding.methods[method] {
			return cloneAccessBinding(binding.AccessBinding), nil
		}
	}
	return AccessBinding{}, ErrAccessUnsupported
}

func (registry *AccessRegistry) Get(pluginInstance string) (AccessBinding, error) {
	if registry == nil {
		return AccessBinding{}, ErrAccessUnavailable
	}
	for _, binding := range registry.bindings {
		if binding.PluginInstance == pluginInstance {
			return cloneAccessBinding(binding.AccessBinding), nil
		}
	}
	return AccessBinding{}, ErrAccessUnavailable
}

func (registry *AccessRegistry) Accepts(binding AccessBinding, schema *pluginv1.SchemaVersion) bool {
	if registry == nil || schema == nil {
		return false
	}
	for _, candidate := range registry.bindings {
		if candidate.PluginInstance != binding.PluginInstance {
			continue
		}
		maximumMinor, ok := candidate.schemas[endpointSchemaKey(schema.GetName(), schema.GetMajor())]
		return ok && schema.GetMinor() <= maximumMinor
	}
	return false
}

func endpointSchemaKey(name string, major uint32) string {
	return fmt.Sprintf("%s/v%d", name, major)
}

func cloneAccessBinding(binding AccessBinding) AccessBinding {
	binding.Features = append([]string(nil), binding.Features...)
	binding.Methods = append([]string(nil), binding.Methods...)
	binding.EndpointSchemas = make([]*pluginv1.SchemaVersion, 0, len(binding.EndpointSchemas))
	for _, schema := range binding.EndpointSchemas {
		binding.EndpointSchemas = append(binding.EndpointSchemas, &pluginv1.SchemaVersion{Name: schema.GetName(), Major: schema.GetMajor(), Minor: schema.GetMinor()})
	}
	return binding
}
