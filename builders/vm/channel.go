package vm

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"io"
	"net"
	"time"
)

// Line frames have a hard bound and independent IDs. A reconnect begins with a
// newline to discard any incomplete frame left by a broken host connection.
const channelFrameLimit = 256 << 10
const channelChunkSize = 64 << 10

type GuestRequest struct {
	Version uint32            `json:"version"`
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	Recipe  *wire.BuildRecipe `json:"recipe,omitempty"`
	Limits  *wire.BuildLimits `json:"limits,omitempty"`
	Digest  string            `json:"digest,omitempty"`
	Offset  uint64            `json:"offset,omitempty"`
	Chunk   []byte            `json:"chunk,omitempty"`
}
type GuestResponse struct {
	Version   uint32 `json:"version"`
	ID        string `json:"id"`
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
	Digest    string `json:"digest,omitempty"`
	Size      uint64 `json:"size,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	Chunk     []byte `json:"chunk,omitempty"`
}
type GuestChannel struct {
	conn     net.Conn
	scanner  *bufio.Scanner
	sequence uint64
	nonce    string
}

func ConnectGuest(socket string) (*GuestChannel, error) {
	c, err := net.DialTimeout("unix", socket, 3*time.Second)
	if err != nil {
		return nil, err
	}
	if _, err = c.Write([]byte("\n")); err != nil {
		c.Close()
		return nil, err
	}
	scanner := bufio.NewScanner(c)
	scanner.Buffer(make([]byte, 4096), channelFrameLimit)
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		c.Close()
		return nil, err
	}
	return &GuestChannel{conn: c, scanner: scanner, nonce: hex.EncodeToString(nonce[:])}, nil
}
func (c *GuestChannel) Close() error { return c.conn.Close() }
func (c *GuestChannel) Call(in GuestRequest) (GuestResponse, error) {
	c.sequence++
	in.Version = 1
	in.ID = fmt.Sprintf("%s-%d", c.nonce, c.sequence)
	if len(in.Chunk) > channelChunkSize {
		return GuestResponse{}, fmt.Errorf("chunk exceeds frame bound")
	}
	c.conn.SetDeadline(time.Now().Add(30 * time.Second))
	data, err := json.Marshal(in)
	if err != nil || len(data) >= channelFrameLimit {
		return GuestResponse{}, fmt.Errorf("invalid guest request")
	}
	if _, err = c.conn.Write(append(data, '\n')); err != nil {
		return GuestResponse{}, err
	}
	for discarded := 0; discarded < 8; discarded++ {
		if !c.scanner.Scan() {
			if err := c.scanner.Err(); err != nil {
				return GuestResponse{}, err
			}
			return GuestResponse{}, io.ErrUnexpectedEOF
		}
		var response GuestResponse
		if err := strictJSON(c.scanner.Bytes(), &response); err != nil {
			return response, fmt.Errorf("invalid guest response")
		}
		if response.Version != 1 || response.ID != in.ID {
			continue
		}
		if len(response.Chunk) > channelChunkSize || len(response.Error) > 256 {
			return response, fmt.Errorf("guest response exceeds bound")
		}
		if response.Error != "" {
			return response, fmt.Errorf("guest rejected %s", in.Method)
		}
		return response, nil
	}
	return GuestResponse{}, fmt.Errorf("guest response sequence mismatch")
}
