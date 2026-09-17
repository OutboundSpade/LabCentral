package domain

type BuildRecipe struct {
	ID                string       `json:"id"`
	Builder           string       `json:"builder"`
	Schema            string       `json:"schema"`
	Architecture      string       `json:"architecture"`
	ConfigurationJSON string       `json:"configurationJSON"`
	Context           []string     `json:"context"`
	BaseImageDigests  []string     `json:"baseImageDigests"`
	Inputs            []BuildInput `json:"inputs,omitempty"`
}
type BuildInput struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
