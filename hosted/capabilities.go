package hosted

import (
	"context"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
)

func (s *Service) planSource(src lc.Source) (lc.Plan, error) {
	for name, image := range src.Lab.Images {
		if _, ok := s.env.Bases[image.Base]; !ok {
			return lc.Plan{}, fmt.Errorf("images.%s.base: configure environment.bases.%s with os: %s before building this revision", name, image.Base, lc.OSFamily(image.OS))
		}
	}
	return lc.PlanLab(src, s.env, nil)
}
func (s *Service) revisionProblem(ctx context.Context, id string) string {
	var src lc.Source
	if s.Store.Get(ctx, "source", id, &src) != nil {
		return "Pinned source is unavailable."
	}
	if src.Lab.Version != 3 {
		return ""
	}
	_, err := s.planSource(src)
	if err != nil {
		return err.Error()
	}
	return ""
}

// A Linux-only service remains valid. Supplied Windows installer pins must be
// complete; each template separately checks its required bindings and capacity.
func ValidateHostedEnvironment(env lc.Environment) error {
	if err := lc.ValidateEnvironment(env); err != nil {
		return err
	}
	if env.Platform != "aws" && env.Platform != "proxmox" {
		return fmt.Errorf("hosted deployment requires AWS or Proxmox")
	}
	if env.Build.WindowsDCVURL != "" || env.Build.WindowsDCVSHA256 != "" {
		return lc.ValidateWindowsDCV(env.Build)
	}
	return nil
}
