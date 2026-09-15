// Package qualification defines the signed, exact-target evidence boundary for
// credentialed Proxmox T3 runs. The production authorizer consumes verified
// evidence; separately signed cleanup authority follows ADR-0017.
package qualification

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/OutboundSpade/LabCentral/pkg/containervm"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/render"
)

const (
	RuntimeEvidenceSchema = "labcentral.proxmox-qualification-evidence/v1.1"
	EvidenceSchema        = "labcentral.proxmox-qualification-evidence/v1"
	EnvelopeSchema        = "labcentral.proxmox-qualification-envelope/v1"
	SignatureMethod       = "Ed25519"
	ProviderPlugin        = "io.labcentral.provider.proxmox"

	MaxDocumentBytes = 1 << 20
	MaxValidity      = 30 * 24 * time.Hour
)

var (
	stableIDPattern       = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	aliasPattern          = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	objectPattern         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)
	digestPattern         = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	sha1Pattern           = regexp.MustCompile(`^[0-9a-f]{40}$`)
	pveReleasePattern     = regexp.MustCompile(`^9\.[0-9]+\.[0-9]+$`)
	pveVersionPattern     = regexp.MustCompile(`^pve-manager/9\.[0-9]+\.[0-9]+/[0-9A-Fa-f]{8,64}$`)
	privilegePattern      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9.]{0,63}$`)
	claimValuePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
	permissionPathPattern = regexp.MustCompile(`^/(vms|(nodes|pool|storage|vms)/[A-Za-z0-9][A-Za-z0-9_.-]{0,254}|sdn/zones/localnetwork/[A-Za-z0-9][A-Za-z0-9_.-]{0,254})$`)
)

var requiredT3Cases = []string{
	"TEST-PROXMOX-003",
	"TEST-PROXMOX-004",
	"TEST-PROXMOX-005",
	"TEST-PROXMOX-007",
	"TEST-PROXMOX-008",
	"TEST-PROXMOX-009",
	"TEST-PROXMOX-010",
	"TEST-PROXMOX-011",
	"TEST-PROXMOX-012",
	"TEST-PROXMOX-013",
	"TEST-PROXMOX-014",
	"TEST-PROXMOX-015",
	"TEST-PROXMOX-017",
	"TEST-PROXMOX-018",
	"TEST-PROXMOX-019",
	"TEST-PROXMOX-022",
}

var requiredFeatures = []string{
	"dependency-resource-input-v1",
	"durable_get_invocation",
	"excluded_claims_on_replan",
	"opaque-allocation-claims-v1",
	"stable_instance_identity_before_plan",
}

type Evidence struct {
	ContainerRuntimeTranscriptDigest string           `json:"containerRuntimeTranscriptDigest,omitempty"`
	SchemaVersion                    string           `json:"schemaVersion"`
	RunID                            string           `json:"runID"`
	IssuedAt                         string           `json:"issuedAt"`
	ValidUntil                       string           `json:"validUntil"`
	Target                           TargetEvidence   `json:"target"`
	Build                            BuildEvidence    `json:"build"`
	Protocol                         ProtocolEvidence `json:"protocol"`
	Profile                          ProfileEvidence  `json:"profile"`
	Cases                            []CaseEvidence   `json:"cases"`
	Probes                           []ProbeEvidence  `json:"probes"`
	Cleanup                          CleanupEvidence  `json:"cleanup"`
}

type TargetEvidence struct {
	TargetID            string `json:"targetID"`
	InstallationID      string `json:"installationID"`
	ConfigurationDigest string `json:"configurationDigest"`
	CapabilityDigest    string `json:"capabilityDigest"`
	InventoryDigest     string `json:"inventoryDigest"`
	PolicyDigest        string `json:"policyDigest"`
	PVERelease          string `json:"pveRelease"`
	PVEVersion          string `json:"pveVersion"`
}

type BuildEvidence struct {
	PluginID         string `json:"pluginID"`
	PluginVersion    string `json:"pluginVersion"`
	BinaryDigest     string `json:"binaryDigest"`
	SourceTreeDigest string `json:"sourceTreeDigest"`
}

type ProtocolEvidence struct {
	Major    uint32   `json:"major"`
	Minor    uint32   `json:"minor"`
	Features []string `json:"features"`
}

type RuntimeProfileEvidence struct {
	SchemaVersion string   `json:"schemaVersion"`
	Pool          string   `json:"pool"`
	TemplateAlias string   `json:"templateAlias"`
	Images        []string `json:"images"`
}

type ProfileEvidence struct {
	ContainerRuntime *RuntimeProfileEvidence `json:"containerRuntime,omitempty"`
	DestinationNode  string                  `json:"destinationNode"`
	VMIDRanges       []IntegerRange          `json:"vmidRanges"`
	Storages         []StorageEvidence       `json:"storages"`
	Networks         []NetworkEvidence       `json:"networks"`
	Templates        []TemplateEvidence      `json:"templates"`
	Permissions      []PermissionEvidence    `json:"permissions"`
}

type IntegerRange struct {
	Start uint32 `json:"start"`
	End   uint32 `json:"end"`
}

type StorageEvidence struct {
	Alias  string `json:"alias"`
	ID     string `json:"id"`
	Type   string `json:"type"`
	Format string `json:"format"`
}

type NetworkEvidence struct {
	Alias      string         `json:"alias"`
	Bridge     string         `json:"bridge"`
	VLANAware  bool           `json:"vlanAware"`
	VLANRanges []IntegerRange `json:"vlanRanges"`
}

type TemplateEvidence struct {
	Alias         string `json:"alias"`
	SourceNode    string `json:"sourceNode"`
	VMID          uint32 `json:"vmid"`
	ConfigDigest  string `json:"configDigest"`
	ContentDigest string `json:"contentDigest"`
}

type PermissionEvidence struct {
	Path       string   `json:"path"`
	Privileges []string `json:"privileges"`
}

type CaseEvidence struct {
	ID             string `json:"id"`
	Outcome        string `json:"outcome"`
	StartedAt      string `json:"startedAt"`
	CompletedAt    string `json:"completedAt"`
	TranscriptHash string `json:"transcriptDigest"`
}

type ProbeEvidence struct {
	Kind                string `json:"kind"`
	SourceInstanceID    string `json:"sourceInstanceID"`
	SourceVMID          uint32 `json:"sourceVMID"`
	DestinationInstance string `json:"destinationInstanceID"`
	EndpointDigest      string `json:"endpointDigest"`
	Protocol            string `json:"protocol"`
	Attempts            uint32 `json:"attempts"`
	Successes           uint32 `json:"successes"`
	StartedAt           string `json:"startedAt"`
	CompletedAt         string `json:"completedAt"`
	AgentImageDigest    string `json:"agentImageDigest"`
	TranscriptDigest    string `json:"transcriptDigest"`
}

type CleanupEvidence struct {
	ProviderAbsenceConfirmed bool               `json:"providerAbsenceConfirmed"`
	CheckedAt                string             `json:"checkedAt"`
	TranscriptDigest         string             `json:"transcriptDigest"`
	Claims                   []ClaimEvidence    `json:"claims"`
	Leftovers                []LeftoverEvidence `json:"leftovers"`
}

type ClaimEvidence struct {
	Namespace string `json:"namespace"`
	Value     string `json:"value"`
	State     string `json:"state"`
}

type LeftoverEvidence struct {
	Kind       string `json:"kind"`
	ProviderID string `json:"providerID"`
}

type Signature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"keyID"`
	Value     string `json:"value"`
}

