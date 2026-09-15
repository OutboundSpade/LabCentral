package proxmox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	qualificationprobe "github.com/OutboundSpade/LabCentral/tools/proxmox-qualification/probe"
	"github.com/OutboundSpade/LabCentral/tools/proxmox-qualification/qualification"
	"google.golang.org/protobuf/proto"
)

const t3IsolationProfileSchema = "labcentral.proxmox-t3-isolation-profile/v1"

type t3IsolationActivation struct {
	profilePath     string
	configPath      string
	credentialPath  string
	agentCAPath     string
	runnerCertPath  string
	runnerKeyPath   string
	confirmedTarget string
}

type t3IsolationProfile struct {
	SchemaVersion           string                  `json:"schemaVersion"`
	RunID                   string                  `json:"runID"`
	TargetID                string                  `json:"targetID"`
	SourceInstanceID        string                  `json:"sourceInstanceID"`
	CrossInstanceID         string                  `json:"crossInstanceID"`
	SourceNetworkResourceID string                  `json:"sourceNetworkResourceID"`
	CrossNetworkResourceID  string                  `json:"crossNetworkResourceID"`
	SourceVMResourceID      string                  `json:"sourceVMResourceID"`
	PeerVMResourceID        string                  `json:"peerVMResourceID"`
	CrossVMResourceID       string                  `json:"crossVMResourceID"`
	SourceNetworkCIDR       string                  `json:"sourceNetworkCIDR"`
	CrossNetworkCIDR        string                  `json:"crossNetworkCIDR"`
	SourceGuestIPv4         string                  `json:"sourceGuestIPv4"`
	PeerGuestIPv4           string                  `json:"peerGuestIPv4"`
	CrossGuestIPv4          string                  `json:"crossGuestIPv4"`
	ImageRepository         string                  `json:"imageRepository"`
	ImageDigest             string                  `json:"imageDigest"`
	TemplateAlias           string                  `json:"templateAlias"`
	StorageAlias            string                  `json:"storageAlias"`
	NetworkAlias            string                  `json:"networkAlias"`
	ExpectedVMIDs           []uint32                `json:"expectedVMIDs"`
	ExpectedVLANs           []uint32                `json:"expectedVLANs"`
	CPUMillis               uint32                  `json:"cpuMillis"`
	MemoryBytes             uint64                  `json:"memoryBytes"`
	Probe                   t3IsolationProbeProfile `json:"probe"`
}

type t3IsolationProbeProfile struct {
	AgentOrigin       string `json:"agentOrigin"`
	IntendedPort      uint16 `json:"intendedPort"`
	ManagementAddress string `json:"managementAddress"`
	CrossInstancePort uint16 `json:"crossInstancePort"`
	Attempts          uint32 `json:"attempts"`
	TimeoutMillis     uint32 `json:"timeoutMillis"`
}

type t3IsolationDocuments struct {
	labJSON      []byte
	resources    map[string][]byte
	overrideJSON []byte
}

type t3AppliedResource struct {
	instanceID   string
	resourceID   string
	kind         string
	expectedVMID uint32
	input        *pluginv1.ResourceMutationInput
	state        *pluginv1.ProviderState
	plan         []byte
	planDigest   string
	claims       []*pluginv1.AllocationClaim
	destroyed    bool
}

