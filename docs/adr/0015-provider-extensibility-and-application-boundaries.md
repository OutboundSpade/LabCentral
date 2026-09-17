# ADR-0015: Provider extensibility and application boundaries

- **Status:** Accepted
- **Date:** 2026-09-04
- **Supersedes:** ADR-0011 (application repository shape only)
- **Superseded by:** ADR-0023 (installation category scope only; other decisions remain accepted)

## Context

The architecture audit and owner approval prioritize external infrastructure providers implementing the existing resource kinds. The runtime has provider-specific startup paths, duplicate application contracts, and process-wide failure propagation.

## Decision

Keep the modular monolith, Huma API, typed resources, core authorization, and separate provider/access processes. Use explicit versioned installation configuration and verified manifests to bind independently distributed plugins to targets. Restart-based installation is sufficient. Host policy validates identities, executable digests, permissions, protocol/features and state compatibility; a plugin never grants itself authority. Existing command-line profiles remain compatibility inputs to the same runtime.

Application-owned command and query ports describe actual atomic operations, grouped by consumer. Remove unused repository/unit-of-work abstractions. Store adapters own transactions and repeat authorization/quota checks within them. Provider transport and serialized state handling belong in an adapter; orchestration consumes typed invocation outcomes.

After successful startup preflight, a failed plugin makes only its targets unavailable. Registration is rebuilt with bounded retries and a circuit breaker, and unknown mutations recover by their original invocation ID. Core readiness depends on core services; plugin availability is reported separately. Systemd/Kubernetes owns attached process lifecycle, and the host owns local children. Third-party production plugins require administrator-provisioned separate identities and enforced filesystem/network permissions; local children are an explicit trusted-development mode.

SQLite/PostgreSQL and artifact semantics from ADR-0011 remain unchanged. Retain existing checksummed migrations and legacy event/state columns until all readers can be migrated without losing replay or audit history. New ports do not divide one atomic classroom admission into separate commits.

## Compatibility and validation

Preserve public wire and persisted formats. Existing official profiles are supported through compatibility configuration. Validate an external provider without recompiling core, two target bindings, independent failure, invocation recovery, and SQLite/PostgreSQL parity. Independent plugin compatibility is defined by negotiated contracts and readable state rather than identical product release versions.
