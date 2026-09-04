# HTTP API

## Conventions

Base path is `/api/v1`; JSON uses stable opaque IDs and RFC 3339 timestamps. Huma generates OpenAPI 3.1 and schema endpoints. Collection responses use opaque cursor pagination (`items`, `nextCursor`) with stable `(createdAt,id)` ordering, bounded `limit`, filters, and authorization at query time. Resource responses include `ETag: "<resourceVersion>"`; updates/deletes require `If-Match` except idempotent action endpoints whose request pins expected generation.

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
| Snapshots | `/lab-instances/{id}/snapshots`, `/snapshots/{id}/actions/{restore|clone}` |
| Access | `/access-sessions`, `/{id}/actions/revoke` |
| Operations/events | `/operations/{id}`, `/{id}/actions/cancel`, `/operations/{id}/events`, `/events` |
| Governance | `/quotas`, `/quota-usage`, `/audit-records` |

Create/update/action requests accept `Idempotency-Key` (opaque 8–128 characters, subject+route scoped). Core stores request digest and result: replay with the same digest returns the original resource/Operation; different body returns `409 idempotency_conflict`. Recommended retention is at least 24 hours and at least the maximum operation duration.

Long actions return `202` with `Operation` and `Location`; object creation may also return reserved object metadata. `GET /operations/{id}` exposes step/resource progress and structured terminal result. `POST .../cancel` records a request, returning current state; it never promises cancellation.

SSE uses `text/event-stream`, event ID equal to durable event ID, named event type, safe JSON envelope, heartbeats, authorization recheck, and `Last-Event-ID` resume within retention. Polling Operation/event cursors are equivalent fallback. Slow clients are disconnected and resume durably.

`GET /server-info` returns server/API version, auth mode/methods, public API base, UI feature flags, enabled provider kinds (not target secrets), no-auth warning, and build info. Reverse-proxy/public URL config is server-controlled and allowlisted.

## Implemented laptop surface

The thin local server currently exposes `GET /server-info`, `GET /session`, `GET /examples/docker-laptop`, `GET /provider-targets`, `POST /provider-targets/{id}/actions/refresh`, `POST /lab-validations`, `POST /lab-plans`, `GET|POST /lab-instances`, `GET /lab-instances/{id}`, `POST /lab-instances/{id}/actions/destroy`, and `GET /operations/{id}` beneath `/api/v1`. Target responses include registration/protocol features, redacted discovery evidence, freshness, and an optional safe authoring example containing only Lab source plus provider aliases and an approved image digest. They never expose target endpoint/CA/ACL configuration or credentials. Validation, plan, and create accept an optional registered `providerTargetId` and bounded provider-specific JSON object; omitting them uses the configured default target. Mutations return `202` and `Location`, bind idempotency keys to source/risk/target/override request digests, and instance reads return ETags. Huma's generated OpenAPI 3.1 document is tested for every implemented operation.

The embedded static handler serves only files that exist in the generated export and does not turn unknown paths into a catch-all HTML response. All API and UI responses receive a restrictive same-origin content security policy, no-referrer policy, MIME-sniffing protection, and frame denial. Local and OIDC login, session/logout, password recovery consumption/issuance, and explicit opaque bearer issuance are implemented. Browser mutations require exact Origin plus cookie/header CSRF; bearer-only mutations use `Authorization`, and mixed credentials fail closed.

Authorized creation of users, organizations, projects, teams, memberships, role assignments, and provider-target grants is implemented. Organization/project/team updates and membership/role/grant renewal use `PUT` plus `If-Match`; membership/role/grant revocation and user enable/disable are version-pinned action endpoints. The last active platform administrator cannot be removed. Authenticated target listing requires a project and prefilters the registered target IDs through the same grant predicate before snapshot reads; instance list/object/operation reads are likewise filtered before resource detail, and mutations recheck object policy. Cursor pagination beyond the bounded instance list, SSE, cancellation, and the broader resource surface remain later production work.

## Versioning and retries

Additive fields/endpoints are minor-compatible. Semantic removal/type change requires `/api/v2`, ADR, migration, and published deprecation. Clients ignore unknown response fields but must not send unknown request fields. `GET`/validation/plan are safely retryable; mutations require idempotency. `429/503` include Retry-After when known.
