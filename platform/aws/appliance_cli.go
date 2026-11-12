package aws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"golang.org/x/crypto/ssh"
)

var appliancePrompt = regexp.MustCompile(`(?:^|[\r\n])([a-zA-Z0-9_.-]+@[a-zA-Z0-9_.-]+)(?:\([a-zA-Z0-9_-]+\))?(?::[~/a-zA-Z0-9_.()\-]+)?[>#\$][ \t]*$`)
var applianceANSI = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

// Vendor CLI runs in the verified guest PTY. There is no local shell or
// author-controlled executable on the operator. Each prompt/output is bounded.
func applianceCLI(ctx context.Context, client *ssh.Client, commands []string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { session.Close() })
	defer stop()
	input, err := session.StdinPipe()
	if err != nil {
		return "", err
	}
	output, err := session.StdoutPipe()
	if err != nil {
		return "", err
	}
	session.Stderr = io.Discard
	if err = session.RequestPty("vt100", 40, 160, ssh.TerminalModes{ssh.ECHO: 0}); err != nil {
		return "", err
	}
	if err = session.Shell(); err != nil {
		return "", err
	}
	chunks := make(chan []byte)
	failures := make(chan error, 1)
	go func() {
		for {
			buf := make([]byte, 2048)
			n, err := output.Read(buf)
			if n > 0 {
				select {
				case chunks <- buf[:n]:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				failures <- err
				return
			}
		}
	}()
	identity := ""
	read := func() (string, error) {
		var b strings.Builder
		for {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case err := <-failures:
				return "", err
			case chunk := <-chunks:
				if b.Len()+len(chunk) > 256<<10 {
					return "", fmt.Errorf("appliance CLI output exceeds bound")
				}
				b.Write(chunk)
				clean := applianceANSI.ReplaceAllString(b.String(), "")
				match := appliancePrompt.FindStringSubmatch(clean)
				if len(match) > 1 && (identity == "" || match[1] == identity) {
					identity = match[1]
					return clean, nil
				}
			}
		}
	}
	if _, err = read(); err != nil {
		return "", err
	}
	var result strings.Builder
	for index, command := range commands {
		if len(command) > 2048 || strings.ContainsAny(command, "\n\r\x00") {
			return "", fmt.Errorf("invalid appliance command")
		}
		if _, err = io.WriteString(input, command+"\n"); err != nil {
			return "", err
		}
		out, err := read()
		if err != nil {
			return "", err
		}
		lower := strings.ToLower(out)
		for _, failure := range []string{"invalid syntax", "unknown command", "server error", "commit failed", "validation error", "configuration is invalid", "invalid value", "invalid reference", "is not a valid", "error:"} {
			if strings.Contains(lower, failure) {
				return "", fmt.Errorf("appliance rejected configuration step %d", index+1)
			}
		}
		if result.Len()+len(out) > 1<<20 {
			return "", fmt.Errorf("appliance CLI operation exceeds output bound")
		}
		result.WriteString(out)
	}
	return result.String(), nil
}

func (p *Provider) configurePANOS(ctx context.Context, client *ssh.Client, c lc.Command, commands []string) error {
	raw, _ := json.Marshal(struct {
		Owner, Machine string
		Commands       []string
	}{c.Owner, c.Machine.Name, commands})
	sum := sha256.Sum256(raw)
	marker := "labcentral-" + hex.EncodeToString(sum[:12])
	find := func() (string, bool, error) {
		out, err := applianceCLI(ctx, client, []string{"set cli pager off", "show config running | match " + marker})
		return marker, strings.Contains(out, `<entry name="`+marker+`">`), err
	}
	_, err := journal.Do(ctx, p.Journal, c.Operation+"/appliance-config", func() (string, error) {
		batch := []string{"set cli pager off", "set cli scripting-mode on", "configure"}
		batch = append(batch, commands...)
		batch = append(batch, "set shared tag "+marker+" comments 'LabCentral configuration receipt'", "commit", "exit")
		if _, err := applianceCLI(ctx, client, batch); err != nil {
			// Retain the bounded driver reason separately from the unresolved
			// mutation. Candidate state can survive; this never permits replay.
			return "", errors.Join(err, p.Journal.Save(c.Operation+"/appliance-config-error", err.Error()))
		}
		for attempt := 0; attempt < 120; attempt++ {
			_, found, err := find()
			if err != nil {
				return "", err
			}
			if found {
				return marker, nil
			}
			if err = waitRouteRetry(ctx, 3*time.Second); err != nil {
				return "", err
			}
		}
		return "", fmt.Errorf("recovery_required: firewall commit remains unconfirmed")
	}, find)
	if err != nil {
		var reason string
		if p.Journal.Load(c.Operation+"/appliance-config-error", &reason) == nil {
			err = fmt.Errorf("%w; original appliance failure: %s", err, reason)
		}
	}
	return err
}
