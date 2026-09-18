package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/pkg/containervm"
	"net"
	"net/url"
	"path/filepath"
	"slices"

	"github.com/OutboundSpade/LabCentral/internal/adapter/providergrpc"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
	"github.com/OutboundSpade/LabCentral/internal/pluginhost"
	"github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/manifest"
)

type extensionBinding struct {
	ContainerRuntime *containervm.RuntimeBuildPolicy `json:"containerRuntime,omitempty"`
	Requirement      string                          `json:"requirement,omitempty"`
	ID               string                          `json:"id"`
	ProjectIDs       []string                        `json:"projectIds"`
	Configuration    json.RawMessage                 `json:"configuration"`
	CredentialFile   string                          `json:"credentialFile,omitempty"`
	Limits           *extensionBuildLimits           `json:"limits,omitempty"`
}
type extensionBuildLimits struct {
	TimeoutSeconds uint32   `json:"timeoutSeconds"`
	CPUCount       uint32   `json:"cpuCount"`
	MemoryBytes    uint64   `json:"memoryBytes"`
	InputBytes     uint64   `json:"inputBytes"`
	OutputBytes    uint64   `json:"outputBytes"`
	Egress         []string `json:"egress"`
}
type installedExtension struct {
	Bindings           []extensionBinding `json:"bindings,omitempty"`
	StreamEndpoint     string             `json:"streamEndpoint,omitempty"`
	Package            string             `json:"package,omitempty"`
	Contribution       string             `json:"contribution,omitempty"`
	Manifest           string             `json:"manifest,omitempty"`
	InstanceID         string             `json:"instanceId"`
	Mode               string             `json:"mode,omitempty"`
	RuntimeDirectory   string             `json:"runtimeDirectory,omitempty"`
	TokenFile          string             `json:"tokenFile,omitempty"`
	Args               []string           `json:"args,omitempty"`
	GrantedPermissions []string           `json:"grantedPermissions"`
	Kind               string             `json:"kind,omitempty"`
	TargetIDs          []string           `json:"targetIds,omitempty"`
	Methods            []string           `json:"methods,omitempty"`
}
type installedBrowser struct {
	Manifest   manifest.Manifest
	InstanceID string
	Assets     map[string]manifest.AssetSnapshot
}
type loadedBackend struct {
	Manifest manifest.Manifest
	Entry    installedExtension
	Client   *pluginhost.Client
}
type loadedExtensions struct {
	Backends []loadedBackend
	Clients  []*pluginhost.Client
	Access   []controlplane.AccessBinding
	Browsers []installedBrowser
}

