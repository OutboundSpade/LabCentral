# ADR-0006: Database-backed operations, leases, and outbox

- **Status:** Accepted
- **Date:** 2026-09-02
- **Supersedes:** None
- **Superseded by:** None

## Context and constraints

Operations must survive restarts and partial failures without Kafka, Redis, or RabbitMQ. SQLite and PostgreSQL must share semantics.

## Considered options

1. In-memory jobs: not restart-safe.
2. External broker plus database: unnecessary local/operational burden and dual-write risk.
3. Database job rows, step checkpoints, leases, and transactional outbox.

## Decision

Use option 3. API transactions persist desired state, an Operation, initial Job, audit intent, and outbox rows atomically. Workers claim jobs with lease owner, expiry, and monotonically increasing fencing token. Every side effect is preceded by a durable invocation record and stable idempotency key. Steps checkpoint before lease release. SQLite runs a bounded single-writer worker configuration; PostgreSQL uses row locking/`SKIP LOCKED`-equivalent claims.

## Consequences

Delivery is at least once; handlers/plugins must be idempotent. Events preserve per-aggregate sequence, not global ordering. External sinks consume outbox deliveries with deduplication.

## Security implications

Payloads contain references and redacted metadata, not secrets. Lease theft cannot authorize a call; worker identity and fencing are checked on commits.

## Compatibility implications

Job/step payloads are versioned and migratable. Old workers must not claim unknown payload versions.
