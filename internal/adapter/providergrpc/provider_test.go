package providergrpc

import (
	"context"
	"errors"
	"io"
	"testing"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"google.golang.org/grpc"
)

type streamClient struct {
	wire.ProviderServiceClient
	results []*wire.ApplyResourceResponse
	failure error
}

func (c *streamClient) ApplyResource(context.Context, *wire.ApplyResourceRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[wire.ApplyResourceResponse], error) {
	return &applyStream{results: c.results, failure: c.failure}, nil
}

type applyStream struct {
	grpc.ClientStream
	results []*wire.ApplyResourceResponse
	failure error
}

func (s *applyStream) Recv() (*wire.ApplyResourceResponse, error) {
	if len(s.results) > 0 {
		v := s.results[0]
		s.results = s.results[1:]
		return v, nil
	}
	if s.failure != nil {
		return nil, s.failure
	}
	return nil, io.EOF
}
func TestMutationStreamRequiresExactlyOneTerminalResult(t *testing.T) {
	terminal := &wire.ApplyResourceResponse{Event: &wire.InvocationEvent{Event: &wire.InvocationEvent_Result{Result: &wire.ResourceResult{ObservedStateJson: []byte(`{"ready":true}`)}}}}
	for _, c := range []struct {
		name      string
		responses []*wire.ApplyResourceResponse
		failure   error
		valid     bool
	}{{"missing", nil, nil, false}, {"single", []*wire.ApplyResourceResponse{terminal}, nil, true}, {"duplicate", []*wire.ApplyResourceResponse{terminal, terminal}, nil, false}, {"response loss", []*wire.ApplyResourceResponse{terminal}, errors.New("connection lost"), false}} {
		t.Run(c.name, func(t *testing.T) {
			result, err := New(&streamClient{results: c.responses, failure: c.failure}).Invoke(context.Background(), "apply", app.ProviderMutation{})
			if (err == nil) != c.valid || c.valid && result == nil {
				t.Fatal(result, err)
			}
		})
	}
}
