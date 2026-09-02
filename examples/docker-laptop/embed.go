// Package dockerlaptop exposes the checked Example A source to the local
// onboarding API. The embedded bytes are verified against the bundle manifest
// by the existing Phase 0 verifier.
package dockerlaptop

import _ "embed"

//go:embed lab.yaml
var labYAML []byte

func LabYAML() []byte { return append([]byte(nil), labYAML...) }
