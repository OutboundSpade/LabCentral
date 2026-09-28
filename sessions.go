package labcentral

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type AccessInfo struct {
	ID       string    `json:"id"`
	URL      string    `json:"url"`
	Username string    `json:"username,omitempty"`
	Password string    `json:"password,omitempty"`
	Expires  time.Time `json:"expires"`
}

// OpenDesktop discloses credentials only in its explicit result, never in events.
func (e *Engine) OpenDesktop(ctx context.Context, instance, machine string) (AccessInfo, error) {
	var info AccessInfo
	err := e.withLock(func() error {
		i, m, r, key, err := e.SSHMaterial(ctx, instance, machine)
		if err != nil {
			return err
		}
		if !Contains(m.Spec.Access, "desktop") {
			return problem(Unsupported, machine, "desktop access is not declared")
		}
		cmd := Command{Action: "access.open", Operation: id(), Owner: instance, Environment: i.Plan.Environment, Machine: m, Prior: r}
		if err = e.state.Put(ctx, "access", cmd.Operation, cmd); err != nil {
			return err
		}
		cmd.ClientPrivateKey = key
		result, err := e.provider.Execute(ctx, cmd, e.observer)
		if err != nil {
			return err
		}
		return json.Unmarshal(result.Resource.Data, &info)
	})
	return info, err
}
func (e *Engine) CloseAccess(ctx context.Context, session string) error {
	return e.withLock(func() error { return e.closeAccess(ctx, session) })
}
func (e *Engine) closeAccess(ctx context.Context, session string) error {
	var cmd Command
	if err := e.state.Get(ctx, "access", session, &cmd); err != nil {
		return err
	}
	key, err := os.ReadFile(filepath.Join(e.state.Root, "credentials", cmd.Owner, "id_ed25519"))
	if err != nil {
		return err
	}
	cmd.ClientPrivateKey = key
	cmd.Action = "access.close"
	r, err := e.provider.Execute(ctx, cmd, e.observer)
	if err != nil {
		return err
	}
	if !r.Absent {
		return fmt.Errorf("access revocation is unconfirmed")
	}
	return e.state.Delete(ctx, "access", session)
}
func (e *Engine) closeInstanceAccess(ctx context.Context, owner string) error {
	rows, err := e.state.List(ctx, "access")
	if err != nil {
		return err
	}
	for _, b := range rows {
		var c Command
		if err = json.Unmarshal(b, &c); err != nil {
			return err
		}
		if c.Owner == owner {
			if err = e.closeAccess(ctx, c.Operation); err != nil {
				return err
			}
		}
	}
	return nil
}
