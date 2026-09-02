package render

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func (r *Renderer) expandComponents(
	ctx context.Context,
	lab AuthorLab,
	rootInputs resolvedInputs,
	engine *ExpressionEngine,
	remainingResources int,
) ([]pendingResource, domain.RenderReport, error) {
	components := make(map[string]AuthorComponent, len(lab.Spec.Components))
	for _, component := range lab.Spec.Components {
		if _, duplicate := components[component.Name]; duplicate {
			return nil, domain.RenderReport{}, renderError(StageExpansion, "duplicate_component", "spec.components."+component.Name, "component name is repeated", nil)
		}
		components[component.Name] = component
	}
	rootVariables := ExpressionVariables{
		Params:           rootInputs.params,
		SecretParameters: rootInputs.secretNames,
	}
	result := make([]pendingResource, 0)
	report := domain.RenderReport{}
	for _, use := range lab.Spec.Uses {
		component, exists := components[use.ComponentRef]
		if !exists {
			return nil, report, renderError(StageExpansion, "component_not_found", "spec.uses."+use.Name, fmt.Sprintf("component %q does not exist", use.ComponentRef), nil)
		}
		contexts, err := expandCommon(ctx, engine, AuthorCommon{
			Name:    use.Name,
			When:    use.When,
			ForEach: use.ForEach,
		}, rootVariables)
		if err != nil {
			return nil, report, err
		}
		for _, expansion := range contexts {
			prefix, err := stableComponentPrefix(use.Name, expansion.key)
			if err != nil {
				return nil, report, err
			}
			componentInputs, err := resolveComponentInputs(component.Parameters, use.Arguments, rootInputs, expansion.variables)
			if err != nil {
				return nil, report, err
			}
			componentInputs.iteration = expansion.variables
			resources, err := qualifyComponentResources(component.Resources, prefix)
			if err != nil {
				return nil, report, err
			}
			componentLab := AuthorLab{
				Metadata: AuthorMetadata{Name: lab.Metadata.Name},
				Spec: AuthorLabSpec{
					Requirements: lab.Spec.Requirements,
					Resources:    resources,
				},
			}
			expanded, componentReport, err := r.expandResources(
				ctx,
				componentLab,
				componentInputs,
				engine,
				remainingResources-len(result),
			)
			if err != nil {
				return nil, report, err
			}
			result = append(result, expanded...)
			report.Warnings = append(report.Warnings, componentReport.Warnings...)
		}
	}
	return result, report, nil
}

