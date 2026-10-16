package labcentral

import (
	"encoding/json"
	"fmt"
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
)

// GatewayIdentity contains public management identity and the owned route target.
// Native credentials and host private keys never belong in this resource data.
type GatewayIdentity struct {
	HostPublicKey string `json:"hostPublicKey"`
	ENI           string `json:"eni"`
}

func InstanceGateway(i Instance) Resource {
	for _, r := range i.Resources {
		if r.Kind == "gateway" {
			return r
		}
	}
	if i.Gateway != nil {
		return Resource{Kind: "gateway"}
	}
	return Resource{}
}

// MachineDialer is the single management path for provider and library callers.
// An absent gateway preserves retained pre-gateway direct management.
func MachineDialer(gateway Resource, private []byte) (*remote.Dialer, error) {
	d := &remote.Dialer{}
	if gateway.Kind == "" {
		return d, nil
	}
	var identity GatewayIdentity
	if gateway.Kind != "gateway" || gateway.Owner == "" || gateway.ID == "" || gateway.Address == "" || !gateway.Complete || json.Unmarshal(gateway.Data, &identity) != nil || identity.HostPublicKey == "" {
		return nil, fmt.Errorf("gateway management identity is incomplete")
	}
	d.GatewayAddress, d.GatewayHostKey, d.PrivateKey = gateway.Address, identity.HostPublicKey, private
	return d, nil
}
