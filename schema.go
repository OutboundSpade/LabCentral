package labcentral

import (
	"encoding/json"
	"reflect"
	"strings"
)

// LabSchema generates the author JSON Schema from YAML field tags and the small
// structural annotation table below. References and provider support are semantic.
func LabSchema() ([]byte, error) {
	s := schemaType(reflect.TypeOf(Lab{}))
	s["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	s["title"] = "LabCentral v3 lab source"
	b, e := json.MarshalIndent(s, "", "  ")
	return append(b, '\n'), e
}

// AuthorSchema generates strict object shapes from public YAML tags. The
// annotation callback supplies package-specific bounds and unions.
func AuthorSchema(value any, annotate func(string, string, map[string]any)) map[string]any {
	return authorSchemaType(reflect.TypeOf(value), annotate)
}
func schemaType(t reflect.Type) map[string]any { return authorSchemaType(t, annotateSchema) }
func authorSchemaType(t reflect.Type, annotate func(string, string, map[string]any)) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeOf(PlatformList{}) {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	}
	if t == reflect.TypeOf(JSONValue{}) {
		return map[string]any{}
	}
	if t == reflect.TypeOf(Extensions{}) {
		return map[string]any{"type": "object", "propertyNames": map[string]any{"pattern": extensionPattern.String()}, "additionalProperties": true}
	}
	if t == reflect.TypeOf(HostnamePeers{}) {
		return map[string]any{"oneOf": []any{map[string]any{"const": "all"}, map[string]any{"type": "array", "uniqueItems": true, "items": nameSchema()}}}
	}
	s := map[string]any{}
	switch t.Kind() {
	case reflect.Struct:
		props := map[string]any{}
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("yaml"), ",")
			if tag[0] == "" || tag[0] == "-" {
				continue
			}
			v := authorSchemaType(f.Type, annotate)
			annotate(t.Name(), tag[0], v)
			props[tag[0]] = v
			if len(tag) == 1 {
				required = append(required, tag[0])
			}
		}
		s = map[string]any{"type": "object", "properties": props, "additionalProperties": false}
		if len(required) > 0 {
			s["required"] = required
		}
		if t == reflect.TypeOf(Input{}) {
			clauses := []any{}
			for _, kind := range []string{"string", "integer", "boolean"} {
				value := map[string]any{"type": kind}
				if kind == "string" {
					value["maxLength"] = 4096
				}
				if kind == "integer" {
					value["minimum"] = -9223372036854775808
					value["maximum"] = 9223372036854775807
				}
				clauses = append(clauses, map[string]any{"if": map[string]any{"properties": map[string]any{"type": map[string]any{"const": kind}}}, "then": map[string]any{"properties": map[string]any{"default": value}}})
			}
			clauses = append(clauses, map[string]any{"if": map[string]any{"required": []string{"required"}, "properties": map[string]any{"required": map[string]any{"const": true}}}, "then": map[string]any{"not": map[string]any{"required": []string{"default"}}}})
			s["allOf"] = clauses
		}
	case reflect.Map:
		s = map[string]any{"type": "object", "propertyNames": nameSchema(), "additionalProperties": authorSchemaType(t.Elem(), annotate)}
	case reflect.Slice:
		s = map[string]any{"type": "array", "items": authorSchemaType(t.Elem(), annotate)}
	case reflect.String:
		s["type"] = "string"
	case reflect.Bool:
		s["type"] = "boolean"
	case reflect.Int:
		s["type"] = "integer"
	}
	annotate(t.Name(), "", s)
	return s
}
func nameSchema() map[string]any {
	return map[string]any{"type": "string", "pattern": namePattern.String()}
}
func annotateSchema(object, field string, s map[string]any) {
	enum := func(v ...string) { s["enum"] = v }
	switch object + "." + field {
	case "Lab.version":
		s["const"] = 3
	case "Lab.name", "Machine.image", "NIC.network":
		s["pattern"] = namePattern.String()
	case "Lab.images", "Lab.machines":
		s["minProperties"] = 1
		s["maxProperties"] = 32
	case "Lab.networks":
		s["minProperties"] = 1
	case "Lab.platforms":
		s["uniqueItems"] = true
		s["items"] = map[string]any{"enum": []string{"aws", "proxmox"}}
	case "Image.base":
		s["pattern"] = basePattern.String()
	case "Image.os":
		enum("linux", "windows")
		s["default"] = "linux"
	case "ImageBuild.playbook":
		s["pattern"] = `^[a-zA-Z0-9_./-]+$`
		s["minLength"] = 1
	case "ImageBuild.files":
		s["minItems"] = 1
		s["uniqueItems"] = true
		s["items"] = map[string]any{"type": "string", "pattern": `^[a-zA-Z0-9_./-]+$`}
	case "Machine.cpu":
		s["minimum"] = 1
		s["maximum"] = 64
	case "Machine.memory", "Machine.disk":
		s["pattern"] = sizePattern.String()
	case "Machine.access":
		s["uniqueItems"] = true
		s["items"] = map[string]any{"enum": []string{"ssh", "desktop"}}
	case "Machine.nics":
		s["minItems"] = 1
	case "NIC.address":
		s["minimum"] = 4
		s["maximum"] = 250
	case "Network.cidr":
		s["minLength"] = 1
	case "Network.egress":
		enum("none", "internet")
		s["default"] = "none"
	case "Input.type":
		enum("string", "integer", "boolean")
	case "Input.description":
		s["maxLength"] = 500
	}
}
