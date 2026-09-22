package controlplane

import (
	"context"
	"encoding/json"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

type TopologyAttachment struct {
	NetworkRef string `json:"networkRef"`
	IPv4       string `json:"ipv4,omitempty"`
	IPv6       string `json:"ipv6,omitempty"`
	MAC        string `json:"mac,omitempty"`
}
type TopologyResource struct {
	ID         string                    `json:"id"`
	Kind       string                    `json:"kind"`
	Phase      string                    `json:"phase"`
	Interfaces []TopologyAttachment      `json:"interfaces"`
	Actions    []domain.AccessDescriptor `json:"actions"`
}
type Topology struct {
	InstanceID string             `json:"instanceId"`
	Generation uint64             `json:"generation"`
	Resources  []TopologyResource `json:"resources"`
}

func (s *Service) Topology(ctx context.Context, subject domain.UserID, id domain.InstanceID) (Topology, error) {
	if s.operationsAuthorizer == nil {
		return Topology{}, ErrAccessUnavailable
	}
	if _, err := s.operationsAuthorizer.AuthorizeInstance(ctx, subject, application.ActionInstanceRead, id); err != nil {
		return Topology{}, err
	}
	instance, resources, err := s.GetInstance(ctx, id)
	if err != nil {
		return Topology{}, err
	}
	result := Topology{InstanceID: string(id), Generation: uint64(instance.Generation), Resources: []TopologyResource{}}
	canAccess := false
	if s.authorizer != nil {
		_, err = s.authorizer.AuthorizeInstance(ctx, subject, application.ActionAccessSessionRead, id)
		canAccess = err == nil
	}
	methods := map[string]bool{}
	snapshot, e := s.store.GetTargetSnapshot(ctx, instance.ProviderTargetID)
	if e == nil && snapshot.ValidUntil.After(time.Now()) {
		target, e := s.targets.Acquire(ctx, instance.ProviderTargetID, application.InvocationScope{}, false)
		if e == nil {
			discovery, e := target.Provider.DecodeDiscovery(snapshot.DiscoveryJSON)
			target.Close()
			if e == nil && snapshot.ConfigFingerprint == target.Context.ConfigFingerprint {
				for _, c := range discovery.Capabilities {
					if c.Support == "SUPPORT_NATIVE" || c.Support == "SUPPORT_EMULATED" {
						methods[c.ID] = true
					}
				}
			}
		}
	}
	for _, resource := range resources {
		if resource.Kind != domain.ResourceVM && resource.Kind != domain.ResourceContainer && resource.Kind != domain.ResourceNetwork {
			continue
		}
		var portable struct {
			Spec struct {
				Interfaces []TopologyAttachment `json:"interfaces"`
			} `json:"spec"`
		}
		if err := json.Unmarshal(resource.CanonicalJSON, &portable); err != nil {
			return Topology{}, err
		}
		node := TopologyResource{ID: string(resource.ID), Kind: string(resource.Kind), Phase: string(resource.Phase), Interfaces: portable.Spec.Interfaces, Actions: []domain.AccessDescriptor{}}
		if node.Interfaces == nil {
			node.Interfaces = []TopologyAttachment{}
		}
		if canAccess && (resource.Phase == domain.ResourceReady || resource.Phase == domain.ResourcePaused) {
			for _, d := range s.access.MethodsFor(string(instance.ProviderTargetID), string(resource.Kind)) {
				if methods["access."+d.Method] {
					node.Actions = append(node.Actions, d)
				}
			}
		}
		result.Resources = append(result.Resources, node)
	}
	return result, nil
}
