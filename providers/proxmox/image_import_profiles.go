package proxmox

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

func importedTemplateProfile(config TargetConfig, alias string) (TemplateProfile, bool) {
	evidence, ok := importedImageProfile(config, alias)
	return evidence.Profile, ok
}
func importedImageProfile(config TargetConfig, alias string) (importedImageEvidence, bool) {
	if config.ImageImport == nil || (config.SchemaVersion != targetConfigV14 && config.SchemaVersion != targetConfigV15 && config.SchemaVersion != targetConfigV16) || !aliasPattern.MatchString(alias) || !strings.HasPrefix(alias, "img-") {
		return importedImageEvidence{}, false
	}
	raw, err := readImageFile(filepath.Join(config.JournalDirectory, "images", alias+".json"))
	if err != nil {
		return importedImageEvidence{}, false
	}
	var e importedImageEvidence
	configRaw, _ := json.Marshal(config)
	if decodeEnvelope(raw, maximumJournalBytes, &e) != nil || e.Version != imageImportPlanV1 || e.Retired || !validRuntimeEvidence(config, e.Plan.ContainerRuntime) || e.ConfigurationDigest != bareDigest(configRaw) || e.Profile.Alias != alias || e.Profile.ContentDigest != "sha256:"+e.Plan.ArtifactDigest || !sha256Pattern.MatchString(e.Profile.ContentDigest) || e.Profile.VMID != e.Plan.VMID || e.Profile.SourceNode != config.DestinationNode || e.Ownership != imageOwnership(e.Plan, config) {
		return importedImageEvidence{}, false
	}
	return e, true
}
