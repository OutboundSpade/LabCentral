package plugin

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
)

type fenceProvider struct {
	started chan lc.Command
	release chan struct{}
}

func (p *fenceProvider) Execute(ctx context.Context, c lc.Command, _ lc.Observer) (lc.Result, error) {
	p.started <- c
	if c.Operation == "first" {
		<-p.release
	}
	return lc.Result{}, nil
}
func TestProviderFilesFenceLateOperationAcrossIndependentClients(t *testing.T) {
	root := t.TempDir()
	native := &fenceProvider{started: make(chan lc.Command, 8), release: make(chan struct{})}
	first := WithNativeFences(root, native)
	second := WithNativeFences(root, native)
	ctx, cancel := context.WithCancel(context.Background())
	command := lc.Command{Owner: "example-owner", Operation: "first", Action: "machine.create", Machine: lc.PlannedMachine{Name: "one"}}
	done := make(chan error, 1)
	go func() { _, err := first.Execute(ctx, command, nil); done <- err }()
	<-native.started
	// Losing the controller's context does not release a fence while its provider
	// still has an in-flight operation. A separate process/handle uses the same files.
	cancel()
	for _, c := range []lc.Command{
		{Owner: command.Owner, Action: "machine.create", Machine: lc.PlannedMachine{Name: "one"}},
		{Owner: command.Owner, Action: "resource.destroy", Prior: lc.Resource{Kind: "machine", Name: "one"}},
		{Owner: command.Owner, Action: "resource.destroy", Prior: lc.Resource{Kind: "gateway", Name: "gateway"}},
		{Owner: command.Owner, Action: "image.abandon"},
	} {
		wait, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
		_, err := second.Execute(wait, c, nil)
		stop()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("late native operation crossed persistent fence", c.Action, err)
		}
	}
	// The fence preserves existing concurrency for unrelated machines and owners.
	for _, c := range []lc.Command{{Owner: command.Owner, Action: "machine.create", Machine: lc.PlannedMachine{Name: "two"}}, {Owner: "other-owner", Action: "network.create"}} {
		if _, err := second.Execute(context.Background(), c, nil); err != nil {
			t.Fatal(err)
		}
	}
	close(native.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := second.Execute(context.Background(), lc.Command{Owner: command.Owner, Action: "image.abandon"}, nil); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".db") {
			t.Fatal("provider fence opened a library database")
		}
	}
}
