package remote

import (
	"bytes"
	"io"
	"sync"
	"testing"
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
