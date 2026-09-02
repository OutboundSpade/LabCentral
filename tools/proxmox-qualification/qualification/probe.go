package qualification

import (
	"errors"

	"github.com/OutboundSpade/LabCentral/tools/proxmox-qualification/probe"
)

// ProbeBinding supplies topology facts that are intentionally not disclosed by
// the guest agent's response.
type ProbeBinding struct {
	SourceInstanceID         string
	SourceVMID               uint32
	CrossInstanceID          string
	ExpectedAgentImageDigest string
}

// BindProbeEvidence converts one validated three-target probe transcript into
// the exact signed-evidence records. All three records share the canonical
// transcript digest so none can be detached from the complete ordered run.
func BindProbeEvidence(request probe.Request, response probe.Response, binding ProbeBinding) ([]ProbeEvidence, error) {
	if !stableIDPattern.MatchString(binding.SourceInstanceID) || binding.SourceVMID < 100 ||
		!stableIDPattern.MatchString(binding.CrossInstanceID) || binding.CrossInstanceID == binding.SourceInstanceID ||
		!digestPattern.MatchString(binding.ExpectedAgentImageDigest) {
		return nil, errors.New("qualification probe topology binding is invalid")
	}
	if err := probe.ValidateResponse(response, request, binding.ExpectedAgentImageDigest); err != nil {
		return nil, errors.New("qualification probe response binding is invalid")
	}
	transcriptDigest, err := probe.TranscriptDigest(request, response)
	if err != nil {
		return nil, errors.New("qualification probe transcript is invalid")
	}
	result := make([]ProbeEvidence, len(response.Results))
	for index, observation := range response.Results {
		result[index] = ProbeEvidence{
			Kind:             observation.Kind,
			SourceInstanceID: binding.SourceInstanceID,
			SourceVMID:       binding.SourceVMID,
			EndpointDigest:   observation.EndpointDigest,
			Protocol:         "tcp",
			Attempts:         observation.Attempts,
			Successes:        observation.Successes,
			StartedAt:        response.StartedAt,
			CompletedAt:      response.CompletedAt,
			AgentImageDigest: response.AgentImageDigest,
			TranscriptDigest: transcriptDigest,
		}
		if observation.Kind == "cross_instance" {
			result[index].DestinationInstance = binding.CrossInstanceID
		}
	}
	return result, nil
}
