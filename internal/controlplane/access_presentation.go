package controlplane

import (
	"context"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"net/url"
)

type AccessPresentation struct {
	Method       string `json:"method"`
	Presentation string `json:"presentation"`
	Renderer     string `json:"renderer,omitempty"`
	StreamPath   string `json:"streamPath,omitempty"`
}

func (s *Service) AccessPresentation(ctx context.Context, subject domain.UserID, id domain.AccessSessionID) (AccessPresentation, error) {
	record, err := s.GetAccessSession(ctx, subject, id)
	if err != nil {
		return AccessPresentation{}, err
	}
	binding, err := s.access.Get(record.AccessPluginInstance)
	if err != nil {
		return AccessPresentation{}, err
	}
	for _, d := range binding.Descriptors {
		if d.Method != record.Method {
			continue
		}
		result := AccessPresentation{Method: d.Method, Presentation: d.Presentation, Renderer: d.Renderer}
		base := binding.StreamBasePath
		if base != "" && d.Presentation != "credential-download" {
			result.StreamPath = base + "/v1/access/" + url.PathEscape(string(id)) + "/websocket"
		}
		return result, nil
	}
	return AccessPresentation{}, ErrAccessUnsupported
}
