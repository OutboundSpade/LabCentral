# Persistence and artifacts

## Relational model sketch

Principal tables: installations; users/auth identities/sessions; organizations/projects/teams/memberships/role assignments; provider targets/grants/capability snapshots; templates/revisions/bundles; instances/resources/provider-state; operations/jobs/steps/plugin invocations; snapshots/access sessions; quotas/reservations/usage; events/outbox deliveries/audit records; idempotency records. IDs and tenant/scope columns are explicit on security-sensitive tables to support filtered queries and defense in depth.

Transactions enforce: immutable revision rows; one target/revision per instance; monotonic resource version/generation; unique operation idempotency scope/key; unique aggregate event sequence; one active conflicting lock; quota reservation serialization; atomic state/audit/outbox. Provider opaque bytes are stored separately with envelope metadata and digest.

SQLite uses WAL, foreign keys, busy timeout, one bounded write-worker pool, and process-level single-active-server lock. It is not supported on unreliable network filesystems or for multiple `labd` replicas. PostgreSQL uses transactions, row/advisory locks where appropriate, `SKIP LOCKED` job claims, and database time for leases. Repository conformance tests define observable semantics rather than identical SQL.

## Artifact-store contract

```text
StageUpload(expected size/media policy) -> uploadID
Write(uploadID, stream) -> measured size/digest
ValidateAndCommit(uploadID, manifest) -> ArtifactRef(digest, size, mediaType)
OpenAuthorized(ref, byteRange?) -> verified stream
DeleteUnreferenced(ref, gcToken) -> result
```

Objects are immutable and content-addressed by SHA-256; mutable metadata lives in relational rows. Filesystem storage uses same-filesystem staging, restrictive permissions, fsync/atomic rename, and no following links. Object storage uses multipart staging, checksum verification, server-side encryption policy, and private buckets; user-facing access is through authorized API/proxy or short-lived scoped URLs.

Reference counts alone do not authorize deletion. GC selects old unreferenced committed objects, records a candidate epoch, rechecks references after grace, then deletes idempotently. Failed upload staging has shorter TTL. VM images normally remain provider inventory refs; LabCentral artifact store holds only approved import sources/build outputs under explicit policy.

## Backup, restore, and migration

Production backup coordinates a database snapshot with an artifact inventory/manifest watermark; restore verifies every referenced digest and reports missing/orphaned artifacts. External secrets are backed up by their owning system. Provider resources are not reconstructed from database backup alone: post-restore starts in observation-safe mode until installation identity, target credentials, and ownership are confirmed.

Migrations are numbered and monotonic. Multi-replica rollouts use expand → dual-read/write if needed → backfill → contract after the compatibility window. Destructive schema changes and irreversible provider-state migrations require backup/restore rehearsal and ADR.

