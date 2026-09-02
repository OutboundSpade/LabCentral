// Package schemas exposes versioned machine contracts to composition roots.
package schemas

import _ "embed"

//go:embed lab-v1alpha1.schema.json
var labV1Alpha1 []byte

func LabV1Alpha1() []byte { return append([]byte(nil), labV1Alpha1...) }
