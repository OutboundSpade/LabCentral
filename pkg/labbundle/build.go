package labbundle

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
)

// BuildMetadata explicitly opts a revision into immutable build-reference semantics.
// Configuration belongs to the selected builder, never to the portable lab model.
type BuildMetadata struct {
	APIVersion string        `yaml:"apiVersion" json:"apiVersion"`
	Recipes    []BuildRecipe `yaml:"recipes" json:"recipes"`
}
type BuildRecipe struct {
	DependsOn         []string     `yaml:"dependsOn,omitempty" json:"dependsOn,omitempty"`
	ContextRoot       string       `yaml:"-" json:"-"`
	ID                string       `yaml:"id" json:"id"`
	Builder           string       `yaml:"builder" json:"builder"`
	Schema            string       `yaml:"schema" json:"schema"`
	Architecture      string       `yaml:"architecture" json:"architecture"`
	ConfigurationJSON string       `yaml:"configurationJSON" json:"configurationJSON"`
	Context           []string     `yaml:"context" json:"context"`
	BaseImageDigests  []string     `yaml:"baseImageDigests" json:"baseImageDigests"`
	Inputs            []BuildInput `yaml:"inputs,omitempty" json:"inputs,omitempty"`
}
type BuildInput struct {
	Name  string `yaml:"name" json:"name"`
	Value string `yaml:"value" json:"value"`
}

var buildName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

func (b Bundle) Builds() *BuildMetadata {
	if b.ResolvedBuilds != nil {
		return b.ResolvedBuilds
	}
	return b.Manifest.Builds
}

func (b Bundle) ValidateBuilds() error {
	if b.Builds() == nil {
		return nil
	}
	m := b.Builds()
	if (m.APIVersion != "labcentral.build/v1" && m.APIVersion != "labcentral.build/v2") || len(m.Recipes) == 0 || len(m.Recipes) > 8 {
		return fmt.Errorf("unsupported build metadata")
	}
	seen := map[string]bool{}
	for _, r := range m.Recipes {
		if !buildName.MatchString(r.ID) || seen[r.ID] || len(r.Builder) == 0 || len(r.Builder) > 257 || !buildName.MatchString(r.Schema) || (r.Architecture != "amd64" && r.Architecture != "arm64") || len(r.ConfigurationJSON) > 64<<10 || len(r.Context) == 0 || len(r.Context) > 64 || len(r.BaseImageDigests) > 32 || len(r.Inputs) > 64 {
			return fmt.Errorf("invalid build recipe")
		}
		seen[r.ID] = true
		if len(r.DependsOn) > 8 || len(r.DependsOn) > 0 && m.APIVersion != "labcentral.build/v2" {
			return fmt.Errorf("dependencies require bounded build v2 semantics")
		}
		var config map[string]json.RawMessage
		if json.Unmarshal([]byte(r.ConfigurationJSON), &config) != nil || config == nil {
			return fmt.Errorf("build configuration must be a JSON object")
		}
		paths := map[string]bool{}
		for _, p := range r.Context {
			if _, ok := b.Files[p]; !ok || p == "bundle.yaml" || !ValidPath(p) || paths[p] {
				return fmt.Errorf("invalid build context")
			}
			paths[p] = true
		}
		for _, d := range r.BaseImageDigests {
			v, e := hex.DecodeString(d)
			if e != nil || len(v) != 32 {
				return fmt.Errorf("invalid base image digest")
			}
		}
		inputs := map[string]bool{}
		for _, v := range r.Inputs {
			if !buildName.MatchString(v.Name) || inputs[v.Name] || len(v.Value) > 4096 {
				return fmt.Errorf("invalid declared build input")
			}
			inputs[v.Name] = true
		}
	}
	recipes := map[string]BuildRecipe{}
	for _, recipe := range m.Recipes {
		recipes[recipe.ID] = recipe
	}
	state := map[string]uint8{}
	var visit func(string) error
	visit = func(id string) error {
		recipe, ok := recipes[id]
		if !ok {
			return fmt.Errorf("missing build dependency %q", id)
		}
		if state[id] == 1 {
			return fmt.Errorf("build dependency cycle")
		}
		if state[id] == 2 {
			return nil
		}
		state[id] = 1
		edges := map[string]bool{}
		for _, dependency := range recipe.DependsOn {
			if edges[dependency] {
				return fmt.Errorf("duplicate build dependency")
			}
			edges[dependency] = true
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range recipes {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}
