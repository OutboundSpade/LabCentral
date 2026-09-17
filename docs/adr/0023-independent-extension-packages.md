# ADR-0023: Independent extension packages and isolated browser contributions

- **Status:** Accepted
- **Date:** 2026-09-17
- **Supersedes:** ADR-0015 (provider-only installation scope); ADR-0009 (built-in-only browser contributions)
- **Superseded by:** None

## Decision

Extend explicit restart-based installation to provider, access, source, builder and browser contributions. A package may contain several contributions; each backend process exposes one category under its own configured identity and least-privilege credentials. Production attachment remains operator-provisioned; a manifest is not an OS sandbox. Preserve existing v1 manifests and installation files.

A v2 package identifies independently versioned contracts, protected executable paths and immutable browser asset digests. Installation verifies and snapshots bytes. Browser assets use a separately configured cookie-free origin and sandboxed frames with scripts but no same-origin, form, popup or top-navigation privileges. A frame-bound nonce and MessageChannel establish a bounded host API. Plugins never receive core session cookies or general bearer tokens. Product actions continue through Huma authorization; no arbitrary fetch proxy is exposed.

Contributions occupy lab panels, resource actions, access presentations or plugin settings. Core exposes sanitized resolved topology and status, not provider state. The initial host polls; SSE, runtime downloads, marketplaces and hot upgrades remain out of scope.

## Compatibility and validation

Preserve prior wire fields, checksummed migrations and immutable revision identities. Negotiate optional new services independently. Validate independent installation against unchanged core/UI artifacts, current authorization, restart recovery, SQLite/PostgreSQL parity and browser isolation. Installation compatibility never establishes target qualification.
