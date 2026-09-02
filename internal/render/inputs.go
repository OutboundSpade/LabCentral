package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"reflect"
	"regexp"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

type resolvedInputs struct {
	params      map[string]any
	secrets     map[string]domain.SecretRef
	secretNames map[string]struct{}
	iteration   map[string]any
}

var parameterNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func resolveInputs(parameters []AuthorParameter, provided domain.TypedInputs) (resolvedInputs, error) {
	providedByName := make(map[string]domain.TypedInput, len(provided))
	for _, input := range provided {
		name := string(input.Name)
		if _, exists := providedByName[name]; exists {
			return resolvedInputs{}, renderError(StageSchema, "duplicate_input", "spec.parameters."+name, "input was provided more than once", nil)
		}
		providedByName[name] = input
	}
	result := resolvedInputs{
		params:      make(map[string]any),
		secrets:     make(map[string]domain.SecretRef),
		secretNames: make(map[string]struct{}),
	}
	declared := make(map[string]struct{}, len(parameters))
	for _, parameter := range parameters {
		if !parameterNamePattern.MatchString(parameter.Name) {
			return resolvedInputs{}, renderError(StageSchema, "invalid_parameter_name", "spec.parameters."+parameter.Name, "parameter name is invalid", nil)
		}
		if _, exists := declared[parameter.Name]; exists {
			return resolvedInputs{}, renderError(StageSchema, "duplicate_parameter", "spec.parameters."+parameter.Name, "parameter is declared more than once", nil)
		}
		declared[parameter.Name] = struct{}{}
		if err := validateParameterDeclaration(parameter); err != nil {
			return resolvedInputs{}, renderError(StageSchema, "invalid_parameter_constraints", "spec.parameters."+parameter.Name, "parameter constraints are invalid", err)
		}
		input, supplied := providedByName[parameter.Name]
		if supplied && input.Kind != parameter.Type {
			return resolvedInputs{}, renderError(StageSchema, "input_type_mismatch", "spec.parameters."+parameter.Name, fmt.Sprintf("input has type %s, expected %s", input.Kind, parameter.Type), nil)
		}
		if parameter.Type == domain.InputSecretRef {
			result.secretNames[parameter.Name] = struct{}{}
			if len(parameter.Default) > 0 {
				return resolvedInputs{}, renderError(StageSchema, "secret_default_forbidden", "spec.parameters."+parameter.Name, "secretRef parameters cannot have defaults", nil)
			}
			if !supplied {
				if parameter.Required {
					return resolvedInputs{}, renderError(StageSchema, "required_input_missing", "spec.parameters."+parameter.Name, "required input is missing", nil)
				}
				continue
			}
			if input.SecretRef == nil {
				return resolvedInputs{}, renderError(StageSchema, "input_type_mismatch", "spec.parameters."+parameter.Name, "secretRef input has no reference", nil)
			}
			if err := input.SecretRef.Validate(); err != nil {
				return resolvedInputs{}, renderError(StageSchema, "invalid_secret_ref", "spec.parameters."+parameter.Name, "secret reference is invalid", err)
			}
			result.secrets[parameter.Name] = *input.SecretRef
			continue
		}
		if supplied {
			value, err := typedInputValue(input)
			if err != nil {
				return resolvedInputs{}, renderError(StageSchema, "invalid_input", "spec.parameters."+parameter.Name, "input value is invalid", err)
			}
			if err := validateParameterValue(parameter, value); err != nil {
				return resolvedInputs{}, renderError(StageSchema, "input_constraint_violation", "spec.parameters."+parameter.Name, "input value violates its parameter constraints", err)
			}
			result.params[parameter.Name] = value
			continue
		}
		if len(parameter.Default) == 0 {
			if parameter.Required {
				return resolvedInputs{}, renderError(StageSchema, "required_input_missing", "spec.parameters."+parameter.Name, "required input is missing", nil)
			}
			continue
		}
		value, err := decodeDefault(parameter.Type, parameter.Default)
		if err != nil {
			return resolvedInputs{}, renderError(StageSchema, "invalid_parameter_default", "spec.parameters."+parameter.Name, "parameter default does not match its type", err)
		}
		if err := validateParameterValue(parameter, value); err != nil {
			return resolvedInputs{}, renderError(StageSchema, "invalid_parameter_default", "spec.parameters."+parameter.Name, "parameter default violates its constraints", err)
		}
		result.params[parameter.Name] = value
	}
	for name := range providedByName {
		if _, exists := declared[name]; !exists {
			return resolvedInputs{}, renderError(StageSchema, "undeclared_input", "inputs."+name, "input was not declared by the Lab", nil)
		}
	}
	return result, nil
}

