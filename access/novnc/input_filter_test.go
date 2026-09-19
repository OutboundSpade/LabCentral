package novnc

import (
	"bytes"
	"encoding/binary"
	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/protobuf/types/known/durationpb"
	"io"
	"net"
	"testing"
	"time"
)

type inputRecorder struct {
	bytes.Buffer
	deadline time.Time
}

func (*inputRecorder) Close() error                         { return nil }
func (*inputRecorder) LocalAddr() net.Addr                  { return nil }
func (*inputRecorder) RemoteAddr() net.Addr                 { return nil }
func (*inputRecorder) SetDeadline(time.Time) error          { return nil }
func (*inputRecorder) SetReadDeadline(time.Time) error      { return nil }
func (r *inputRecorder) SetWriteDeadline(v time.Time) error { r.deadline = v; return nil }

type fragmentedReader struct{ io.Reader }

func (r fragmentedReader) Read(b []byte) (int, error) {
	if len(b) > 1 {
		b = b[:1]
	}
	return r.Reader.Read(b)
}
func TestSharedRFBFiltersEveryGuestMutation(t *testing.T) {
	safe := [][]byte{make([]byte, 20), {2, 0, 0, 1, 0, 0, 0, 0}, {3, 1, 0, 0, 0, 0, 0, 20, 0, 20}, {150, 1, 0, 0, 0, 0, 0, 20, 0, 20}, {248, 0, 0, 0, 0, 0, 0, 0, 1, 42}}
	desktop := make([]byte, 24)
	desktop[0] = 251
	desktop[6] = 1
	mutations := [][]byte{{4, 1, 0, 0, 0, 0, 0, 65}, {5, 1, 0, 1, 0, 1}, {5, 128, 0, 1, 0, 1, 1}, {6, 0, 0, 0, 0, 0, 0, 3, 'a', 'b', 'c'}, {6, 0, 0, 0, 255, 255, 255, 252, 0, 0, 0, 0}, {250, 0, 1, 2}, desktop, {255, 0, 0, 1, 0, 0, 0, 65, 0, 0, 0, 30}}
	all := []byte{0}
	expected := []byte{1}
	for i, m := range mutations {
		all = append(all, m...)
		if i < len(safe) {
			all = append(all, safe[i]...)
			expected = append(expected, safe[i]...)
		}
	}
	for _, mode := range []string{"viewer", "driver", "expired"} {
		t.Run(mode, func(t *testing.T) {
			up := &inputRecorder{}
			s := &session{shared: true, upstream: up}
			if mode != "viewer" {
				start := time.Now()
				if mode == "expired" {
					start = start.Add(-6 * time.Second)
				}
				if err := s.setInputLease(&pluginv1.ConsoleInputLease{Fence: 3, ValidFor: durationpb.New(5 * time.Second)}, start); err != nil {
					t.Fatal(err)
				}
			}
			err := s.forwardClient(fragmentedReader{bytes.NewReader(all)})
			if err != io.EOF {
				t.Fatal(err)
			}
			want := expected
			if mode == "driver" {
				want = append([]byte{1}, all[1:]...)
			}
			if !bytes.Equal(up.Bytes(), want) {
				t.Fatalf("forwarded forbidden or malformed RFB bytes: got %x want %x", up.Bytes(), want)
			}
		})
	}
}
func TestSharedRFBRejectsAmbiguousAndUnboundedMessages(t *testing.T) {
	hugeClipboard := []byte{6, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(hugeClipboard[4:], 1<<30)
	for _, frame := range [][]byte{{1}, {255, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, {2, 0, 255, 255}, hugeClipboard, {248, 0, 0, 0, 0, 0, 0, 0, 65}, {251, 0, 0, 0, 0, 0, 255, 0}} {
		if _, _, err := readClientMessage(bytes.NewReader(frame)); err == nil {
			t.Fatalf("accepted %x", frame)
		}
	}
}
func TestInputLeaseFailsClosedOnReplayAndMissingGrant(t *testing.T) {
	s := &session{}
	grant := &pluginv1.ConsoleInputLease{Fence: 4, ValidFor: durationpb.New(5 * time.Second)}
	if err := s.setInputLease(grant, time.Now()); err != nil {
		t.Fatal(err)
	}
	grant.Fence = 3
	if err := s.setInputLease(grant, time.Now()); err == nil || !s.inputUntil.IsZero() {
		t.Fatal("stale fence accepted")
	}
	grant.Fence = 5
	grant.ValidFor = durationpb.New(6 * time.Second)
	if err := s.setInputLease(grant, time.Now()); err == nil {
		t.Fatal("excess lease accepted")
	}
	if err := s.setInputLease(nil, time.Now()); err != nil || !s.inputUntil.IsZero() {
		t.Fatal("viewer decision retained input")
	}
}
