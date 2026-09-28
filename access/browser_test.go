package access

import (
	"bytes"
	"context"
	"crypto/des"
	"encoding/binary"
	"encoding/json"
	"github.com/coder/websocket"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

// The fake server tests the shipped browser assets, not Proxmox itself.
func TestBundledBrowser(t *testing.T) {
	if os.Getenv("LABCENTRAL_PLAYWRIGHT_MODULE") == "" {
		t.Skip("set LABCENTRAL_PLAYWRIGHT_MODULE for the browser check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var keyboard atomic.Bool
	disconnect := make(chan struct{})
	mux := http.NewServeMux()
	mux.Handle("/", staticHandler())
	mux.HandleFunc("/credentials", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"password": "browser-test"})
	})
	mux.HandleFunc("/probe", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"keyboard": keyboard.Load()})
	})
	mux.HandleFunc("/disconnect", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-disconnect:
		default:
			close(disconnect)
		}
	})
	mux.HandleFunc("/websocket", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		conn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
		go func() {
			select {
			case <-disconnect:
				ws.CloseNow()
			case <-ctx.Done():
			}
		}()
		read := func(n int) []byte {
			b := make([]byte, n)
			if _, err := io.ReadFull(conn, b); err != nil {
				return nil
			}
			return b
		}
		_, _ = conn.Write([]byte("RFB 003.008\n"))
		if read(12) == nil {
			return
		}
		_, _ = conn.Write([]byte{1, 2})
		if read(1) == nil {
			return
		}
		challenge := []byte("0123456789abcdef")
		_, _ = conn.Write(challenge)
		got := read(16)
		if got == nil {
			return
		}
		key := []byte("browser-")
		for i, b := range key {
			var rev byte
			for n := 0; n < 8; n++ {
				rev = (rev << 1) | (b & 1)
				b >>= 1
			}
			key[i] = rev
		}
		cipher, _ := des.NewCipher(key)
		want := make([]byte, 16)
		cipher.Encrypt(want[:8], challenge[:8])
		cipher.Encrypt(want[8:], challenge[8:])
		if !bytes.Equal(got, want) {
			t.Error("browser sent wrong VNC credentials")
			return
		}
		_, _ = conn.Write([]byte{0, 0, 0, 0})
		if read(1) == nil {
			return
		}
		init := []byte{0, 64, 0, 64, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0, 0, 0, 0, 4, 't', 'e', 's', 't'}
		_, _ = conn.Write(init)
		sent := false
		redShift := uint(16)
		bigEndian := false
		for {
			kind := read(1)
			if kind == nil {
				return
			}
			switch kind[0] {
			case 0:
				format := read(19)
				if format == nil {
					return
				}
				redShift = uint(format[13])
				bigEndian = format[5] != 0
			case 2:
				head := read(3)
				if head == nil {
					return
				}
				if read(int(binary.BigEndian.Uint16(head[1:]))*4) == nil {
					return
				}
			case 3:
				if read(9) == nil {
					return
				}
				if !sent {
					frame := make([]byte, 16+64*64*4)
					copy(frame, []byte{0, 0, 0, 1, 0, 0, 0, 0, 0, 64, 0, 64, 0, 0, 0, 0})
					for n := 16; n < len(frame); n += 4 {
						if bigEndian {
							binary.BigEndian.PutUint32(frame[n:n+4], 255<<redShift)
						} else {
							binary.LittleEndian.PutUint32(frame[n:n+4], 255<<redShift)
						}
					}
					_, _ = conn.Write(frame)
					sent = true
				}
			case 4:
				if read(7) == nil {
					return
				}
				keyboard.Store(true)
			case 5:
				if read(5) == nil {
					return
				}
			case 6:
				head := read(7)
				if head == nil {
					return
				}
				n := binary.BigEndian.Uint32(head[3:])
				if n > 1<<20 {
					return
				}
				if read(int(n)) == nil {
					return
				}
			default:
				return
			}
		}
	})
	relay, err := NewRelay(ctx, mux)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	cmd := exec.CommandContext(ctx, "node", "testdata/browser.mjs", relay.URL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}
