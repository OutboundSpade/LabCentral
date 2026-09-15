package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"github.com/OutboundSpade/LabCentral/providers/proxmox"
	"github.com/OutboundSpade/LabCentral/tools/proxmox-qualification/qualification"
)

const maximumTrustPolicyBytes = 64 << 10

type productionAuthorizer struct {
	document         []byte
	cleanupDocument  []byte
	evidence         qualification.Evidence
	trust            qualification.TrustPolicy
	binaryDigest     string
	sourceTreeDigest string
	now              func() time.Time
}

func loadProductionAuthorizer(documentPath, trustPath, binaryDigest, sourceTreeDigest string) (*productionAuthorizer, error) {
	document, err := readPublicInput(documentPath, qualification.MaxDocumentBytes)
	if err != nil {
		return nil, fmt.Errorf("read qualification document: %w", err)
	}
	parsed, err := qualification.ParseSigned(document)
	if err != nil {
		return nil, err
	}
	trustBytes, err := readPublicInput(trustPath, maximumTrustPolicyBytes)
	if err != nil {
		return nil, fmt.Errorf("read qualification trust policy: %w", err)
	}
	trust, err := qualification.DecodeTrustPolicy(trustBytes)
	if err != nil {
		return nil, err
	}
	if !qualifiedDigest(binaryDigest) || !qualifiedDigest(sourceTreeDigest) {
		return nil, errors.New("provider build lacks qualification-bound binary or source provenance")
	}
	return &productionAuthorizer{
		document: append([]byte(nil), document...), evidence: parsed.Evidence, trust: trust,
		binaryDigest: binaryDigest, sourceTreeDigest: sourceTreeDigest, now: time.Now,
	}, nil
}

func (authorizer *productionAuthorizer) AuthorizeMutation(ctx context.Context, input proxmox.MutationAuthorizationInput) (proxmox.MutationAuthorization, error) {
	if err := ctx.Err(); err != nil {
		return proxmox.MutationAuthorization{}, err
	}
	if input.Target == nil || input.Target.GetTargetId() == "" || !qualifiedDigest(input.Target.GetConfigFingerprint()) || !qualifiedDigest(input.MutationInventoryDigest) {
		return proxmox.MutationAuthorization{}, errors.New("production qualification target binding is invalid")
	}
	if input.Config.Qualification.InventoryDigest != input.MutationInventoryDigest {
		return proxmox.MutationAuthorization{}, errors.New("production qualification inventory binding is stale")
	}
	now := authorizer.now().UTC()
	trustedKeys, err := authorizer.trust.TrustedKeysAt(now)
	if err != nil {
		return proxmox.MutationAuthorization{}, err
	}
	pveVersion := qualifiedPVEVersion(input.Inventory)
	verification, err := qualification.Verify(authorizer.document, qualification.VerificationPolicy{
		Now:                          now,
		TrustedKeys:                  trustedKeys,
		ExpectedTargetID:             input.Target.GetTargetId(),
		ExpectedInstallationID:       input.Config.InstallationID,
		ExpectedConfigurationDigest:  input.Target.GetConfigFingerprint(),
		ExpectedCapabilityDigest:     input.Config.Qualification.CapabilityDigest,
		ExpectedInventoryDigest:      input.MutationInventoryDigest,
		ExpectedPolicyDigest:         input.Config.Qualification.PolicyDigest,
		ExpectedPVERelease:           input.Config.Qualification.PVERelease,
		ExpectedPVEVersion:           pveVersion,
		ExpectedProviderBinaryDigest: authorizer.binaryDigest,
		ExpectedSourceTreeDigest:     authorizer.sourceTreeDigest,
	})
	if err != nil || !verification.Qualifies {
		return proxmox.MutationAuthorization{}, errors.New("signed qualification evidence does not authorize this target")
	}
	issuedAt, err := time.Parse(time.RFC3339, authorizer.evidence.IssuedAt)
	if err != nil || now.Before(issuedAt) || now.Sub(issuedAt) > authorizer.trust.MaxEvidenceAge() {
		return proxmox.MutationAuthorization{}, errors.New("signed qualification evidence exceeds deployment maximum age")
	}
	if authorizer.evidence.Build.PluginVersion != proxmox.PluginVersion || authorizer.evidence.Protocol.Major != 1 || authorizer.evidence.Protocol.Minor != 1 {
		return proxmox.MutationAuthorization{}, errors.New("signed qualification protocol or provider version is incompatible")
	}
	profile, err := qualificationProfile(input.Config, input.Inventory)
	if err != nil || !reflect.DeepEqual(profile, normalizedQualificationProfile(authorizer.evidence.Profile)) {
		return proxmox.MutationAuthorization{}, errors.New("signed qualification profile does not match current target")
	}
	return proxmox.MutationAuthorization{Evidence: "signed evidence " + verification.EvidenceDigest + " trusted as " + verification.KeyID}, nil
}

func qualifiedPVEVersion(inventory proxmox.Inventory) string {
	version := inventory.Version.Version
	if len(version) > len("pve-manager/") && version[:len("pve-manager/")] == "pve-manager/" {
		return version
	}
	return "pve-manager/" + version + "/" + inventory.Version.RepoID
}