func resolveComponentInputs(
	parameters []AuthorParameter,
	arguments map[string]json.RawMessage,
	rootInputs resolvedInputs,
	iteration map[string]any,
) (resolvedInputs, error) {
	result := resolvedInputs{
		params:      make(map[string]any),
		secrets:     make(map[string]domain.SecretRef),
		secretNames: make(map[string]struct{}),
	}
	declared := make(map[string]struct{}, len(parameters))
	for _, parameter := range parameters {
		if _, duplicate := declared[parameter.Name]; duplicate {
			return resolvedInputs{}, renderError(StageExpansion, "duplicate_component_parameter", "spec.components.parameters."+parameter.Name, "component parameter is repeated", nil)
		}
		declared[parameter.Name] = struct{}{}
		if err := validateParameterDeclaration(parameter); err != nil {
			return resolvedInputs{}, renderError(StageExpansion, "invalid_component_parameter_constraints", "spec.components.parameters."+parameter.Name, "component parameter constraints are invalid", err)
		}
		if parameter.Type == domain.InputSecretRef {
			result.secretNames[parameter.Name] = struct{}{}
			if len(parameter.Default) > 0 {
				return resolvedInputs{}, renderError(StageExpansion, "secret_default_forbidden", "spec.components.parameters."+parameter.Name, "component secretRef parameters cannot have defaults", nil)
			}
			raw, supplied := arguments[parameter.Name]
			if !supplied {
				if parameter.Required {
					return resolvedInputs{}, renderError(StageExpansion, "component_argument_missing", "spec.components.parameters."+parameter.Name, "required component secret reference is missing", nil)
				}
				continue
			}
			var binding string
			if err := json.Unmarshal(raw, &binding); err != nil || !strings.HasPrefix(binding, "params.") {
				return resolvedInputs{}, renderError(StageExpansion, "component_secret_binding_invalid", "spec.uses.arguments."+parameter.Name, "secretRef component argument must bind a declared root params.<name> reference", err)
			}
			rootName := strings.TrimPrefix(binding, "params.")
			secret, exists := rootInputs.secrets[rootName]
			if !exists {
				return resolvedInputs{}, renderError(StageExpansion, "component_secret_binding_invalid", "spec.uses.arguments."+parameter.Name, "root secretRef input is missing or not declared", nil)
			}
			result.secrets[parameter.Name] = secret
			continue
		}
		raw, supplied := arguments[parameter.Name]
		if !supplied {
			raw = parameter.Default
		}
		if len(raw) == 0 {
			if parameter.Required {
				return resolvedInputs{}, renderError(StageExpansion, "component_argument_missing", "spec.components.parameters."+parameter.Name, "required component argument is missing", nil)
			}
			continue
		}
		value, bound, err := resolveComponentBinding(raw, rootInputs.params, iteration)
		if err != nil {
			return resolvedInputs{}, renderError(StageExpansion, "component_argument_invalid", "spec.components.parameters."+parameter.Name, "component argument binding is invalid", err)
		}
		if bound {
			raw, err = json.Marshal(value)
			if err != nil {
				return resolvedInputs{}, renderError(StageExpansion, "component_argument_invalid", "spec.components.parameters."+parameter.Name, "component argument binding could not be materialized", err)
			}
		}
		value, err = decodeDefault(parameter.Type, raw)
		if err != nil {
			return resolvedInputs{}, renderError(StageExpansion, "component_argument_invalid", "spec.components.parameters."+parameter.Name, "component argument does not match its declared type", err)
		}
		if err := validateParameterValue(parameter, value); err != nil {
			return resolvedInputs{}, renderError(StageExpansion, "component_argument_constraint_violation", "spec.components.parameters."+parameter.Name, "component argument violates its parameter constraints", err)
		}
		result.params[parameter.Name] = value
	}
	for name := range arguments {
		if _, exists := declared[name]; !exists {
			return resolvedInputs{}, renderError(StageExpansion, "component_argument_undeclared", "spec.uses.arguments."+name, "component argument is not declared", nil)
		}
	}
	return result, nil
}

func resolveComponentBinding(raw json.RawMessage, rootParams, iteration map[string]any) (any, bool, error) {
	var binding string
	if err := json.Unmarshal(raw, &binding); err != nil {
		return nil, false, nil
	}
	if strings.HasPrefix(binding, "params.") {
		name := strings.TrimPrefix(binding, "params.")
		value, exists := rootParams[name]
		if !exists {
			return nil, false, fmt.Errorf("root parameter %q is missing or not declared", name)
		}
		return value, true, nil
	}
	if value, exists := iteration[binding]; exists {
		return value, true, nil
	}
	return nil, false, nil
}

func stableComponentPrefix(useName, iterationKey string) (string, error) {
	if iterationKey == "" {
		if _, err := domain.NewName(useName); err != nil {
			return "", renderError(StageExpansion, "invalid_component_use_name", "spec.uses."+useName, "component use name is invalid", err)
		}
		return useName, nil
	}
	candidate := useName + "-" + iterationKey
	if _, err := domain.NewName(candidate); err == nil {
		return candidate, nil
	}
	hash := sha256.Sum256([]byte(iterationKey))
	suffix := hex.EncodeToString(hash[:16])
	maximumBaseLength := 63 - 1 - len(suffix)
	base := strings.TrimRight(useName[:min(len(useName), maximumBaseLength)], "-")
	candidate = base + "-" + suffix
	if _, err := domain.NewName(candidate); err != nil {
		return "", renderError(StageExpansion, "invalid_component_use_name", "spec.uses."+useName, "component use could not form a stable qualified name", err)
	}
	return candidate, nil
}

