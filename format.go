package labcentral

import (
	"encoding/json"
	"fmt"
	"go.yaml.in/yaml/v3"
	"strings"
)

// Extensions are opaque namespaced JSON values. Encoding/json orders map keys.
type Extensions map[string]json.RawMessage

// JSONValue retains scalar type and distinguishes omission from false, zero and "".
type JSONValue json.RawMessage

func jsonNode(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.MappingNode:
		m := map[string]any{}
		for i := 0; i < len(n.Content); i += 2 {
			if n.Content[i].Tag != "!!str" {
				return nil, fmt.Errorf("JSON objects require string keys")
			}
			v, e := jsonNode(n.Content[i+1])
			if e != nil {
				return nil, e
			}
			m[n.Content[i].Value] = v
		}
		return m, nil
	case yaml.SequenceNode:
		a := []any{}
		for _, c := range n.Content {
			v, e := jsonNode(c)
			if e != nil {
				return nil, e
			}
			a = append(a, v)
		}
		return a, nil
	case yaml.ScalarNode:
		if !Contains([]string{"!!str", "!!int", "!!float", "!!bool", "!!null"}, n.Tag) {
			return nil, fmt.Errorf("tag %s is not JSON", n.Tag)
		}
		var v any
		if e := n.Decode(&v); e != nil {
			return nil, e
		}
		if _, e := json.Marshal(v); e != nil {
			return nil, e
		}
		return v, nil
	}
	return nil, fmt.Errorf("expected JSON value")
}
func (x *JSONValue) UnmarshalYAML(n *yaml.Node) error {
	v, e := jsonNode(n)
	if e != nil {
		return e
	}
	b, e := json.Marshal(v)
	if n.Tag == "!!float" && !strings.ContainsAny(string(b), ".eE") {
		b = append(b, []byte(".0")...)
	}
	*x = b
	return e
}
func (x JSONValue) MarshalJSON() ([]byte, error) {
	if len(x) == 0 {
		return []byte("null"), nil
	}
	return json.RawMessage(x).MarshalJSON()
}
func (x *JSONValue) UnmarshalJSON(b []byte) error { *x = append((*x)[:0], b...); return nil }
func (x *Extensions) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("extensions must be a mapping")
	}
	m := Extensions{}
	for i := 0; i < len(n.Content); i += 2 {
		v, e := jsonNode(n.Content[i+1])
		if e != nil {
			return e
		}
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		m[n.Content[i].Value] = b
	}
	*x = m
	return nil
}
func (p *HostnamePeers) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!str" && n.Value == "all" {
		*p = HostnamePeers{}
		return nil
	}
	if n.Kind != yaml.SequenceNode {
		return fmt.Errorf("hostnamePeers must be all or a list of names")
	}
	p.Explicit = true
	p.Names = []string{}
	for _, c := range n.Content {
		if c.Tag != "!!str" {
			return fmt.Errorf("hostnamePeers entries must be strings")
		}
		p.Names = append(p.Names, c.Value)
	}
	return nil
}

func (p HostnamePeers) MarshalYAML() (any, error) {
	if !p.Explicit {
		return "all", nil
	}
	if p.Names == nil {
		return []string{}, nil
	}
	return p.Names, nil
}
func (x JSONValue) MarshalYAML() (any, error) {
	var v any
	if len(x) == 0 {
		return nil, nil
	}
	e := json.Unmarshal(x, &v)
	return v, e
}
func (x Extensions) MarshalYAML() (any, error) {
	m := map[string]any{}
	for k, b := range x {
		var v any
		if e := json.Unmarshal(b, &v); e != nil {
			return nil, e
		}
		m[k] = v
	}
	return m, nil
}

func (p HostnamePeers) MarshalJSON() ([]byte, error) {
	if !p.Explicit {
		return json.Marshal("all")
	}
	if p.Names == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(p.Names)
}
func (p *HostnamePeers) UnmarshalJSON(b []byte) error {
	if string(b) == `"all"` {
		*p = HostnamePeers{}
		return nil
	}
	var names []string
	if e := json.Unmarshal(b, &names); e != nil {
		return e
	}
	*p = HostnamePeers{Explicit: true, Names: names}
	return nil
}

func (p *PlatformList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.SequenceNode {
		return fmt.Errorf("platforms must be a list")
	}
	p.Names = []string{}
	for _, c := range n.Content {
		if c.Tag != "!!str" {
			return fmt.Errorf("platform names must be strings")
		}
		p.Names = append(p.Names, c.Value)
	}
	return nil
}
func (p PlatformList) MarshalYAML() (any, error) {
	if p.Names == nil {
		return []string{}, nil
	}
	return p.Names, nil
}
func (p PlatformList) MarshalJSON() ([]byte, error) {
	if p.Names == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(p.Names)
}
func (p *PlatformList) UnmarshalJSON(b []byte) error { return json.Unmarshal(b, &p.Names) }
