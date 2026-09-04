package httpapi

import (
	"fmt"
	"regexp"

	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

// InputValue has exactly one typed value; plaintext secrets are not inputs.
type InputValue struct {
	Name       string   `json:"name" minLength:"1" maxLength:"63"`
	Kind       string   `json:"kind" enum:"string,integer,number,boolean,enum,stringList,cidr,ip,duration"`
	String     *string  `json:"string,omitempty" maxLength:"65536"`
	Integer    *int64   `json:"integer,omitempty"`
	Number     *float64 `json:"number,omitempty"`
	Boolean    *bool    `json:"boolean,omitempty"`
	StringList []string `json:"stringList,omitempty" maxItems:"10000"`
}

func decodeRenderOptions(values []InputValue) (controlplane.RenderOptions, error) {
	out := controlplane.RenderOptions{}
	if len(values) > 256 {
		return out, fmt.Errorf("at most 256 inputs are allowed")
	}
	seen := map[string]bool{}
	for _, v := range values {
		if seen[v.Name] {
			return out, fmt.Errorf("input names must be unique")
		}
		seen[v.Name] = true
		n := 0
		for _, present := range []bool{v.String != nil, v.Integer != nil, v.Number != nil, v.Boolean != nil, v.StringList != nil} {
			if present {
				n++
			}
		}
		if n != 1 {
			return out, fmt.Errorf("each input requires exactly one typed value")
		}
		if !parameterNamePattern.MatchString(v.Name) {
			return out, fmt.Errorf("invalid parameter name")
		}
		name := domain.Name(v.Name)
		input := domain.TypedInput{Name: name, Kind: domain.InputKind(v.Kind)}
		switch input.Kind {
		case domain.InputString, domain.InputCIDR, domain.InputIP, domain.InputDuration:
			if v.String == nil {
				return out, fmt.Errorf("input requires a string value")
			}
			input.String = *v.String
		case domain.InputInteger:
			if v.Integer == nil {
				return out, fmt.Errorf("input requires an integer value")
			}
			input.Integer = *v.Integer
		case domain.InputNumber:
			if v.Number == nil {
				return out, fmt.Errorf("input requires a number value")
			}
			input.Number = *v.Number
		case domain.InputBoolean:
			if v.Boolean == nil {
				return out, fmt.Errorf("input requires a boolean value")
			}
			input.Boolean = *v.Boolean
		case domain.InputStringList:
			if v.StringList == nil {
				return out, fmt.Errorf("input requires a string list")
			}
			input.StringList = append([]string(nil), v.StringList...)
		case domain.InputEnum:
			if v.String == nil && v.Integer == nil {
				return out, fmt.Errorf("enum input requires a string or integer")
			}
			input.Enum = &domain.EnumValue{String: v.String, Integer: v.Integer}
		default:
			return out, fmt.Errorf("unsupported input kind")
		}
		out.Inputs = append(out.Inputs, input)
	}
	return out, nil
}

var parameterNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
