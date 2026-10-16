package hosted

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// WorkerOptions are operator policy, read once at startup. Zero selects the
// documented default; changing them requires a service restart.
type WorkerOptions struct {
	PrepareTimeoutMinutes        int `json:"prepareTimeoutMinutes,omitempty"`
	WindowsPrepareTimeoutMinutes int `json:"windowsPrepareTimeoutMinutes,omitempty"`
	MaxProvisioningConcurrency   int `json:"maxProvisioningConcurrency,omitempty"`
	MaxBulkJobs                  int `json:"maxBulkJobs,omitempty"`
	MaxGuestJobs                 int `json:"maxGuestJobs,omitempty"`
	MaxImageBuildJobs            int `json:"maxImageBuildJobs,omitempty"`
	JobTimeoutMinutes            int `json:"jobTimeoutMinutes,omitempty"`
}

func (o WorkerOptions) Effective() WorkerOptions {
	if o.MaxProvisioningConcurrency == 0 {
		o.MaxProvisioningConcurrency = 3
	}
	if o.MaxBulkJobs == 0 {
		o.MaxBulkJobs = 3
	}
	if o.MaxGuestJobs == 0 {
		o.MaxGuestJobs = 12
	}
	if o.MaxImageBuildJobs == 0 {
		o.MaxImageBuildJobs = min(2, o.MaxBulkJobs)
	}
	if o.PrepareTimeoutMinutes == 0 {
		o.PrepareTimeoutMinutes = 30
	}
	if o.WindowsPrepareTimeoutMinutes == 0 {
		o.WindowsPrepareTimeoutMinutes = 45
	}
	if o.JobTimeoutMinutes == 0 {
		o.JobTimeoutMinutes = 120
	}
	return o
}

func (o WorkerOptions) Validate() error {
	o = o.Effective()
	for _, v := range []struct {
		name           string
		value, maximum int
	}{
		{"maxProvisioningConcurrency", o.MaxProvisioningConcurrency, 32},
		{"maxBulkJobs", o.MaxBulkJobs, 32},
		{"maxGuestJobs", o.MaxGuestJobs, 32},
		{"maxImageBuildJobs", o.MaxImageBuildJobs, 32},
		{"jobTimeoutMinutes", o.JobTimeoutMinutes, 1440},
		{"prepareTimeoutMinutes", o.PrepareTimeoutMinutes, 1440},
		{"windowsPrepareTimeoutMinutes", o.WindowsPrepareTimeoutMinutes, 1440},
	} {
		if v.value < 1 || v.value > v.maximum {
			return fmt.Errorf("worker.%s must be 1–%d, or zero for the default", v.name, v.maximum)
		}
	}
	if o.MaxImageBuildJobs > o.MaxBulkJobs {
		return fmt.Errorf("worker.maxImageBuildJobs cannot exceed worker.maxBulkJobs")
	}
	return nil
}

// ValidateConfig checks static service policy without contacting identity or
// infrastructure. Credential validity and native capacity require live checks.
func ValidateConfig(c Config) error {
	if err := c.Capacity.Validate(); err != nil {
		return err
	}
	if err := validateIdentityGroups(c); err != nil {
		return err
	}
	if err := c.Aggregate.Validate(); err != nil {
		return err
	}
	if err := c.Engine.Validate(); err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	if err := c.Desktop.Validate(); err != nil {
		return err
	}
	if err := c.Worker.Validate(); err != nil {
		return err
	}
	if c.WeeklyCloudMinutes < 0 || c.WeeklyCloudMinutes > 10080 {
		return fmt.Errorf("weeklyCloudMinutes must be 1–10080, or zero for the 180-minute default")
	}
	for _, raw := range []string{c.Origin, c.DesktopOrigin, c.Issuer} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return fmt.Errorf("origins and issuer must be absolute HTTPS origins")
		}
	}
	if c.Origin == c.DesktopOrigin {
		return fmt.Errorf("desktop requires a separate origin")
	}
	if c.BackchannelOrigin != "" {
		u, err := url.Parse(c.BackchannelOrigin)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return fmt.Errorf("OIDC backchannel must be an explicit HTTPS origin")
		}
	}
	return nil
}

// Shared by team and personal preparation. Cleanup never acquires this gate.
// Waiting is cancellable and cannot turn an expired allocation into a new create.
func (s *Service) acquireProvisioning(ctx context.Context) (func(), error) {
	if err := s.Config.Worker.Validate(); err != nil {
		return nil, err
	}
	s.provisionOnce.Do(func() { s.provisionSlots = make(chan struct{}, s.Config.Worker.Effective().MaxProvisioningConcurrency) })
	select {
	case s.provisionSlots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-s.provisionSlots
			return nil, err
		}
		return func() { <-s.provisionSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func validateIdentityGroups(c Config) error {
	for _, group := range []struct{ name, value string }{{"memberGroup", c.MemberGroup}, {"adminGroup", c.AdminGroup}} {
		if group.value == "" || strings.ContainsFunc(group.value, unicode.IsSpace) {
			return fmt.Errorf("%s must be a non-empty string without whitespace", group.name)
		}
	}
	return nil
}