func TestContractTESTPROXMOX027CredentialedIsolationRunnerBindsExactTopologyAndProbeInputs(t *testing.T) {
	empty, err := readT3IsolationActivation(func(string) string { return "" })
	if err != nil || empty != (t3IsolationActivation{}) {
		t.Fatalf("empty isolation activation = %#v, %v", empty, err)
	}
	complete := map[string]string{
		"LABCENTRAL_PROXMOX_T3_ISOLATION_EXECUTE":         t3ExecuteSentinel,
		"LABCENTRAL_PROXMOX_T3_ISOLATION_PROFILE":         "/qualification/isolation.json",
		"LABCENTRAL_PROXMOX_T3_ISOLATION_TARGET_CONFIG":   "/qualification/target.json",
		"LABCENTRAL_PROXMOX_T3_ISOLATION_CREDENTIAL_FILE": "/qualification/credential.json",
		"LABCENTRAL_PROXMOX_T3_ISOLATION_AGENT_CA":        "/qualification/agent-ca.crt",
		"LABCENTRAL_PROXMOX_T3_ISOLATION_RUNNER_CERT":     "/qualification/runner.crt",
		"LABCENTRAL_PROXMOX_T3_ISOLATION_RUNNER_KEY":      "/qualification/runner.key",
		"LABCENTRAL_PROXMOX_T3_ISOLATION_CONFIRM_TARGET":  "target.proxmox-t3",
	}
	activation, err := readT3IsolationActivation(func(name string) string { return complete[name] })
	if err != nil || activation.confirmedTarget != "target.proxmox-t3" {
		t.Fatalf("complete isolation activation = %#v, %v", activation, err)
	}
	delete(complete, "LABCENTRAL_PROXMOX_T3_ISOLATION_AGENT_CA")
	if _, err := readT3IsolationActivation(func(name string) string { return complete[name] }); err == nil {
		t.Fatal("partial isolation activation was accepted")
	}
	complete["LABCENTRAL_PROXMOX_T3_ISOLATION_AGENT_CA"] = complete["LABCENTRAL_PROXMOX_T3_ISOLATION_RUNNER_CERT"]
	if _, err := readT3IsolationActivation(func(name string) string { return complete[name] }); err == nil {
		t.Fatal("reused isolation identity path was accepted")
	}

	t.Run("profile plans exactly two VLANs and three VMIDs", func(t *testing.T) {
		target := mutationTestTarget(t)
		var config TargetConfig
		if err := decodeEnvelope(target.GetConfigEnvelope(), maximumConfigBytes, &config); err != nil {
			t.Fatal(err)
		}
		profile := t3IsolationTestProfile(target.GetTargetId())
		if err := validateT3IsolationProfile(profile, config, target.GetTargetId()); err != nil {
			t.Fatal(err)
		}
		for name, mutate := range map[string]func(*t3IsolationProfile){
			"overlapping networks": func(candidate *t3IsolationProfile) { candidate.CrossNetworkCIDR = candidate.SourceNetworkCIDR },
			"duplicate VMIDs":      func(candidate *t3IsolationProfile) { candidate.ExpectedVMIDs[1] = candidate.ExpectedVMIDs[0] },
			"agent outside source": func(candidate *t3IsolationProfile) { candidate.Probe.AgentOrigin = "https://10.20.0.12:9443" },
			"management in lab":    func(candidate *t3IsolationProfile) { candidate.Probe.ManagementAddress = "10.20.0.1:8006" },
			"missing peer service": func(candidate *t3IsolationProfile) { candidate.Probe.IntendedPort = 0 },
		} {
			t.Run("rejects "+name, func(t *testing.T) {
				candidate := t3IsolationTestProfile(target.GetTargetId())
				mutate(&candidate)
				if err := validateT3IsolationProfile(candidate, config, target.GetTargetId()); err == nil {
					t.Fatalf("unsafe isolation profile accepted: %s", name)
				}
			})
		}
		sourceDocuments, crossDocuments, err := makeT3IsolationDocuments(profile)
		if err != nil {
			t.Fatal(err)
		}
		service := NewService()
		sourceValidation := validateT3IsolationLab(t, service, target, sourceDocuments)
		crossValidation := validateT3IsolationLab(t, service, target, crossDocuments)

		sourceNetwork, _ := planT3IsolationResource(t, service, target, profile, sourceValidation, sourceDocuments.resources[profile.SourceNetworkResourceID], profile.SourceInstanceID, profile.SourceNetworkResourceID, nil, nil, vlanClaimNamespace, profile.ExpectedVLANs[0])
		crossNetwork, _ := planT3IsolationResource(t, service, target, profile, crossValidation, crossDocuments.resources[profile.CrossNetworkResourceID], profile.CrossInstanceID, profile.CrossNetworkResourceID, cloneClaims(sourceNetwork.GetProposedAllocations()), nil, vlanClaimNamespace, profile.ExpectedVLANs[1])
		sourceDependency := t3IsolationDependency(profile.SourceNetworkResourceID, sourceNetwork, nil)
		sourceVM, _ := planT3IsolationResource(t, service, target, profile, sourceValidation, sourceDocuments.resources[profile.SourceVMResourceID], profile.SourceInstanceID, profile.SourceVMResourceID, nil, []*pluginv1.ResourceDependency{sourceDependency}, vmidClaimNamespace, profile.ExpectedVMIDs[0])
		peerVM, _ := planT3IsolationResource(t, service, target, profile, sourceValidation, sourceDocuments.resources[profile.PeerVMResourceID], profile.SourceInstanceID, profile.PeerVMResourceID, cloneClaims(sourceVM.GetProposedAllocations()), []*pluginv1.ResourceDependency{sourceDependency}, vmidClaimNamespace, profile.ExpectedVMIDs[1])
		crossDependency := t3IsolationDependency(profile.CrossNetworkResourceID, crossNetwork, nil)
		crossVMExclusions := append(cloneClaims(sourceVM.GetProposedAllocations()), cloneClaims(peerVM.GetProposedAllocations())...)
		planT3IsolationResource(t, service, target, profile, crossValidation, crossDocuments.resources[profile.CrossVMResourceID], profile.CrossInstanceID, profile.CrossVMResourceID, crossVMExclusions, []*pluginv1.ResourceDependency{crossDependency}, vmidClaimNamespace, profile.ExpectedVMIDs[2])

		profile.CrossInstanceID = profile.SourceInstanceID
		if err := validateT3IsolationProfile(profile, config, target.GetTargetId()); err == nil {
			t.Fatal("same-instance cross target was accepted")
		}
	})
}

