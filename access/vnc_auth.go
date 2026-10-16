package access

import (
	"crypto/des"
	"encoding/binary"
	"fmt"
	"io"
	"math/bits"
	"net"
	"time"
)

// Authenticate the native RFB connection here, then offer None authentication
// inside our already authenticated relay. Neither API nor VNC tickets cross the
// browser boundary. Only RFB 3.8 with VNC authentication is accepted from QEMU.
func consoleHandshake(up, down net.Conn, password string) error {
	deadline := time.Now().Add(15 * time.Second)
	_ = up.SetDeadline(deadline)
	_ = down.SetDeadline(deadline)
	defer up.SetDeadline(time.Time{})
	defer down.SetDeadline(time.Time{})
	version := make([]byte, 12)
	if _, err := io.ReadFull(up, version); err != nil {
		return err
	}
	if string(version) != "RFB 003.008\n" {
		return fmt.Errorf("unsupported console protocol")
	}
	if _, err := up.Write(version); err != nil {
		return err
	}
	var count [1]byte
	if _, err := io.ReadFull(up, count[:]); err != nil {
		return err
	}
	types := make([]byte, int(count[0]))
	if _, err := io.ReadFull(up, types); err != nil {
		return err
	}
	found := false
	for _, t := range types {
		if t == 2 {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("console requires VNC authentication")
	}
	if _, err := up.Write([]byte{2}); err != nil {
		return err
	}
	var challenge [16]byte
	if _, err := io.ReadFull(up, challenge[:]); err != nil {
		return err
	}
	var key [8]byte
	copy(key[:], password)
	for n := range key {
		key[n] = bits.Reverse8(key[n])
	}
	cipher, err := des.NewCipher(key[:]) // RFB's fixed legacy VNC authentication.
	if err != nil {
		return err
	}
	cipher.Encrypt(challenge[:8], challenge[:8])
	cipher.Encrypt(challenge[8:], challenge[8:])
	if _, err = up.Write(challenge[:]); err != nil {
		return err
	}
	var result [4]byte
	if _, err = io.ReadFull(up, result[:]); err != nil {
		return err
	}
	if binary.BigEndian.Uint32(result[:]) != 0 {
		return fmt.Errorf("console authentication rejected")
	}
	if _, err = down.Write(version); err != nil {
		return err
	}
	if _, err = io.ReadFull(down, version); err != nil {
		return err
	}
	if string(version) != "RFB 003.008\n" {
		return fmt.Errorf("unsupported browser console protocol")
	}
	if _, err = down.Write([]byte{1, 1}); err != nil {
		return err
	}
	if _, err = io.ReadFull(down, count[:]); err != nil {
		return err
	}
	if count[0] != 1 {
		return fmt.Errorf("invalid browser console authentication")
	}
	_, err = down.Write(result[:])
	return err
}
