# ADR-0025: Source bundles, isolated builds and target image preparation

- **Status:** Accepted
- **Date:** 2026-09-17
- **Supersedes:** None
- **Superseded by:** None

## Decision

Source plugins resolve administrator-configured references to immutable multi-file bundles and provenance; core validates and publishes under author authority. Synchronization is explicit through the API. Credentials are invocation-scoped and artifacts transferred through bounded scoped channels; plugins have no database access.

Builder plugins consume immutable recipes, contexts and pinned inputs in disposable isolated execution environments without provider/control credentials. Core manages typed source/build/import/preparation work subjects using durable jobs, database-time leases, fencing, audit/outbox and original invocation recovery. Each external invocation is a child operation; this is not a general plugin-defined workflow engine or within-lab parallel execution.

Build results are immutable content-addressed artifacts. Provider imports yield target-specific image bindings. Build deduplication is project-scoped and includes recipe/context/base digests, architecture, builder identity and all declared inputs. Imports additionally bind target/configuration. One cancelled waiter cannot cancel shared work. Provider planning sees persisted resolved image bindings. Existing revisions retain their renderer semantics; build reference resolution is explicitly versioned.

Admission rechecks authority and quotas after preparation; classroom admission remains atomic. Image approval and target qualification are independent of import completion. Arbitrary URL imports remain disabled. Extend backup/restore and reference-aware offline GC to all new records; target images require explicit retirement. VM-backed container runtime implementation remains provider-owned.

## Compatibility and validation

Preserve prior wire fields, checksummed migrations and immutable revision identities. Negotiate optional new services independently. Validate independent installation against unchanged core/UI artifacts, current authorization, restart recovery, SQLite/PostgreSQL parity and browser isolation. Installation compatibility never establishes target qualification.
