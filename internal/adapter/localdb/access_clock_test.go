package localdb

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func TestAccessLaunchDeadlineUsesDatabaseClockBound(t *testing.T) {
	for name, offset := range map[string]time.Duration{"clock-ahead": application.DefaultAccessLaunchTTL + time.Second, "shorter-deadline": time.Minute, "already-expired": -time.Second} {
		t.Run(name, func(t *testing.T) {
			store := newSQLiteAccessContractStore(t)
			setupQuotaTestTenancy(t, store)
			instance, digest := createReadyAccessTestInstance(t, store, "instance.clock")
			pending := beginAccessTestSession(t, store, instance, digest, "access.clock", "clock")
			state := []byte("fixture-state")
			hash := sha256.Sum256(state)
			requested := time.Now().Add(offset)
			record, err := store.FinalizeAccessSession(context.Background(), application.FinalizeAccessSessionRequest{
				GovernanceRecordIDs: governanceIDs("clock-finalize"), ID: pending.ID, SubjectID: pending.SubjectID,
				EndpointSchemaName: "proxmox.vnc-endpoint", EndpointSchemaMajor: 1, EndpointDigest: digest, ProviderStateDigest: digest,
				LaunchToken: accessTestHash("clock-launch"), LaunchExpiresAt: requested, ExpiresAt: pending.ExpiresAt,
				AccessStateSchema: "novnc.access-state", AccessStateMajor: 1, AccessStateDigest: domain.SHA256Digest(fmt.Sprintf("%x", hash)), AccessState: state,
			})
			if offset < 0 {
				if err == nil {
					t.Fatal("expired launch accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if record.LaunchExpiresAt.After(requested) || record.LaunchExpiresAt.After(time.Now().Add(application.DefaultAccessLaunchTTL)) {
				t.Fatal("stored launch deadline exceeds caller or database bound")
			}
			if name == "shorter-deadline" && !record.LaunchExpiresAt.Equal(requested.Truncate(time.Microsecond)) {
				t.Fatal("shorter caller deadline changed")
			}
		})
	}
}
