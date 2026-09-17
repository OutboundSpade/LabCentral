package controlplane

import (
	"fmt"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/OutboundSpade/LabCentral/internal/application"
)

const (
	AccessAuthorizationFeature = "access-authorization-recheck-v1"
	AccessProtocolMinor        = application.AccessProtocolMinor
)

var accessMethodPattern = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,63}$`)

type AccessBinding struct {
	StreamBasePath  string
	PluginID        string
	PluginVersion   string
	PluginInstance  string
	AccessKind      string
	Audience        string
	Access          application.AccessPlugin
	ProtocolMajor   uint32
	ProtocolMinor   uint32
	Features        []string
	Methods         []string
	EndpointSchemas []application.ProviderSchema
	TargetIDs       []string
	Descriptors     []domain.AccessDescriptor
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
		if len(input.Descriptors) == 0 {
			for _, method := range input.Methods {
				d, ok := domain.LegacyAccessDescriptor(method)
				if !ok {
					return nil, fmt.Errorf("custom access method requires a descriptor")
				}
				for _, schema := range input.EndpointSchemas {
					d.EndpointSchemas = append(d.EndpointSchemas, domain.AccessEndpointSchema{Name: schema.Name, Major: schema.Major, Minor: schema.Minor})
				}
				input.Descriptors = append(input.Descriptors, d)
			}
		}
		seenDescriptors := map[string]bool{}
		for _, d := range input.Descriptors {
			if err := d.Validate(); err != nil {
				return nil, err
			}
			if seenDescriptors[d.Method] || !slices.Contains(input.Methods, d.Method) {
				return nil, fmt.Errorf("descriptor does not match enabled methods")
			}
			seenDescriptors[d.Method] = true
			for _, schema := range d.EndpointSchemas {
				found := false
				for _, declared := range input.EndpointSchemas {
					if schema.Name == declared.Name && schema.Major == declared.Major && schema.Minor <= declared.Minor {
						found = true
					}
				}
				if !found {
					return nil, fmt.Errorf("descriptor endpoint schema exceeds advertised contract")
				}
			}
			if d.Presentation == "credential-download" {
				for _, feature := range []string{"access-credential-delivery-v1", "access-enforcement-inspection-v1", "access-issuance-recovery-v1"} {
					if !slices.Contains(input.Features, feature) {
						return nil, fmt.Errorf("network access requires %s", feature)
					}
				}
			}
		}
		if len(seenDescriptors) != len(input.Methods) {
			return nil, fmt.Errorf("descriptor missing for enabled method")
		}
		for _, target := range input.TargetIDs {
			if _, err := domain.NewProviderTargetID(target); err != nil {
				return nil, err
			}
		}
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
			if !providerKindPattern.MatchString(schema.Name) || schema.Major == 0 {
				return nil, fmt.Errorf("access plugin endpoint schema set is invalid")
			}
			key := endpointSchemaKey(schema.Name, schema.Major)
			if _, duplicate := binding.schemas[key]; duplicate {
				return nil, fmt.Errorf("access plugin endpoint schema set is invalid")
			}
			binding.schemas[key] = schema.Minor
		}
		if len(binding.schemas) == 0 {
			return nil, fmt.Errorf("access plugin must advertise at least one endpoint schema")
		}
		binding.AccessBinding = cloneAccessBinding(binding.AccessBinding)
		registry.bindings = append(registry.bindings, binding)
	}
	sort.Slice(registry.bindings, func(i, j int) bool { return registry.bindings[i].PluginInstance < registry.bindings[j].PluginInstance })
	return registry, nil
}

func (registry *AccessRegistry) Select(method string) (AccessBinding, error) {
	if registry == nil || !accessMethodPattern.MatchString(method) {
		return AccessBinding{}, ErrAccessUnsupported
	}
	var selected *AccessBinding
	for _, binding := range registry.bindings {
		if binding.methods[method] {
			if selected != nil {
				return AccessBinding{}, fmt.Errorf("ambiguous access method: configure explicit bindings")
			}
			v := cloneAccessBinding(binding.AccessBinding)
			selected = &v
		}
	}
	if selected != nil {
		return *selected, nil
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

func (registry *AccessRegistry) Accepts(binding AccessBinding, schema application.ProviderSchema) bool {
	if registry == nil {
		return false
	}
	for _, candidate := range registry.bindings {
		if candidate.PluginInstance != binding.PluginInstance {
			continue
		}
		maximumMinor, ok := candidate.schemas[endpointSchemaKey(schema.Name, schema.Major)]
		return ok && schema.Minor <= maximumMinor
	}
	return false
}

func endpointSchemaKey(name string, major uint32) string {
	return fmt.Sprintf("%s/v%d", name, major)
}

func cloneAccessBinding(binding AccessBinding) AccessBinding {
	binding.TargetIDs = append([]string(nil), binding.TargetIDs...)
	binding.Descriptors = append([]domain.AccessDescriptor(nil), binding.Descriptors...)
	for i := range binding.Descriptors {
		binding.Descriptors[i].EndpointSchemas = append([]domain.AccessEndpointSchema(nil), binding.Descriptors[i].EndpointSchemas...)
		binding.Descriptors[i].TargetKinds = append([]string(nil), binding.Descriptors[i].TargetKinds...)
	}
	binding.Features = append([]string(nil), binding.Features...)
	binding.Methods = append([]string(nil), binding.Methods...)
	binding.EndpointSchemas = append([]application.ProviderSchema(nil), binding.EndpointSchemas...)
	return binding
}

func (registry *AccessRegistry) Resolve(method, target, kind string, schema application.ProviderSchema) (AccessBinding, error) {
	if registry == nil || !domain.ValidAccessMethod(method) {
		return AccessBinding{}, ErrAccessUnsupported
	}
	var selected *AccessBinding
	for _, binding := range registry.bindings {
		if !binding.methods[method] || (len(binding.TargetIDs) > 0 && !slices.Contains(binding.TargetIDs, target)) {
			continue
		}
		acceptsKind := false
		for _, d := range binding.Descriptors {
			if d.Method == method && slices.Contains(d.TargetKinds, kind) {
				for _, accepted := range d.EndpointSchemas {
					if accepted.Name == schema.Name && accepted.Major == schema.Major && schema.Minor <= accepted.Minor {
						acceptsKind = true
					}
				}
			}
		}
		if !acceptsKind || !registry.Accepts(binding.AccessBinding, schema) {
			continue
		}
		if selected != nil {
			return AccessBinding{}, fmt.Errorf("ambiguous access adapter binding")
		}
		v := cloneAccessBinding(binding.AccessBinding)
		selected = &v
	}
	if selected == nil {
		return AccessBinding{}, ErrAccessUnsupported
	}
	return *selected, nil
}
func (registry *AccessRegistry) MethodsFor(target, kind string) []domain.AccessDescriptor {
	result := []domain.AccessDescriptor{}
	seen := map[string]bool{}
	if registry == nil {
		return result
	}
	for _, binding := range registry.bindings {
		if len(binding.TargetIDs) > 0 && !slices.Contains(binding.TargetIDs, target) {
			continue
		}
		for _, d := range binding.Descriptors {
			if !seen[d.Method] && slices.Contains(d.TargetKinds, kind) {
				result = append(result, d)
				seen[d.Method] = true
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Method < result[j].Method })
	return result
}
