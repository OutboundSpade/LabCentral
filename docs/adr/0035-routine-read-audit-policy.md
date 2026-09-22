# ADR-0035: Keep routine successful metadata reads off the audit writer

- **Status:** Accepted
- **Date:** 2026-09-21
- **Supersedes:** None
- **Superseded by:** [ADR-0037](0037-console-observation-and-lease-write-budget.md) (successful access metadata observations only), and [ADR-0038](0038-retained-input-leases-and-capability-hints.md) (UI capability hints only)

## Decision

Successful `instance.read`, `template.read` and `providerTarget.health.read`
authorization checks do not append audit/event records, joining the existing
`audit.read` exception. Every request still evaluates current identity, roles,
ownership and target grants. No authorization result is cached.

All denials and failures, mutations, administrative actions, console access and
other actions retain their synchronous durable audit behavior. Use an explicit
allowlist so future permissions remain audited by default. Existing records and
public schemas are unchanged.

## Reason and compatibility

Routine browser polling previously turned reads into synchronous disk writes.
During provisioning on shared storage those writes queued behind durable worker
updates, freezing pages despite WAL and independent read connections. Recording
every successful poll also increased contention for worker lease renewals.
The audit stream no longer inventories successful metadata views; it retains
security failures and state/access changes. This refines the audit policy in the
design documentation without reversing an accepted ADR.
