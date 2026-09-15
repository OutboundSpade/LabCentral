# ADR-0021: Bounded independent lab deployments

- **Status:** Accepted
- **Date:** 2026-09-15
- **Supersedes:** ADR-0018 (journal-wide execution serialization only)
- **Superseded by:** None

## Decision

The owner requested configurable provider deployment concurrency. Run independent
lab operations concurrently, bounded per configured target by a positive limit
of 1–16, defaulting to one. The database serializes target admission (transaction advisory locks in PostgreSQL,
single-process SQLite writer) and counts
all running operations, including expired leases and unknown outcomes. Recovery
of an already-running operation does not consume another slot. Limits apply
across replicas; replicas must share the same operator configuration.

Each lab still executes its deterministic dependency order sequentially. Parallel
branches inside one lab require a separate extension to multi-invocation recovery
and compensation; a dependency graph alone does not implement that extension.

Proxmox holds a shared compatibility lock plus exclusive, deterministic locks for
the invocation and committed VMID/VLAN claims throughout remote execution. Legacy
providers and journal-wide maintenance still acquire the compatibility lock
exclusively. Conflicting resources and duplicate invocations cannot overlap;
independent claims may proceed. Locks are kernel-owned and released on process
exit. Journal record formats and public schemas/protocols remain unchanged.

## Compatibility and validation

Preserve exact-target qualification, ownership markers, allocation fencing,
atomic checkpoints and separate provider identities. The changed provider must
pass fresh T3 qualification and a live overlapping-deployment check before its
private testbed rollout. This extends the owner's delegated review for the same
PVE1 target; no independent human or production approval is claimed.

Test SQLite/PostgreSQL admission, exhausted capacity, lease recovery, cancellation,
independent versus conflicting provider locks and compatibility with old exclusive
locks. Cross-node shared-filesystem failover qualification remains a production
gate. No performance guarantee or within-lab parallelism is implied.