func readInstallation(file string) (installation, error) {
	var install installation
	b, err := manifest.ProtectedFile(file, 1<<20)
	if err != nil {
		return install, err
	}
	if err = manifest.Decode(b, &install); err != nil {
		return install, err
	}
	if install.APIVersion != "labcentral.io/installation/v1" && install.APIVersion != "labcentral.io/installation/v2" {
		return install, fmt.Errorf("unsupported installation version")
	}
	if len(install.Providers)+len(install.Extensions) < 1 || len(install.Providers)+len(install.Extensions) > 32 {
		return install, fmt.Errorf("installation requires 1-32 entries")
	}
	if install.APIVersion == "labcentral.io/installation/v1" && len(install.Extensions) > 0 {
		return install, fmt.Errorf("extensions require installation v2")
	}
	ids := map[string]bool{}
	for _, p := range install.Providers {
		if ids[p.InstanceID] {
			return install, fmt.Errorf("duplicate plugin identity")
		}
		ids[p.InstanceID] = true
	}
	for _, p := range install.Extensions {
		if p.InstanceID == "" || ids[p.InstanceID] {
			return install, fmt.Errorf("duplicate or missing plugin identity")
		}
		ids[p.InstanceID] = true
	}
	return install, nil
}
func loadInstalledExtensions(ctx context.Context, cfg config, runtimeParent string, authorization application.AccessAuthorizationHandler) (result loadedExtensions, err error) {
	if cfg.installationFile == "" {
		return result, nil
	}
	audience := cfg.publicOrigin
	if audience == "" && cfg.authMode == "none" {
		audience = "http://" + cfg.listen
	}
	install, err := readInstallation(cfg.installationFile)
	if err != nil {
		return result, err
	}
	defer func() {
		if err != nil {
			for _, c := range result.Clients {
				_ = c.Close()
			}
		}
	}()
	for _, entry := range install.Extensions {
		if entry.Package != "" {
			if entry.Manifest != "" || entry.Contribution == "" {
				return result, fmt.Errorf("select either a contribution or a manifest")
			}
			entry.Manifest, err = manifest.ContributionManifest(install.PluginRoot, entry.Package, entry.Contribution)
			if err != nil {
				return result, err
			}
		} else if entry.Contribution != "" {
			return result, fmt.Errorf("contribution requires a package")
		}
		m, executable, e := manifest.Load(install.PluginRoot, entry.Manifest, entry.GrantedPermissions)
		if e != nil {
			return result, e
		}
		if len(m.Categories) != 1 {
			return result, fmt.Errorf("one category required")
		}
		category := m.Categories[0]
		if len(entry.Bindings) > 32 {
			return result, fmt.Errorf("too many extension bindings")
		}
		bindingIDs := map[string]bool{}
		for _, binding := range entry.Bindings {
			if category != "source" && category != "builder" {
				return result, fmt.Errorf("invocation bindings require source or builder")
			}
			if _, e := domain.NewOperationID(binding.ID); e != nil || bindingIDs[binding.ID] {
				return result, fmt.Errorf("invalid or duplicate binding ID")
			}
			bindingIDs[binding.ID] = true
			if len(binding.ProjectIDs) == 0 || len(binding.ProjectIDs) > 64 {
				return result, fmt.Errorf("binding requires 1-64 project IDs")
			}
			for _, id := range binding.ProjectIDs {
				if _, e := domain.NewProjectID(id); e != nil {
					return result, e
				}
			}
			if e := manifest.ValidateConfiguration(m.ConfigurationSchema, binding.Configuration); e != nil {
				return result, e
			}
			if binding.CredentialFile != "" {
				if !filepath.IsAbs(binding.CredentialFile) {
					return result, fmt.Errorf("credential file must be absolute")
				}
				secret, e := manifest.ProtectedFile(binding.CredentialFile, 64<<10)
				clear(secret)
				if e != nil {
					return result, e
				}
			}
			if binding.ContainerRuntime != nil && (category != "builder" || entry.Kind != "container-runtime") {
				return result, fmt.Errorf("runtime packaging policy requires a container-runtime builder")
			}
			if category == "builder" {
				l := binding.Limits
				if l == nil || l.TimeoutSeconds == 0 || l.TimeoutSeconds > 86400 || l.CPUCount == 0 || l.CPUCount > 256 || l.MemoryBytes == 0 || l.InputBytes == 0 || l.OutputBytes == 0 || l.Egress == nil || len(l.Egress) > 64 {
					return result, fmt.Errorf("builder requires explicit bounded time, CPU, memory, input, output and egress limits")
				}
			}
			if binding.Requirement != "" && (category != "builder" || len(binding.Requirement) > 64) {
				return result, fmt.Errorf("logical requirement requires a bounded builder identity")
			}
			if category == "source" && binding.Limits != nil {
				return result, fmt.Errorf("build limits require builder")
			}
		}

		if category != "access" && (len(entry.Methods) > 0 || len(entry.TargetIDs) > 0) {
			return result, fmt.Errorf("access selection fields require an access contribution")
		}
		if entry.StreamEndpoint != "" {
			endpoint, e := url.Parse(entry.StreamEndpoint)
			if e != nil || category != "access" || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || net.ParseIP(endpoint.Hostname()) == nil || !net.ParseIP(endpoint.Hostname()).IsLoopback() || endpoint.Port() == "" {
				return result, fmt.Errorf("access stream endpoint must be a literal loopback HTTP origin")
			}
		}
		if category == "browser" {
			if entry.Mode != "" || entry.Kind != "" || len(entry.Args) > 0 || len(entry.TargetIDs) > 0 || len(entry.Methods) > 0 || entry.TokenFile != "" || entry.RuntimeDirectory != "" {
				return result, fmt.Errorf("browser entry cannot configure backend inputs")
			}
			assets, e := manifest.SnapshotBrowser(install.PluginRoot, entry.Manifest, m)
			if e != nil {
				return result, e
			}
			result.Browsers = append(result.Browsers, installedBrowser{Manifest: m, InstanceID: entry.InstanceID, Assets: assets})
			continue
		}
		pc := pluginhost.Config{ConfiguredPluginID: m.ID, PluginInstanceID: entry.InstanceID, RuntimeParent: runtimeParent}
		switch category {
		case "access":
			if !slices.Contains(m.AccessKinds, entry.Kind) {
				return result, fmt.Errorf("access kind does not match manifest")
			}
			pc.ConfiguredAccessKind = entry.Kind
			pc.AccessAudience = audience
			pc.AccessAuthorization = authorization
		case "source":
			if !slices.Contains(m.SourceKinds, entry.Kind) {
				return result, fmt.Errorf("source kind does not match manifest")
			}
			pc.ConfiguredSourceKind = entry.Kind
		case "builder":
			if !slices.Contains(m.BuilderKinds, entry.Kind) {
				return result, fmt.Errorf("builder kind does not match manifest")
			}
			pc.ConfiguredBuilderKind = entry.Kind
		default:
			return result, fmt.Errorf("providers must use target bindings")
		}
		if entry.Mode != "attach" && (entry.Mode != "local" || cfg.authMode != "none") {
			return result, fmt.Errorf("production extensions require isolated attach mode")
		}
		if entry.Mode == "attach" && (len(entry.Args) > 0 || !filepath.IsAbs(entry.RuntimeDirectory) || !filepath.IsAbs(entry.TokenFile)) {
			return result, fmt.Errorf("invalid extension attach configuration")
		}
		if entry.Mode == "local" {
			if entry.RuntimeDirectory != "" || entry.TokenFile != "" {
				return result, fmt.Errorf("local extension contains attach inputs")
			}
			pc.Executable = executable
			pc.ExecutableSHA256 = m.SHA256
			pc.Args = entry.Args
		}
		start := func(callCtx context.Context) (*pluginhost.Client, error) {
			var c *pluginhost.Client
			var e error
			if entry.Mode == "attach" {
				c, e = attachConfiguredPlugin(callCtx, entry.RuntimeDirectory, entry.TokenFile, pc)
			} else {
				c, e = pluginhost.Launch(callCtx, pc)
			}
			if e != nil {
				return nil, e
			}
			if c.Info.GetBuildDigest() != "sha256:"+m.SHA256 || c.Info.GetPluginVersion() != m.Version || c.SelectedProtocol.GetMajor() != m.ProtocolMajor || c.SelectedProtocol.GetMinor() < m.MinimumProtocolMinor || c.SelectedProtocol.GetMinor() > m.MaximumProtocolMinor {
				_ = c.Close()
				return nil, fmt.Errorf("extension handshake does not match manifest")
			}
			return c, nil
		}
		client, e := start(ctx)
		if e != nil {
			return result, e
		}
		if (category == "source" || category == "builder") && len(entry.Bindings) > 0 && !slices.Contains(client.Info.GetFeatures(), "artifact-transfer-v1") {
			_ = client.Close()
			return result, fmt.Errorf("invocation bindings require artifact-transfer-v1")
		}
		client = pluginhost.Supervise(ctx, client, start)
		result.Clients = append(result.Clients, client)
		result.Backends = append(result.Backends, loadedBackend{Manifest: m, Entry: entry, Client: client})
		if category == "access" {
			methods := entry.Methods
			if len(methods) == 0 {
				methods = m.AccessMethods
			}
			for _, method := range methods {
				if !slices.Contains(m.AccessMethods, method) || !slices.Contains(client.Info.GetSupportedAccessMethods(), method) {
					return result, fmt.Errorf("unadvertised access method")
				}
			}
			for _, schema := range client.Info.GetAcceptedAccessEndpointSchemas() {
				if !slices.Contains(m.EndpointSchemas, schema.GetName()) {
					return result, fmt.Errorf("access endpoint schema exceeds installed manifest")
				}
			}
			descriptors := providergrpc.AccessDescriptors(client.Info.GetAccessDescriptors())
			enabledDescriptors := descriptors[:0]
			for _, descriptor := range descriptors {
				if slices.Contains(methods, descriptor.Method) {
					enabledDescriptors = append(enabledDescriptors, descriptor)
				}
			}
			result.Access = append(result.Access, controlplane.AccessBinding{PluginID: m.ID, PluginVersion: m.Version, PluginInstance: entry.InstanceID, AccessKind: entry.Kind, StreamBasePath: streamBasePath(entry), Audience: audience, Access: providergrpc.NewAccess(client.Access), ProtocolMajor: client.SelectedProtocol.GetMajor(), ProtocolMinor: client.SelectedProtocol.GetMinor(), Features: client.Info.GetFeatures(), Methods: methods, EndpointSchemas: providergrpc.Schemas(client.Info.GetAcceptedAccessEndpointSchemas()), TargetIDs: entry.TargetIDs, Descriptors: enabledDescriptors})
		}
	}
	return result, nil
}

func streamBasePath(entry installedExtension) string {
	if entry.StreamEndpoint == "" {
		return ""
	}
	return "/extension-stream/" + url.PathEscape(entry.InstanceID)
}