// TestCredentialedProxmoxT3Isolation creates an explicitly reserved source
// network with source/peer guests and a distinct cross-instance network/guest,
// runs the preinstalled bounded probe from the source guest, and destroys every
// owned resource in reverse order. It is test-only and does not enable the
// production provider or constitute the complete T3 failure/recovery matrix.
func TestCredentialedProxmoxT3Isolation(t *testing.T) {
	activation, err := readT3IsolationActivation(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if activation == (t3IsolationActivation{}) {
		t.Skip("credentialed Proxmox T3 isolation run is not explicitly enabled")
	}

	profileBytes, err := readT3File(activation.profilePath, 1<<20, false)
	if err != nil {
		t.Fatalf("read T3 isolation profile: %v", err)
	}
	var profile t3IsolationProfile
	if err := decodeEnvelope(profileBytes, 1<<20, &profile); err != nil {
		t.Fatalf("decode T3 isolation profile: %v", err)
	}
	configBytes, err := readT3File(activation.configPath, maximumConfigBytes, false)
	if err != nil {
		t.Fatalf("read target config: %v", err)
	}
	credentialBytes, err := readT3File(activation.credentialPath, maximumCredentialBytes, true)
	if err != nil {
		t.Fatalf("read target credential: %v", err)
	}
	defer zeroBytesForT3(credentialBytes)
	agentCAPEM, err := readT3File(activation.agentCAPath, 64<<10, false)
	if err != nil {
		t.Fatalf("read probe agent CA: %v", err)
	}
	runnerCertPEM, err := readT3File(activation.runnerCertPath, 64<<10, false)
	if err != nil {
		t.Fatalf("read probe runner certificate: %v", err)
	}
	runnerKeyPEM, err := readT3File(activation.runnerKeyPath, 64<<10, true)
	if err != nil {
		t.Fatalf("read probe runner key: %v", err)
	}
	defer zeroBytesForT3(runnerKeyPEM)

	var config TargetConfig
	if err := decodeEnvelope(configBytes, maximumConfigBytes, &config); err != nil {
		t.Fatalf("decode target config: %v", err)
	}
	if err := validateT3IsolationProfile(profile, config, activation.confirmedTarget); err != nil {
		t.Fatal(err)
	}
	probeClient, err := qualificationprobe.NewClient(profile.Probe.AgentOrigin, agentCAPEM, runnerCertPEM, runnerKeyPEM, profile.ImageDigest)
	if err != nil {
		t.Fatalf("configure probe client: %v", err)
	}
	defer probeClient.CloseIdleConnections()

	fingerprint := sha256.Sum256(configBytes)
	target := &pluginv1.TargetContext{
		TargetId: profile.TargetID, ConfigEnvelope: configBytes, CredentialEnvelope: credentialBytes,
		ConfigFingerprint: "sha256:" + hex.EncodeToString(fingerprint[:]),
	}
	service := newT0MutationService(ClientFactoryFunc(func(configEnvelope, credentialEnvelope []byte) (DiscoveryClient, error) {
		return NewClientFromEnvelopes(configEnvelope, credentialEnvelope)
	}))
	service.sleep = time.Sleep
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	discovery, err := service.DiscoverTarget(ctx, &pluginv1.DiscoverTargetRequest{Target: target})
	if err != nil {
		t.Fatalf("target discovery failed: %v", err)
	}
	if capabilitySupport(discovery, "workload.vm") != pluginv1.Capability_SUPPORT_NATIVE {
		t.Fatal("exact target failed the package-private mutation preflight gate")
	}

	sourceDocuments, crossDocuments, err := makeT3IsolationDocuments(profile)
	if err != nil {
		t.Fatal(err)
	}
	sourceValidation := validateT3IsolationLab(t, service, target, sourceDocuments)
	crossValidation := validateT3IsolationLab(t, service, target, crossDocuments)
	applied := make([]*t3AppliedResource, 0, 5)
	defer func() {
		for _, cleanupErr := range cleanupT3IsolationResources(service, profile, target, config, configBytes, credentialBytes, applied) {
			t.Errorf("T3 isolation cleanup: %v", cleanupErr)
		}
	}()

	sourceNetwork := planApplyT3IsolationResource(t, ctx, service, target, profile, sourceValidation, sourceDocuments.resources[profile.SourceNetworkResourceID], profile.SourceInstanceID, profile.SourceNetworkResourceID, nil, nil, vlanClaimNamespace, profile.ExpectedVLANs[0], "Network", &applied)
	crossNetwork := planApplyT3IsolationResource(t, ctx, service, target, profile, crossValidation, crossDocuments.resources[profile.CrossNetworkResourceID], profile.CrossInstanceID, profile.CrossNetworkResourceID, cloneClaims(sourceNetwork.claims), nil, vlanClaimNamespace, profile.ExpectedVLANs[1], "Network", &applied)
	sourceDependency := t3IsolationAppliedDependency(sourceNetwork)
	sourceVM := planApplyT3IsolationResource(t, ctx, service, target, profile, sourceValidation, sourceDocuments.resources[profile.SourceVMResourceID], profile.SourceInstanceID, profile.SourceVMResourceID, nil, []*pluginv1.ResourceDependency{sourceDependency}, vmidClaimNamespace, profile.ExpectedVMIDs[0], "VM", &applied)
	peerVM := planApplyT3IsolationResource(t, ctx, service, target, profile, sourceValidation, sourceDocuments.resources[profile.PeerVMResourceID], profile.SourceInstanceID, profile.PeerVMResourceID, cloneClaims(sourceVM.claims), []*pluginv1.ResourceDependency{sourceDependency}, vmidClaimNamespace, profile.ExpectedVMIDs[1], "VM", &applied)
	crossExclusions := append(cloneClaims(sourceVM.claims), cloneClaims(peerVM.claims)...)
	crossDependency := t3IsolationAppliedDependency(crossNetwork)
	planApplyT3IsolationResource(t, ctx, service, target, profile, crossValidation, crossDocuments.resources[profile.CrossVMResourceID], profile.CrossInstanceID, profile.CrossVMResourceID, crossExclusions, []*pluginv1.ResourceDependency{crossDependency}, vmidClaimNamespace, profile.ExpectedVMIDs[2], "VM", &applied)

	if err := waitForT3ProbeAgent(ctx, profile.Probe.AgentOrigin, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	probeRequest := t3IsolationProbeRequest(profile, randomT3Nonce(t))
	probeResponse, transcriptDigest, err := probeClient.Run(ctx, probeRequest)
	if err != nil {
		t.Fatalf("guest-origin probe failed: %v", err)
	}
	probeEvidence, err := qualification.BindProbeEvidence(probeRequest, probeResponse, qualification.ProbeBinding{
		SourceInstanceID: profile.SourceInstanceID, SourceVMID: profile.ExpectedVMIDs[0],
		CrossInstanceID: profile.CrossInstanceID, ExpectedAgentImageDigest: profile.ImageDigest,
	})
	if err != nil {
		t.Fatalf("bind guest-origin probe evidence: %v", err)
	}
	if !t3IsolationProbePassed(probeEvidence) {
		t.Fatalf("guest isolation semantics failed; transcript %s", transcriptDigest)
	}

	cleanupErrors := cleanupT3IsolationResources(service, profile, target, config, configBytes, credentialBytes, applied)
	if len(cleanupErrors) != 0 {
		t.Fatalf("T3 isolation cleanup failed: %v", cleanupErrors[0])
	}
	encodedEvidence, err := json.Marshal(probeEvidence)
	if err != nil {
		t.Fatalf("encode bounded probe evidence: %v", err)
	}
	t.Logf("credentialed T3 guest isolation passed for target %s VMIDs %v VLANs %v transcript %s probeEvidence=%s", profile.TargetID, profile.ExpectedVMIDs, profile.ExpectedVLANs, transcriptDigest, encodedEvidence)
}

func readT3IsolationActivation(getenv func(string) string) (t3IsolationActivation, error) {
	names := []string{
		"LABCENTRAL_PROXMOX_T3_ISOLATION_EXECUTE",
		"LABCENTRAL_PROXMOX_T3_ISOLATION_PROFILE",
		"LABCENTRAL_PROXMOX_T3_ISOLATION_TARGET_CONFIG",
		"LABCENTRAL_PROXMOX_T3_ISOLATION_CREDENTIAL_FILE",
		"LABCENTRAL_PROXMOX_T3_ISOLATION_AGENT_CA",
		"LABCENTRAL_PROXMOX_T3_ISOLATION_RUNNER_CERT",
		"LABCENTRAL_PROXMOX_T3_ISOLATION_RUNNER_KEY",
		"LABCENTRAL_PROXMOX_T3_ISOLATION_CONFIRM_TARGET",
	}
	values := make(map[string]string, len(names))
	configured := false
	for _, name := range names {
		values[name] = strings.TrimSpace(getenv(name))
		configured = configured || values[name] != ""
	}
	if !configured {
		return t3IsolationActivation{}, nil
	}
	if values[names[0]] != t3ExecuteSentinel {
		return t3IsolationActivation{}, errors.New("credentialed T3 isolation requires the exact destructive execution sentinel")
	}
	for _, name := range names[1:] {
		if values[name] == "" {
			return t3IsolationActivation{}, fmt.Errorf("credentialed T3 isolation requires %s", name)
		}
	}
	paths := names[1:7]
	seen := make(map[string]bool, len(paths))
	for _, name := range paths {
		path := values[name]
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || seen[path] {
			return t3IsolationActivation{}, fmt.Errorf("%s must be an absolute, clean, non-root, distinct path", name)
		}
		seen[path] = true
	}
	if !logicalIDPattern.MatchString(values[names[7]]) {
		return t3IsolationActivation{}, errors.New("credentialed T3 isolation target confirmation is invalid")
	}
	return t3IsolationActivation{
		profilePath: values[names[1]], configPath: values[names[2]], credentialPath: values[names[3]],
		agentCAPath: values[names[4]], runnerCertPath: values[names[5]], runnerKeyPath: values[names[6]],
		confirmedTarget: values[names[7]],
	}, nil
}

func validateT3IsolationProfile(profile t3IsolationProfile, config TargetConfig, confirmedTarget string) error {
	identities := []string{
		profile.RunID, profile.TargetID, profile.SourceInstanceID, profile.CrossInstanceID,
		profile.SourceNetworkResourceID, profile.CrossNetworkResourceID, profile.SourceVMResourceID,
		profile.PeerVMResourceID, profile.CrossVMResourceID,
	}
	seenIdentities := make(map[string]bool, len(identities))
	for index, identity := range identities {
		if !logicalIDPattern.MatchString(identity) {
			return errors.New("credentialed T3 isolation profile identity is invalid")
		}
		if index >= 2 && seenIdentities[identity] {
			return errors.New("credentialed T3 isolation instance/resource identities must be distinct")
		}
		seenIdentities[identity] = true
	}
	if profile.SchemaVersion != t3IsolationProfileSchema || profile.TargetID != confirmedTarget || profile.SourceInstanceID == profile.CrossInstanceID ||
		!aliasPattern.MatchString(profile.TemplateAlias) || !aliasPattern.MatchString(profile.StorageAlias) || !aliasPattern.MatchString(profile.NetworkAlias) ||
		!sha256Pattern.MatchString(profile.ImageDigest) || len(profile.ImageRepository) == 0 || len(profile.ImageRepository) > 256 || strings.ContainsAny(profile.ImageRepository, "\x00\r\n") {
		return errors.New("credentialed T3 isolation identity or selector is invalid")
	}
	if err := config.validateMutation(); err != nil {
		return fmt.Errorf("credentialed T3 isolation target config: %w", err)
	}
	if profile.CPUMillis == 0 || profile.CPUMillis > config.Limits.MaxCPUMillis || profile.MemoryBytes < 16<<20 || profile.MemoryBytes > config.Limits.MaxMemoryBytes || config.Limits.MaxVMs < 2 {
		return errors.New("credentialed T3 isolation compute profile is invalid")
	}
	if len(profile.ExpectedVMIDs) != 3 || len(profile.ExpectedVLANs) != 2 || !uniqueUint32(profile.ExpectedVMIDs) || !uniqueUint32(profile.ExpectedVLANs) {
		return errors.New("credentialed T3 isolation allocations must be exact and distinct")
	}
	for _, vmid := range profile.ExpectedVMIDs {
		if vmid < 100 || !rangeContains(config.VMIDRanges, vmid) {
			return errors.New("credentialed T3 isolation VMID is outside the target configuration")
		}
	}
	templateOK, storageOK, networkOK := false, false, false
	for _, template := range config.Templates {
		templateOK = templateOK || template.Alias == profile.TemplateAlias && template.ContentDigest == profile.ImageDigest
	}
	for _, storage := range config.Storages {
		storageOK = storageOK || storage.Alias == profile.StorageAlias
	}
	for _, network := range config.Networks {
		if network.Alias != profile.NetworkAlias {
			continue
		}
		networkOK = rangeContains(network.VLANRanges, profile.ExpectedVLANs[0]) && rangeContains(network.VLANRanges, profile.ExpectedVLANs[1])
	}
	if !templateOK || !storageOK || !networkOK {
		return errors.New("credentialed T3 isolation selectors do not match the target configuration")
	}

	sourcePrefix, err := netip.ParsePrefix(profile.SourceNetworkCIDR)
	if err != nil || !sourcePrefix.Addr().Is4() || sourcePrefix != sourcePrefix.Masked() {
		return errors.New("credentialed T3 source network is invalid")
	}
	crossPrefix, err := netip.ParsePrefix(profile.CrossNetworkCIDR)
	if err != nil || !crossPrefix.Addr().Is4() || crossPrefix != crossPrefix.Masked() || sourcePrefix.Overlaps(crossPrefix) {
		return errors.New("credentialed T3 cross-instance network is invalid or overlaps the source network")
	}
	sourceAddress, sourceErr := netip.ParseAddr(profile.SourceGuestIPv4)
	peerAddress, peerErr := netip.ParseAddr(profile.PeerGuestIPv4)
	crossAddress, crossErr := netip.ParseAddr(profile.CrossGuestIPv4)
	if sourceErr != nil || peerErr != nil || crossErr != nil || !sourceAddress.Is4() || !peerAddress.Is4() || !crossAddress.Is4() ||
		!sourcePrefix.Contains(sourceAddress) || !sourcePrefix.Contains(peerAddress) || !crossPrefix.Contains(crossAddress) ||
		sourceAddress == sourcePrefix.Addr() || peerAddress == sourcePrefix.Addr() || crossAddress == crossPrefix.Addr() ||
		sourceAddress == peerAddress || sourceAddress == crossAddress || peerAddress == crossAddress {
		return errors.New("credentialed T3 isolation guest addressing is invalid")
	}
	origin, err := url.Parse(profile.Probe.AgentOrigin)
	if err != nil || origin.Scheme != "https" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" && origin.Path != "/" {
		return errors.New("credentialed T3 probe agent origin is invalid")
	}
	originAddress, err := netip.ParseAddrPort(origin.Host)
	if err != nil || originAddress.Addr() != sourceAddress || originAddress.Port() < 1024 {
		return errors.New("credentialed T3 probe agent origin is not the exact source guest")
	}
	managementAddress, err := netip.ParseAddrPort(profile.Probe.ManagementAddress)
	if err != nil || !managementAddress.Addr().Is4() || sourcePrefix.Contains(managementAddress.Addr()) || crossPrefix.Contains(managementAddress.Addr()) {
		return errors.New("credentialed T3 management probe destination is invalid")
	}
	request := t3IsolationProbeRequest(profile, strings.Repeat("a", 64))
	if err := qualificationprobe.ValidateRequest(request); err != nil {
		return fmt.Errorf("credentialed T3 probe request: %w", err)
	}
	return nil
}

func makeT3IsolationDocuments(profile t3IsolationProfile) (t3IsolationDocuments, t3IsolationDocuments, error) {
	sourceNetwork, err := t3IsolationNetworkResource(profile.SourceNetworkResourceID, profile.SourceNetworkCIDR)
	if err != nil {
		return t3IsolationDocuments{}, t3IsolationDocuments{}, err
	}
	crossNetwork, err := t3IsolationNetworkResource(profile.CrossNetworkResourceID, profile.CrossNetworkCIDR)
	if err != nil {
		return t3IsolationDocuments{}, t3IsolationDocuments{}, err
	}
	sourceVM, err := t3IsolationVMResource(profile, profile.SourceVMResourceID, profile.SourceNetworkResourceID, profile.SourceGuestIPv4)
	if err != nil {
		return t3IsolationDocuments{}, t3IsolationDocuments{}, err
	}
	peerVM, err := t3IsolationVMResource(profile, profile.PeerVMResourceID, profile.SourceNetworkResourceID, profile.PeerGuestIPv4)
	if err != nil {
		return t3IsolationDocuments{}, t3IsolationDocuments{}, err
	}
	crossVM, err := t3IsolationVMResource(profile, profile.CrossVMResourceID, profile.CrossNetworkResourceID, profile.CrossGuestIPv4)
	if err != nil {
		return t3IsolationDocuments{}, t3IsolationDocuments{}, err
	}
	overrideJSON, err := json.Marshal(ProviderOverride{TemplateAlias: profile.TemplateAlias, StorageAlias: profile.StorageAlias, NetworkAlias: profile.NetworkAlias})
	if err != nil {
		return t3IsolationDocuments{}, t3IsolationDocuments{}, err
	}
	source, err := marshalT3IsolationLab(profile.RunID+".source", []canonicalResource{sourceNetwork, sourceVM, peerVM}, overrideJSON)
	if err != nil {
		return t3IsolationDocuments{}, t3IsolationDocuments{}, err
	}
	cross, err := marshalT3IsolationLab(profile.RunID+".cross", []canonicalResource{crossNetwork, crossVM}, overrideJSON)
	return source, cross, err
}

func t3IsolationNetworkResource(resourceID, cidr string) (canonicalResource, error) {
	spec := canonicalNetworkSpec{Kind: "isolated", IPv4CIDR: cidr}
	spec.IPAM.Mode = "static"
	spec.Egress.Mode = "deny"
	encoded, err := json.Marshal(spec)
	return canonicalResource{ID: resourceID, Kind: "Network", Spec: encoded}, err
}

func t3IsolationVMResource(profile t3IsolationProfile, resourceID, networkID, address string) (canonicalResource, error) {
	spec := canonicalVMSpec{OSFamily: "linux"}
	spec.Image.Repository, spec.Image.Digest = profile.ImageRepository, profile.ImageDigest
	spec.Compute.CPUMillis, spec.Compute.MemoryBytes, spec.Compute.Architecture = profile.CPUMillis, profile.MemoryBytes, "amd64"
	spec.Interfaces = append(spec.Interfaces, struct {
		NetworkRef string `json:"networkRef"`
		IPv4       string `json:"ipv4,omitempty"`
		IPv6       string `json:"ipv6,omitempty"`
		MAC        string `json:"mac,omitempty"`
	}{NetworkRef: networkID, IPv4: address})
	encoded, err := json.Marshal(spec)
	return canonicalResource{ID: resourceID, Kind: "VM", DependsOn: []string{networkID}, Spec: encoded}, err
}

func marshalT3IsolationLab(name string, resources []canonicalResource, overrideJSON []byte) (t3IsolationDocuments, error) {
	lab := canonicalLabDocument{
		APIVersion: "canonical.labcentral.io/v1alpha1", Metadata: canonicalLabMetadata{Name: name},
		Requirements: canonicalLabRequirements{IsolationClass: "virtualMachine"}, Resources: resources,
	}
	labJSON, err := json.Marshal(lab)
	if err != nil {
		return t3IsolationDocuments{}, err
	}
	result := t3IsolationDocuments{labJSON: labJSON, resources: make(map[string][]byte, len(resources)), overrideJSON: overrideJSON}
	for _, resource := range resources {
		encoded, err := json.Marshal(resource)
		if err != nil {
			return t3IsolationDocuments{}, err
		}
		result.resources[resource.ID] = encoded
	}
	return result, nil
}

func validateT3IsolationLab(t *testing.T, service *Service, target *pluginv1.TargetContext, documents t3IsolationDocuments) []byte {
	t.Helper()
	validation, err := service.ValidateLab(context.Background(), &pluginv1.ValidateLabRequest{Target: target, CanonicalLabJson: documents.labJSON, ProviderOverrideJson: documents.overrideJSON})
	if err != nil || len(validation.GetErrors()) != 0 {
		t.Fatalf("T3 isolation validation = %#v, %v", validation.GetErrors(), err)
	}
	return validation.GetProviderPlan()
}

func planT3IsolationResource(t *testing.T, service *Service, target *pluginv1.TargetContext, profile t3IsolationProfile, validationPlan, resourceJSON []byte, instanceID, resourceID string, excluded []*pluginv1.AllocationClaim, dependencies []*pluginv1.ResourceDependency, expectedNamespace string, expectedValue uint32) (*pluginv1.PlanResourceResponse, *pluginv1.ResourceMutationInput) {
	t.Helper()
	input := &pluginv1.ResourceMutationInput{
		Context: t3IsolationCall(profile, instanceID, "plan", resourceID), Target: target,
		CanonicalResourceJson: resourceJSON, ProviderPlan: validationPlan,
		OwnershipTokens: []string{randomT3Token(t)}, Dependencies: dependencies,
	}
	plan, err := t3ReservedPlan(t, service, context.Background(), &pluginv1.PlanResourceRequest{Input: input, ExcludedAllocations: excluded}, profile.ExpectedVMIDs[0], profile.ExpectedVLANs[0])
	if err != nil || plan.GetError() != nil || !claimSetEquals(plan.GetProposedAllocations(), expectedNamespace, expectedValue) {
		t.Fatalf("T3 isolation %s plan = %#v, %v", resourceID, plan, err)
	}
	return plan, input
}

func planApplyT3IsolationResource(t *testing.T, ctx context.Context, service *Service, target *pluginv1.TargetContext, profile t3IsolationProfile, validationPlan, resourceJSON []byte, instanceID, resourceID string, excluded []*pluginv1.AllocationClaim, dependencies []*pluginv1.ResourceDependency, expectedNamespace string, expectedValue uint32, kind string, tracked *[]*t3AppliedResource) *t3AppliedResource {
	t.Helper()
	plan, input := planT3IsolationResource(t, service, target, profile, validationPlan, resourceJSON, instanceID, resourceID, excluded, dependencies, expectedNamespace, expectedValue)
	input.Context = t3IsolationCall(profile, instanceID, "apply", resourceID)
	input.ProviderPlan = plan.GetProviderPlan()
	input.CommittedAllocations = cloneClaims(plan.GetProposedAllocations())
	vmid := uint32(0)
	if kind == "VM" {
		vmid = expectedValue
	}
	resource := &t3AppliedResource{
		instanceID: instanceID, resourceID: resourceID, kind: kind, expectedVMID: vmid,
		input: input, plan: plan.GetProviderPlan(), planDigest: plan.GetProviderPlanDigest(),
		claims: cloneClaims(plan.GetProposedAllocations()),
	}
	// Register the cleanup claim before the first provider write. Apply may
	// conservatively return an unknown outcome without provider state, but the
	// plan still binds the exact address and ownership marker needed to recover.
	*tracked = append(*tracked, resource)
	stream := &applyCapture{grpcServerStream: grpcServerStream{ctx: ctx}}
	if err := service.ApplyResource(&pluginv1.ApplyResourceRequest{Input: input}, stream); err != nil {
		t.Fatalf("T3 isolation %s apply: %v", resourceID, err)
	}
	result := finalApplyResult(stream.events)
	if result == nil || result.GetError() != nil {
		t.Fatalf("T3 isolation %s apply = %#v", resourceID, result)
	}
	resource.state = result.GetProviderState()
	return resource
}

func t3IsolationDependency(resourceID string, plan *pluginv1.PlanResourceResponse, state *pluginv1.ProviderState) *pluginv1.ResourceDependency {
	return &pluginv1.ResourceDependency{
		LogicalResourceId: resourceID, ProviderPlan: plan.GetProviderPlan(), ProviderPlanDigest: plan.GetProviderPlanDigest(),
		ProviderState: state, CommittedAllocations: cloneClaims(plan.GetProposedAllocations()),
	}
}

func t3IsolationAppliedDependency(resource *t3AppliedResource) *pluginv1.ResourceDependency {
	return &pluginv1.ResourceDependency{
		LogicalResourceId: resource.resourceID, ProviderPlan: resource.plan, ProviderPlanDigest: resource.planDigest,
		ProviderState: resource.state, CommittedAllocations: cloneClaims(resource.claims),
	}
}

func cleanupT3IsolationResources(service *Service, profile t3IsolationProfile, target *pluginv1.TargetContext, config TargetConfig, configBytes, credentialBytes []byte, resources []*t3AppliedResource) []error {
	result := make([]error, 0)
	for index := len(resources) - 1; index >= 0; index-- {
		resource := resources[index]
		if resource == nil || resource.destroyed {
			continue
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		if resource.state == nil {
			state, absent, err := recoverT3IsolationCleanupState(cleanupCtx, service, config, configBytes, credentialBytes, resource)
			if err != nil {
				result = append(result, fmt.Errorf("%s: %w", resource.resourceID, err))
				cancel()
				continue
			}
			if absent {
				resource.destroyed = true
				cancel()
				continue
			}
			resource.state = state
		}
		destroyInput := proto.Clone(resource.input).(*pluginv1.ResourceMutationInput)
		destroyInput.Context = t3IsolationCall(profile, resource.instanceID, "destroy", resource.resourceID)
		destroyInput.Target = target
		destroyInput.PriorState = resource.state
		stream := &destroyCapture{grpcServerStream: grpcServerStream{ctx: cleanupCtx}}
		err := service.DestroyResource(&pluginv1.DestroyResourceRequest{Input: destroyInput}, stream)
		if err == nil {
			destroyResult := finalDestroyResult(stream.events)
			if destroyResult == nil || destroyResult.GetError() != nil {
				err = fmt.Errorf("owned %s destroy failed", resource.kind)
			}
		}
		if err == nil && resource.kind == "VM" {
			client, clientErr := NewClientFromEnvelopes(configBytes, credentialBytes)
			if clientErr != nil {
				err = errors.New("reopen target client for absence check")
			} else {
				_, exists, absenceErr := readOptionalVMConfig(cleanupCtx, client, config.DestinationNode, resource.expectedVMID)
				client.CloseIdleConnections()
				if absenceErr != nil || exists {
					err = fmt.Errorf("provider absence was not confirmed for reserved VMID %d", resource.expectedVMID)
				}
			}
		}
		if err == nil {
			resource.destroyed = true
		} else {
			result = append(result, fmt.Errorf("%s: %w", resource.resourceID, err))
		}
		cancel()
	}
	return result
}

func recoverT3IsolationCleanupState(ctx context.Context, service *Service, config TargetConfig, configBytes, credentialBytes []byte, resource *t3AppliedResource) (*pluginv1.ProviderState, bool, error) {
	var plan QEMUPlan
	if err := decodeEnvelope(resource.plan, maximumConfigBytes, &plan); err != nil || plan.validate() != nil {
		return nil, false, errors.New("cleanup plan is invalid")
	}
	if plan.ResourceKind == "Network" {
		state := &QEMUProviderState{
			SchemaVersion: qemuStateV10, ResourceKind: "Network", TargetID: plan.TargetID,
			Node: config.DestinationNode, LogicalResourceID: plan.LogicalResourceID,
			Generation: plan.Generation, Networks: append([]NetworkBinding(nil), plan.Networks...), Phase: "ready",
		}
		return successResult(state, &QEMUObservedState{SchemaVersion: qemuStateV10, ResourceKind: "Network", Node: state.Node, Networks: state.Networks}, resource.input.GetOwnershipTokens()).GetProviderState(), false, nil
	}
	created, err := service.factory.New(configBytes, credentialBytes)
	if err != nil {
		return nil, false, errors.New("reopen target client for cleanup")
	}
	client, ok := created.(MutationAPI)
	if !ok {
		created.CloseIdleConnections()
		return nil, false, errors.New("target cleanup client does not support mutation")
	}
	defer client.CloseIdleConnections()
	journal, err := OpenInvocationJournal(config.JournalDirectory)
	if err != nil {
		return nil, false, errors.New("open invocation journal for cleanup")
	}
	record, err := journal.Read(resource.input.GetContext().GetInvocationId())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, errors.New("read apply invocation for cleanup")
	}
	if err == nil && record.TaskUPID != "" {
		upid, parseErr := ParseUPID(record.TaskUPID)
		if parseErr != nil {
			return nil, false, errors.New("parse apply task for cleanup")
		}
		waitErr := service.waitTask(ctx, client, upid.Node, upid)
		if waitErr != nil && !errors.Is(waitErr, errTaskReportedFailure) {
			return nil, false, errors.New("apply task terminal state was not confirmed before cleanup")
		}
	}
	current, exists, err := readOptionalVMConfig(ctx, client, plan.Destination.Node, planVMID(plan))
	if err != nil {
		return nil, false, errors.New("read VM before cleanup")
	}
	if !exists {
		return nil, true, nil
	}
	if plan.Ownership == nil || verifyOwnership(current.Description, *plan.Ownership) != nil {
		return nil, false, errors.New("VM ownership did not match cleanup plan")
	}
	if current.Fields["lock"] == "clone" {
		current, err = service.waitCloneUnlocked(ctx, client, plan.Destination.Node, planVMID(plan))
		if err != nil {
			return nil, false, errors.New("clone completion was not confirmed before cleanup")
		}
	}
	state := qemuVMState(plan, current, "stopped", "")
	if err := state.validate(); err != nil {
		return nil, false, errors.New("recovered cleanup state is invalid")
	}
	observed := &QEMUObservedState{SchemaVersion: qemuStateV10, ResourceKind: "VM", Node: state.Node, VMID: state.VMID, ConfigDigest: state.ConfigDigest, Networks: append([]NetworkBinding(nil), state.Networks...)}
	return successResult(state, observed, resource.input.GetOwnershipTokens()).GetProviderState(), false, nil
}

func t3IsolationCall(profile t3IsolationProfile, instanceID, action, resourceID string) *pluginv1.CallContext {
	digest := sha256.Sum256([]byte(profile.RunID + "\x00" + instanceID + "\x00" + action + "\x00" + resourceID))
	id := "t3." + hex.EncodeToString(digest[:])
	operationDigest := sha256.Sum256([]byte(profile.RunID + "\x00" + instanceID))
	return &pluginv1.CallContext{
		RequestId: id + ".request", OperationId: "t3." + hex.EncodeToString(operationDigest[:]), InvocationId: id,
		LabInstanceId: instanceID, LogicalResourceId: resourceID, ProviderTargetId: profile.TargetID,
		IdempotencyKey: id, DesiredGeneration: 1, FencingToken: 1,
	}
}

func t3IsolationProbeRequest(profile t3IsolationProfile, nonce string) qualificationprobe.Request {
	peerAddress, _ := netip.ParseAddr(profile.PeerGuestIPv4)
	crossAddress, _ := netip.ParseAddr(profile.CrossGuestIPv4)
	return qualificationprobe.Request{
		SchemaVersion: qualificationprobe.RequestSchema, RunID: profile.RunID, Nonce: nonce,
		Attempts: profile.Probe.Attempts, TimeoutMillis: profile.Probe.TimeoutMillis,
		Targets: []qualificationprobe.Target{
			{Kind: "intended_segment", Address: netip.AddrPortFrom(peerAddress, profile.Probe.IntendedPort).String()},
			{Kind: "management", Address: profile.Probe.ManagementAddress},
			{Kind: "cross_instance", Address: netip.AddrPortFrom(crossAddress, profile.Probe.CrossInstancePort).String()},
		},
	}
}

func waitForT3ProbeAgent(ctx context.Context, origin string, maximum time.Duration) error {
	parsed, err := url.Parse(origin)
	if err != nil {
		return errors.New("probe agent origin is invalid")
	}
	deadline := time.Now().Add(maximum)
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	for {
		connection, dialErr := dialer.DialContext(ctx, "tcp", parsed.Host)
		if dialErr == nil {
			_ = connection.Close()
			return nil
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return errors.New("probe agent did not become reachable within the readiness bound")
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.New("probe agent readiness was cancelled")
		case <-timer.C:
		}
	}
}

func t3IsolationProbePassed(records []qualification.ProbeEvidence) bool {
	return len(records) == 3 && records[0].Kind == "intended_segment" && records[0].Successes == records[0].Attempts &&
		records[1].Kind == "management" && records[1].Successes == 0 &&
		records[2].Kind == "cross_instance" && records[2].Successes == 0
}

func randomT3Nonce(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw)
}

