package proxmox

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	accessendpoint "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/accessendpoint"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type AccessAPI interface {
	DiscoveryClient
	EffectivePermissions(context.Context) (EffectivePermissions, error)
	VMConfig(context.Context, string, uint32) (VMConfig, error)
	VNCProxy(context.Context, string, uint32) (VNCProxy, error)
}

type VNCProxy struct {
	Port        uint16
	Password    string
	Certificate string
}

func (service *Service) DiscoverAccessEndpoint(ctx context.Context, request *pluginv1.DiscoverAccessEndpointRequest) (*pluginv1.DiscoverAccessEndpointResponse, error) {
	fail := func(code, message string) (*pluginv1.DiscoverAccessEndpointResponse, error) {
		return &pluginv1.DiscoverAccessEndpointResponse{Error: mutationError(code, "permanent", message, "", "")}, nil
	}
	if request == nil || request.GetContext() == nil || request.GetTarget() == nil || request.GetResourceState() == nil || request.GetMethod() != "vnc" {
		return fail("invalid_access_request", "Proxmox VNC access request is invalid")
	}
	call := request.GetContext()
	if call.GetLabInstanceId() == "" || call.GetLogicalResourceId() == "" || call.GetProviderTargetId() == "" || call.GetDesiredGeneration() == 0 ||
		call.GetProviderTargetId() != request.GetTarget().GetTargetId() {
		return fail("invalid_access_binding", "Proxmox VNC access binding is invalid")
	}
	var config TargetConfig
	if err := decodeEnvelope(request.GetTarget().GetConfigEnvelope(), maximumConfigBytes, &config); err != nil || config.validateMutation() != nil || !config.ConsoleAccess.Enabled || !service.allowT0Mutation {
		return fail("access_not_qualified", "Proxmox VNC access is not enabled for this target")
	}
	state, err := decodeQEMUState(request.GetResourceState())
	if err != nil || state.ResourceKind != "VM" || !stateMatchesTargetConfig(config, state) || state.TargetID != call.GetProviderTargetId() ||
		state.LogicalResourceID != call.GetLogicalResourceId() || state.Generation != call.GetDesiredGeneration() || state.Ownership == nil ||
		state.Ownership.InstanceID != call.GetLabInstanceId() || len(request.GetResourceState().GetOwnershipIds()) != 1 ||
		request.GetResourceState().GetOwnershipIds()[0] != state.Ownership.Token {
		return fail("stale_access_binding", "Proxmox VNC access resource binding is stale")
	}
	discovered, err := service.factory.New(request.GetTarget().GetConfigEnvelope(), request.GetTarget().GetCredentialEnvelope())
	if err != nil {
		return fail("access_target_unavailable", "Proxmox VNC target is unavailable")
	}
	defer discovered.CloseIdleConnections()
	client, ok := discovered.(AccessAPI)
	if !ok {
		return fail("access_api_unavailable", "Proxmox VNC access API is unavailable")
	}
	inventory, err := client.Discover(ctx)
	if err != nil || inventory.normalize() != nil {
		return fail("access_target_unavailable", "Proxmox VNC target inventory is unavailable")
	}
	mutationDigest, err := inventory.MutationDigestForConfig(config)
	if err != nil || config.Qualification.InventoryDigest != mutationDigest {
		return fail("access_qualification_stale", "Proxmox VNC target qualification is stale")
	}
	permissions, err := client.EffectivePermissions(ctx)
	if err != nil || !permissionsSatisfy(config, permissions) {
		return fail("access_permission_denied", "Proxmox VNC target permissions are insufficient")
	}
	if _, err := service.authorizeMutation(ctx, request.GetTarget(), config, inventory, mutationDigest); err != nil {
		return fail("access_not_qualified", "Proxmox VNC access qualification is unavailable")
	}
	current, err := client.VMConfig(ctx, state.Node, state.VMID)
	if err != nil || current.Digest != state.ConfigDigest || verifyOwnership(current.Description, *state.Ownership) != nil {
		return fail("stale_access_binding", "Proxmox VNC resource ownership is stale")
	}
	proxy, err := client.VNCProxy(ctx, state.Node, state.VMID)
	if err != nil {
		return fail("access_proxy_unavailable", "Proxmox VNC proxy is unavailable")
	}
	endpointURL, _ := url.Parse(config.Endpoint)
	if endpointURL == nil || endpointURL.Hostname() == "" || proxy.Port < 5900 || proxy.Certificate == "" || proxy.Password == "" {
		return fail("invalid_access_endpoint", "Proxmox VNC proxy returned an invalid endpoint")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(proxy.Certificate)) {
		return fail("invalid_access_endpoint", "Proxmox VNC proxy returned invalid trust material")
	}
	trustBundle := proxy.Certificate
	if config.CACertificatePEM != "" {
		trustBundle = config.CACertificatePEM + "\n" + trustBundle
	}
	if len(trustBundle) > 64<<10 {
		return fail("invalid_access_endpoint", "Proxmox VNC proxy returned excessive trust material")
	}
	providerDigest := hex.EncodeToString(request.GetResourceState().GetDigest())
	envelope := accessendpoint.ProxmoxVNC{
		SchemaVersion: accessendpoint.ProxmoxVNCSchema, TargetID: state.TargetID, InstanceID: state.Ownership.InstanceID,
		LogicalResourceID: state.LogicalResourceID, Method: "vnc", ProviderStateDigest: providerDigest,
		Host: endpointURL.Hostname(), ServerName: endpointURL.Hostname(), Port: proxy.Port,
		CACertificatePEM: trustBundle, Password: proxy.Password,
	}
	contents, err := json.Marshal(envelope)
	if err != nil || len(contents) > 64<<10 {
		return fail("invalid_access_endpoint", "Proxmox VNC proxy endpoint could not be encoded")
	}
	digest := sha256.Sum256(contents)
	now := service.now().UTC()
	return &pluginv1.DiscoverAccessEndpointResponse{
		OpaqueConnectionData: contents, ValidUntil: timestamppb.New(now.Add(time.Duration(config.ConsoleAccess.MaximumDurationSeconds) * time.Second)),
		EndpointSchema: &pluginv1.SchemaVersion{Name: "proxmox.vnc-endpoint", Major: 1, Minor: 0}, EndpointDigest: hex.EncodeToString(digest[:]),
		TargetId: state.TargetID, LabInstanceId: state.Ownership.InstanceID, LogicalResourceId: state.LogicalResourceID,
		Method: "vnc", ProviderStateDigest: providerDigest,
	}, nil
}

