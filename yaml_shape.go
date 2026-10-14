package labcentral

import (
	"fmt"
	"go.yaml.in/yaml/v3"
	"reflect"
	"regexp"
	"strings"
)

// Check author scalar types before yaml's convenient string coercions. Custom
// unions still decode themselves, but report errors at their manifest field.
func checkLabYAML(n *yaml.Node, t reflect.Type, path string, annotate func(string, string, map[string]any)) error {
	bad := func(m string) error { return problem(Invalid, path, m) }
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeOf(PlatformList{}) {
		var x PlatformList
		if e := x.UnmarshalYAML(n); e != nil {
			return bad(e.Error())
		}
		return nil
	}
	if t == reflect.TypeOf(JSONValue{}) {
		if n.Tag == "!!null" {
			return bad("default cannot be null")
		}
		var x JSONValue
		if e := x.UnmarshalYAML(n); e != nil {
			return bad(e.Error())
		}
		return nil
	}
	if t == reflect.TypeOf(Extensions{}) {
		var x Extensions
		if e := x.UnmarshalYAML(n); e != nil {
			return bad(e.Error())
		}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if !extensionPattern.MatchString(k) {
				return problem(Invalid, path+"."+k, "expected namespaced extension key")
			}
		}
		return nil
	}
	if t == reflect.TypeOf(HostnamePeers{}) {
		var p HostnamePeers
		if e := p.UnmarshalYAML(n); e != nil {
			return bad(e.Error())
		}
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			return bad("expected object")
		}
		fields := map[string]reflect.Type{}
		required := map[string]bool{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("yaml"), ",")[0]
			if name != "" && name != "-" {
				fields[name] = f.Type
				if !strings.Contains(f.Tag.Get("yaml"), ",omitempty") {
					required[name] = true
				}
			}
		}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i].Value
			f := k
			if path != "" {
				f = path + "." + k
			}
			delete(required, k)
			ft, ok := fields[k]
			if !ok {
				return problem(Invalid, f, "unknown field")
			}
			if e := checkLabYAML(n.Content[i+1], ft, f, annotate); e != nil {
				return e
			}
			ann := map[string]any{}
			annotate(t.Name(), k, ann)
			if e := checkYAMLAnnotation(n.Content[i+1], ann, f); e != nil {
				return e
			}
		}
		for _, k := range sortedKeys(required) {
			f := k
			if path != "" {
				f = path + "." + k
			}
			return problem(Invalid, f, "required")
		}
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			return bad("expected object")
		}
		for i := 0; i < len(n.Content); i += 2 {
			f := path + "." + n.Content[i].Value
			if !namePattern.MatchString(n.Content[i].Value) {
				return problem(Invalid, f, "invalid name")
			}
			if e := checkLabYAML(n.Content[i+1], t.Elem(), f, annotate); e != nil {
				return e
			}
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			return bad("expected array")
		}
		for i, c := range n.Content {
			if e := checkLabYAML(c, t.Elem(), fmtPathIndex(path, i), annotate); e != nil {
				return e
			}
		}
	case reflect.String:
		if n.Tag != "!!str" {
			return bad("expected string")
		}
	case reflect.Bool:
		if n.Tag != "!!bool" {
			return bad("expected boolean")
		}
	case reflect.Int:
		if n.Tag != "!!int" {
			return bad("expected integer")
		}
	default:
		return bad(fmt.Sprintf("unsupported type %s", t))
	}
	ann := map[string]any{}
	annotate(t.Name(), "", ann)
	return checkYAMLAnnotation(n, ann, path)
}

func checkYAMLAnnotation(n *yaml.Node, ann map[string]any, path string) error {
	bad := func(m string) error { return problem(Invalid, path, m) }
	if enum, ok := ann["enum"].([]string); ok && !Contains(enum, n.Value) {
		return bad("unsupported value")
	}
	if constant, ok := ann["const"].(int); ok {
		var v int
		if e := n.Decode(&v); e != nil || v != constant {
			return bad("unsupported version")
		}
	}
	if pattern, ok := ann["pattern"].(string); ok && !regexp.MustCompile(pattern).MatchString(n.Value) {
		return bad("invalid syntax")
	}
	if max, ok := ann["maxLength"].(int); ok && len(n.Value) > max {
		return bad("value is too long")
	}
	if max, ok := ann["maxItems"].(int); ok && len(n.Content) > max {
		return bad("too many array entries")
	}
	if unique, ok := ann["uniqueItems"].(bool); ok && unique && n.Kind == yaml.SequenceNode {
		seen := map[string]bool{}
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				continue
			}
			if seen[c.Value] {
				return bad("duplicate array entry")
			}
			seen[c.Value] = true
		}
	}
	if constant, ok := ann["const"].(bool); ok {
		var v bool
		if e := n.Decode(&v); e != nil || v != constant {
			return bad("expected true")
		}
	}
	if min, ok := ann["minLength"].(int); ok && len(n.Value) < min {
		return bad("value is too short")
	}
	if min, ok := ann["minItems"].(int); ok && len(n.Content) < min {
		return bad("expected non-empty array")
	}
	if min, ok := ann["minProperties"].(int); ok && len(n.Content)/2 < min {
		return bad("too few objects")
	}
	if max, ok := ann["maxProperties"].(int); ok && len(n.Content)/2 > max {
		return bad("too many objects")
	}
	if n.Tag == "!!int" {
		var v int
		if e := n.Decode(&v); e != nil {
			return bad(e.Error())
		}
		if min, ok := ann["minimum"].(int); ok && v < min {
			return bad("below supported bound")
		}
		if max, ok := ann["maximum"].(int); ok && v > max {
			return bad("above supported bound")
		}
	}
	return nil
}

// YAMLLineError annotates a field error with its declaration line, including
// the nearest parent for omitted fields. It preserves the exact field path.
func YAMLLineError(data []byte, err error) error {
	p, ok := err.(*Error)
	if !ok {
		return &Error{Code: Invalid, Field: "instructor.yaml", Message: err.Error()}
	}
	if strings.HasPrefix(p.Message, "line ") {
		return err
	}
	var root yaml.Node
	if yaml.Unmarshal(data, &root) != nil || len(root.Content) == 0 {
		return err
	}
	n := root.Content[0]
	parts := regexp.MustCompile(`[^.\[\]]+`).FindAllString(p.Field, -1)
	for _, part := range parts {
		next := n
		if n.Kind == yaml.MappingNode {
			for i := 0; i < len(n.Content); i += 2 {
				if n.Content[i].Value == part {
					next = n.Content[i+1]
					break
				}
			}
		} else if n.Kind == yaml.SequenceNode {
			var i int
			if _, e := fmt.Sscanf(part, "%d", &i); e == nil && i >= 0 && i < len(n.Content) {
				next = n.Content[i]
			}
		}
		if next == n {
			break
		}
		n = next
	}
	copy := *p
	copy.Message = fmt.Sprintf("line %d: %s", n.Line, p.Message)
	return &copy
}
