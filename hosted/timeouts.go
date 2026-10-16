package hosted

import (
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"time"
)

// Desktop budgets include SSH admission and provider readiness. Reverse proxies
// must allow at least this long for a launch request. Zero uses the OS default.
type DesktopOptions struct {
	OpenSeconds        int `json:"openSeconds,omitempty"`
	WindowsOpenSeconds int `json:"windowsOpenSeconds,omitempty"`
}

func (o DesktopOptions) Effective() DesktopOptions {
	if o.OpenSeconds == 0 {
		o.OpenSeconds = 90
	}
	if o.WindowsOpenSeconds == 0 {
		o.WindowsOpenSeconds = 600
	}
	return o
}

func (o DesktopOptions) Validate() error {
	for _, v := range []struct {
		name string
		n    int
	}{{"openSeconds", o.OpenSeconds}, {"windowsOpenSeconds", o.WindowsOpenSeconds}} {
		if v.n < 0 || v.n > 3600 {
			return fmt.Errorf("desktop.%s must be 1–3600, or zero for the default", v.name)
		}
	}
	return nil
}
func (s *Service) desktopOpenTimeout(os string) time.Duration {
	options := s.Config.Desktop.Effective()
	n := options.OpenSeconds
	if os == "windows" {
		n = options.WindowsOpenSeconds
	}
	return time.Duration(n) * time.Second
}
func (s *Service) prepareTimeout(plan lc.Plan) time.Duration {
	n := s.Config.Worker.Effective().PrepareTimeoutMinutes
	for _, m := range plan.Machines {
		if m.OS == "windows" {
			n = s.Config.Worker.Effective().WindowsPrepareTimeoutMinutes
			break
		}
	}
	return time.Duration(n) * time.Minute
}
func desktopSessionID(d *desktopSession) string {
	if d.record.OS == "windows" {
		return "console"
	}
	return d.info.Username
}
