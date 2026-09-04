package proxmox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	accessendpoint "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/accessendpoint"
)

type accessTestAPI struct {
	inventory   Inventory
	permissions EffectivePermissions
	config      VMConfig
	proxy       VNCProxy
	proxyCalls  int
	closed      bool
}

func (client *accessTestAPI) Discover(context.Context) (Inventory, error) {
	return client.inventory, nil
}
func (client *accessTestAPI) CloseIdleConnections() { client.closed = true }
func (client *accessTestAPI) EffectivePermissions(context.Context) (EffectivePermissions, error) {
	return client.permissions, nil
}
func (client *accessTestAPI) VMConfig(context.Context, string, uint32) (VMConfig, error) {
	return client.config, nil
}
func (client *accessTestAPI) VNCProxy(context.Context, string, uint32) (VNCProxy, error) {
	client.proxyCalls++
	return client.proxy, nil
}

func TestAccessContract016ProxmoxNoVNCExactTarget(t *testing.T) {
	target := mutationTestTarget(t)
	var config TargetConfig
	if err := json.Unmarshal(target.GetConfigEnvelope(), &config); err != nil {
		t.Fatal(err)
	}
	config.SchemaVersion = targetConfigV12
	config.ConsoleAccess = ConsoleAccessConfig{Enabled: true, MaximumDurationSeconds: 1800}
	for index := range config.PermissionScopes {
		if config.PermissionScopes[index].Path == "/pool/lab" {
			config.PermissionScopes[index].Privileges = append(config.PermissionScopes[index].Privileges, "VM.Console")
		}
	}
	configBytes, _ := json.Marshal(config)
	target.ConfigEnvelope = configBytes
	marker := OwnershipMarker{InstallationID: config.InstallationID, TargetID: target.GetTargetId(), InstanceID: "instance.test", LogicalResource: "vm.test", Generation: 3, Token: "ownership-secret"}
	description, err := encodeOwnershipMarker(marker)
	if err != nil {
		t.Fatal(err)
	}
	markerBytes, _ := json.Marshal(marker)
	state := QEMUProviderState{
		SchemaVersion: qemuStateV10, ResourceKind: "VM", TargetID: target.GetTargetId(), Node: config.DestinationNode,
		VMID: 105, Ownership: &marker, OwnershipDigest: digestDocument(markerBytes), LogicalResourceID: "vm.test", Generation: 3,
		Volumes: []string{"local-lvm:vm-105-disk-0"}, ConfigDigest: strings.Repeat("d", 40), Phase: "running",
	}
	stateBytes, _ := json.Marshal(state)
	stateDigest := sha256.Sum256(stateBytes)
	outer := &pluginv1.ProviderState{
		Schema: &pluginv1.SchemaVersion{Name: "proxmox.qemu-state", Major: 1}, PluginId: PluginID, PluginVersion: PluginVersion,
		Opaque: stateBytes, OwnershipIds: []string{marker.Token}, Digest: stateDigest[:],
	}
	certificateServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	certificate := certificateServer.Certificate()
	certificateServer.Close()
	certificatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}))
	configuredCAServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	configuredCertificate := configuredCAServer.Certificate()
	configuredCAServer.Close()
	config.CACertificatePEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: configuredCertificate.Raw}))
	configBytes, _ = json.Marshal(config)
	target.ConfigEnvelope = configBytes
	paths, propagating := map[string][]string{}, map[string][]string{}
	for _, scope := range config.PermissionScopes {
		paths[scope.Path] = append([]string(nil), scope.Privileges...)
		propagating[scope.Path] = append([]string(nil), scope.Privileges...)
	}
	client := &accessTestAPI{
		inventory: mutationTestInventory(), permissions: EffectivePermissions{Paths: paths, PropagatingPaths: propagating},
		config: VMConfig{Digest: state.ConfigDigest, Description: description, Fields: map[string]string{"digest": state.ConfigDigest, "description": description}},
		proxy:  VNCProxy{Port: 5907, Password: "provider-password-canary", Certificate: certificatePEM},
	}
	service := newT0MutationService(ClientFactoryFunc(func([]byte, []byte) (DiscoveryClient, error) { return client, nil }))
	response, err := service.DiscoverAccessEndpoint(context.Background(), &pluginv1.DiscoverAccessEndpointRequest{
		Context: &pluginv1.CallContext{LabInstanceId: marker.InstanceID, LogicalResourceId: marker.LogicalResource, ProviderTargetId: marker.TargetID, DesiredGeneration: marker.Generation},
		Target:  target, ResourceState: outer, Method: "vnc",
	})
	if err != nil || response.GetError() != nil {
		t.Fatalf("discover access = %#v, %v", response, err)
	}
	if !client.closed || client.proxyCalls != 1 || response.GetProviderStateDigest() != hex.EncodeToString(stateDigest[:]) || response.GetTargetId() != marker.TargetID || response.GetLabInstanceId() != marker.InstanceID || response.GetLogicalResourceId() != marker.LogicalResource {
		t.Fatalf("access endpoint binding = %#v, client=%#v", response, client)
	}
	var endpoint accessendpoint.ProxmoxVNC
	if err := json.Unmarshal(response.GetOpaqueConnectionData(), &endpoint); err != nil {
		t.Fatal(err)
	}
	if endpoint.Password != client.proxy.Password || endpoint.TargetID != marker.TargetID || endpoint.InstanceID != marker.InstanceID || endpoint.LogicalResourceID != marker.LogicalResource || endpoint.ProviderStateDigest != response.GetProviderStateDigest() {
		t.Fatalf("typed endpoint = %#v", endpoint)
	}
	if !strings.Contains(endpoint.CACertificatePEM, config.CACertificatePEM) || !strings.Contains(endpoint.CACertificatePEM, certificatePEM) {
		t.Fatal("VNC endpoint omitted configured or Proxmox-returned trust material")
	}

	outer.OwnershipIds = []string{"attacker-owned"}
	denied, err := service.DiscoverAccessEndpoint(context.Background(), &pluginv1.DiscoverAccessEndpointRequest{
		Context: &pluginv1.CallContext{LabInstanceId: marker.InstanceID, LogicalResourceId: marker.LogicalResource, ProviderTargetId: marker.TargetID, DesiredGeneration: marker.Generation},
		Target:  target, ResourceState: outer, Method: "vnc",
	})
	if err != nil || denied.GetError().GetCode() != "stale_access_binding" || client.proxyCalls != 1 {
		t.Fatalf("stale ownership denial = %#v, %v; proxy calls=%d", denied, err, client.proxyCalls)
	}
}

var _ AccessAPI = (*accessTestAPI)(nil)
