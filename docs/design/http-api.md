# HTTP API

## Conventions

Base path is `/api/v1`; JSON uses stable opaque IDs and RFC 3339 timestamps. Huma generates OpenAPI 3.1 and schema endpoints. Collection responses use opaque cursor pagination (`items`, `nextCursor`) with stable `(createdAt,id)` ordering—or immutable stream position for audit/events—bounded `limit`, filters, and authorization at query time. Resource responses include `ETag: "<resourceVersion>"`; updates/deletes require `If-Match` except idempotent action endpoints whose request pins expected generation.

Errors use `application/problem+json` with `type`, `title`, HTTP status, stable `code`, safe detail, instance/correlation ID, retryable/retryAfter, and `violations[] {fieldPath, resourceRef, code, message}`. Provider raw errors are sanitized.

## Resources

| Area | Representative endpoints |
|---|---|
| Bootstrap/auth | `GET /server-info`, `GET /session`, `POST /auth/tokens`, `POST /auth/logout`, `/auth/local/*`, `/auth/oidc/*` |
| Tenancy | `/users`, `/organizations`, `/projects`, `/teams`, `/memberships`, `/role-assignments` |
| Providers | `/provider-targets`, `/{id}/capabilities`, `/{id}/health`, `/{id}/grants` |
| Templates | `/lab-templates`, `/{id}/revisions`, `/lab-bundles` upload/finalize/metadata |
| Validation/plans | `POST /lab-validations`, `POST /lab-plans`, `GET /lab-plans/{id}` |
| Instances | `/lab-instances`, `/{id}/resources`, `/{id}/actions/{start|stop|pause|resume|reset|refresh|apply|destroy}` |
| Classroom batches | `POST /lab-instance-batches`, `GET /lab-instance-batches/{id}` |
| Snapshots | `/lab-instances/{id}/snapshots`, `/snapshots/{id}/actions/{restore|clone}` |
| Access | `/access-sessions`, `/{id}/actions/revoke` |
| Operations/events | `/operations/{id}`, `/{id}/actions/cancel`, `/operations/{id}/events`, `/events` |
| Governance | `/quotas`, `/quota-usage`, `/audit-records` |

Create/update/action requests accept `Idempotency-Key` (opaque 8–128 characters, subject+route scoped). Core stores request digest and result: replay with the same digest returns the original resource/Operation; different body returns `409 idempotency_conflict`. Recommended retention is at least 24 hours and at least the maximum operation duration.

Long actions return `202` with `Operation` and `Location`; object creation may also return reserved object metadata. `GET /operations/{id}` exposes step/resource progress and structured terminal result. `POST .../cancel` records a request, returning current state; it never promises cancellation.

SSE uses `text/event-stream`, event ID equal to durable event ID, named event type, safe JSON envelope, heartbeats, authorization recheck, and `Last-Event-ID` resume within retention. Polling Operation/event cursors are equivalent fallback. Slow clients are disconnected and resume durably.

`GET /server-info` returns server/API version, auth mode/methods, public API base, UI feature flags, enabled provider kinds (not target secrets), no-auth warning, and build info. Reverse-proxy/public URL config is server-controlled and allowlisted.

## Implemented control-plane surface

The thin local server currently exposes `GET /server-info`, `GET /session`, `GET /examples/docker-laptop`, `GET /provider-targets`, `POST /provider-targets/{id}/actions/refresh`, `POST /lab-validations`, `POST /lab-plans`, `GET|POST /lab-instances`, `GET /lab-instances/{id}`, `POST /lab-instances/{id}/actions/destroy`, and `GET /operations/{id}` beneath `/api/v1`. Target responses include registration/protocol features, redacted discovery evidence, freshness, and an optional safe authoring example containing only Lab source plus provider aliases and an approved image digest. They never expose target endpoint/CA/ACL configuration or credentials. Validation, plan, and create accept an optional registered `providerTargetId` and bounded provider-specific JSON object; omitting them uses the configured default target. Mutations return `202` and `Location`, bind idempotency keys to source/risk/target/override request digests, and instance reads return ETags. Huma's generated OpenAPI 3.1 document is tested for every implemented operation.

