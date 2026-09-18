package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ImportedImageRepository identifies target-local build results. References to
// it always require a retained, approved image binding at atomic admission.
const ImportedImageRepository = "labcentral.local/images"

func ImportedImageReferences(canonical []byte) ([]ImageRef, error) {
	var doc struct {
		Resources []struct {
			Kind string `json:"kind"`
			Spec struct {
				Image struct {
					Repository string `json:"repository"`
					Digest     string `json:"digest"`
				} `json:"image"`
			} `json:"spec"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(canonical, &doc); err != nil {
		return nil, err
	}
	seen := map[ImageRef]bool{}
	for _, resource := range doc.Resources {
		if !strings.HasPrefix(resource.Spec.Image.Repository, ImportedImageRepository+"/") {
			continue
		}
		if resource.Kind != "VM" && resource.Kind != "Container" {
			return nil, fmt.Errorf("imported image on unsupported resource")
		}
		digest, err := ParseSHA256Digest(strings.TrimPrefix(resource.Spec.Image.Digest, "sha256:"))
		if err != nil {
			return nil, err
		}
		ref, err := NewImageRef(resource.Spec.Image.Repository + "@sha256:" + digest.Hex())
		if err != nil {
			return nil, err
		}
		seen[ref] = true
	}
	out := make([]ImageRef, 0, len(seen))
	for digest := range seen {
		out = append(out, digest)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}
