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
		c := lc.Command{Action: action, Environment: lc.Environment{Platform: "aws"}, Source: lc.Source{Lab: lc.Lab{Images: map[string]lc.Image{"web": {OS: "windows"}}}}}
		_, err := p.Execute(context.Background(), c, nil)
		var typed *lc.Error
		if !errors.As(err, &typed) || typed.Code != lc.Unsupported || typed.Field != "images.web.os" {
			t.Fatalf("%s reached native configuration: %v", action, err)
		}
	}
	c := lc.Command{Action: "machine.create", Environment: lc.Environment{Platform: "aws"}, Machine: lc.PlannedMachine{Name: "web", Spec: lc.Machine{NICs: []lc.NIC{{Network: "lab"}, {Network: "lab"}}}}}
	_, err := p.Execute(context.Background(), c, nil)
	var typed *lc.Error
	if !errors.As(err, &typed) || typed.Code != lc.Unsupported || typed.Field != "machines.web.nics[1]" {
		t.Fatal("machine request bypassed capabilities", err)
	}
}
