package aws

import (
	"context"
	"errors"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"testing"
)

func TestUnsupportedRequestsFailBeforeNativeConfiguration(t *testing.T) {
	p := &Provider{}
	for _, action := range []string{"check", "image.build", "network.create", "machine.create"} {
		c := lc.Command{Action: action, Environment: lc.Environment{Platform: "aws"}, Source: lc.Source{Lab: lc.Lab{Images: map[string]lc.Image{"web": {OS: "windows"}}, Machines: map[string]lc.Machine{"web": {Image: "web", Forwarding: true}}}}}
		_, err := p.Execute(context.Background(), c, nil)
		var typed *lc.Error
		if !errors.As(err, &typed) || typed.Code != lc.Unsupported || typed.Field != "machines.web.forwarding" {
			t.Fatalf("%s reached native configuration: %v", action, err)
		}
	}
	c := lc.Command{Action: "machine.create", Environment: lc.Environment{Platform: "aws"}, Machine: lc.PlannedMachine{Name: "web", Spec: lc.Machine{NICs: make([]lc.NIC, 9)}}}
	_, err := p.Execute(context.Background(), c, nil)
	var typed *lc.Error
	if !errors.As(err, &typed) || typed.Code != lc.Unsupported || typed.Field != "machines.web.nics[8]" {
		t.Fatal("machine request bypassed capabilities", err)
	}
}
