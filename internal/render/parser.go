package render

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v4"
)

type ParseLimits struct {
	MaxBytes   int
	MaxNodes   int
	MaxDepth   int
	MaxAliases int
}

func DefaultParseLimits() ParseLimits {
	return ParseLimits{
		MaxBytes:   1 << 20,
		MaxNodes:   100_000,
		MaxDepth:   128,
		MaxAliases: 1_000,
	}
}

type parser struct {
	limits     ParseLimits
	nodes      int
	aliases    int
	aliasStack map[*yaml.Node]bool
}

// ParseYAML returns only JSON-compatible values. It rejects YAML features that
// do not have unambiguous portable JSON semantics.
func ParseYAML(input []byte, limits ParseLimits) (any, error) {
	if err := validateParseLimits(limits); err != nil {
		return nil, err
	}
	if len(input) == 0 {
		return nil, renderError(StageParse, "empty_document", "", "YAML document is empty", nil)
	}
	if len(input) > limits.MaxBytes {
		return nil, renderError(StageParse, "document_too_large", "", "YAML document exceeds the configured byte limit", nil)
	}
	if !utf8.Valid(input) {
		return nil, renderError(StageParse, "invalid_utf8", "", "YAML document is not valid UTF-8", nil)
	}
	var documents []yaml.Node
	if err := yaml.Load(input, &documents, yaml.WithAllDocuments(true), yaml.WithUniqueKeys(false)); err != nil {
		return nil, renderError(StageParse, "invalid_yaml", "", "YAML syntax is invalid", err)
	}
	if len(documents) != 1 {
		return nil, renderError(StageParse, "document_count_invalid", "", "exactly one YAML document is required", nil)
	}
	root := &documents[0]
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) != 1 {
			return nil, renderError(StageParse, "invalid_yaml", "", "YAML document has no root value", nil)
		}
		root = root.Content[0]
	}
	state := &parser{limits: limits, aliasStack: make(map[*yaml.Node]bool)}
	return state.convert(root, "", 1)
}

func validateParseLimits(limits ParseLimits) error {
	if limits.MaxBytes <= 0 || limits.MaxNodes <= 0 || limits.MaxDepth <= 0 || limits.MaxAliases < 0 {
		return renderError(StageParse, "invalid_parser_limits", "", "parser limits must be positive", nil)
	}
	return nil
}

func (p *parser) convert(node *yaml.Node, currentPath string, depth int) (any, error) {
	if node == nil {
		return nil, renderError(StageParse, "invalid_yaml", currentPath, "YAML node is missing", nil)
	}
	p.nodes++
	if p.nodes > p.limits.MaxNodes {
		return nil, renderError(StageParse, "node_limit_exceeded", currentPath, "YAML node limit exceeded", nil)
	}
	if depth > p.limits.MaxDepth {
		return nil, renderError(StageParse, "depth_limit_exceeded", currentPath, "YAML depth limit exceeded", nil)
	}
	switch node.Kind {
	case yaml.MappingNode:
		if len(node.Content)%2 != 0 {
			return nil, renderError(StageParse, "invalid_mapping", currentPath, "mapping has an unmatched key", nil)
		}
		result := make(map[string]any, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			keyNode := node.Content[index]
			p.nodes++
			if p.nodes > p.limits.MaxNodes {
				return nil, renderError(StageParse, "node_limit_exceeded", currentPath, "YAML node limit exceeded", nil)
			}
			if keyNode.Kind != yaml.ScalarNode || keyNode.ShortTag() != "!!str" {
				return nil, renderError(StageParse, "non_string_yaml_key", currentPath, "mapping keys must be strings", nil)
			}
			key := keyNode.Value
			path := joinPath(currentPath, key)
			if _, exists := result[key]; exists {
				return nil, renderError(StageParse, "duplicate_yaml_key", path, "mapping key is repeated", nil)
			}
			value, err := p.convert(node.Content[index+1], path, depth+1)
			if err != nil {
				return nil, err
			}
			result[key] = value
		}
		return result, nil
	case yaml.SequenceNode:
		result := make([]any, len(node.Content))
		for index, child := range node.Content {
			value, err := p.convert(child, fmt.Sprintf("%s[%d]", currentPath, index), depth+1)
			if err != nil {
				return nil, err
			}
			result[index] = value
		}
		return result, nil
	case yaml.AliasNode:
		p.aliases++
		if p.aliases > p.limits.MaxAliases {
			return nil, renderError(StageParse, "alias_limit_exceeded", currentPath, "YAML alias expansion limit exceeded", nil)
		}
		if node.Alias == nil {
			return nil, renderError(StageParse, "invalid_alias", currentPath, "YAML alias has no target", nil)
		}
		if p.aliasStack[node.Alias] {
			return nil, renderError(StageParse, "alias_cycle", currentPath, "YAML alias cycle detected", nil)
		}
		p.aliasStack[node.Alias] = true
		value, err := p.convert(node.Alias, currentPath, depth+1)
		delete(p.aliasStack, node.Alias)
		return value, err
	case yaml.ScalarNode:
		return convertScalar(node, currentPath)
	default:
		return nil, renderError(StageParse, "unsupported_yaml_node", currentPath, "YAML node kind is not portable", nil)
	}
}

func convertScalar(node *yaml.Node, currentPath string) (any, error) {
	switch node.ShortTag() {
	case "!!str":
		if !utf8.ValidString(node.Value) {
			return nil, renderError(StageParse, "invalid_utf8", currentPath, "string is not valid UTF-8", nil)
		}
		return node.Value, nil
	case "!!bool":
		var value bool
		if err := node.Decode(&value); err != nil {
			return nil, renderError(StageParse, "invalid_boolean", currentPath, "Boolean scalar is invalid", err)
		}
		return value, nil
	case "!!int":
		var value int64
		if err := node.Decode(&value); err != nil {
			return nil, renderError(StageParse, "integer_out_of_range", currentPath, "integer is outside the signed 64-bit range", err)
		}
		return json.Number(strconv.FormatInt(value, 10)), nil
	case "!!float":
		var value float64
		if err := node.Decode(&value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, renderError(StageParse, "invalid_number", currentPath, "number must be a finite IEEE-754 value", err)
		}
		return json.Number(strconv.FormatFloat(value, 'g', -1, 64)), nil
	case "!!null":
		return nil, nil
	default:
		return nil, renderError(StageParse, "unsafe_yaml_tag", currentPath, fmt.Sprintf("YAML tag %q is not permitted", node.Tag), nil)
	}
}

func joinPath(parent, child string) string {
	if parent == "" {
		return child
	}
	if child == "" {
		return parent
	}
	return strings.Join([]string{parent, child}, ".")
}
