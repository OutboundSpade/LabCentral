# ADR-0010: Scope-aware RBAC with relationship checks

- **Status:** Accepted
- **Date:** 2026-09-02
- **Supersedes:** None
- **Superseded by:** None

## Context and constraints

Classrooms require platform, organization, project, team, and user ownership without an external authorization service in local mode.

## Considered options

1. Flat global roles.
2. Embedded role assignments plus explicit ownership/team relationship checks.
3. External policy engine required for all deployments.

## Decision

Use option 2. Assign named roles at platform/organization/project/team scopes. Application authorization evaluates action, subject, scope ancestry, target grants, instance ownership/share, and policy constraints. No-auth mode supplies the same evaluator with one synthetic Platform Administrator. Provider registration/credentials remain platform-scoped in v1.

## Consequences

Rules remain inspectable and testable. A future policy module may add constraints but cannot grant beyond core maximum permissions.

## Security implications

Default deny, consistent list filtering, object-level rechecks on mutations/access, and cross-tenant tests are mandatory. Batch classroom creation records the instructor as actor and each participant/team as owner.

## Compatibility implications

Permission action names are stable API identifiers; renamed actions require migration and an ADR.
