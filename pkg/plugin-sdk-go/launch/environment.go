// Package launch defines the deliberately small inherited environment used by
// the LabCentral plugin supervisor. Values are launch-scoped and never include
// provider target credentials.
package launch

const (
	RegistrationEndpoint = "LABCENTRAL_PLUGIN_REGISTRATION_ENDPOINT"
	ServiceEndpoint      = "LABCENTRAL_PLUGIN_SERVICE_ENDPOINT"
	Token                = "LABCENTRAL_PLUGIN_LAUNCH_TOKEN"
	TokenFile            = "LABCENTRAL_PLUGIN_LAUNCH_TOKEN_FILE"
	ConfiguredPluginID   = "LABCENTRAL_PLUGIN_CONFIGURED_ID"
	PluginInstanceID     = "LABCENTRAL_PLUGIN_INSTANCE_ID"
	RuntimeDirectory     = "LABCENTRAL_PLUGIN_RUNTIME_DIR"
	SocketMode           = "LABCENTRAL_PLUGIN_SOCKET_MODE"
)
