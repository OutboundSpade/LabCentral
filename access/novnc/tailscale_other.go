//go:build !linux

package novnc

import (
	"context"
	"fmt"
	"net"
)

func dialTailscaleDevice(context.Context, string) (net.Conn, error) {
	return nil, fmt.Errorf("pinned tailnet console transport requires Linux")
}
