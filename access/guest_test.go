package access

import (
	"context"
	"strings"
	"testing"

	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	"github.com/OutboundSpade/LabCentral/v2/internal/sshtest"
)

func TestGuestOwnershipMessageDoesNotExposeOutput(t *testing.T) {
	server := sshtest.NewWithResult(t, nil, func(string, string, []byte) ([]byte, uint32) {
		return []byte("ownership_mismatch: PRIVATE-GUEST-PAYLOAD"), 1
	})
	key, _ := sshtest.Key(t)
	client, err := remote.Dial(context.Background(), server.Address, "lab", key, server.HostKey)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = guestRun(context.Background(), client, "fixture")
	if err == nil || !strings.Contains(err.Error(), "ownership_mismatch:") || !strings.Contains(err.Error(), "recovery") || strings.Contains(err.Error(), "PRIVATE-GUEST-PAYLOAD") {
		t.Fatal(err)
	}
}