func typedInputValue(input domain.TypedInput) (any, error) {
	switch input.Kind {
	case domain.InputString:
		return input.String, nil
	case domain.InputEnum:
		if input.Enum == nil {
			return nil, fmt.Errorf("enum input has no typed value")
		}
		return input.Enum.Value()
	case domain.InputInteger:
		return input.Integer, nil
	case domain.InputNumber:
		if math.IsNaN(input.Number) || math.IsInf(input.Number, 0) {
			return nil, fmt.Errorf("number must be finite")
		}
		return input.Number, nil
	case domain.InputBoolean:
		return input.Boolean, nil
	case domain.InputStringList:
		return append([]string(nil), input.StringList...), nil
	case domain.InputCIDR:
		prefix, err := netip.ParsePrefix(input.String)
		if err != nil {
			return nil, err
		}
		return prefix.Masked().String(), nil
	case domain.InputIP:
		address, err := netip.ParseAddr(input.String)
		if err != nil {
			return nil, err
		}
		return address.String(), nil
	case domain.InputDuration:
		duration, err := time.ParseDuration(input.String)
		if err != nil {
			return nil, err
		}
		if duration <= 0 {
			return nil, fmt.Errorf("duration must be positive")
		}
		return duration.String(), nil
	default:
		return nil, fmt.Errorf("unsupported input type %q", input.Kind)
	}
}

func decodeDefault(kind domain.InputKind, raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	switch kind {
	case domain.InputString:
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("default is not a string")
		}
		return text, nil
	case domain.InputEnum:
		switch typed := value.(type) {
		case string:
			return typed, nil
		case json.Number:
			integer, err := typed.Int64()
			if err != nil {
				return nil, fmt.Errorf("enum default is not an integer: %w", err)
			}
			return integer, nil
		default:
			return nil, fmt.Errorf("enum default must be a string or integer")
		}
	case domain.InputInteger:
		number, ok := value.(json.Number)
		if !ok {
			return nil, fmt.Errorf("default is not an integer")
		}
		return number.Int64()
	case domain.InputNumber:
		number, ok := value.(json.Number)
		if !ok {
			return nil, fmt.Errorf("default is not a number")
		}
		parsed, err := number.Float64()
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return nil, fmt.Errorf("default is not a finite number: %w", err)
		}
		return parsed, nil
	case domain.InputBoolean:
		boolean, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("default is not Boolean")
		}
		return boolean, nil
	case domain.InputStringList:
		items, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("default is not a list")
		}
		strings := make([]string, len(items))
		for index, item := range items {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("default item %d is not a string", index)
			}
			strings[index] = text
		}
		return strings, nil
	case domain.InputCIDR:
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("default is not a CIDR string")
		}
		prefix, err := netip.ParsePrefix(text)
		if err != nil {
			return nil, err
		}
		return prefix.Masked().String(), nil
	case domain.InputIP:
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("default is not an IP string")
		}
		address, err := netip.ParseAddr(text)
		if err != nil {
			return nil, err
		}
		return address.String(), nil
	case domain.InputDuration:
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("default is not a duration string")
		}
		duration, err := time.ParseDuration(text)
		if err != nil {
			return nil, err
		}
		if duration <= 0 {
			return nil, fmt.Errorf("duration must be positive")
		}
		return duration.String(), nil
	default:
		return nil, fmt.Errorf("unsupported input type %q", kind)
	}
}

func validateParameterDeclaration(parameter AuthorParameter) error {
	if parameter.Min != nil && parameter.Max != nil && *parameter.Min > *parameter.Max {
		return fmt.Errorf("min must not exceed max")
	}
	if parameter.Min != nil || parameter.Max != nil {
		if parameter.Type != domain.InputInteger && parameter.Type != domain.InputNumber {
			return fmt.Errorf("min and max apply only to integer and number parameters")
		}
	}
	if parameter.Pattern != "" {
		if parameter.Type != domain.InputString {
			return fmt.Errorf("pattern applies only to string parameters")
		}
		if _, err := regexp.Compile(parameter.Pattern); err != nil {
			return fmt.Errorf("compile pattern: %w", err)
		}
	}
	if parameter.Type == domain.InputEnum && len(parameter.Enum) == 0 {
		return fmt.Errorf("enum parameters require at least one allowed value")
	}
	if parameter.Type != domain.InputEnum && len(parameter.Enum) > 0 {
		return fmt.Errorf("enum values apply only to enum parameters")
	}
	return nil
}

func validateParameterValue(parameter AuthorParameter, value any) error {
	if parameter.Type == domain.InputEnum {
		matched := false
		for index, raw := range parameter.Enum {
			allowed, err := decodeDefault(domain.InputEnum, raw)
			if err != nil {
				return fmt.Errorf("decode enum item %d: %w", index, err)
			}
			if reflect.DeepEqual(value, allowed) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("value is not one of the allowed enum members")
		}
	}
	if parameter.Min != nil || parameter.Max != nil {
		var numeric float64
		switch typed := value.(type) {
		case int64:
			numeric = float64(typed)
		case float64:
			numeric = typed
		default:
			return fmt.Errorf("constrained value is not numeric")
		}
		if parameter.Min != nil && numeric < *parameter.Min {
			return fmt.Errorf("value is less than min %v", *parameter.Min)
		}
		if parameter.Max != nil && numeric > *parameter.Max {
			return fmt.Errorf("value is greater than max %v", *parameter.Max)
		}
	}
	if parameter.Pattern != "" {
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("pattern-constrained value is not a string")
		}
		pattern, err := regexp.Compile(parameter.Pattern)
		if err != nil {
			return fmt.Errorf("compile pattern: %w", err)
		}
		if !pattern.MatchString(text) {
			return fmt.Errorf("value does not match pattern")
		}
	}
	return nil
}
