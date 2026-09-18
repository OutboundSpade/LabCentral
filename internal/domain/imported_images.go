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

// OCIImageIdentity describes content, independently of the archive that carries it.
type OCIImageIdentity struct {
	ManifestDigest string `json:"manifestDigest"`
	ConfigDigest   string `json:"configDigest"`
	Architecture   string `json:"architecture"`
	OS             string `json:"os"`
}

func validImageDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := ParseSHA256Digest(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
func (i OCIImageIdentity) Validate() error {
	if !validImageDigest(i.ManifestDigest) || !validImageDigest(i.ConfigDigest) || i.Architecture != "amd64" || i.OS != "linux" {
		return fmt.Errorf("Linux amd64 OCI identity required")
	}
	return nil
}

// ContainerRuntimeImage binds the OCI content to a fixed runtime construction
// policy. It does not grant approval or infrastructure qualification.
type ContainerRuntimeImage struct {
	SchemaVersion  string `json:"schemaVersion"`
	ManifestDigest string `json:"manifestDigest"`
	ConfigDigest   string `json:"configDigest"`
	BaseSHA256     string `json:"baseSHA256"`
	AgentSHA256    string `json:"agentSHA256"`
}

func (p ContainerRuntimeImage) Validate() error {
	if p.SchemaVersion != "labcentral.container-image/v1" || !validImageDigest(p.ManifestDigest) || !validImageDigest(p.ConfigDigest) || !validImageDigest("sha256:"+p.BaseSHA256) || !validImageDigest("sha256:"+p.AgentSHA256) {
		return fmt.Errorf("invalid packaged container image evidence")
	}
	return nil
}
