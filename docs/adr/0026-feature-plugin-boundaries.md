# ADR-0026: Feature plugin boundaries

- **Status:** Accepted
- **Date:** 2026-09-18
- **Supersedes:** ADR-0023 (browser contribution slots); ADR-0009 (built-in feature screens)
- **Superseded by:** None

## Decision

Core retains typed lab semantics, immutable revisions, rendering, authentication and authorization, project/team ownership, quotas, expiration, durable operations, artifacts, atomic admission, provider coordination and Huma/OpenAPI. Basic lab management and administration remain built in. Feature packages cannot replace or bypass these boundaries.

Independently installed Template Library, Teams and Classroom browser packages supply feature workflows. Teams and collaborative access work without Classroom. Standard distributions install Template Library and Teams; Classroom is explicitly enabled per installation. Existing classroom URLs retain compatibility routing when the package is installed.

Add top-level page/navigation contributions and typed permission-declared host operations. The host routes only known operations through the canonical authorized API, binds the current project, bounds messages and keeps session cookies, bearer credentials and arbitrary HTTP proxying unavailable to frames. Immutable assets, opaque sandbox origins, frame-bound channels and restart-based installation remain mandatory. Contribution permissions never grant user authority.

Existing manifests, slots and host protocol v1 remain readable. New bridge operations and slots are optional capabilities; packages declare the required host contract. Missing feature packages must not prevent core readiness or ordinary lab management.

## Compatibility and validation

Preserve old wire fields, immutable revision identities, provider state and checksummed migrations. Use additive migrations and negotiated contracts. Test authorization, crash recovery and SQLite/PostgreSQL parity at changed boundaries. See [implementation contract](../design/portable-template-collaboration.md) and the dependency-ordered tasks in `design/checklist.yaml`.
