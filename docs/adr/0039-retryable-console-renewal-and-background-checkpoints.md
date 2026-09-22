# ADR-0039: Retryable access renewal and background SQLite checkpoints

- **Status:** Accepted
- **Date:** 2026-09-21
- **Supersedes:** ADR-0037 and ADR-0038 only for credential replacement during durable authorization renewal
- **Superseded by:** None

An active access-connection renewal keeps the same random opaque connection credential. Core
rehashes it with the current key on a durable renewal and repeats current
identity, authority, generation, resource and expiry checks. A successful
transaction whose response is lost can therefore be retried with the credential
already held by the authenticated access plugin. Only the one-time launch token
is consumed. Ending, revoking or expiring the lease still invalidates it; a retry
cannot revive an expired lease. No deadline, session lifetime or input fence is
extended without the existing authorized transaction. Read retention remains
bounded as in ADR-0038.

Previously, a commit completing near a callback timeout could replace the only
valid credential without delivering it, disconnecting an otherwise authorized
viewer. Keeping the credential does not make it a bearer credential usable by a
browser: callback transport identity, plugin registration, audience and resource
binding remain mandatory. It is reusable within its bounded active connection;
this is an explicit change to the previous replacement-on-renewal behavior.
Protocol fields and persisted schema are unchanged. Existing plugins already
accept unchanged lease values. Rolling back restores replacement behavior on
subsequent renewals without a data migration.

For file-backed SQLite, the single application writer disables automatic WAL
checkpoints. A dedicated connection performs PASSIVE checkpoints every five
seconds and reports failures. It cannot execute application mutations. The
writer and checkpointer retain synchronous=FULL, so every acknowledged WAL
commit remains durable; checkpoint work no longer occupies the application
writer connection. Shutdown cancels the task and closes it before releasing the
process ownership lock. PostgreSQL behavior is unchanged.

Validate a lost committed renewal response, unchanged revocation/expiry and
input bounds, independent checkpoint progress while the application connection
is occupied, FULL durability settings, and sustained real browser load. This
reduces avoidable contention, but does not guarantee acceptable fsync latency on
storage saturated by unrelated workloads.