func qualificationProfile(config proxmox.TargetConfig, inventory proxmox.Inventory) (qualification.ProfileEvidence, error) {
	profile := qualification.ProfileEvidence{DestinationNode: config.DestinationNode}
	if r := config.ContainerRuntime; r != nil {
		profile.ContainerRuntime = &qualification.RuntimeProfileEvidence{SchemaVersion: r.SchemaVersion, Pool: config.Pool, TemplateAlias: r.TemplateAlias, Images: append([]string(nil), r.Images...)}
	}
	for _, value := range config.VMIDRanges {
		profile.VMIDRanges = append(profile.VMIDRanges, qualification.IntegerRange{Start: value.Start, End: value.End})
	}
	for _, value := range config.Storages {
		storageType := ""
		for _, observed := range inventory.Storages {
			if observed.Node == config.DestinationNode && observed.ID == value.ID {
				if storageType != "" && storageType != observed.Type {
					return qualification.ProfileEvidence{}, errors.New("configured storage identity is ambiguous")
				}
				storageType = observed.Type
			}
		}
		if storageType == "" {
			return qualification.ProfileEvidence{}, errors.New("configured storage is absent")
		}
		profile.Storages = append(profile.Storages, qualification.StorageEvidence{Alias: value.Alias, ID: value.ID, Type: storageType, Format: value.Format})
	}
	for _, value := range config.Networks {
		vlanAware := false
		for _, observed := range inventory.Bridges {
			if observed.Node == config.DestinationNode && observed.Name == value.Bridge && observed.Active && observed.VLANAware {
				vlanAware = true
			}
		}
		ranges := make([]qualification.IntegerRange, 0, len(value.VLANRanges))
		for _, item := range value.VLANRanges {
			ranges = append(ranges, qualification.IntegerRange{Start: item.Start, End: item.End})
		}
		profile.Networks = append(profile.Networks, qualification.NetworkEvidence{Alias: value.Alias, Bridge: value.Bridge, VLANAware: vlanAware, VLANRanges: ranges})
	}
	for _, value := range config.Templates {
		profile.Templates = append(profile.Templates, qualification.TemplateEvidence{
			Alias: value.Alias, SourceNode: value.SourceNode, VMID: value.VMID,
			ConfigDigest: value.ConfigDigest, ContentDigest: value.ContentDigest,
		})
	}
	for _, value := range config.PermissionScopes {
		profile.Permissions = append(profile.Permissions, qualification.PermissionEvidence{Path: value.Path, Privileges: append([]string(nil), value.Privileges...)})
	}
	return normalizedQualificationProfile(profile), nil
}

func normalizedQualificationProfile(profile qualification.ProfileEvidence) qualification.ProfileEvidence {
	result := profile
	if profile.ContainerRuntime != nil {
		r := *profile.ContainerRuntime
		r.Images = append([]string(nil), r.Images...)
		sort.Strings(r.Images)
		result.ContainerRuntime = &r
	}
	result.VMIDRanges = append([]qualification.IntegerRange(nil), profile.VMIDRanges...)
	result.Storages = append([]qualification.StorageEvidence(nil), profile.Storages...)
	result.Networks = append([]qualification.NetworkEvidence(nil), profile.Networks...)
	result.Templates = append([]qualification.TemplateEvidence(nil), profile.Templates...)
	result.Permissions = append([]qualification.PermissionEvidence(nil), profile.Permissions...)
	sort.Slice(result.VMIDRanges, func(i, j int) bool { return result.VMIDRanges[i].Start < result.VMIDRanges[j].Start })
	sort.Slice(result.Storages, func(i, j int) bool { return result.Storages[i].Alias < result.Storages[j].Alias })
	sort.Slice(result.Networks, func(i, j int) bool { return result.Networks[i].Alias < result.Networks[j].Alias })
	for index := range result.Networks {
		result.Networks[index].VLANRanges = append([]qualification.IntegerRange(nil), result.Networks[index].VLANRanges...)
		sort.Slice(result.Networks[index].VLANRanges, func(i, j int) bool {
			return result.Networks[index].VLANRanges[i].Start < result.Networks[index].VLANRanges[j].Start
		})
	}
	sort.Slice(result.Templates, func(i, j int) bool { return result.Templates[i].Alias < result.Templates[j].Alias })
	sort.Slice(result.Permissions, func(i, j int) bool { return result.Permissions[i].Path < result.Permissions[j].Path })
	for index := range result.Permissions {
		result.Permissions[index].Privileges = append([]string(nil), result.Permissions[index].Privileges...)
		sort.Strings(result.Permissions[index].Privileges)
	}
	return result
}

func readPublicInput(path string, maximum int) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, errors.New("path must be absolute, clean, and non-root")
	}
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Mode().Perm()&0o022 != 0 || before.Size() <= 0 || before.Size() > int64(maximum) {
		return nil, errors.New("path must name a bounded non-writable regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || after.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("input identity or permissions changed")
	}
	contents, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(contents) == 0 || len(contents) > maximum {
		return nil, errors.New("input could not be read within its bound")
	}
	return contents, nil
}

func qualifiedDigest(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || value[:len("sha256:")] != "sha256:" {
		return false
	}
	decoded, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil && len(decoded) == sha256.Size
}

var _ proxmox.MutationAuthorizer = (*productionAuthorizer)(nil)

func (a *productionAuthorizer) AuthorizeCleanup(ctx context.Context, in proxmox.CleanupAuthorizationInput) (proxmox.MutationAuthorization, error) {
	if err := ctx.Err(); err != nil {
		return proxmox.MutationAuthorization{}, err
	}
	evidence, err := qualification.VerifyCleanup(a.cleanupDocument, a.trust, a.now().UTC(), qualification.CleanupGrant{TargetID: in.Target.GetTargetId(), InstallationID: in.Config.InstallationID, ConfigurationDigest: in.Target.GetConfigFingerprint(), InventoryDigest: in.MutationInventoryDigest, ProviderBinaryDigest: a.binaryDigest, InstanceID: in.InstanceID, ResourceID: in.ResourceID, OwnershipDigest: in.OwnershipDigest})
	return proxmox.MutationAuthorization{Evidence: evidence}, err
}
