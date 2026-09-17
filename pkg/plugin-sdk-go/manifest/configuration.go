package manifest

import (
	"bytes"
	"fmt"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type noExternalSchemas struct{}

func (noExternalSchemas) Load(string) (any, error) {
	return nil, fmt.Errorf("configuration schemas must be self-contained")
}

// ValidateConfigurationSchema compiles a bounded, self-contained JSON Schema.
// External schema references can neither read files nor make network requests.
func ValidateConfigurationSchema(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if len(data) > 64<<10 {
		return fmt.Errorf("configuration schema exceeds limit")
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("invalid configuration schema")
	}
	if _, ok := document.(map[string]any); !ok {
		return fmt.Errorf("configuration schema must be an object")
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(noExternalSchemas{})
	const location = "https://schemas.labcentral.invalid/extension-config"
	if err := compiler.AddResource(location, document); err != nil {
		return fmt.Errorf("invalid configuration schema")
	}
	if _, err := compiler.Compile(location); err != nil {
		return fmt.Errorf("configuration schema is invalid or references external data")
	}
	return nil
}
