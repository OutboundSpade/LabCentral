package providergrpc

import (
	"context"
	"strings"
	"testing"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"google.golang.org/grpc"
)

type digestCheckingAccessClient struct {
	wire.AccessServiceClient
	t     *testing.T
	calls int
}

func (c *digestCheckingAccessClient) CreateAccess(_ context.Context, r *wire.CreateAccessRequest, _ ...grpc.CallOption) (*wire.CreateAccessResponse, error) {
	c.calls++
	if r.GetEndpointDigest() != strings.Repeat("a", 64) || r.GetProviderStateDigest() != strings.Repeat("b", 64) {
		c.t.Fatal("noncanonical digest crossed the plugin wire boundary")
	}
	return &wire.CreateAccessResponse{}, nil
}
func TestAccessCreateCanonicalizesDomainDigestForPluginWire(t *testing.T) {
	for _, prefix := range []string{"", "sha256:"} {
		client := &digestCheckingAccessClient{t: t}
		_, err := NewAccess(client).Create(context.Background(), app.AccessCreate{EndpointDigest: prefix + strings.Repeat("a", 64), StateDigest: prefix + strings.Repeat("b", 64)})
		if err != nil || client.calls != 1 {
			t.Fatalf("valid domain digest rejected: %v", err)
		}
	}
	for _, request := range []app.AccessCreate{
		{EndpointDigest: "invalid", StateDigest: strings.Repeat("b", 64)},
		{EndpointDigest: strings.Repeat("a", 64), StateDigest: "invalid"},
	} {
		client := &digestCheckingAccessClient{t: t}
		if _, err := NewAccess(client).Create(context.Background(), request); err == nil || client.calls != 0 {
			t.Fatal("invalid digest reached plugin")
		}
	}
}
