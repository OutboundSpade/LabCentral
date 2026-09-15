package novnc

import (
	"context"
	"net"
	"syscall"
	"time"
)

func dialTailscaleDevice(ctx context.Context, address string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second, Control: func(_, _ string, raw syscall.RawConn) error {
		var bindErr error
		if err := raw.Control(func(fd uintptr) {
			bindErr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, "tailscale0")
		}); err != nil {
			return err
		}
		return bindErr
	}}
	return dialer.DialContext(ctx, "tcp4", address)
}