func uniqueUint32(values []uint32) bool {
	seen := make(map[uint32]bool, len(values))
	for _, value := range values {
		if seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func t3IsolationTestProfile(targetID string) t3IsolationProfile {
	return t3IsolationProfile{
		SchemaVersion: t3IsolationProfileSchema, RunID: "qualification.isolation-test", TargetID: targetID,
		SourceInstanceID: "instance.isolation-source", CrossInstanceID: "instance.isolation-cross",
		SourceNetworkResourceID: "network.source", CrossNetworkResourceID: "network.cross",
		SourceVMResourceID: "vm.source", PeerVMResourceID: "vm.peer", CrossVMResourceID: "vm.cross",
		SourceNetworkCIDR: "10.20.0.0/24", CrossNetworkCIDR: "10.21.0.0/24",
		SourceGuestIPv4: "10.20.0.10", PeerGuestIPv4: "10.20.0.11", CrossGuestIPv4: "10.21.0.10",
		ImageRepository: "labcentral/proxmox/debian", ImageDigest: testTemplateDigest,
		TemplateAlias: "debian", StorageAlias: "primary", NetworkAlias: "isolated",
		ExpectedVMIDs: []uint32{100, 101, 102}, ExpectedVLANs: []uint32{200, 201}, CPUMillis: 2000, MemoryBytes: 2 << 30,
		Probe: t3IsolationProbeProfile{
			AgentOrigin: "https://10.20.0.10:9443", IntendedPort: 22, ManagementAddress: "10.0.0.2:8006",
			CrossInstancePort: 22, Attempts: 3, TimeoutMillis: 500,
		},
	}
}
