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

`TASK-DOCKER-001` implements the local artifact port under `internal/adapter/filesystem`. The adapter creates a private root, keeps staging and objects on the same filesystem, persists the expected manifest beside a random upload, bounds writes by the declared byte count, measures SHA-256 while streaming, remeasures before commit, changes committed objects to mode 0400, fsyncs files/directories, and atomically renames into a `sha256/<prefix>/<digest>` key. Open re-verifies content and refuses symlink or non-regular paths. Authorization remains an application concern expressed by `AuthorizedArtifactRef`; the raw artifact tree is not served by HTTP.

`TASK-PROD-001` adds an explicitly selected shared-POSIX mode for multiple replicas. Mutable staging operations use per-upload kernel advisory locks across independently opened stores, startup verifies that two file descriptions cannot hold the same exclusive lock, and immutable digest commits remain same-filesystem atomic renames. The operator must provide one durable ReadWriteMany filesystem with coherent `flock`, rename, and `fsync` semantics and the same service UID on every replica; ordinary local disks and filesystems that do not provide those semantics are rejected as production topology choices. Object storage and artifact GC remain `TASK-PROD-004` work.

The control-plane relational adapter adds checksummed migrations for instances, ordered logical resources, Operations, invocation records, immutable instance target/override bindings, and expiring provider-target capability snapshots on top of the job/outbox, quota, and allocation schemas. SQLite startup enables foreign keys, WAL, a busy timeout, one database connection, and a non-blocking process file lock. PostgreSQL uses native binary/integer types, database time, `SKIP LOCKED`, transaction-scoped idempotency locks, bounded serialization/deadlock retry, and a session advisory lock around migrations. Target rows persist no secret or target configuration bytes.

## Backup, restore, and migration

Production backup coordinates a database snapshot with an artifact inventory/manifest watermark; restore verifies every referenced digest and reports missing/orphaned artifacts. External secrets are backed up by their owning system. Provider resources are not reconstructed from database backup alone: post-restore starts in observation-safe mode until installation identity, target credentials, and ownership are confirmed.

The implemented `TASK-PROD-001` compatibility gate uses PostgreSQL custom-format `pg_dump`, drops only its isolated test schema, restores it with `pg_restore --exit-on-error`, reapplies the migration verifier, compares every durable table count, and reads the restored terminal Instance. The operator procedure is [`../operations/postgresql-control-plane.md`](../operations/postgresql-control-plane.md). It requires a full replica stop for this first conservative backup contract; online coordinated watermarks and encrypted automated backups remain part of `TASK-PROD-004`.

Migrations are numbered and monotonic. Multi-replica rollouts use expand → dual-read/write if needed → backfill → contract after the compatibility window. Destructive schema changes and irreversible provider-state migrations require backup/restore rehearsal and ADR.