func (client *Client) VNCProxy(ctx context.Context, node string, vmid uint32) (VNCProxy, error) {
	if !nodeNamePattern.MatchString(node) || vmid < 100 {
		return VNCProxy{}, fmt.Errorf("VNC proxy identity is invalid")
	}
	type response struct {
		Port        apiUint16 `json:"port"`
		Password    string    `json:"password"`
		Certificate string    `json:"cert"`
	}
	raw, err := requestAPI[response](ctx, client, "POST", fmt.Sprintf("/nodes/%s/qemu/%d/vncproxy", node, vmid), url.Values{})
	if err != nil {
		return VNCProxy{}, err
	}
	if raw.Port < 5900 || raw.Password == "" || len(raw.Password) > 256 || raw.Certificate == "" || len(raw.Certificate) > 64<<10 {
		return VNCProxy{}, fmt.Errorf("Proxmox VNC proxy response is invalid")
	}
	return VNCProxy{Port: uint16(raw.Port), Password: raw.Password, Certificate: raw.Certificate}, nil
}

type apiUint16 uint16

func (value *apiUint16) UnmarshalJSON(contents []byte) error {
	var number uint64
	if len(contents) >= 2 && contents[0] == '"' && contents[len(contents)-1] == '"' {
		_, err := fmt.Sscanf(string(contents[1:len(contents)-1]), "%d", &number)
		if err != nil {
			return fmt.Errorf("expected Proxmox integer")
		}
	} else if _, err := fmt.Sscanf(string(contents), "%d", &number); err != nil {
		return fmt.Errorf("expected Proxmox integer")
	}
	if number > 65535 {
		return fmt.Errorf("Proxmox integer exceeds uint16")
	}
	*value = apiUint16(number)
	return nil
}
