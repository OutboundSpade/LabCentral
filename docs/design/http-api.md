# HTTP API

Huma registration is the product API authority. The checked [OpenAPI document](../../api/http/openapi.json) lists the implemented paths, payloads, limits and response schemas. Generate it with `go run ./tools/openapi`; generate frontend DTOs with `bun run generate:api` in `web`. CI rejects drift. Do not add a second handwritten DTO contract or treat a planned endpoint as implemented.

HTTP handlers are grouped by feature under `api/http`: lab operations, templates, access, quota/lifetime, audit/events, authentication and administration. Common DTOs, authorization helpers, static serving and middleware remain separate. Core orchestration receives application/domain values rather than HTTP or plugin wire messages.

## Current behavior

Requests use `/api/v1`, stable IDs, JSON and RFC3339 times. Asynchronous accepted mutations return an operation and `202`/`Location`. Idempotency keys bind actor/action/request identity; resource updates use version preconditions where specified by OpenAPI. Collection filtering and authorization are endpoint-specific; consult the actual operation schema rather than assuming every collection has identical pagination.

The API includes server/session information; local/OIDC authentication and bearer/recovery flows; users, organizations, projects, teams, memberships, roles and target grants; registered targets and refresh; validation/planning; immutable templates/revisions; single and classroom-batch creation; instances/destroy and operation reads; quotas/lifetime; audit/events; and controlled access sessions. General lifecycle/snapshot/clone APIs, arbitrary bundle import, remote agents and SSE delivery are not implied by the provider protocol and remain future work.

Validation, plan, single create and batch create accept typed `inputs`. Each item names a declared parameter, its kind and exactly one matching value field. Names may contain underscores; duplicates, unknown kinds, extra values and plaintext-secret kinds fail closed. Optional target override envelopes remain separately bounded and versioned. Resolved inputs participate in instance identity and idempotency. Revision source verification and renderer semantics follow ADR-0016.

Target listing reports fresh, stale, undiscovered or unavailable targets with safe discovery facts. It does not reveal target credentials, endpoint/CA/ACL configuration or secret file paths. Unavailable provider targets do not make the core API unready. Template publication stores canonical authoring representations; instance digests describe resolved Labs.

## Authorization and browser boundary

Local/OIDC mode uses secure cookies, exact Origin and CSRF checks for browser mutations. Bearer-only requests use Authorization; mixed credentials fail closed. No-auth mode must be explicitly selected and remains loopback-only. Reads prefilter object/target scope; mutations repeat mutable authorization and quota checks in database transactions. Plugins cannot grant authority.

Access launches contain one-time tokens and a protected proxy URL, never provider management credentials. The separate access process exchanges tokens and periodically rechecks its authorization lease with core. Revocation/expiry and ownership changes close access conservatively.

Static serving exposes only generated export files. Unknown paths do not receive a catch-all HTML page. Responses retain same-origin CSP, no-referrer, MIME-sniffing and frame protections. Problem responses sanitize provider errors and expose stable product error codes where supported.
