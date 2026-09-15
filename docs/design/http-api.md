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

## Classroom discovery (TASK-UI-001)

Authenticated `GET /projects` returns authorized project choices with evaluated
`allowedActions` for navigation. `GET /users` and `GET /memberships` require
membership administration at their declared scopes. `GET /projects/{id}/members`
requires instructor batch authority on the exact target and returns only active
users who may create their own lab there. Queries use bounded candidate pages,
keyset pagination and visible-only continuation IDs; policy is re-evaluated on
subsequent requests. Navigation visibility never grants mutation authority.

`GET /lab-templates/{id}/revisions/{revisionId}/content` checks project/template
binding, verifies stored content, and returns author source plus typed parameter
declarations. The generated OpenAPI document registers authenticated handlers
without constructing a running no-auth server. Static route shells are served
for `/launch`, `/lab`, `/templates`, `/classroom`, `/people`, and `/settings`;
unknown URLs remain 404. Valid `/access/{id}` paths serve the generated
`200.html` shell and match an explicit client route. Root-relative boot assets
prevent nested console URLs from requesting assets beneath `/access/`.
Console launch accepts the exact expected path as a relative URL or an absolute
URL on the configured origin; query strings, fragments and foreign origins are
rejected. Launch material remains in session storage only until consumed.

## Administrative operations (TASK-OPS-001)

GET /admin/operations requires platform membership administration authority.
It returns durable queued/running/failed counts and at most 200 operation rows,
filtered by optional project and active/failed/all, with a time-and-ID cursor.
GET /admin/lab-instances/{id}/usage requires the same platform authority plus
instance read access, rechecked after the bounded provider call. Unavailable
samples are explicit and never represented as zero usage. Responses exclude
credentials, provider state and upstream addresses. The /operations static
shell is administrative navigation; server authorization remains authoritative.
Bulk UI deletion uses the existing per-instance destroy endpoint and preserves
independent idempotency keys and results through uncertain-response retries.

## Administration discovery (TASK-UI-005)

GET /organizations requires platform organization administration and returns
bounded keyset pages, including organizations without projects. GET
/provider-target-grants requires platform provider-target-grant authority and
returns active, scheduled and ended grants with versions and validity. Both use
DirectoryQuery (`after`, `limit` up to 200). GET /admin/provider-targets requires
platform provider-target-grant authority and returns the registered target list
without a selected project. It reuses the existing redacted target DTO; the
project-scoped provider-targets endpoint retains its existing authorization.
These additive reads use application repository ports and the existing SQL adapter.

Organization/project display names accept 1–128 Unicode characters, no surrounding
whitespace or control characters. This expands accepted names without changing
stable IDs, persisted fields, existing slug names or scope relationships. Usernames
and team names retain their existing validation. Existing update/revoke commands
retain resource-version preconditions and append-only governance records.

Static shells include /workspaces and /plugins. The generated OpenAPI and frontend
DTOs include all new discovery endpoints. The plugin installation contract remains
a protected operator file plus restart, as ADR-0015 specifies.
