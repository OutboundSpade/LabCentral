package instructor

import (
	"errors"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
)

func TestParticipantTerminalGrantsValidateAgainstSource(t *testing.T) {
	src, _ := fixture(t)
	src.Lab.Machines["no-ssh"] = lc.Machine{Access: []string{"desktop"}}
	for _, tc := range []struct {
		name           string
		grants, hidden []string
		valid          bool
	}{
		{"legacy", nil, nil, true},
		{"opted-in", []string{"workstation"}, nil, true},
		{"unknown", []string{"missing"}, nil, false},
		{"no-ssh", []string{"no-ssh"}, nil, false},
		{"duplicate", []string{"workstation", "workstation"}, nil, false},
		{"hidden", []string{"workstation"}, []string{"workstation"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := Bundle{Version: 3, Visibility: Visibility{Audience: "students", ParticipantTerminals: tc.grants, HiddenMachines: tc.hidden}}
			err := bundle.Validate(src)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var problem *lc.Error
			if !errors.As(err, &problem) || problem.Field != "visibility.participantTerminals" {
				t.Fatal("grant rejected for wrong reason", err)
			}
		})
	}
}
