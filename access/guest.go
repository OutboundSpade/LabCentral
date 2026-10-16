package access

import (
	"bytes"
	"context"
	"fmt"

	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	"golang.org/x/crypto/ssh"
)

// Surface a fixed actionable category without exposing guest output, which may
// contain an account name, credential or a PowerShell payload in an error frame.
func guestRun(ctx context.Context, client *ssh.Client, command string) ([]byte, error) {
	output, err := remote.Run(ctx, client, command)
	if err != nil {
		if bytes.Contains(output, []byte("access generation was revoked")) {
			return output, fmt.Errorf("recovery_required: desktop generation was revoked; recover access before reopening: %w", err)
		}
		for _, marker := range []string{"ownership_mismatch:", "desktop ownership mismatch", "console account SID mismatch", "console is owned by another access", "console user ownership mismatch"} {
			if bytes.Contains(output, []byte(marker)) {
				return output, fmt.Errorf("ownership_mismatch: desktop account, session or generation differs; access retained for recovery: %w", err)
			}
		}
	}
	return output, err
}
