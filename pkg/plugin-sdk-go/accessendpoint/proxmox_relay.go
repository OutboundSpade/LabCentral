package accessendpoint

const ProxmoxVNCRelaySchema = "proxmox.vnc-relay-endpoint/v1.0"

// ProxmoxVNCRelay is an invocation-only capability, never durable state or a URL.
type ProxmoxVNCRelay struct {
	SchemaVersion       string `json:"schemaVersion"`
	TargetID            string `json:"targetId"`
	InstanceID          string `json:"instanceId"`
	LogicalResourceID   string `json:"logicalResourceId"`
	Method              string `json:"method"`
	ProviderStateDigest string `json:"providerStateDigest"`
	RelayID             string `json:"relayId"`
	RelayToken          string `json:"relayToken"`
	Password            string `json:"password"`
}
