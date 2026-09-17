# ADR-0009: Resource HTTP API and statically exported UI

- **Status:** Accepted
- **Date:** 2026-09-02
- **Supersedes:** None
- **Superseded by:** ADR-0023 (browser contribution scope only; other decisions remain accepted)

## Context and constraints

The HTTP API is canonical. Long provider calls cannot hold HTTP requests. The UI must require no production Node.js and should provide static route entry points.

## Considered options

1. Server-rendered SvelteKit Node deployment.
2. Single fallback SPA document.
3. SvelteKit static export with prerendered route shells and client-side API data.

## Decision

Use option 3. Bun builds SvelteKit/DaisyUI assets. Go embeds/serves them with explicit static entry points and a narrow fallback only for documented client routes. Runtime bootstrap comes from unauthenticated `GET /api/v1/server-info`; authenticated identity comes from `/api/v1/session`. Mutations return `202 Operation`; progress uses SSE with `Last-Event-ID` and polling fallback.

## Consequences

Deep links work through emitted route entry points; dynamic data loads after hydration. OpenAPI is generated and checked as a breaking-change contract.

## Security implications

Bootstrap exposes no secrets. Cookie sessions use Secure/HttpOnly/SameSite and CSRF tokens; bearer clients use Authorization headers. CSP and no-referrer policies protect access tokens/URLs.

## Compatibility implications

`/api/v1` is independently versioned. UI release declares a supported server range.
