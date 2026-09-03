package proxmox

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
)

const t4UpgradeReadSentinel = "I_ACKNOWLEDGE_LABCENTRAL_PROXMOX_T4_READ_ONLY_UPGRADE"

type t4UpgradeReadActivation struct {
	journalDirectory string
	targetID         string
	invocationIDs    []string
}

func TestContractTESTPROXMOX028UpgradeReadabilityRunnerIsExplicitAndReadOnly(t *testing.T) {
	empty, err := readT4UpgradeReadActivation(func(string) string { return "" })
	if err != nil || !reflect.DeepEqual(empty, t4UpgradeReadActivation{}) {
		t.Fatalf("empty upgrade activation = %#v, %v", empty, err)
	}
	complete := map[string]string{
		"LABCENTRAL_PROXMOX_T4_UPGRADE_EXECUTE":     t4UpgradeReadSentinel,
		"LABCENTRAL_PROXMOX_T4_UPGRADE_JOURNAL_DIR": "/qualification/journal",
		"LABCENTRAL_PROXMOX_T4_UPGRADE_TARGET_ID":   "target.proxmox-t3",
		"LABCENTRAL_PROXMOX_T4_UPGRADE_INVOCATIONS": "invocation.network,invocation.vm,invocation.destroy",
	}
	activation, err := readT4UpgradeReadActivation(func(name string) string { return complete[name] })
	if err != nil || len(activation.invocationIDs) != 3 {
		t.Fatalf("complete upgrade activation = %#v, %v", activation, err)
	}
	delete(complete, "LABCENTRAL_PROXMOX_T4_UPGRADE_TARGET_ID")
	if _, err := readT4UpgradeReadActivation(func(name string) string { return complete[name] }); err == nil {
		t.Fatal("partial upgrade activation was accepted")
	}
}

// TestProxmoxT4UpgradeReadability opens only a protected local journal. It is
// intended to run with terminal records produced by an earlier provider build;
// it creates no provider client and has no mutation path.
func TestProxmoxT4UpgradeReadability(t *testing.T) {
	activation, err := readT4UpgradeReadActivation(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(activation, t4UpgradeReadActivation{}) {
		t.Skip("Proxmox T4 upgrade readability rehearsal is not explicitly enabled")
	}
	journal, err := OpenInvocationJournal(activation.journalDirectory)
	if err != nil {
		t.Fatalf("open prior-build invocation journal: %v", err)
	}
	stateKinds := map[string]bool{}
	destroyed := false
	for _, invocationID := range activation.invocationIDs {
		record, err := journal.Read(invocationID)
		if err != nil {
			t.Fatalf("read prior-build invocation %s: %v", invocationID, err)
		}
		if record.SchemaVersion != journalV10 || record.TargetID != activation.targetID || record.Result == nil {
			t.Fatalf("prior-build invocation %s has an unexpected binding", invocationID)
		}
		if record.Action == "destroy" && record.Phase == "destroyed" && record.Result.Observed != nil && record.Result.Observed.Destroyed {
			destroyed = true
		}
		if record.Result.State == nil {
			continue
		}
		stateBytes, err := json.Marshal(record.Result.State)
		if err != nil {
			t.Fatalf("encode prior-build provider state: %v", err)
		}
		digest := sha256.Sum256(stateBytes)
		decoded, err := decodeQEMUState(&pluginv1.ProviderState{
			Schema:        &pluginv1.SchemaVersion{Name: "proxmox.qemu-state", Major: 1, Minor: 0},
			PluginId:      PluginID,
			PluginVersion: PluginVersion,
			Opaque:        stateBytes,
			Digest:        digest[:],
		})
		if err != nil || !reflect.DeepEqual(decoded, *record.Result.State) {
			t.Fatalf("decode prior-build provider state for %s: %v", invocationID, err)
		}
		stateKinds[decoded.ResourceKind] = true
	}
	if !stateKinds["Network"] || !stateKinds["VM"] || !destroyed {
		t.Fatalf("upgrade rehearsal coverage is incomplete: state kinds=%v destroyed=%t", stateKinds, destroyed)
	}
	t.Logf("read-only upgrade rehearsal passed for target %s: %d checksummed %s records, Network and VM %s states, destroyed tombstone", activation.targetID, len(activation.invocationIDs), journalV10, qemuStateV10)
}

func readT4UpgradeReadActivation(getenv func(string) string) (t4UpgradeReadActivation, error) {
	names := []string{
		"LABCENTRAL_PROXMOX_T4_UPGRADE_EXECUTE",
		"LABCENTRAL_PROXMOX_T4_UPGRADE_JOURNAL_DIR",
		"LABCENTRAL_PROXMOX_T4_UPGRADE_TARGET_ID",
		"LABCENTRAL_PROXMOX_T4_UPGRADE_INVOCATIONS",
	}
	values := make(map[string]string, len(names))
	configured := false
	for _, name := range names {
		values[name] = strings.TrimSpace(getenv(name))
		configured = configured || values[name] != ""
	}
	if !configured {
		return t4UpgradeReadActivation{}, nil
	}
	if values[names[0]] != t4UpgradeReadSentinel {
		return t4UpgradeReadActivation{}, errors.New("Proxmox T4 upgrade rehearsal requires the exact read-only execution sentinel")
	}
	for _, name := range names[1:] {
		if values[name] == "" {
			return t4UpgradeReadActivation{}, fmt.Errorf("Proxmox T4 upgrade rehearsal requires %s", name)
		}
	}
	directory := values[names[1]]
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == "/" {
		return t4UpgradeReadActivation{}, errors.New("Proxmox T4 upgrade journal must be an absolute, clean, non-root path")
	}
	if !stableIdentifier(values[names[2]]) {
		return t4UpgradeReadActivation{}, errors.New("Proxmox T4 upgrade target ID is invalid")
	}
	invocationIDs := strings.Split(values[names[3]], ",")
	if len(invocationIDs) < 3 || len(invocationIDs) > 32 {
		return t4UpgradeReadActivation{}, errors.New("Proxmox T4 upgrade rehearsal requires 3 to 32 invocation IDs")
	}
	seen := make(map[string]bool, len(invocationIDs))
	for index, invocationID := range invocationIDs {
		invocationIDs[index] = strings.TrimSpace(invocationID)
		if !invocationIDPattern.MatchString(invocationIDs[index]) || seen[invocationIDs[index]] {
			return t4UpgradeReadActivation{}, errors.New("Proxmox T4 upgrade invocation IDs must be valid and distinct")
		}
		seen[invocationIDs[index]] = true
	}
	return t4UpgradeReadActivation{journalDirectory: directory, targetID: values[names[2]], invocationIDs: invocationIDs}, nil
}

func stableIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || index > 0 && (character == '.' || character == '_' || character == '-') {
			continue
		}
		return false
	}
	return true
}
