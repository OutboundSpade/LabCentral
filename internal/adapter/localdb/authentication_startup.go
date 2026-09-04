package localdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ValidateAuthenticationStartup performs the read-only checks which must pass
// before labd starts a provider process or contacts an OIDC issuer.
func (s *Store) ValidateAuthenticationStartup(ctx context.Context, mode string) error {
	var recorded string
	err := s.db.QueryRowContext(ctx, `SELECT setting_value FROM labcentral_installation_settings WHERE setting_key = 'authentication_mode'`).Scan(&recorded)
	switch {
	case err == nil:
		if mode == "none" {
			return fmt.Errorf("installation authentication mode %q cannot be downgraded to no-auth; restart with --auth-mode=%s", recorded, recorded)
		}
		if mode != recorded {
			return fmt.Errorf("configured authentication mode %q conflicts with installation mode %q; restart with --auth-mode=%s", mode, recorded, recorded)
		}
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("inspect installation authentication mode: %w", err)
	}

	if mode == "none" {
		return nil
	}
	if mode != "local" && mode != "oidc" {
		return fmt.Errorf("configured authentication mode %q is invalid", mode)
	}

	var users, instances, jobs int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM labcentral_users`).Scan(&users); err != nil {
		return fmt.Errorf("inspect authentication identities: %w", err)
	}
	if users != 0 {
		return fmt.Errorf("cannot enable %s authentication: database contains %d user identity record(s) but has no recorded authentication mode; restore consistent installation metadata or deploy with a new database", mode, users)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM labcentral_local_instances`).Scan(&instances); err != nil {
		return fmt.Errorf("inspect legacy lab instances: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM labcentral_jobs`).Scan(&jobs); err != nil {
		return fmt.Errorf("inspect legacy durable jobs: %w", err)
	}
	if instances != 0 || jobs != 0 {
		return fmt.Errorf("cannot enable %s authentication on a legacy no-auth database containing %d lab instance(s) and %d durable job(s): automated actor and owner adoption is unsafe; keep this database in explicit --auth-mode=none, or deploy authenticated mode with a new database and recreate labs under real projects and owners", mode, instances, jobs)
	}
	return nil
}
