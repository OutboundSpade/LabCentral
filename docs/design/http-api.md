# HTTP API

## Conventions

Base path is `/api/v1`; JSON uses stable opaque IDs and RFC 3339 timestamps. Huma generates OpenAPI 3.1 and schema endpoints. Collection responses use opaque cursor pagination (`items`, `nextCursor`) with stable `(createdAt,id)` ordering, bounded `limit`, filters, and authorization at query time. Resource responses include `ETag: "<resourceVersion>"`; updates/deletes require `If-Match` except idempotent action endpoints whose request pins expected generation.

Errors use `application/problem+json` with `type`, `title`, HTTP status, stable `code`, safe detail, instance/correlation ID, retryable/retryAfter, and `violations[] {fieldPath, resourceRef, code, message}`. Provider raw errors are sanitized.

## Resources

| Area | Representative endpoints |
|---|---|
| Bootstrap/auth | `GET /server-info`, `GET /session`, `/auth/local/*`, `/auth/oidc/*` |
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

## Versioning and retries

Additive fields/endpoints are minor-compatible. Semantic removal/type change requires `/api/v2`, ADR, migration, and published deprecation. Clients ignore unknown response fields but must not send unknown request fields. `GET`/validation/plan are safely retryable; mutations require idempotency. `429/503` include Retry-After when known.

