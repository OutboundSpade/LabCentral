package httpapi

import (
	"context"
	"crypto/sha256"
	"fmt"
	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/authorization"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"google.golang.org/protobuf/proto"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestConsoleControlAPIUsesOwnSessionAndCurrentAuthority(t *testing.T) {
	f := newGovernanceHTTPFixture(t)
	ctx := context.Background()
	evaluator, err := authorization.New(f.database)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.SetAccessRuntime(&controlplane.AccessRegistry{}, evaluator, f.tokenHasher); err != nil {
		t.Fatal(err)
	}
	instance := seedQuotaHTTPInstance(t, f.database, "instance.console-api", "project.alpha", "user.author", app.OwnerUser, "user.author", 3600)
	digest := sha256.Sum256([]byte("console-api-state"))
	d := domain.SHA256Digest(fmt.Sprintf("%x", digest))
	state, err := proto.Marshal(&wire.ProviderState{Schema: &wire.SchemaVersion{Name: "fixture", Major: 1}, PluginId: "fixture.provider", PluginVersion: "1", Opaque: []byte("{}"), OwnershipIds: []string{"owner.console"}, Digest: digest[:]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sqlDB.Exec(`UPDATE labcentral_local_instances SET phase='ready' WHERE instance_id=?`, instance.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sqlDB.Exec(`UPDATE labcentral_local_resources SET phase='ready',kind='VM',provider_state=? WHERE instance_id=?`, state, instance.ID); err != nil {
		t.Fatal(err)
	}
	ids := func(suffix string) app.GovernanceRecordIDs {
		return app.GovernanceRecordIDs{AuditID: "audit.console-" + suffix, EventID: "event.console-" + suffix}
	}
	identity := app.AccessCallbackIdentity{PluginID: "fixture.access", PluginInstanceID: "fixture.console", Audience: "https://lab.example", ProtocolMajor: 1, ProtocolMinor: 2}
	id := domain.AccessSessionID("access.console-api")
	pending, err := f.database.BeginAccessSession(ctx, app.BeginAccessSessionRequest{SharedConsole: true, GovernanceRecordIDs: ids("begin"), ID: id, SubjectID: "user.author", InstanceID: instance.ID, ResourceID: "container.ttl", Method: "vnc", AccessPluginID: identity.PluginID, AccessPluginVersion: "1", AccessPluginInstance: identity.PluginInstanceID, AccessKind: "novnc", Audience: identity.Audience, ProviderStateDigest: d, ResourceGeneration: instance.Generation, IdempotencyKey: "console-api-begin", RequestDigest: d})
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("x", 43)
	_, err = f.database.FinalizeAccessSession(ctx, app.FinalizeAccessSessionRequest{GovernanceRecordIDs: ids("finalize"), ID: id, SubjectID: "user.author", ExpiresAt: pending.ExpiresAt, ProviderStateDigest: d, EndpointSchemaName: "fixture", EndpointSchemaMajor: 1, EndpointDigest: d, AccessStateSchema: "fixture", AccessStateMajor: 1, AccessStateDigest: d, AccessState: []byte("{}"), LaunchToken: f.tokenHasher.HashCurrent("access-launch", token), LaunchExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := f.service.ExchangeAccessToken(ctx, identity, string(id), token)
	if err != nil || !decision.Granted || !decision.SharedConsole || decision.InputLease != nil {
		t.Fatal("viewer activation", decision, err)
	}
	author := f.client(t, "author", "an-author-production-password")
	admin := f.client(t, "admin", "a-production-length-password")
	viewer := f.client(t, "auditor", "an-auditor-production-password")
	path := "/api/v1/access-sessions/" + string(id) + "/console"
	response := author.request(http.MethodGet, path, nil, "", "")
	requireGovernanceStatus(t, response, 200)
	response = admin.request(http.MethodPost, path+"/take-control", nil, "", "console-foreign-take")
	requireGovernanceStatus(t, response, 404)
	response = viewer.request(http.MethodGet, path, nil, "", "")
	requireGovernanceStatus(t, response, 403)
	response = author.request(http.MethodPost, path+"/take-control", nil, "", "console-own-take")
	requireGovernanceStatus(t, response, 200)
	granted, err := f.service.RecheckAccessLease(ctx, identity, string(id), decision.Lease)
	if err != nil || !granted.Granted || granted.InputLease == nil || granted.InputLease.Fence == 0 {
		t.Fatal("core callback driver grant", granted, err)
	}
	response = author.request(http.MethodGet, path, nil, "", "")
	requireGovernanceStatus(t, response, 200)
	var presence app.ConsolePresence
	decodeGovernanceResponse(t, response, &presence)
	if presence.DriverSessionID != id {
		t.Fatal("driver presentation")
	}
	response = author.request(http.MethodPost, path+"/release-control", nil, "", "console-own-release")
	requireGovernanceStatus(t, response, 200)
	watched, err := f.service.RecheckAccessLease(ctx, identity, string(id), granted.Lease)
	if err != nil || !watched.Granted || watched.InputLease != nil {
		t.Fatal("release", watched, err)
	}
	if _, err = f.sqlDB.Exec(`UPDATE labcentral_role_assignments SET valid_until_us=1 WHERE subject_id='user.author'`); err != nil {
		t.Fatal(err)
	}
	response = author.request(http.MethodPost, path+"/take-control", nil, "", "console-revoked-take")
	requireGovernanceStatus(t, response, 403)
}
