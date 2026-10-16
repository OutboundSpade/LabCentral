package proxmox

import (
	"errors"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
)

// Only the clone submission can be definitely rejected. Task/wait failures have
// already accepted the mutation and must retain their uncertain intent.
func cloneError(err error) error {
	var response *HTTPError
	if errors.As(err, &response) && (response.Status == 400 || response.Status == 401 || response.Status == 403 || response.Status == 429) {
		return journal.Rejected(err)
	}
	return err
}
