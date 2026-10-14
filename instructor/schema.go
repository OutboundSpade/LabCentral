package instructor

import (
	"encoding/json"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"strings"
)

func Schema() ([]byte, error) {
	s := lc.AuthorSchema(Bundle{}, annotateSchema)
	s["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	s["title"] = "LabCentral v3 private instructor source"
	s["allOf"] = []any{
		map[string]any{"if": map[string]any{"required": []string{"breaks"}, "properties": map[string]any{"breaks": map[string]any{"minItems": 1}}}, "then": map[string]any{"required": []string{"reset"}}},
		map[string]any{"if": map[string]any{"anyOf": []any{map[string]any{"required": []string{"stages"}, "properties": map[string]any{"stages": map[string]any{"minItems": 1}}}, map[string]any{"required": []string{"scenarios"}, "properties": map[string]any{"scenarios": map[string]any{"minItems": 1}}}}}, "then": map[string]any{"required": []string{"practice"}}},
	}
	b, e := json.MarshalIndent(s, "", "  ")
	return append(b, '\n'), e
}

func (b *Bundle) YAMLAnnotations() func(string, string, map[string]any) { return annotateSchema }
func annotateSchema(object, field string, s map[string]any) {
	key := object + "." + field
	bound := func(min, max int) { s["minimum"] = min; s["maximum"] = max }
	items := func(min, max int) { s["minItems"] = min; s["maxItems"] = max }
	length := func(min, max int) { s["minLength"] = min; s["maxLength"] = max }
	switch key {
	case "Bundle.version":
		s["const"] = 3
	case "Visibility.audience":
		s["enum"] = []string{"students", "coaches"}
	case "Visibility.hiddenMachines", "Step.guestHosts":
		items(0, 32)
		s["uniqueItems"] = true
		s["items"] = map[string]any{"type": "string", "pattern": idPattern.String()}
	case "Bundle.notes", "Break.walkthrough":
		length(0, 32000)
	case "Bundle.checks", "Bundle.breaks", "Bundle.stages":
		items(0, 20)
	case "Bundle.scenarios":
		items(0, 12)
	case "Bundle.reset", "Check.steps", "Break.steps":
		items(1, 8)
	case "Step.timeoutSeconds":
		bound(10, 600)
		s["default"] = 90
	case "Step.playbook":
		length(1, 1024)
		s["pattern"] = `^[a-zA-Z0-9_./-]+$`
	case "Step.files", "Step.assets":
		max := 64
		if field == "assets" {
			max = 20
		}
		items(0, max)
		s["uniqueItems"] = true
		s["items"] = map[string]any{"type": "string", "minLength": 1}
	case "Check.id", "Break.id", "Stage.id", "Scenario.id", "Variant.id", "Practice.desktopMachine", "Step.machine", "Stage.break", "Completion.check":
		s["pattern"] = idPattern.String()
	case "Check.name", "Break.name", "Stage.title", "Scenario.title":
		length(1, 120)
	case "Check.points":
		bound(1, 100)
	case "Stage.instructions", "Stage.solution", "Break.description":
		min := 0
		if field == "instructions" {
			min = 1
		}
		length(min, 6000)
	case "Stage.prompt":
		length(0, 1000)
	case "Answer.accepted":
		items(1, 10)
		s["items"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 1000}
	case "Completion.instructor", "Variant.compatible":
		s["const"] = true
	case "Completion.":
		s["minProperties"] = 1
		s["maxProperties"] = 1
	case "Library.category":
		length(1, 80)
	case "Library.difficulty", "Scenario.difficulty":
		s["enum"] = []string{"beginner", "intermediate", "advanced"}
	case "Library.estimatedMinutes", "Scenario.estimatedMinutes":
		bound(1, 1440)
	case "Library.tags":
		items(0, 12)
		s["uniqueItems"] = true
		s["items"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 48}
	case "Scenario.description":
		length(1, 2000)
	case "Scenario.variants":
		items(2, 32)
	case "Variant.breaks", "Variant.stages":
		max := 8
		if field == "stages" {
			max = 20
		}
		items(1, max)
		s["uniqueItems"] = true
		s["items"] = map[string]any{"type": "string", "pattern": idPattern.String()}
	}
	if strings.HasSuffix(key, ".extensions") {
		s["propertyNames"] = map[string]any{"pattern": extensionPattern.String()}
	}
}
