package qualification

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/tools/proxmox-qualification/probe"
)

func TestContractTESTPROXMOX026ProbeTranscriptMapsToSignedEvidence(t *testing.T) {
	request := probe.Request{
		SchemaVersion: probe.RequestSchema,
		RunID:         "qualification.probe-001",
		Nonce:         strings.Repeat("a", 64),
		Attempts:      3,
		TimeoutMillis: 500,
		Targets: []probe.Target{
			{Kind: "intended_segment", Address: "10.220.0.11:9443"},
			{Kind: "management", Address: "10.0.0.2:8006"},
			{Kind: "cross_instance", Address: "10.221.0.10:9443"},
		},
	}
	agentDigest := "sha256:" + strings.Repeat("b", 64)
	handler, err := probe.NewHandler(agentDigest)
	if err != nil {
		t.Fatal(err)
	}
	handler.Dial = func(_ context.Context, address string) error {
		if address == request.Targets[0].Address {
			return nil
		}
		return context.DeadlineExceeded
	}
	times := []time.Time{
		time.Date(2026, 9, 2, 20, 30, 0, 0, time.UTC),
		time.Date(2026, 9, 2, 20, 30, 1, 0, time.UTC),
	}
	handler.Now = func() time.Time {
		value := times[0]
		if len(times) > 1 {
			times = times[1:]
		}
		return value
	}
	response, err := handler.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := BindProbeEvidence(request, response, ProbeBinding{
		SourceInstanceID:         "instance.source",
		SourceVMID:               910001,
		CrossInstanceID:          "instance.cross",
		ExpectedAgentImageDigest: agentDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 3 || evidence[0].Successes != 3 || evidence[1].Successes != 0 ||
		evidence[2].Successes != 0 || evidence[2].DestinationInstance != "instance.cross" ||
		evidence[0].TranscriptDigest != evidence[2].TranscriptDigest {
		t.Fatalf("mapped evidence = %#v", evidence)
	}
	for _, item := range evidence {
		for _, target := range request.Targets {
			if strings.Contains(item.EndpointDigest, target.Address) || strings.Contains(item.TranscriptDigest, target.Address) {
				t.Fatalf("raw target leaked into evidence: %#v", item)
			}
		}
	}

	t.Run("topology and expected image remain external bindings", func(t *testing.T) {
		invalid := response
		invalid.AgentImageDigest = "sha256:" + strings.Repeat("c", 64)
		if _, err := BindProbeEvidence(request, invalid, ProbeBinding{
			SourceInstanceID: "instance.source", SourceVMID: 910001,
			CrossInstanceID: "instance.source", ExpectedAgentImageDigest: agentDigest,
		}); err == nil {
			t.Fatal("invalid topology and image binding was accepted")
		}
	})
}