The embedded static handler serves only files that exist in the generated export and does not turn unknown paths into a catch-all HTML response. All API and UI responses receive a restrictive same-origin content security policy, no-referrer policy, MIME-sniffing protection, and frame denial. Local and OIDC login, session/logout, password recovery consumption/issuance, and explicit opaque bearer issuance are implemented. Browser mutations require exact Origin plus cookie/header CSRF; bearer-only mutations use `Authorization`, and mixed credentials fail closed.

Authorized creation of users, organizations, projects, teams, memberships, role assignments, and provider-target grants is implemented. Organization/project/team updates and membership/role/grant renewal use `PUT` plus `If-Match`; membership/role/grant revocation and user enable/disable are version-pinned action endpoints. The last active platform administrator cannot be removed. Authenticated target listing requires a project and prefilters the registered target IDs through the same grant predicate before snapshot reads; instance list/object/operation reads are likewise filtered before resource detail, and mutations recheck object policy.

The in-progress `TASK-PROD-003` surface adds project-scoped `GET|POST /lab-templates`, `GET|PUT /lab-templates/{id}`, and list/publish/get routes beneath `/{id}/revisions`. Template metadata updates require `If-Match`; published revisions are immutable, content-addressed, and carry author/renderer/canonical provenance. Instance creation accepts exactly one inline `source` or immutable `revisionId`; revision lookup is project-authorized before artifact access and the resulting Instance persists the revision binding.

`GET /audit-records`, `GET /events`, and bounded `GET /audit-records/export` use authorization-scoped SQL filters before reading records. Platform, organization, and project queries require `audit.read` at that exact scope. Record pagination uses a versioned opaque stream cursor; time bounds are RFC 3339; export is newline-delimited JSON with explicit record/byte caps and a continuation header. Audit/event payloads use closed bounded JSON objects and reject duplicate or secret-bearing keys.

Scoped governance now exposes `GET|PUT /quotas/{scopeKind}/{scopeId}/{dimension}`, `GET /quota-usage/{scopeKind}/{scopeId}`, and `GET|PUT /lifetime-policies/{scopeKind}/{scopeId}`. Scope kinds and hard dimensions are closed typed values; organization identity is mandatory only where needed to disambiguate project/team/user scope. Mutations require `Idempotency-Key` plus exactly one create precondition (`If-None-Match: *`) or versioned update precondition (`If-Match: "N"`) and return ETags. Dynamic quota usage is `Cache-Control: no-store`.

`POST /lab-instances/{id}/actions/extend-ttl` requires `instance.ttl.extend`, `If-Match`, and an idempotency key. The response is a deliberately narrow lifetime snapshot rather than a mixed current/historical Instance: expiry, maximum expiry, warning/grace, renewal policy, policy digest, resource version, and update time. Authorization occurs before object mutation and the store repeats active actor, role, membership, participant-owner, and target-grant checks inside the serializable write transaction. Expiration is internal policy work and is not exposed as a caller-spoofable system identity.

`POST /lab-instance-batches` requires an immutable `revisionId`, project, common target/lifetime/override, and 1–100 unique `user|team` owners. It is instructor/platform-administrator only, returns `202` with a durable batch resource and each independently observable Operation, and binds replay to the entire ordered request. Admission is all-or-nothing across authorization, quotas, claims, Instances, jobs, Operations, audit/event/outbox, and idempotency. `GET /lab-instance-batches/{id}` reauthorizes every owner at read time.

`POST /access-sessions`, `GET /access-sessions/{id}`, and
`POST /access-sessions/{id}/actions/revoke` implement the VNC access lifecycle.
Creation requires owner-scoped authorization and idempotency, returns `201`
with `Cache-Control: no-store`, `Referrer-Policy: no-referrer`, a token-free
launch URL, and one one-time token. The DTO excludes endpoint bytes, provider
state, keyed hashes, plugin opaque state, and callback leases. Reads and
version-pinned revocation reauthorize the exact Instance. The static router
supports `/access/{id}` deep links; the browser passes launch material from
same-tab session storage as the first same-origin WebSocket frame.

SSE, cancellation, and the remaining resource surface are not yet implemented.

## Versioning and retries

Additive fields/endpoints are minor-compatible. Semantic removal/type change requires `/api/v2`, ADR, migration, and published deprecation. Clients ignore unknown response fields but must not send unknown request fields. `GET`/validation/plan are safely retryable; mutations require idempotency. `429/503` include Retry-After when known.
