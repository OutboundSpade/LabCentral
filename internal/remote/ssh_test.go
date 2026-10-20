package remote

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/v2/internal/sshtest"
)

// SSH copies stdout and stderr concurrently. Neither io.Copy fast path may
// bypass the shared output lock or the guest-output bound.
func TestConcurrentOutputCopiesRemainBounded(t *testing.T) {
	var output limitedOutput
	var wg sync.WaitGroup
	for _, value := range []byte{'a', 'b'} {
		wg.Add(1)
		go func(value byte) {
			defer wg.Done()
			data := bytes.Repeat([]byte{value}, 800<<10)
			// Hide WriterTo to also exercise io.Copy's destination ReaderFrom path.
			n, err := io.Copy(&output, struct{ io.Reader }{bytes.NewReader(data)})
			if err != nil || n != int64(len(data)) {
				t.Errorf("copy: %d %v", n, err)
			}
		}(value)
	}
	wg.Wait()
	got := output.Bytes()
	if !output.Exceeded || len(got) != 1<<20 {
		t.Fatalf("output bound: length=%d exceeded=%v", len(got), output.Exceeded)
	}
	for _, b := range got {
		if b != 'a' && b != 'b' {
			t.Fatal("corrupted output")
		}
	}
}

func TestRunInputCancellationWithoutRemoteExit(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := sshtest.NewWithResult(t, nil, func(_ string, _ string, input []byte) ([]byte, uint32) {
		if string(input) != "fixture payload" {
			t.Errorf("stdin changed: %q", input)
		}
		close(started)
		<-release // The server never supplies an exit status during cancellation.
		return nil, 0
	})
	defer close(release)
	private, _ := sshtest.Key(t)
	client, err := Dial(context.Background(), server.Address, "lab", private, server.HostKey)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := RunInput(ctx, client, "fixture command", []byte("fixture payload")); result <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("command did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation lost its unknown outcome", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation waited for a remote exit status")
	}
}
