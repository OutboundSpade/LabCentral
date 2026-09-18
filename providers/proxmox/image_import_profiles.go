package proxmox

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

func importedTemplateProfile(config TargetConfig, alias string) (TemplateProfile, bool) {
	if config.ImageImport == nil || config.SchemaVersion != targetConfigV14 || !aliasPattern.MatchString(alias) || !strings.HasPrefix(alias, "img-") {
		return TemplateProfile{}, false
	}
	raw, err := readImageFile(filepath.Join(config.JournalDirectory, "images", alias+".json"))
	if err != nil {
		return TemplateProfile{}, false
	}
	var e importedImageEvidence
	configRaw, _ := json.Marshal(config)
	if decodeEnvelope(raw, maximumJournalBytes, &e) != nil || e.Version != imageImportPlanV1 || e.Retired || e.ConfigurationDigest != bareDigest(configRaw) || e.Profile.Alias != alias || e.Profile.ContentDigest != "sha256:"+e.Plan.ArtifactDigest || !sha256Pattern.MatchString(e.Profile.ContentDigest) || e.Profile.VMID != e.Plan.VMID || e.Profile.SourceNode != config.DestinationNode || e.Ownership != imageOwnership(e.Plan, config) {
		return TemplateProfile{}, false
	}
	return e.Profile, true
}
