package hosted

import (
	"context"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"sync"
)

// AggregateOptions bound service-wide effects across independent engine jobs.
// They supplement (rather than replace) per-operation engine concurrency.
type AggregateOptions struct {
	MaxNativeOperations int `json:"maxNativeOperations,omitempty"`
	MaxSSHSessions      int `json:"maxSSHSessions,omitempty"`
}

func (o AggregateOptions) Effective() AggregateOptions {
	if o.MaxNativeOperations == 0 {
		o.MaxNativeOperations = 8
	}
	if o.MaxSSHSessions == 0 {
		o.MaxSSHSessions = 24
	}
	return o
}
func (o AggregateOptions) Validate() error {
	o = o.Effective()
	for _, v := range []struct {
		name string
		n    int
	}{{"maxNativeOperations", o.MaxNativeOperations}, {"maxSSHSessions", o.MaxSSHSessions}} {
		if v.n < 1 || v.n > 64 {
			return fmt.Errorf("aggregate.%s must be 1–64, or zero for the default", v.name)
		}
	}
	return nil
}
func acquireSlot(ctx context.Context, slots chan struct{}) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-slots
			return nil, err
		}
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// NewBudgetedProvider must wrap the provider before opening the hosted engine.
// All machine creates/deletes, including recovery, share this one budget.
func NewBudgetedProvider(p lc.Provider, o AggregateOptions) (*BudgetedProvider, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return &BudgetedProvider{p, make(chan struct{}, o.Effective().MaxNativeOperations), make(chan struct{}, o.Effective().MaxSSHSessions)}, nil
}

type BudgetedProvider struct {
	lc.Provider
	slots    chan struct{}
	sshSlots chan struct{}
}

func (p *BudgetedProvider) Execute(ctx context.Context, c lc.Command, o lc.Observer) (lc.Result, error) {
	if c.Action == "image.build" || c.Action == "image.abandon" || (c.Action == "machine.create" || c.Action == "gateway.create") || (c.Action == "resource.destroy" && (c.Prior.Kind == "machine" || c.Prior.Kind == "gateway")) {
		release, err := acquireSlot(ctx, p.slots)
		if err != nil {
			return lc.Result{}, err
		}
		defer release()
	}
	// Creates and builds include guest readiness/validation SSH in adapters.
	// Reserve conservatively for the whole call; access obtains its lease before
	// library machine guards, avoiding a guard/budget inversion with guest checks.
	if c.Action == "image.build" || (c.Action == "machine.create" || c.Action == "gateway.create") || c.Action == "access.open" || c.Action == "access.close" || (c.Action == "discover" && c.Machine.Name != "") {
		if lease, ok := ctx.Value(sshBudgetKey{}).(chan struct{}); ok && lease == p.sshSlots {
			return p.Provider.Execute(ctx, c, o)
		}
		release, err := acquireSlot(ctx, p.sshSlots)
		if err != nil {
			return lc.Result{}, err
		}
		defer release()
	}
	return p.Provider.Execute(ctx, c, o)
}
func (s *Service) acquireSSH(ctx context.Context) (func(), error) {
	if err := s.initSSH(); err != nil {
		return nil, err
	}
	return acquireSlot(ctx, s.sshSlots)
}

func (s *Service) initSSH() error {
	if err := s.Config.Aggregate.Validate(); err != nil {
		return err
	}
	s.sshOnce.Do(func() {
		if s.sshSlots == nil {
			s.sshSlots = make(chan struct{}, s.Config.Aggregate.Effective().MaxSSHSessions)
		}
	})
	return nil
}

// Participants use at most half the SSH pool, with per-user and per-team caps.
// Coaches have a separate allowance of up to four, reduced for small pools to
// retain at least one control slot even when both terminal allowances are full.
// Admission never queues behind another long-lived session.
func (s *Service) acquireTerminal(ctx context.Context, user, team string) (func(), error) {
	return s.acquireTerminalForRole(ctx, user, team, false)
}

func (s *Service) acquireTerminalForRole(ctx context.Context, user, team string, coach bool) (func(), error) {
	if err := s.initSSH(); err != nil {
		return nil, err
	}
	s.terminalMu.Lock()
	defer s.terminalMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	participantLimit := cap(s.sshSlots) / 2
	coachLimit := min(4, cap(s.sshSlots)-participantLimit-1)
	if (coach && s.coachTerminals >= coachLimit) ||
		(!coach && (s.terminalCount >= participantLimit || s.terminalUsers[user] >= 2 || s.terminalTeams[team] >= 8)) {
		return nil, fmt.Errorf("terminal capacity reached")
	}
	select {
	case s.sshSlots <- struct{}{}:
	default:
		return nil, fmt.Errorf("SSH capacity reached")
	}
	if s.terminalUsers == nil {
		s.terminalUsers, s.terminalTeams = map[string]int{}, map[string]int{}
	}
	if coach {
		s.coachTerminals++
	} else {
		s.terminalCount++
		s.terminalUsers[user]++
		s.terminalTeams[team]++
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.terminalMu.Lock()
			defer s.terminalMu.Unlock()
			<-s.sshSlots
			if coach {
				s.coachTerminals--
				return
			}
			s.terminalCount--
			s.terminalUsers[user]--
			if s.terminalUsers[user] == 0 {
				delete(s.terminalUsers, user)
			}
			s.terminalTeams[team]--
			if s.terminalTeams[team] == 0 {
				delete(s.terminalTeams, team)
			}
		})
	}, nil
}

type sshBudgetKey struct{}

func (s *Service) sshContext(ctx context.Context) (context.Context, func(), error) {
	release, err := s.acquireSSH(ctx)
	if err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, sshBudgetKey{}, s.sshSlots), release, nil
}
