# ADR-0011: Repository and artifact-store interfaces

- **Status:** Accepted
- **Date:** 2026-09-02
- **Supersedes:** None
- **Superseded by:** None

## Context and constraints

Local mode needs SQLite/filesystem; production needs PostgreSQL and may need object storage. Large bundles/images do not belong in relational rows.

## Considered options

1. SQL calls throughout domain services.
2. Backend-neutral repository/unit-of-work ports plus an artifact store.
3. Event sourcing as primary persistence.

## Decision

Use option 2. Application services consume cohesive repositories and a transaction/unit-of-work abstraction, not one repository per table. Artifacts use content-addressed keys, immutable manifests, streaming upload/download, checksums, quotas, and staging-to-commit. SQLite is local/single-node; PostgreSQL is required for multi-replica production. Filesystem artifacts are single-node; object storage is required when replicas do not share a safe volume.

## Consequences

Conformance tests run against both databases/stores. SQL-specific claim strategies live in adapters. Database and artifact backups must be coordinated through referenced digests.

## Security implications

Bundle extraction occurs in a quarantined staging directory with path/symlink/type/count/size/decompression limits. Artifact access is authorized through metadata; raw store keys are not public.

## Compatibility implications

Database migrations are monotonic and support expand/migrate/contract. Artifact manifests and provider-state envelopes are versioned separately.
