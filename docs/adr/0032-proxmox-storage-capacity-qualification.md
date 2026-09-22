# ADR-0032: Capacity-bounded Proxmox storage qualification

- **Status:** Accepted
- **Date:** 2026-09-21
- **Supersedes:** None
- **Superseded by:** None

## Context

Directory storage can use ZFS. Its filesystem-reported total capacity depends
on referenced and available bytes and may change during ordinary activity.
Treating that counter as an immutable infrastructure identity makes otherwise
unchanged targets lose qualification. Existing signed projections must retain
their byte identities.

## Decision

Add closed target-config v1.6 and mutation-inventory v1.2. Preserve all v1.0
through v1.5 target behavior and prior inventory projections. v1.6 retains the
v1.5 network, CPU, console and image-import contracts, including separately
validated imported-image evidence.

v1.6 requires `storageCapacityFloors`, one `{id, minimumTotalBytes}` entry for
every configured destination disk storage and image staging storage, with no
extra or duplicate entries. Minima are positive signed 64-bit byte counts for
the fixed destination node. The byte-exact signed target configuration binds
these operator-reviewed minima; changing them requires fresh qualification.

Every fresh mutation-inventory projection first validates the full discovered
inventory, then requires each configured storage to exist at the destination
node and report total capacity at least its minimum. Unknown, zero, malformed,
missing or below-minimum capacity fails closed. The new projection omits all
filesystem total counters, just as earlier projections omitted used/free
counters. Storage IDs, nodes, types, formats, content, active/enabled/shared
status, other structural infrastructure facts and discovery issues remain bound.
The v1.4 configured-static-template filtering is retained. A distinct v1.2
separator prevents reuse of old inventory identities. Discovery continues to
report actual capacity; projection never overwrites those measurements.

A minimum is an admission floor, not a space reservation or a guarantee of
available bytes. Quotas, allocation fencing, provider operation failures,
ownership and cleanup contracts remain unchanged. Runtime signature, reviewer
trust, expiration and independent production review requirements remain intact;
this decision introduces no signing or qualification bypass.

## Validation and rollout

Test small capacity drift and growth, exact/below-minimum boundaries, missing or
malformed storage, staging coverage, unchanged discovery capacity, structural
drift rejection and legacy digest equivalence. Run affected normal/race tests
and actual exact-build clone/lifecycle/failure/import/isolation qualification
before enabling the changed target. Retain prior configuration and executable
for rollback; old evidence never qualifies a changed executable/configuration.
Primary provider sources are recorded in `docs/references.md`.
