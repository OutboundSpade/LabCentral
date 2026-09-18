package containervm

import (
	"fmt"
	"github.com/OutboundSpade/LabCentral/pkg/ocilayout"
)

const PackagedImageVersion = "labcentral.container-image/v1"
const RuntimeBuildSchema = "labcentral.container-runtime.v1"
const PackagedRuntimeVersion = "labcentral.container-vm/v2"

// PackagedImage keeps OCI identity distinct from the containing qcow2 artifact.
// It is evidence for approval, never approval or infrastructure qualification.
type PackagedImage struct {
	SchemaVersion  string `json:"schemaVersion"`
	ManifestDigest string `json:"manifestDigest"`
	ConfigDigest   string `json:"configDigest"`
	BaseSHA256     string `json:"baseSHA256"`
	AgentSHA256    string `json:"agentSHA256"`
}

func (p PackagedImage) Validate() error {
	if p.SchemaVersion != PackagedImageVersion || !ocilayout.SHA256(p.ManifestDigest) || !ocilayout.SHA256(p.ConfigDigest) || !ocilayout.SHA256("sha256:"+p.BaseSHA256) || !ocilayout.SHA256("sha256:"+p.AgentSHA256) {
		return fmt.Errorf("invalid packaged container image evidence")
	}
	return nil
}

// RuntimeBuildPolicy is operator configuration, excluded from portable source.
// The base is at a fixed path inside the pinned toolchain guest.
type RuntimeBuildPolicy struct {
	BaseSHA256  string `json:"baseSHA256"`
	AgentSHA256 string `json:"agentSHA256"`
	DiskGiB     uint32 `json:"diskGiB"`
}

func (p RuntimeBuildPolicy) Validate() error {
	if !ocilayout.SHA256("sha256:"+p.BaseSHA256) || !ocilayout.SHA256("sha256:"+p.AgentSHA256) || p.DiskGiB < 8 || p.DiskGiB > 64 {
		return fmt.Errorf("invalid runtime build policy")
	}
	return nil
}

type RuntimeBuildSettings struct {
	Policy RuntimeBuildPolicy `json:"policy"`
	Image  ocilayout.Identity `json:"image"`
}

func (s RuntimeBuildSettings) Validate() error {
	if err := s.Policy.Validate(); err != nil {
		return err
	}
	return s.Image.Validate()
}
func (s RuntimeBuildSettings) Evidence() PackagedImage {
	return PackagedImage{SchemaVersion: PackagedImageVersion, ManifestDigest: s.Image.ManifestDigest, ConfigDigest: s.Image.ConfigDigest, BaseSHA256: s.Policy.BaseSHA256, AgentSHA256: s.Policy.AgentSHA256}
}
