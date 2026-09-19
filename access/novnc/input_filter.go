package novnc

import (
	"encoding/binary"
	"fmt"
	pluginv1 "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"io"
	"time"
)

const maximumInputLease = 5 * time.Second

// Frame the byte stream rather than WebSocket messages: clients can fragment,
// concatenate, or disguise RFB input in otherwise harmless transport frames.
func readClientMessage(r io.Reader) ([]byte, bool, error) {
	var kind [1]byte
	if _, err := io.ReadFull(r, kind[:]); err != nil {
		return nil, false, err
	}
	size := 0
	mutates := false
	switch kind[0] {
	case 0:
		size = 20 // SetPixelFormat
	case 2:
		size = 4 // SetEncodings
	case 3:
		size = 10 // FramebufferUpdateRequest
	case 4:
		size = 8
		mutates = true // KeyEvent
	case 5:
		size = 6
		mutates = true // PointerEvent (possibly extended)
	case 6:
		size = 8
		mutates = true // ClientCutText, including extended clipboard
	case 150:
		size = 10 // EnableContinuousUpdates
	case 248:
		size = 9 // Fence
	case 250:
		size = 4
		mutates = true // XVP power operation
	case 251:
		size = 8
		mutates = true // SetDesktopSize
	case 255:
		size = 12
		mutates = true // QEMU extended key
	default:
		return nil, false, fmt.Errorf("unsupported RFB client message")
	}
	frame := make([]byte, size)
	frame[0] = kind[0]
	if _, err := io.ReadFull(r, frame[1:]); err != nil {
		return nil, false, err
	}
	tail := 0
	switch kind[0] {
	case 2:
		tail = int(binary.BigEndian.Uint16(frame[2:4])) * 4
		if tail > 4096 {
			return nil, false, fmt.Errorf("encoding list exceeds limit")
		}
	case 5:
		if frame[1]&0x80 != 0 {
			tail = 1
		}
	case 6:
		n := int64(int32(binary.BigEndian.Uint32(frame[4:8])))
		if n < 0 {
			n = -n
		}
		if n > 1<<20 {
			return nil, false, fmt.Errorf("clipboard exceeds limit")
		}
		tail = int(n)
	case 248:
		tail = int(frame[8])
		if tail > 64 {
			return nil, false, fmt.Errorf("fence payload exceeds limit")
		}
	case 251:
		tail = int(frame[6]) * 16
		if tail > 16*16 {
			return nil, false, fmt.Errorf("desktop layout exceeds limit")
		}
	case 255:
		if frame[1] != 0 {
			return nil, false, fmt.Errorf("unsupported QEMU client message")
		}
	}
	if tail != 0 {
		offset := len(frame)
		frame = append(frame, make([]byte, tail)...)
		if _, err := io.ReadFull(r, frame[offset:]); err != nil {
			return nil, false, err
		}
	}
	return frame, mutates, nil
}
func (current *session) setInputLease(grant *pluginv1.ConsoleInputLease, started time.Time) error {
	current.mu.Lock()
	defer current.mu.Unlock()
	current.inputUntil = time.Time{}
	if grant == nil {
		return nil
	}
	if grant.GetFence() == 0 || grant.GetValidFor() == nil || !grant.GetValidFor().IsValid() {
		return fmt.Errorf("invalid input lease")
	}
	duration := grant.GetValidFor().AsDuration()
	if duration <= 0 || duration > maximumInputLease || grant.GetFence() < current.inputFence {
		return fmt.Errorf("invalid input lease")
	}
	current.inputFence = grant.GetFence()
	// time.Time retains Go's monotonic component from the request start.
	current.inputUntil = started.Add(duration)
	return nil
}
func (current *session) forwardClient(r io.Reader) error {
	if !current.shared {
		_, err := io.Copy(current.upstream, r)
		return err
	}
	var clientInit [1]byte
	if _, err := io.ReadFull(r, clientInit[:]); err != nil {
		return err
	}
	// Ignore an exclusive ClientInit from even an authorized/hostile viewer.
	if err := current.writeClientFrame([]byte{1}, false); err != nil {
		return err
	}
	for {
		frame, mutates, err := readClientMessage(r)
		if err != nil {
			return err
		}
		err = current.writeClientFrame(frame, mutates)
		clear(frame)
		if err != nil {
			return err
		}
	}
}
func (current *session) writeClientFrame(frame []byte, mutates bool) error {
	current.mu.Lock()
	defer current.mu.Unlock()
	now := time.Now()
	if current.closed {
		return io.ErrClosedPipe
	}
	if mutates && !now.Before(current.inputUntil) {
		return nil
	}
	deadline := now.Add(time.Second)
	if mutates && current.inputUntil.Before(deadline) {
		deadline = current.inputUntil
	}
	if err := current.upstream.SetWriteDeadline(deadline); err != nil {
		return err
	}
	for len(frame) > 0 {
		n, err := current.upstream.Write(frame)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return current.upstream.SetWriteDeadline(time.Time{})
}
