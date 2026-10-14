package labcentral

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
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

// AccessOptions carries an already-authorized caller's opaque workspace identity.
// Provider processes do not authenticate users or decide membership.
type AccessOptions struct {
	Principal       string `json:"principal,omitempty"`
	Expires         string `json:"expires,omitempty"` // RFC3339, at most eight hours away
	RetainWorkspace bool   `json:"retainWorkspace,omitempty"`
}

// Machine operations share a lifecycle guard, so another machine can be used
// concurrently while recovery and destruction still require the whole instance.
func (e *Engine) withMachineLock(ctx context.Context, instance, machine string, fn func() error) error {
	unlock, err := e.state.LockKeySharedContext(ctx, "instance:"+instance)
	if err != nil {
		return problem(Busy, "instance", err.Error())
	}
	defer unlock()
	release, err := e.state.LockKeyContext(ctx, "machine:"+instance+"\x00"+machine)
	if err != nil {
		return problem(Busy, "machine", err.Error())
	}
	defer release()
	return fn()
}

// OpenDesktop discloses credentials only in its explicit result, never in events.
func (e *Engine) OpenDesktop(ctx context.Context, instance, machine string) (AccessInfo, error) {
	return e.OpenDesktopWithOptions(ctx, instance, machine, AccessOptions{})
}

func (e *Engine) OpenDesktopWithOptions(ctx context.Context, instance, machine string, opts AccessOptions) (AccessInfo, error) {
	var info AccessInfo
	if len(opts.Principal) > 256 || (opts.RetainWorkspace && opts.Principal == "") {
		return info, problem(Invalid, "access.principal", "a bounded principal is required for retained workspaces")
	}
	if opts.Expires != "" {
		expires, err := time.Parse(time.RFC3339, opts.Expires)
		if err != nil || !expires.After(time.Now()) || expires.After(time.Now().Add(8*time.Hour)) {
			return info, problem(Invalid, "access.expires", "expiry must be within eight hours")
		}
	}
	err := e.withMachineLock(ctx, instance, machine, func() error {
		i, m, r, key, err := e.SSHMaterial(ctx, instance, machine)
		if err != nil {
			return err
		}
		if !Contains(m.Spec.Access, "desktop") {
			return problem(Unsupported, machine, "desktop access is not declared")
		}
		if i.Status != "ready" {
			return problem(Busy, "instance", "lab is not ready")
		}
		if opts.RetainWorkspace && i.Plan.Environment.Platform != "aws" {
			return problem(Unsupported, "access", "retained desktop workspaces currently require AWS")
		}
		operation := id()
		if opts.Principal != "" {
			operation = fmt.Sprintf("%x", sha256.Sum256([]byte(instance+"\x00"+machine+"\x00"+opts.Principal)))[:24]
		}
		cmd := Command{Action: "access.open", Operation: operation, Owner: instance, Environment: i.Plan.Environment, Machine: m, Prior: r, Access: opts, Gateway: InstanceGateway(i)}
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
	var cmd Command
	if err := e.state.Get(ctx, "access", session, &cmd); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	return e.withMachineLock(ctx, cmd.Owner, cmd.Machine.Name, func() error { return e.closeAccess(ctx, session) })
}
func (e *Engine) closeAccess(ctx context.Context, session string) error {
	var cmd Command
	if err := e.state.Get(ctx, "access", session, &cmd); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
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