func qualifyComponentResources(resources AuthorResources, prefix string) (AuthorResources, error) {
	encoded, err := json.Marshal(resources)
	if err != nil {
		return AuthorResources{}, renderError(StageExpansion, "component_clone_failed", "", "component resources could not be copied", err)
	}
	var result AuthorResources
	if err := json.Unmarshal(encoded, &result); err != nil {
		return AuthorResources{}, renderError(StageExpansion, "component_clone_failed", "", "component resources could not be copied", err)
	}
	for index := range result.Networks {
		if err := qualifyCommon(&result.Networks[index].AuthorCommon, prefix); err != nil {
			return AuthorResources{}, err
		}
	}
	for index := range result.Volumes {
		if err := qualifyCommon(&result.Volumes[index].AuthorCommon, prefix); err != nil {
			return AuthorResources{}, err
		}
	}
	for index := range result.VMs {
		if err := qualifyCommon(&result.VMs[index].AuthorCommon, prefix); err != nil {
			return AuthorResources{}, err
		}
		for interfaceIndex := range result.VMs[index].Interfaces {
			result.VMs[index].Interfaces[interfaceIndex].NetworkRef, err = qualifyAuthorReference(result.VMs[index].Interfaces[interfaceIndex].NetworkRef, prefix)
			if err != nil {
				return AuthorResources{}, err
			}
		}
		for mountIndex := range result.VMs[index].Mounts {
			result.VMs[index].Mounts[mountIndex].VolumeRef, err = qualifyAuthorReference(result.VMs[index].Mounts[mountIndex].VolumeRef, prefix)
			if err != nil {
				return AuthorResources{}, err
			}
		}
	}
	for index := range result.Containers {
		if err := qualifyCommon(&result.Containers[index].AuthorCommon, prefix); err != nil {
			return AuthorResources{}, err
		}
		for interfaceIndex := range result.Containers[index].Interfaces {
			result.Containers[index].Interfaces[interfaceIndex].NetworkRef, err = qualifyAuthorReference(result.Containers[index].Interfaces[interfaceIndex].NetworkRef, prefix)
			if err != nil {
				return AuthorResources{}, err
			}
		}
		for mountIndex := range result.Containers[index].Mounts {
			result.Containers[index].Mounts[mountIndex].VolumeRef, err = qualifyAuthorReference(result.Containers[index].Mounts[mountIndex].VolumeRef, prefix)
			if err != nil {
				return AuthorResources{}, err
			}
		}
	}
	for index := range result.AccessEndpoints {
		if err := qualifyCommon(&result.AccessEndpoints[index].AuthorCommon, prefix); err != nil {
			return AuthorResources{}, err
		}
		result.AccessEndpoints[index].TargetRef, err = qualifyAuthorReference(result.AccessEndpoints[index].TargetRef, prefix)
		if err != nil {
			return AuthorResources{}, err
		}
	}
	return result, nil
}

func qualifyCommon(common *AuthorCommon, prefix string) error {
	qualified, err := qualifiedChildName(prefix, common.Name)
	if err != nil {
		return err
	}
	common.Name = qualified
	for index, dependency := range common.DependsOn {
		common.DependsOn[index], err = qualifyAuthorReference(dependency, prefix)
		if err != nil {
			return err
		}
	}
	return nil
}

func qualifiedChildName(prefix, child string) (string, error) {
	candidate := prefix + "-" + child
	if _, err := domain.NewName(candidate); err == nil {
		return candidate, nil
	}
	hash := sha256.Sum256([]byte(prefix + "\x00" + child))
	suffix := hex.EncodeToString(hash[:16])
	maximumPrefixLength := 63 - 1 - len(suffix)
	base := strings.TrimRight(prefix[:min(len(prefix), maximumPrefixLength)], "-")
	candidate = base + "-" + suffix
	if _, err := domain.NewName(candidate); err != nil {
		return "", renderError(StageExpansion, "invalid_qualified_resource_name", prefix+"."+child, "component resource could not form a stable qualified name", err)
	}
	return candidate, nil
}

func qualifyAuthorReference(reference, prefix string) (string, error) {
	parts := strings.Split(reference, ".")
	if len(parts) != 2 {
		return "", renderError(StageReferenceResolution, "invalid_reference", reference, "component reference must contain a collection and local name", nil)
	}
	if _, exists := referenceKinds[parts[0]]; !exists {
		return "", renderError(StageReferenceResolution, "invalid_reference_kind", reference, "component reference uses an unknown collection", nil)
	}
	name, err := qualifiedChildName(prefix, parts[1])
	if err != nil {
		return "", err
	}
	return parts[0] + "." + name, nil
}
