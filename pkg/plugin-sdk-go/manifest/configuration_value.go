package manifest

import (
	"bytes"
	"fmt"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ValidateConfiguration validates invocation configuration without resolving any
// external schema resources. Values are opaque to core and never portable resources.
func ValidateConfiguration(schema, value []byte) error {
	if len(value) == 0 || len(value) > 64<<10 {
		return fmt.Errorf("configuration must contain 1-65536 bytes")
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(value))
	if err != nil {
		return fmt.Errorf("invalid configuration JSON")
	}
	if _, ok := document.(map[string]any); !ok {
		return fmt.Errorf("configuration must be an object")
	}
	if len(schema) == 0 {
		if string(bytes.TrimSpace(value)) != "{}" {
			return fmt.Errorf("configuration requires a declared schema")
		}
		return nil
	}
	if err := ValidateConfigurationSchema(schema); err != nil {
		return err
	}
	definition, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return err
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(noExternalSchemas{})
	const location = "https://schemas.labcentral.invalid/extension-config"
	if err := compiler.AddResource(location, definition); err != nil {
		return err
	}
	compiled, err := compiler.Compile(location)
	if err != nil {
		return err
	}
	if err := compiled.Validate(document); err != nil {
		return fmt.Errorf("configuration does not satisfy the declared schema")
	}
	return nil
}
