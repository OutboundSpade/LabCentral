//go:build faultinject

package proxmox

import "github.com/OutboundSpade/LabCentral/v2/internal/faultinject"

func faultError(err error, method, path string) error {
	if status := faultinject.Status(err); status != 0 {
		return &HTTPError{Status: status, Method: method, Path: path}
	}
	return err
}