type SignedEvidence struct {
	SchemaVersion string    `json:"schemaVersion"`
	Evidence      Evidence  `json:"evidence"`
	Signature     Signature `json:"signature"`
}

type VerificationPolicy struct {
	Now                          time.Time
	TrustedKeys                  map[string]ed25519.PublicKey
	ExpectedTargetID             string
	ExpectedInstallationID       string
	ExpectedConfigurationDigest  string
	ExpectedCapabilityDigest     string
	ExpectedInventoryDigest      string
	ExpectedPolicyDigest         string
	ExpectedPVERelease           string
	ExpectedPVEVersion           string
	ExpectedProviderBinaryDigest string
	ExpectedSourceTreeDigest     string
}

type Verification struct {
	EvidenceDigest string   `json:"evidenceDigest"`
	KeyID          string   `json:"keyID"`
	Qualifies      bool     `json:"qualifies"`
	Reasons        []string `json:"reasons"`
}

func RequiredT3Cases() []string { return append([]string(nil), requiredT3Cases...) }

func RequiredProtocolFeatures() []string { return append([]string(nil), requiredFeatures...) }

// DecodeEvidence performs duplicate-key rejection before closed typed decoding.
func DecodeEvidence(input []byte) (Evidence, error) {
	var result Evidence
	if err := decodeClosed(input, &result); err != nil {
		return Evidence{}, err
	}
	if _, _, err := validateEvidence(result); err != nil {
		return Evidence{}, err
	}
	return result, nil
}

