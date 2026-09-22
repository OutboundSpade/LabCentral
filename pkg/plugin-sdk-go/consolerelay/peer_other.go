//go:build !linux

package consolerelay

import (
	"fmt"
	"net"
	"os"
)

func ownedBy(os.FileInfo, uint32) bool       { return false }
func lockDirectory(string) (*os.File, error) { return nil, fmt.Errorf("console relay requires Linux") }
func verifyPeer(*net.UnixConn, uint32) error {
	return fmt.Errorf("console relay requires Linux peer identity")
}
