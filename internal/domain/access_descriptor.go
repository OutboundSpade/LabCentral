package domain

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var accessMethodID = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,63}$`)

func ValidAccessMethod(method string) bool { return accessMethodID.MatchString(method) }

type AccessEndpointSchema struct {
	Name  string `json:"name"`
	Major uint32 `json:"major"`
	Minor uint32 `json:"minor"`
}

type AccessDescriptor struct {
	EndpointSchemas       []AccessEndpointSchema `json:"endpointSchemas"`
	Method                string                 `json:"method"`
	Title                 string                 `json:"title"`
	TargetKinds           []string               `json:"targetKinds"`
	Presentation          string                 `json:"presentation"`
	MaximumOfflineSeconds uint32                 `json:"maximumOfflineSeconds"`
	Renderer              string                 `json:"renderer,omitempty"`
}

func (d AccessDescriptor) Validate() error {
	if (d.Method != "ssh" && d.Method != "vnc" && !strings.Contains(d.Method, ".")) || !ValidAccessMethod(d.Method) || d.Title == "" || len(d.Title) > 128 || d.MaximumOfflineSeconds == 0 || d.MaximumOfflineSeconds > 30 || len(d.TargetKinds) == 0 || len(d.TargetKinds) > 3 {
		return fmt.Errorf("invalid access descriptor or unbounded closure")
	}
	if !slices.Contains([]string{"terminal", "graphical", "credential-download"}, d.Presentation) {
		return fmt.Errorf("unsupported access presentation")
	}
	if d.Renderer != "" {
		parts := strings.Split(d.Renderer, "/")
		if d.Presentation == "credential-download" || len(parts) != 2 || len(d.Renderer) > 257 {
			return fmt.Errorf("invalid access renderer binding")
		}
		for _, part := range parts {
			if _, err := NewOperationID(part); err != nil {
				return fmt.Errorf("invalid access renderer identity")
			}
		}
	}
	if len(d.EndpointSchemas) == 0 || len(d.EndpointSchemas) > 32 {
		return fmt.Errorf("endpoint schemas required")
	}
	schemas := map[string]bool{}
	for _, schema := range d.EndpointSchemas {
		key := fmt.Sprintf("%s/%d", schema.Name, schema.Major)
		if !ValidAccessMethod(schema.Name) || schema.Major == 0 || schemas[key] {
			return fmt.Errorf("invalid endpoint schema")
		}
		schemas[key] = true
	}
	seen := map[string]bool{}
	for _, kind := range d.TargetKinds {
		if seen[kind] || !slices.Contains([]string{"VM", "Container", "Network"}, kind) {
			return fmt.Errorf("invalid access target kind")
		}
		seen[kind] = true
	}
	if slices.Contains(d.TargetKinds, "Network") && d.Presentation != "credential-download" {
		return fmt.Errorf("network access requires credential delivery")
	}
	return nil
}
func LegacyAccessDescriptor(method string) (AccessDescriptor, bool) {
	presentation := "graphical"
	if method == "ssh" {
		presentation = "terminal"
	} else if method != "vnc" {
		return AccessDescriptor{}, false
	}
	return AccessDescriptor{Method: method, Title: method, TargetKinds: []string{"VM", "Container"}, Presentation: presentation, MaximumOfflineSeconds: 30}, true
}
