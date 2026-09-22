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

Fine-grained stable actions include `providerTarget.register|configure|health.read|grant`, `template.create|read|update|revision.publish`, `instance.create|batchCreate|read|start|stop|reset|ttl.extend|snapshot|restore|clone|destroy`, `accessSession.read|create|revoke`, `quota.manage`, `membership.manage`, `audit.read`.

The implemented policy matrix grants template metadata/revision management to Lab Authors and scoped administrators, template reads to classroom roles, batch creation to instructors, own-instance TTL/access actions to participants, and scoped audit/event reads to administrators and viewers. Audit/event endpoints authorize the requested platform, organization, or project scope before issuing their filtered query; a project query must include its organization scope. Quota management, Instance TTL extension, classroom batch creation, and short-lived browser console access have production command paths. Quota handlers authorize the exact requested scope before store access; TTL mutation additionally repeats active actor, role, membership, owner relationship, and target-grant evaluation inside its serializable write transaction. Batch creation authorizes every requested owner before planning and repeats active actor, instructor/platform role, membership, owner scope, and target-grant checks inside the all-or-nothing write transaction. Controlled access rechecks the same live authority at one-time exchange and every 15-second lease, and closes within 30 seconds if the core cannot renew authorization.

ADR-0035 keeps successful routine metadata reads (`instance.read`, `template.read`, `providerTarget.health.read`, `audit.read`) off the durable audit writer. Every request still evaluates current authority. All denied/failed reads and all other policy boundary calls emit bounded authorization decisions; SQL-prefiltered instance listing uses one summary decision on failure. Mutations, administration and console access remain synchronously audited. Authenticated HTTP contexts bind the user actor to administrator transactions, while bootstrap and reconciliation use explicit system actors.

Authorization evaluates actor, action, resource/scope ancestry, membership validity, instance ownership/shares, target grant, quota/policy constraints, and sensitive subfield. List endpoints apply the identical predicate in query filters and cannot reveal existence across tenants. Operations capture the actor and reauthorize at execution for destructive/credential-sensitive steps; loss of permission stops at a safe boundary.

Users, organizations, projects, teams, memberships, role assignments, and target grants are durable. Mutable administrative records return resource versions and ETags; renewal, state changes, and revocation require `If-Match`, so concurrent writers produce one winner. Revocation retains the identity tuple with an ended validity interval and renewal updates that same tuple. Disabling a user revokes all sessions without reviving them on re-enable, and the store refuses to disable or revoke the last active platform administrator. Enabling authentication on a legacy no-auth database containing instances or jobs is refused with an offline migration/new-database instruction because inventing real actors and owners would be unsafe.

## Classroom isolation

Batch creation expands one request into independent instances owned by each user/team, with shared revision but fresh logical IDs/secrets. Participants cannot enumerate or access other teams’ instances. Instructors receive explicit project-scope operator rights. Snapshots/clones preserve access policy by rule, not by copying arbitrary ACL rows; a clone receives a new owner selection authorized at creation.

The implemented batch API accepts exactly one immutable revision and 1–100 unique user/team owners. It allocates a durable batch ID plus fresh Instance, Operation, reservation, audit/event, provider invocation, and ownership identities for every item. The adapter orders owner-specific locks and atomically persists the batch, all child scheduling units, quota reservations, provider allocation claims, one batch audit/event/outbox envelope, and the idempotency binding. Any authorization, quota, allocation, or relational conflict rolls the complete batch back. Reading the batch reauthorizes every owner; participants have no batch-read path and continue to see only their own or active-team Instances through the ordinary filtered Instance API.

## Workspace display and administration

TASK-UI-005 exposes existing project/organization mutations in the UI and adds
bounded administrative directories. Display names may contain spaces and Unicode
(up to 128 characters, no control characters or surrounding whitespace). Names
never establish identity or ownership. Scope IDs, role/target authorization and
optimistic update preconditions are unchanged. Project creation does not implicitly
create memberships, roles or target grants. A revoked target grant is renewed by
updating the same identity tuple with its current resource version.

## Self-service project collaboration

ADR-0029 adds project-limited self-service team enrollment without granting roles or organization membership. Existing administrative teams retain their semantics. Core continues to own every authorization decision. The new enrollment and driver-control contract is defined in [portable templates and collaboration](portable-template-collaboration.md).

## Managed classroom onboarding (ADR-0034)

`--oidc-auto-provision-project` opts into atomic first-login user, organization
membership and participant-role creation for a verified issuer/subject. Display
names use the verified name claim (preferred username fallback) and refresh at
sign-in. No email/name account linking, role restoration or disabled-user revival
is performed. Existing accounts retain their explicitly assigned authority.

`--disable-participant-deployment` suspends participant validation/planning and
launches. API permissions and transaction admission/preparation enforce the same
policy. Students retain group enrollment, assigned-lab viewing and consoles.
Instructors can manage project groups and deploy their labs. Restart all replicas
with consistent settings to change the installation policy; omit the flag to
restore the default participant deployment policy.

Under [ADR-0036](../adr/0036-instructor-managed-group-lab-deletion.md), participant
assignments cannot destroy team-owned labs. Students retain shared access and
personal-lab deletion; instructors retain scoped group-lab deletion. Both the core
lab details and Teams feature hide group deletion controls for students. Direct
API requests use the same core authorization rule. Expiration remains automatic.

Successful `accessSession.read` observations use the same ownership, role and target-grant checks as creation, without writing success audits. Denials, grants, revocations and control changes retain durable audit ([ADR-0037](../adr/0037-console-observation-and-lease-write-budget.md)).

Stale public requests do not emit cookie deletion because response ordering could erase a concurrently rotated session. Explicit logout still clears cookies. The UI may recover a GET that raced rotation only after cookie change and fresh confirmation of the same identity/roles; mutations are not replayed and revoked server tokens remain invalid.

Project capability hints are computed from current policy after authorizing the project read. Missing hypothetical permissions are not audited execution denials; actual attempted actions and denied project reads still are. See [ADR-0038](../adr/0038-retained-input-leases-and-capability-hints.md).

Logical query cancellation discards stale results but allows the network
response to deliver credential headers, with a bounded transport timeout.
Static documents/assets do not authenticate or rotate cookies. A GET can wait
up to ten seconds for a replacement cookie before failing authentication; it
retries only after verifying the same current identity and roles. Independent
requests remain concurrent so slow topology queries cannot block presence or
other pages. Mutations are never replayed; revoked tokens remain invalid.
A stale mutation's `authentication_required` response is still returned without
replay. Before discarding sibling requests' identity, the UI verifies the current
session. A valid same-identity/roles replacement cookie prevents a best-effort
old-console revocation from logging out the new console. Failed verification,
changed identity/roles and permission denials still invalidate the cache.
