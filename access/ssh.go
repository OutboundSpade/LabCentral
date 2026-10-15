package access

import (
	"context"
	"errors"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func Shell(ctx context.Context, e *lc.Engine, instance, machine string, in *os.File, out, errOut io.Writer) (int, error) {
	inst, m, r, key, err := e.SSHMaterial(ctx, instance, machine)
	if err != nil {
		return 0, err
	}
	if !lc.Contains(m.Spec.Access, "ssh") {
		return 0, &lc.Error{Code: lc.Unsupported, Field: machine, Message: "SSH access is not declared"}
	}
	dialer, err := lc.MachineDialer(lc.InstanceGateway(inst), key)
	if err != nil {
		return 0, err
	}
	defer dialer.Close()
	client, err := dialer.Dial(ctx, r.Address, m.SSHUser(), key, m.HostPublicKey)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return 0, err
	}
	defer session.Close()
	session.Stdin = in
	session.Stdout = out
	session.Stderr = errOut
	fd := int(in.Fd())
	if term.IsTerminal(fd) {
		state, err := term.MakeRaw(fd)
		if err != nil {
			return 0, err
		}
		defer term.Restore(fd, state)
		w, h, err := term.GetSize(fd)
		if err != nil {
			return 0, err
		}
		if err = session.RequestPty("xterm-256color", h, w, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
			return 0, err
		}
		resize := make(chan os.Signal, 1)
		signal.Notify(resize, syscall.SIGWINCH)
		defer signal.Stop(resize)
		done := make(chan struct{})
		defer close(done)
		go func() {
			for {
				select {
				case <-done:
					return
				case <-resize:
					if w, h, err := term.GetSize(fd); err == nil {
						_ = session.WindowChange(h, w)
					}
				}
			}
		}()
	}
	if err = session.Shell(); err != nil {
		return 0, err
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Close()
		case <-done:
		}
	}()
	err = session.Wait()
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		return exit.ExitStatus(), nil
	}
	return 0, err
}
