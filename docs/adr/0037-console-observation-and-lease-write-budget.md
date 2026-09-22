# ADR-0037: Separate console observation from durable authority changes

- **Status:** Accepted
- **Date:** 2026-09-21
- **Supersedes:** ADR-0035 only for successful access metadata observations
- **Superseded by:** [ADR-0038](0038-retained-input-leases-and-capability-hints.md) (unchanged driver observations only); ADR-0039 (credential replacement during durable renewal only)

## Decision

Introduce `accessSession.read` with the same role, ownership and target-grant
requirements as access creation. Successful session and presence observations do
not append authorization audit records. Denials and failures remain audited.
Creating/revoking access and requesting/changing control retain durable audit.
No authorization decision is cached; presence checks current database authority.

Shared-console callbacks retain their one-second current-authorization check.
A spectator whose session is neither holder nor requested driver can return the
existing opaque authorization lease from a consistent read snapshot while more
than half the normal 15-second lease remains. This response does not rotate the
token, extend its deadline, update its last durable renewal, or grant input.
Due renewals, holders, pending drivers and denials use the existing transactional
write path. Input leases remain fenced and at most five seconds; a callback
cannot restore expired input without a new explicit request.

## Reason and compatibility

Presence was evaluated as access creation twice per poll, producing durable
writes for every viewer. Spectators also rotated durable tokens every second,
even though only input holders need that write cadence. On shared rotating disks
this competes with launch and provisioning and can starve bounded callbacks.
WAL read concurrency alone cannot remove writes introduced by read requests.

Public HTTP and plugin schemas, authorization deadlines, revocation bounds,
SQLite FULL durability, and provider qualification remain unchanged. The plugin
already accepts the same opaque lease and an unchanged future deadline. A
transient internal result flag distinguishes retention from rotation. Unknown
permissions remain audited by default. Rollback requires no database migration.

## Validation

Exercise current authorization and denied observations, unchanged spectator
lease deadlines/tokens, renewal before expiry, driver handoffs, stale generation,
and blocked SQLite writers with PostgreSQL parity and race checks. Run real
independent browser sessions across five groups, measuring failed launches,
disconnects, request latency and server disk pressure before and after deployment.
Keep identities, endpoints, detailed measurements and rollback records private.

VNC launch discovery through upstream authentication is serialized per resource.
Proxmox's `qm vncproxy` resets that VM's VNC password; overlapping launches can
invalidate an earlier handshake. Coordination uses a process-local bounded gate
under SQLite's single-process ownership lock, or a PostgreSQL transaction-scoped
advisory lock across replicas. Neither holds SQLite's writer or authority rows
across plugin calls. Other resources proceed independently; cancellation releases
coordination. This gate does not authorize access and cannot coordinate external
Proxmox UI clients. Provider and access plugin bytes are unchanged.

Public responses carrying an invalid stale cookie must not clear a newer cookie
installed concurrently by rotation or sign-in. Explicit logout still clears it.
A browser GET racing rotation may retry once only after the CSRF cookie changes
and a fresh session check confirms the same identity and roles. Mutations are
never replayed. Old server credentials remain immediately invalid after rotation;
no grace period or cached authorization is introduced.
