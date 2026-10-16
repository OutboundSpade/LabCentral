package access

import (
	"context"
	"crypto/des"
	"encoding/binary"
	"io"
	"math/bits"
	"net"
	"testing"
	"time"
)

func TestConsoleHandshakeKeepsTicketServerSide(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "rejected"}[reject], func(t *testing.T) {
			native, up := net.Pipe()
			browser, down := net.Pipe()
			defer native.Close()
			defer up.Close()
			defer browser.Close()
			defer down.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			stop := context.AfterFunc(ctx, func() { native.Close(); browser.Close() })
			defer stop()
			secret := "native-ticket-private"
			nativeDone := make(chan error, 1)
			go func() {
				fail := func(err error) { nativeDone <- err }
				if _, err := native.Write([]byte("RFB 003.008\n")); err != nil {
					fail(err)
					return
				}
				version := make([]byte, 12)
				if _, err := io.ReadFull(native, version); err != nil {
					fail(err)
					return
				}
				if _, err := native.Write([]byte{1, 2}); err != nil {
					fail(err)
					return
				}
				selected := make([]byte, 1)
				if _, err := io.ReadFull(native, selected); err != nil {
					fail(err)
					return
				}
				if selected[0] != 2 {
					fail(io.ErrUnexpectedEOF)
					return
				}
				challenge := []byte("random-challenge")
				expected := make([]byte, 16)
				key := make([]byte, 8)
				copy(key, secret)
				for n := range key {
					key[n] = bits.Reverse8(key[n])
				}
				cipher, _ := des.NewCipher(key)
				cipher.Encrypt(expected[:8], challenge[:8])
				cipher.Encrypt(expected[8:], challenge[8:])
				if _, err := native.Write(challenge); err != nil {
					fail(err)
					return
				}
				response := make([]byte, 16)
				if _, err := io.ReadFull(native, response); err != nil {
					fail(err)
					return
				}
				if string(response) != string(expected) {
					fail(io.ErrUnexpectedEOF)
					return
				}
				result := make([]byte, 4)
				if reject {
					binary.BigEndian.PutUint32(result, 1)
				}
				_, err := native.Write(result)
				fail(err)
			}()
			result := make(chan error, 1)
			go func() { result <- consoleHandshake(up, down, secret) }()
			if !reject {
				version := make([]byte, 12)
				if _, err := io.ReadFull(browser, version); err != nil {
					t.Fatal(err)
				}
				if string(version) != "RFB 003.008\n" {
					t.Fatal(string(version))
				}
				browser.Write(version)
				types := make([]byte, 2)
				if _, err := io.ReadFull(browser, types); err != nil {
					t.Fatal(err)
				}
				if string(types) != string([]byte{1, 1}) {
					t.Fatal("native auth exposed", types)
				}
				browser.Write([]byte{1})
				success := make([]byte, 4)
				if _, err := io.ReadFull(browser, success); err != nil {
					t.Fatal(err)
				}
				if binary.BigEndian.Uint32(success) != 0 {
					t.Fatal("browser failed")
				}
			}
			if err := <-result; (err != nil) != reject {
				t.Fatal(err, reject)
			}
			if err := <-nativeDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}
