package render

import (
	"bytes"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const labSchemaURL = "https://schemas.labcentral.io/labs/v1alpha1/lab.schema.json"

type SchemaValidator struct {
	schema *jsonschema.Schema
}

func NewSchemaValidator(schemaBytes []byte) (*SchemaValidator, error) {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		return nil, fmt.Errorf("decode Lab JSON Schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(labSchemaURL, document); err != nil {
		return nil, fmt.Errorf("register Lab JSON Schema: %w", err)
	}
	schema, err := compiler.Compile(labSchemaURL)
	if err != nil {
		return nil, fmt.Errorf("compile Lab JSON Schema: %w", err)
	}
	return &SchemaValidator{schema: schema}, nil
}

func (v *SchemaValidator) Validate(document any) error {
	if v == nil || v.schema == nil {
		return renderError(StageSchema, "schema_unavailable", "", "Lab schema validator is not initialized", nil)
	}
	if err := v.schema.Validate(document); err != nil {
		return renderError(StageSchema, "schema_validation_failed", "", "Lab document does not satisfy the authoring schema", err)
	}
	return nil
}
