package novnc

import (
	"crypto/des"
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

var rfb38 = []byte("RFB 003.008\n")

// authenticateUpstream consumes VNC authentication before any browser is
// attached. The downstream client therefore sees only a one-time,
// authorization-checked no-auth RFB transport and never receives the provider
// credential.
func authenticateUpstream(connection net.Conn, password string) error {
	version := make([]byte, len(rfb38))
	if _, err := io.ReadFull(connection, version); err != nil || string(version) != string(rfb38) {
		return fmt.Errorf("upstream RFB version is unsupported")
	}
	if _, err := connection.Write(rfb38); err != nil {
		return fmt.Errorf("write upstream RFB version: %w", err)
	}
	var count [1]byte
	if _, err := io.ReadFull(connection, count[:]); err != nil || count[0] == 0 || count[0] > 32 {
		return fmt.Errorf("upstream RFB security types are invalid")
	}
	types := make([]byte, int(count[0]))
	if _, err := io.ReadFull(connection, types); err != nil {
		return fmt.Errorf("read upstream RFB security types: %w", err)
	}
	vncAuth := false
	for _, securityType := range types {
		vncAuth = vncAuth || securityType == 2
	}
	if !vncAuth {
		return fmt.Errorf("upstream RFB VNC authentication is unavailable")
	}
	if _, err := connection.Write([]byte{2}); err != nil {
		return fmt.Errorf("select upstream RFB authentication: %w", err)
	}
	challenge := make([]byte, 16)
	if _, err := io.ReadFull(connection, challenge); err != nil {
		return fmt.Errorf("read upstream RFB challenge: %w", err)
	}
	response, err := vncChallengeResponse(password, challenge)
	if err != nil {
		return err
	}
	if _, err := connection.Write(response); err != nil {
		return fmt.Errorf("write upstream RFB challenge response: %w", err)
	}
	var result [4]byte
	if _, err := io.ReadFull(connection, result[:]); err != nil || binary.BigEndian.Uint32(result[:]) != 0 {
		return fmt.Errorf("upstream RFB authentication failed")
	}
	return nil
}

func presentAuthorizedDownstream(connection net.Conn) error {
	if _, err := connection.Write(rfb38); err != nil {
		return err
	}
	version := make([]byte, len(rfb38))
	if _, err := io.ReadFull(connection, version); err != nil || string(version) != string(rfb38) {
		return fmt.Errorf("downstream RFB version is unsupported")
	}
	if _, err := connection.Write([]byte{1, 1}); err != nil { // one security type: None
		return err
	}
	var selected [1]byte
	if _, err := io.ReadFull(connection, selected[:]); err != nil || selected[0] != 1 {
		return fmt.Errorf("downstream RFB security selection is invalid")
	}
	var success [4]byte
	_, err := connection.Write(success[:])
	return err
}

func vncChallengeResponse(password string, challenge []byte) ([]byte, error) {
	if len(challenge) != 16 || password == "" {
		return nil, fmt.Errorf("VNC authentication input is invalid")
	}
	var key [8]byte
	for index := 0; index < len(key) && index < len(password); index++ {
		key[index] = reverseBits(password[index])
	}
	block, err := des.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	response := make([]byte, len(challenge))
	block.Encrypt(response[:8], challenge[:8])
	block.Encrypt(response[8:], challenge[8:])
	return response, nil
}

func reverseBits(value byte) byte {
	value = value>>4 | value<<4
	value = (value&0xcc)>>2 | (value&0x33)<<2
	return (value&0xaa)>>1 | (value&0x55)<<1
}
