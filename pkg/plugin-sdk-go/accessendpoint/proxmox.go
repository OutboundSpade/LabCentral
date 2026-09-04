// Package accessendpoint contains closed, versioned endpoint envelopes passed
// directly from provider plugins to access plugins. These envelopes are never
// persisted by the control plane or returned to browsers.
package accessendpoint

const ProxmoxVNCSchema = "proxmox.vnc-endpoint/v1.0"

type ProxmoxVNC struct {
	SchemaVersion       string `json:"schemaVersion"`
	TargetID            string `json:"targetId"`
	InstanceID          string `json:"instanceId"`
	LogicalResourceID   string `json:"logicalResourceId"`
	Method              string `json:"method"`
	ProviderStateDigest string `json:"providerStateDigest"`
	Host                string `json:"host"`
	ServerName          string `json:"serverName"`
	Port                uint16 `json:"port"`
	CACertificatePEM    string `json:"caCertificatePEM"`
	Password            string `json:"password"`
}
