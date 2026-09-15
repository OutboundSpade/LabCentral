package localdb

import (
	"context"
	"fmt"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func (s *Store) ListMemberships(ctx context.Context, organization domain.OrganizationID, after string, limit uint32) ([]application.MembershipRecord, error) {
	if organization == "" || limit == 0 || limit > 200 {
		return nil, fmt.Errorf("invalid membership query")
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT membership_id, user_id, organization_id, COALESCE(team_id, ''), valid_from_us, valid_until_us, resource_version FROM labcentral_memberships WHERE organization_id = ? AND membership_id > ? ORDER BY membership_id LIMIT ?`), organization, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]application.MembershipRecord, 0)
	for rows.Next() {
		var item application.MembershipRecord
		var from, until int64
		if err := rows.Scan(&item.ID, &item.UserID, &item.OrganizationID, &item.TeamID, &from, &until, &item.ResourceVersion); err != nil {
			return nil, err
		}
		item.ValidFrom, item.ValidUntil = time.UnixMicro(from).UTC(), optionalTime(until)
		items = append(items, item)
	}
	return items, rows.Err()
}

// Directory queries return bounded candidate pages. The application policy
// filters project visibility and classroom access before any HTTP response.
func (s *Store) ListProjects(ctx context.Context, after string, limit uint32) ([]application.ProjectRecord, error) {
	if limit == 0 || limit > 200 {
		return nil, fmt.Errorf("invalid directory limit")
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT project_id, organization_id, name, resource_version FROM labcentral_projects WHERE project_id > ? ORDER BY project_id LIMIT ?`), after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]application.ProjectRecord, 0)
	for rows.Next() {
		var item application.ProjectRecord
		if err := rows.Scan(&item.ID, &item.OrganizationID, &item.Name, &item.ResourceVersion); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// Never select password hashes or external identity subjects for directories.
func (s *Store) ListDirectoryUsers(ctx context.Context, organization domain.OrganizationID, after string, limit uint32, now time.Time) ([]application.UserRecord, error) {
	if limit == 0 || limit > 200 {
		return nil, fmt.Errorf("invalid directory limit")
	}
	query := `SELECT u.user_id, u.username, u.display_name, u.disabled, u.created_at_us, u.updated_at_us, u.resource_version FROM labcentral_users u WHERE u.user_id > ?`
	args := []any{after}
	if organization != "" {
		query += ` AND u.disabled = 0 AND EXISTS (SELECT 1 FROM labcentral_memberships m WHERE m.user_id = u.user_id AND m.organization_id = ? AND m.valid_from_us <= ? AND (m.valid_until_us = 0 OR m.valid_until_us > ?))`
		args = append(args, organization, now.UnixMicro(), now.UnixMicro())
	}
	query += ` ORDER BY u.user_id LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]application.UserRecord, 0)
	for rows.Next() {
		var item application.UserRecord
		var created, updated int64
		if err := rows.Scan(&item.ID, &item.Username, &item.DisplayName, &item.Disabled, &created, &updated, &item.ResourceVersion); err != nil {
			return nil, err
		}
		item.CreatedAt, item.UpdatedAt = time.UnixMicro(created).UTC(), time.UnixMicro(updated).UTC()
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListOrganizations(ctx context.Context, after string, limit uint32) ([]application.OrganizationRecord, error) {
	if limit == 0 || limit > 200 {
		return nil, fmt.Errorf("invalid directory limit")
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT organization_id, name, resource_version FROM labcentral_organizations WHERE organization_id > ? ORDER BY organization_id LIMIT ?`), after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]application.OrganizationRecord, 0)
	for rows.Next() {
		var item application.OrganizationRecord
		if err := rows.Scan(&item.ID, &item.Name, &item.ResourceVersion); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListTargetGrants(ctx context.Context, after string, limit uint32) ([]application.TargetGrantRecord, error) {
	if limit == 0 || limit > 200 {
		return nil, fmt.Errorf("invalid directory limit")
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT grant_id, target_id, scope_kind, scope_id, valid_from_us, valid_until_us, resource_version FROM labcentral_provider_target_grants WHERE grant_id > ? ORDER BY grant_id LIMIT ?`), after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]application.TargetGrantRecord, 0)
	for rows.Next() {
		var item application.TargetGrantRecord
		var from, until int64
		if err := rows.Scan(&item.ID, &item.TargetID, &item.ScopeKind, &item.ScopeID, &from, &until, &item.ResourceVersion); err != nil {
			return nil, err
		}
		item.ValidFrom, item.ValidUntil = time.UnixMicro(from).UTC(), optionalTime(until)
		items = append(items, item)
	}
	return items, rows.Err()
}
