# ADR-0034: Managed classroom onboarding and sole-console acquisition

- **Status:** Accepted
- **Date:** 2026-09-21
- **Supersedes:** ADR-0029 (unconditional participant launch and reconnect control UX only)
- **Superseded by:** None

## Decision

Operators may opt into project-scoped OIDC provisioning. Only a verified exact
issuer/subject identifies an account; names and emails never link accounts or
grant elevated roles. Create a user, organization membership and project participant
role atomically on first login. Refresh display names on subsequent logins without
restoring revoked memberships/roles or enabling disabled users.

An installation setting can suspend participant deployment, including direct and
prepared admissions, while instructors retain group management and deployment.
Default behavior remains compatible; roles, ownership and target grants still apply.

A newly connected browser requests control once when it is the sole active console
session. Core atomically verifies sole presence and absence of a current or pending
driver before requesting the existing fenced input lease. A reconnect starts as a
viewer and may make this fresh conditional request; it never restores an old lease.
Explicit takeovers retain their existing semantics. Authorization and lease expiry
remain authoritative; no provider or access-plugin contract changes.

## Validation

Cover duplicate first login, name changes, disabled accounts, revoked roles,
participant deployment denial with retained access, and racing console requests.
