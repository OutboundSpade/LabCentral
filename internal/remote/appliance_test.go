package remote

import (
	"context"
	"testing"

	"github.com/OutboundSpade/LabCentral/v2/internal/sshtest"
	"golang.org/x/crypto/ssh"
)

func TestConsoleAttestedSSHPinsRejectDifferentHosts(t *testing.T) {
	server := sshtest.New(t, nil, nil)
	private, _ := sshtest.Key(t)
	pub, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(server.HostKey))
	for _, pin := range []string{ssh.FingerprintSHA256(pub), "MD5:" + ssh.FingerprintLegacyMD5(pub)} {
		client, err := Dial(context.Background(), server.Address, "admin", private, pin)
		if err != nil {
			t.Fatal(err)
		}
		client.Close()
	}
	_, wrong := sshtest.Key(t)
	other, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(wrong))
	if client, err := Dial(context.Background(), server.Address, "admin", private, ssh.FingerprintSHA256(other)); err == nil {
		client.Close()
		t.Fatal("accepted wrong console host key")
	}
	for _, pin := range []string{"SHA256:bad", "MD5:bad", "SHA256:", ssh.FingerprintSHA256(pub) + ",untrusted"} {
		if _, err := clientConfig("admin", private, pin); err == nil {
			t.Fatal("malformed console pin accepted", pin)
		}
	}
}
