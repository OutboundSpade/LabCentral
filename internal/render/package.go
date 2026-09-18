package render

import (
	"fmt"
	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
)

const PackageRendererVersion = "labcentral.renderer/v1alpha1+package/v1"

// packageDocument composes only bounded data declarations. Imported components
// are still checked by the ordinary closed Lab schema and typed decoder.
func packageDocument(source []byte, limits ParseLimits) (any, error) {
	entry, _, err := labbundle.Source(source)
	if err != nil {
		return nil, err
	}
	document, err := ParseYAML(entry, limits)
	if err != nil {
		return nil, err
	}
	if !labbundle.Archive(source) {
		return document, nil
	}
	bundle, err := labbundle.Read(source)
	if err != nil {
		return nil, err
	}
	if bundle.Manifest.Package == nil {
		return document, nil
	}
	root, ok := document.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("lab must be an object")
	}
	spec, ok := root["spec"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("lab spec must be an object")
	}
	components := []any{}
	if existing, ok := spec["components"]; ok {
		var valid bool
		components, valid = existing.([]any)
		if !valid {
			return nil, fmt.Errorf("components must be an array")
		}
	}
	names := map[string]bool{}
	for _, component := range components {
		c, ok := component.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid component")
		}
		name, ok := c["name"].(string)
		if !ok || names[name] {
			return nil, fmt.Errorf("duplicate component")
		}
		names[name] = true
	}
	for _, imp := range bundle.Manifest.Package.Components {
		if names[imp.Name] {
			return nil, fmt.Errorf("imported component %q collides with a local component", imp.Name)
		}
		imported, err := ParseYAML(bundle.Files[imp.Path], limits)
		if err != nil {
			return nil, err
		}
		component, ok := imported.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("imported component must be an object")
		}
		component["name"] = imp.Name
		components = append(components, component)
		names[imp.Name] = true
	}
	if len(components) > 0 {
		spec["components"] = components
	}
	return document, nil
}
