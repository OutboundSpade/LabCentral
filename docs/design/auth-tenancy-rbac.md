# Authentication, tenancy, and RBAC

## Authentication modes

- `none`: one synthetic `local-admin`, loopback-only development listener, and persistent UI/API warning. The operator must select `--auth-mode=none` explicitly. Non-loopback no-auth startup is always refused, reverse-proxy headers are ignored, and an installation that has selected local or OIDC authentication cannot be downgraded to no-auth.
- `local`: Argon2id password hashes with per-user salt and versioned parameters; database-time account/IP/global rate limiting across replicas and retained signing keys; a bounded Argon2 worker semaphore; secure recovery/bootstrap; session rotation; optional later MFA.
- `oidc`: authorization-code + PKCE, exact issuer/audience/nonce/state checks, configured claim mapping, key rotation, and local membership/role assignments. OIDC authenticates; it does not automatically grant platform admin.

Authenticated browser requests use Secure/HttpOnly session cookies plus a separate CSRF cookie/header and exact Origin checks. An authenticated caller can explicitly mint an opaque bearer credential at `POST /auth/tokens`; it has the same bounded lifetime, user-disable/password-change revocation, and self-logout semantics as a session, but its durable credential type and authentication path are distinct. A browser token cannot be replayed as bearer to bypass CSRF, and a bearer token cannot be replayed as a browser cookie. Bearer requests do not use browser CSRF, are never silently rotated, and are rejected when combined with a session cookie. Retained keyring entries keep existing sessions, recovery tokens, OIDC flows, and rate-limit buckets valid through a rolling rotation; removing a key intentionally retires its outstanding credentials.

## Scope model

Organization contains Projects; Teams belong to an Organization and may be granted to Projects. Users join through Memberships. Role assignments bind subject (user/team) to scope (platform/organization/project/team). Instances have one owner (`user|team|project`) and optional explicit shares. Provider Targets are global and usable only through target grants (ADR-0003).

## Roles and principal permissions

| Role | Principal actions |
|---|---|
| Platform Administrator | all platform actions, auth mode, global policy, provider admins |
| Organization Administrator | org/projects/teams/memberships/quotas/templates and org audit; no provider credential values |
| Provider Administrator | register/configure/health/credential refs/target grants; no implicit lab console access |
| Lab Author | create/edit templates, upload bundles, publish revisions within granted project |
| Instructor / Lab Operator | validate/plan/batch-create, lifecycle/TTL/snapshot/reset/destroy shared class labs |
| Participant / Student | create within grants/quotas, view/use own or team labs, allowed lifecycle/access actions |
| Viewer / Auditor | read permitted resources/events/audit; no access session unless separately granted |

Fine-grained stable actions include `providerTarget.register|configure|health.read|grant`, `template.create|revision.publish`, `instance.create|batchCreate|read|start|stop|reset|ttl.extend|snapshot|restore|clone|destroy`, `accessSession.create|revoke`, `quota.manage`, `membership.manage`, `audit.read`.

Authorization evaluates actor, action, resource/scope ancestry, membership validity, instance ownership/shares, target grant, quota/policy constraints, and sensitive subfield. List endpoints apply the identical predicate in query filters and cannot reveal existence across tenants. Operations capture the actor and reauthorize at execution for destructive/credential-sensitive steps; loss of permission stops at a safe boundary.

Users, organizations, projects, teams, memberships, role assignments, and target grants are durable. Mutable administrative records return resource versions and ETags; renewal, state changes, and revocation require `If-Match`, so concurrent writers produce one winner. Revocation retains the identity tuple with an ended validity interval and renewal updates that same tuple. Disabling a user revokes all sessions without reviving them on re-enable, and the store refuses to disable or revoke the last active platform administrator. Enabling authentication on a legacy no-auth database containing instances or jobs is refused with an offline migration/new-database instruction because inventing real actors and owners would be unsafe.

## Classroom isolation

Batch creation expands one request into independent instances owned by each user/team, with shared revision but fresh logical IDs/secrets. Participants cannot enumerate or access other teams’ instances. Instructors receive explicit project-scope operator rights. Snapshots/clones preserve access policy by rule, not by copying arbitrary ACL rows; a clone receives a new owner selection authorized at creation.
