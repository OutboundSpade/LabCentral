package hosted

import (
	"context"
	lc "github.com/OutboundSpade/LabCentral/v2"
)

type exerciseSSH interface {
	WithSSHMaterial(context.Context, string, string, func(lc.Instance, lc.PlannedMachine, lc.Resource, []byte) error) error
}

type terminalSSH interface {
	WithSSHSession(context.Context, string, string, func(lc.Instance, lc.PlannedMachine, lc.Resource, []byte) error) error
}