// Sign returns a canonical JSON envelope. It signs valid failed runs as well as
// successful runs so failure and retained-claim evidence remains auditable.
func Sign(evidence Evidence, keyID string, privateKey ed25519.PrivateKey) ([]byte, error) {
	if !stableIDPattern.MatchString(keyID) {
		return nil, errors.New("qualification signing key ID is invalid")
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("qualification signing key has invalid length")
	}
	if _, _, err := validateEvidence(evidence); err != nil {
		return nil, err
	}
	canonical, err := canonicalEvidence(evidence)
	if err != nil {
		return nil, err
	}
	signature := ed25519.Sign(privateKey, signatureInput(canonical))
	document := SignedEvidence{
		SchemaVersion: EnvelopeSchema,
		Evidence:      evidence,
		Signature: Signature{
			Algorithm: SignatureMethod,
			KeyID:     keyID,
			Value:     base64.RawURLEncoding.EncodeToString(signature),
		},
	}
	return canonicalJSON(document)
}

func ParseSigned(input []byte) (SignedEvidence, error) {
	if len(input) == 0 || len(input) > MaxDocumentBytes {
		return SignedEvidence{}, errors.New("qualification document size is invalid")
	}
	var result SignedEvidence
	if err := decodeClosed(input, &result); err != nil {
		return SignedEvidence{}, err
	}
	if result.SchemaVersion != EnvelopeSchema {
		return SignedEvidence{}, errors.New("qualification envelope schema is unsupported")
	}
	if result.Signature.Algorithm != SignatureMethod || !stableIDPattern.MatchString(result.Signature.KeyID) {
		return SignedEvidence{}, errors.New("qualification signature metadata is invalid")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(result.Signature.Value)
	if err != nil || len(decoded) != ed25519.SignatureSize {
		return SignedEvidence{}, errors.New("qualification signature encoding is invalid")
	}
	if _, _, err := validateEvidence(result.Evidence); err != nil {
		return SignedEvidence{}, err
	}
	return result, nil
}

// Verify authenticates the artifact and evaluates it against exact deployment
// expectations. A valid signature does not imply Qualifies: failed cases,
// isolation probes, expiry, leftovers, or retained claims all fail closed.
func Verify(input []byte, policy VerificationPolicy) (Verification, error) {
	if err := validatePolicy(policy); err != nil {
		return Verification{}, err
	}
	document, err := ParseSigned(input)
	if err != nil {
		return Verification{}, err
	}
	publicKey, trusted := policy.TrustedKeys[document.Signature.KeyID]
	if !trusted || len(publicKey) != ed25519.PublicKeySize {
		return Verification{}, errors.New("qualification signing key is not trusted")
	}
	canonical, err := canonicalEvidence(document.Evidence)
	if err != nil {
		return Verification{}, err
	}
	signature, _ := base64.RawURLEncoding.DecodeString(document.Signature.Value)
	if !ed25519.Verify(publicKey, signatureInput(canonical), signature) {
		return Verification{}, errors.New("qualification signature verification failed")
	}
	digest := sha256.Sum256(canonical)
	_, reasons, err := validateEvidence(document.Evidence)
	if err != nil {
		return Verification{}, err
	}
	if document.Evidence.Target.TargetID != policy.ExpectedTargetID {
		reasons = append(reasons, "target_id_mismatch")
	}
	if document.Evidence.Target.InstallationID != policy.ExpectedInstallationID {
		reasons = append(reasons, "installation_id_mismatch")
	}
	if document.Evidence.Target.ConfigurationDigest != policy.ExpectedConfigurationDigest {
		reasons = append(reasons, "configuration_digest_mismatch")
	}
	if document.Evidence.Target.CapabilityDigest != policy.ExpectedCapabilityDigest {
		reasons = append(reasons, "capability_digest_mismatch")
	}
	if document.Evidence.Target.InventoryDigest != policy.ExpectedInventoryDigest {
		reasons = append(reasons, "inventory_digest_mismatch")
	}
	if document.Evidence.Target.PolicyDigest != policy.ExpectedPolicyDigest {
		reasons = append(reasons, "policy_digest_mismatch")
	}
	if document.Evidence.Target.PVERelease != policy.ExpectedPVERelease || document.Evidence.Target.PVEVersion != policy.ExpectedPVEVersion {
		reasons = append(reasons, "pve_version_mismatch")
	}
	if document.Evidence.Build.BinaryDigest != policy.ExpectedProviderBinaryDigest {
		reasons = append(reasons, "provider_binary_digest_mismatch")
	}
	if document.Evidence.Build.SourceTreeDigest != policy.ExpectedSourceTreeDigest {
		reasons = append(reasons, "source_tree_digest_mismatch")
	}
	now := policy.Now.UTC()
	issuedAt, _ := parseTimestamp(document.Evidence.IssuedAt)
	validUntil, _ := parseTimestamp(document.Evidence.ValidUntil)
	if now.Before(issuedAt.Add(-5*time.Minute)) || now.After(validUntil) {
		reasons = append(reasons, "evidence_not_current")
	}
	sort.Strings(reasons)
	return Verification{
		EvidenceDigest: "sha256:" + hex.EncodeToString(digest[:]),
		KeyID:          document.Signature.KeyID,
		Qualifies:      len(reasons) == 0,
		Reasons:        reasons,
	}, nil
}

func validatePolicy(policy VerificationPolicy) error {
	if policy.Now.IsZero() || len(policy.TrustedKeys) == 0 || !stableIDPattern.MatchString(policy.ExpectedTargetID) || !stableIDPattern.MatchString(policy.ExpectedInstallationID) || !digestPattern.MatchString(policy.ExpectedConfigurationDigest) || !digestPattern.MatchString(policy.ExpectedCapabilityDigest) || !digestPattern.MatchString(policy.ExpectedInventoryDigest) || !digestPattern.MatchString(policy.ExpectedPolicyDigest) || !pveReleasePattern.MatchString(policy.ExpectedPVERelease) || !pveVersionPattern.MatchString(policy.ExpectedPVEVersion) || !digestPattern.MatchString(policy.ExpectedProviderBinaryDigest) || !digestPattern.MatchString(policy.ExpectedSourceTreeDigest) {
		return errors.New("qualification verification policy is incomplete")
	}
	if !strings.Contains(policy.ExpectedPVEVersion, "/"+policy.ExpectedPVERelease+"/") {
		return errors.New("qualification verification policy PVE version does not match release")
	}
	for keyID, publicKey := range policy.TrustedKeys {
		if !stableIDPattern.MatchString(keyID) || len(publicKey) != ed25519.PublicKeySize {
			return errors.New("qualification verification policy contains an invalid key")
		}
	}
	return nil
}

func validateEvidence(evidence Evidence) (bool, []string, error) {
	if (evidence.SchemaVersion != EvidenceSchema && evidence.SchemaVersion != RuntimeEvidenceSchema) || !stableIDPattern.MatchString(evidence.RunID) {
		return false, nil, errors.New("qualification evidence identity is invalid")
	}
	if evidence.Profile.ContainerRuntime != nil {
		if evidence.SchemaVersion != RuntimeEvidenceSchema || !digestPattern.MatchString(evidence.ContainerRuntimeTranscriptDigest) {
			return false, nil, errors.New("runtime qualification transcript is required")
		}
	} else if evidence.SchemaVersion == RuntimeEvidenceSchema || evidence.ContainerRuntimeTranscriptDigest != "" {
		return false, nil, errors.New("unexpected runtime qualification")
	}
	issuedAt, err := parseTimestamp(evidence.IssuedAt)
	if err != nil {
		return false, nil, fmt.Errorf("qualification issuedAt: %w", err)
	}
	validUntil, err := parseTimestamp(evidence.ValidUntil)
	if err != nil || !validUntil.After(issuedAt) || validUntil.Sub(issuedAt) > MaxValidity {
		return false, nil, errors.New("qualification validity window is invalid")
	}
	if err := validateTarget(evidence.Target); err != nil {
		return false, nil, err
	}
	if err := validateBuild(evidence.Build); err != nil {
		return false, nil, err
	}
	if err := validateProtocol(evidence.Protocol); err != nil {
		return false, nil, err
	}
	if err := validateProfile(evidence.Profile); err != nil {
		return false, nil, err
	}
	reasons := make([]string, 0)
	caseReasons, err := validateCases(evidence.Cases, issuedAt)
	if err != nil {
		return false, nil, err
	}
	reasons = append(reasons, caseReasons...)
	probeReasons, err := validateProbes(evidence.Probes, issuedAt)
	if err != nil {
		return false, nil, err
	}
	reasons = append(reasons, probeReasons...)
	cleanupReasons, err := validateCleanup(evidence.Cleanup, issuedAt)
	if err != nil {
		return false, nil, err
	}
	reasons = append(reasons, cleanupReasons...)
	sort.Strings(reasons)
	return len(reasons) == 0, reasons, nil
}

func validateTarget(target TargetEvidence) error {
	if !stableIDPattern.MatchString(target.TargetID) || !stableIDPattern.MatchString(target.InstallationID) || !digestPattern.MatchString(target.ConfigurationDigest) || !digestPattern.MatchString(target.CapabilityDigest) || !digestPattern.MatchString(target.InventoryDigest) || !digestPattern.MatchString(target.PolicyDigest) || !pveReleasePattern.MatchString(target.PVERelease) || !pveVersionPattern.MatchString(target.PVEVersion) {
		return errors.New("qualification target evidence is invalid")
	}
	if !strings.Contains(target.PVEVersion, "/"+target.PVERelease+"/") {
		return errors.New("qualification PVE version does not match release")
	}
	return nil
}

func validateBuild(build BuildEvidence) error {
	if build.PluginID != ProviderPlugin || !stableIDPattern.MatchString(build.PluginVersion) || !digestPattern.MatchString(build.BinaryDigest) || !digestPattern.MatchString(build.SourceTreeDigest) {
		return errors.New("qualification provider build evidence is invalid")
	}
	return nil
}

func validateProtocol(protocol ProtocolEvidence) error {
	if protocol.Major != 1 || protocol.Minor < 1 || protocol.Minor > 65535 || len(protocol.Features) == 0 || len(protocol.Features) > 32 {
		return errors.New("qualification protocol evidence is invalid")
	}
	seen := make(map[string]bool, len(protocol.Features))
	for index, feature := range protocol.Features {
		if !stableIDPattern.MatchString(feature) || seen[feature] || index > 0 && protocol.Features[index-1] >= feature {
			return errors.New("qualification protocol features must be unique and sorted")
		}
		seen[feature] = true
	}
	for _, required := range requiredFeatures {
		if !seen[required] {
			return fmt.Errorf("qualification protocol feature %q is missing", required)
		}
	}
	return nil
}

func validateProfile(profile ProfileEvidence) error {
	if !objectPattern.MatchString(profile.DestinationNode) || len(profile.VMIDRanges) == 0 || len(profile.VMIDRanges) > 16 || len(profile.Storages) == 0 || len(profile.Storages) > 16 || len(profile.Networks) == 0 || len(profile.Networks) > 16 || len(profile.Templates) == 0 || len(profile.Templates) > 64 || len(profile.Permissions) == 0 || len(profile.Permissions) > 32 {
		return errors.New("qualification target profile is incomplete")
	}
	if err := validateRanges(profile.VMIDRanges, 100, 999999999); err != nil {
		return fmt.Errorf("qualification VMID ranges: %w", err)
	}
	aliases := make(map[string]bool)
	storageIDs := make(map[string]bool)
	for _, storage := range profile.Storages {
		if !aliasPattern.MatchString(storage.Alias) || !objectPattern.MatchString(storage.ID) || storageIDs[storage.ID] || aliases[storage.Alias] || !((storage.Type == "dir" && storage.Format == "qcow2") || (storage.Type == "lvmthin" && storage.Format == "raw")) {
			return errors.New("qualification storage profile is invalid")
		}
		aliases[storage.Alias], storageIDs[storage.ID] = true, true
	}
	bridges := make(map[string]bool)
	for _, network := range profile.Networks {
		if !aliasPattern.MatchString(network.Alias) || aliases[network.Alias] || !objectPattern.MatchString(network.Bridge) || bridges[network.Bridge] || !network.VLANAware {
			return errors.New("qualification network profile is invalid")
		}
		if err := validateRanges(network.VLANRanges, 1, 4094); err != nil {
			return fmt.Errorf("qualification VLAN ranges: %w", err)
		}
		aliases[network.Alias], bridges[network.Bridge] = true, true
	}
	templateIDs := make(map[string]bool)
	for _, template := range profile.Templates {
		identity := fmt.Sprintf("%s/%d", template.SourceNode, template.VMID)
		if !aliasPattern.MatchString(template.Alias) || aliases[template.Alias] || template.SourceNode != profile.DestinationNode || template.VMID < 100 || templateIDs[identity] || !sha1Pattern.MatchString(template.ConfigDigest) || !digestPattern.MatchString(template.ContentDigest) {
			return errors.New("qualification template profile is invalid")
		}
		aliases[template.Alias], templateIDs[identity] = true, true
	}
	if runtime := profile.ContainerRuntime; runtime != nil {
		if runtime.SchemaVersion != containervm.Version || !objectPattern.MatchString(runtime.Pool) || len(runtime.Images) == 0 || len(runtime.Images) > 64 {
			return errors.New("runtime qualification profile invalid")
		}
		found := false
		for _, t := range profile.Templates {
			if t.Alias == runtime.TemplateAlias {
				found = true
			}
		}
		if !found {
			return errors.New("runtime template not qualified")
		}
		seen := map[string]bool{}
		for _, image := range runtime.Images {
			if !containervm.ValidImage(image) || seen[image] {
				return errors.New("runtime image invalid")
			}
			seen[image] = true
		}
	}
	paths := make(map[string]bool)
	for _, permission := range profile.Permissions {
		if !validPermissionPath(permission.Path) || paths[permission.Path] || len(permission.Privileges) == 0 || len(permission.Privileges) > 32 {
			return errors.New("qualification permission profile is invalid")
		}
		paths[permission.Path] = true
		seen := make(map[string]bool)
		for index, privilege := range permission.Privileges {
			if !privilegePattern.MatchString(privilege) || (privilege == "VM.GuestAgent.Unrestricted" && (profile.ContainerRuntime == nil || permission.Path != "/pool/"+profile.ContainerRuntime.Pool)) || privilege == "Sys.Modify" || seen[privilege] || index > 0 && permission.Privileges[index-1] >= privilege {
				return errors.New("qualification privileges must be least-privilege, unique, and sorted")
			}
			seen[privilege] = true
		}
	}
	return nil
}

func validateCases(cases []CaseEvidence, issuedAt time.Time) ([]string, error) {
	if len(cases) != len(requiredT3Cases) {
		return nil, errors.New("qualification case matrix is incomplete")
	}
	required := make(map[string]bool, len(requiredT3Cases))
	for _, id := range requiredT3Cases {
		required[id] = true
	}
	seen := make(map[string]bool, len(cases))
	reasons := make([]string, 0)
	for index, result := range cases {
		if result.ID != requiredT3Cases[index] || !required[result.ID] || seen[result.ID] || (result.Outcome != "passed" && result.Outcome != "failed" && result.Outcome != "not_applicable") || !digestPattern.MatchString(result.TranscriptHash) {
			return nil, errors.New("qualification case result is invalid")
		}
		if err := validateInterval(result.StartedAt, result.CompletedAt, issuedAt); err != nil {
			return nil, fmt.Errorf("qualification case %s: %w", result.ID, err)
		}
		seen[result.ID] = true
		if result.Outcome != "passed" {
			reasons = append(reasons, "case_not_passed:"+result.ID)
		}
	}
	return reasons, nil
}

func validateProbes(probes []ProbeEvidence, issuedAt time.Time) ([]string, error) {
	if len(probes) != 3 {
		return nil, errors.New("qualification requires exactly three isolation probes")
	}
	requiredOrder := []string{"intended_segment", "management", "cross_instance"}
	required := map[string]bool{"intended_segment": true, "management": true, "cross_instance": true}
	seen := make(map[string]bool, 3)
	reasons := make([]string, 0)
	for index, probe := range probes {
		if probe.Kind != requiredOrder[index] || !required[probe.Kind] || seen[probe.Kind] || !stableIDPattern.MatchString(probe.SourceInstanceID) || probe.SourceVMID < 100 || !digestPattern.MatchString(probe.EndpointDigest) || (probe.Protocol != "icmp" && probe.Protocol != "tcp") || probe.Attempts < 3 || probe.Attempts > 100 || probe.Successes > probe.Attempts || !digestPattern.MatchString(probe.AgentImageDigest) || !digestPattern.MatchString(probe.TranscriptDigest) {
			return nil, errors.New("qualification isolation probe is invalid")
		}
		if err := validateInterval(probe.StartedAt, probe.CompletedAt, issuedAt); err != nil {
			return nil, fmt.Errorf("qualification probe %s: %w", probe.Kind, err)
		}
		if probe.Kind == "cross_instance" {
			if !stableIDPattern.MatchString(probe.DestinationInstance) || probe.DestinationInstance == probe.SourceInstanceID {
				return nil, errors.New("qualification cross-instance probe destination is invalid")
			}
		} else if probe.DestinationInstance != "" {
			return nil, errors.New("qualification probe has an unexpected destination instance")
		}
		seen[probe.Kind] = true
		passed := probe.Kind == "intended_segment" && probe.Successes == probe.Attempts || probe.Kind != "intended_segment" && probe.Successes == 0
		if !passed {
			reasons = append(reasons, "isolation_probe_failed:"+probe.Kind)
		}
	}
	return reasons, nil
}

func validateCleanup(cleanup CleanupEvidence, issuedAt time.Time) ([]string, error) {
	checkedAt, err := parseTimestamp(cleanup.CheckedAt)
	if err != nil || checkedAt.After(issuedAt) || !digestPattern.MatchString(cleanup.TranscriptDigest) || len(cleanup.Claims) == 0 || len(cleanup.Claims) > 64 || len(cleanup.Leftovers) > 64 {
		return nil, errors.New("qualification cleanup evidence is invalid")
	}
	reasons := make([]string, 0)
	if !cleanup.ProviderAbsenceConfirmed {
		reasons = append(reasons, "cleanup_absence_unconfirmed")
	}
	seenClaims := make(map[string]bool)
	seenNamespaces := make(map[string]bool)
	for _, claim := range cleanup.Claims {
		key := claim.Namespace + "\x00" + claim.Value
		if (claim.Namespace != "proxmox.qemu.vmid" && claim.Namespace != "proxmox.network.vlan") || !claimValuePattern.MatchString(claim.Value) || seenClaims[key] || (claim.State != "released" && claim.State != "retained" && claim.State != "orphaned") {
			return nil, errors.New("qualification cleanup claim is invalid")
		}
		seenClaims[key] = true
		seenNamespaces[claim.Namespace] = true
		if claim.State != "released" {
			reasons = append(reasons, "cleanup_claim_not_released:"+claim.Namespace+":"+claim.Value)
		}
	}
	if !seenNamespaces["proxmox.qemu.vmid"] || !seenNamespaces["proxmox.network.vlan"] {
		return nil, errors.New("qualification cleanup must account for VMID and VLAN claims")
	}
	seenLeftovers := make(map[string]bool)
	for _, leftover := range cleanup.Leftovers {
		key := leftover.Kind + "\x00" + leftover.ProviderID
		if !stableIDPattern.MatchString(leftover.Kind) || !claimValuePattern.MatchString(leftover.ProviderID) || seenLeftovers[key] {
			return nil, errors.New("qualification cleanup leftover is invalid")
		}
		seenLeftovers[key] = true
	}
	if len(cleanup.Leftovers) > 0 {
		reasons = append(reasons, "cleanup_leftovers_present")
	}
	return reasons, nil
}

func validateRanges(ranges []IntegerRange, minimum, maximum uint32) error {
	previousEnd := uint32(0)
	for index, value := range ranges {
		if value.Start < minimum || value.End > maximum || value.Start > value.End || index > 0 && value.Start <= previousEnd {
			return errors.New("ranges must be sorted, disjoint, and in bounds")
		}
		previousEnd = value.End
	}
	return nil
}

func validPermissionPath(value string) bool {
	return permissionPathPattern.MatchString(value)
}

func validateInterval(start, complete string, issuedAt time.Time) error {
	startedAt, err := parseTimestamp(start)
	if err != nil {
		return errors.New("startedAt is invalid")
	}
	completedAt, err := parseTimestamp(complete)
	if err != nil || completedAt.Before(startedAt) || completedAt.After(issuedAt) {
		return errors.New("completedAt is invalid")
	}
	return nil
}

func parseTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.Format(time.RFC3339) != value || parsed.Location() != time.UTC {
		return time.Time{}, errors.New("timestamp must be canonical UTC RFC3339")
	}
	return parsed, nil
}

func canonicalEvidence(evidence Evidence) ([]byte, error) { return canonicalJSON(evidence) }

func canonicalJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode qualification JSON: %w", err)
	}
	decoded, err := render.DecodeJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("decode qualification JSON: %w", err)
	}
	canonical, err := render.Canonicalize(decoded)
	if err != nil {
		return nil, fmt.Errorf("canonicalize qualification JSON: %w", err)
	}
	return canonical, nil
}

func decodeClosed(input []byte, destination any) error {
	if len(input) == 0 || len(input) > MaxDocumentBytes {
		return errors.New("qualification JSON size is invalid")
	}
	decoded, err := render.DecodeJSON(input)
	if err != nil {
		return fmt.Errorf("qualification JSON is invalid: %w", err)
	}
	canonical, err := render.Canonicalize(decoded)
	if err != nil {
		return fmt.Errorf("qualification JSON is not canonicalizable: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("qualification JSON does not match the closed schema: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("qualification JSON contains multiple values")
	}
	return nil
}

func signatureInput(canonical []byte) []byte {
	result := make([]byte, 0, len(EvidenceSchema)+1+len(canonical))
	result = append(result, EvidenceSchema...)
	result = append(result, 0)
	result = append(result, canonical...)
	return result
}
