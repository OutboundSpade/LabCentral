# Reconciliation and durable jobs

## Reconciliation model

LabCentral reconciles only after explicit validate/plan/apply/refresh/lifecycle requests or policy timers (TTL, scheduled destruction, configured idle shutdown). It does not continuously repair drift in v1. `refresh` reads provider state and records drift; `apply` is the explicit authority to converge a reviewed plan.

An instance and every logical resource carry desired `generation`; observations carry `observedGeneration`. A spec/lifecycle change increments generation transactionally with its Operation. A stale worker may observe but cannot commit against a newer generation/fencing token.

## Operation and job execution

```mermaid
stateDiagram-v2
  [*] --> Queued
  Queued --> Running: lease
  Running --> Running: checkpoint / renew
  Running --> CancelRequested: user request
  CancelRequested --> Canceled: safe boundary
  CancelRequested --> Running: too late
  Running --> Compensating: resource attempt failed
  Compensating --> Failed: cleanup complete
  Compensating --> Partial: cleanup incomplete
  Running --> Succeeded
  Running --> Failed
  Running --> TimedOut
  TimedOut --> Running: outcome resolution / retry
```

Operation is user-visible intent; jobs are schedulable attempts; steps are checkpointed graph/resource actions; plugin invocations record the external side-effect boundary. Claim transactions set `leaseOwner`, `leaseUntil`, increment `fencingToken`, and honor operation/resource/instance locks. Workers renew before half-life. Expired work is reclaimed only after its invocation is queried; unknown external outcome blocks duplicate non-idempotent action.

Recommended initial defaults: 30-second job lease, heartbeat every 10 seconds, operation-specific deadlines, exponential retry with full jitter and maximum attempts/elapsed time by error class. Permanent validation/auth/quota/state-version errors are not retried automatically.

The single-node worker gives one provider resource invocation a ten-minute
deadline while continuing to renew its 30-second database lease. This covers
the Proxmox provider's bounded five-minute task polling plus configuration,
start, and guest-readiness confirmation without weakening fencing. Credential
resolution is scoped to the same invocation with one minute of expiry margin.

Target deployment limits follow [ADR-0021](../adr/0021-bounded-independent-deployments.md).
`GET/PUT /admin/provider-targets/{id}/deployment-settings` exposes a durable 1–16
override. The startup flag is the fallback until the first write (resource version
zero). PUT requires `If-Match`, including `"0"` for the initial override, and records
the administrator audit/event transactionally. Settings updates and claims share
the PostgreSQL target admission lock. SQLite uses its existing single writer.
Every replica reads the override during admission; reducing it leaves active and
unknown work occupying slots and permits recovery. A bounded worker pool supports
runtime increases without restarting. The existing target `vms` quota independently
limits reservations across all projects; neither setting asserts physical capacity.

## Graph execution

References and `dependsOn` form a DAG. Stable Kahn topological sorting produces a deterministic dependency order. The current worker executes each lab in that order, and destroys in reverse order. ADR-0021 permits independent lab operations to overlap up to an operator-configured target limit. Parallel branches inside a single lab remain future work: the existing recovery record tracks one unresolved invocation at a time.

On dependency failure, descendants become `blocked` with causal IDs; unrelated branches may continue when policy permits. Failure of resource 7 cleans only primitives newly created for 7, persists its diagnostic state, retains 1–6, and makes the instance `Degraded`. Retry resumes satisfied dependencies and uses original idempotency keys for the same attempt or a derived key for a new desired generation.

Destroy takes the reverse DAG, attempts all owned resources despite independent failures, and records leftovers. A destroy with leftovers is `partial`/instance `Degraded`, never `Destroyed`. Unknown ownership mismatch stops deletion and requires operator resolution.

```mermaid
flowchart LR
  A[Apply resource] --> B{Provider result}
  B -->|success| C[Persist state + event]
  B -->|failure with created IDs| D[Compensate this resource]
  D -->|complete| E[Failed; prior resources retained]
  D -->|incomplete| F[Cleanup failed + leftovers]
  B -->|timeout/connection loss| G[Unknown]
  G --> H[Query invocation / refresh]
  H --> B
```

## Quotas and reservations

Provider allocation claims are separate from capacity quota reservations. Under ADR-0013 the core persists a bounded opaque `(targetID, namespace, value)` unique claim together with its plan. Planning conflicts trigger bounded replan with exclusions. `held` becomes `committed` at plan acceptance; an unknown invocation, compensation failure, leftover, or destroy failure retains the claim as unavailable rather than recycling it. Only positive provider absence permits `released`. Claim writes use the same operation/instance locks and fencing checks as plan and provider-state checkpoints.

The `TASK-PROXMOX-003` repository prototype enforces batch atomicity, plan-binding checks, target scoping, state transitions, conflict reporting, and absence-gated release on both SQL dialects. The `TASK-PROXMOX-005` local create unit of work now writes accepted plans, committed claims, instance, resources, operation/job, audit intent, event, outbox, and idempotency record in one SQLite transaction; exact conflicts roll the entire transaction back and feed the bounded replan loop. Docker continues to propose no claims. Worker unknown outcomes and cleanup failures retain or orphan claims, while successful destroy or completed compensation releases them only after the provider reports absence.

Limits compose from platform, organization, project, team, user, and target; every applicable limit must admit the request. Before scheduling/apply, the same serializable transaction that creates the Instance/job creates an expiring reservation for instances, VM/container count, CPU, memory, storage, public IPs, published ports, and snapshots. Hard quantities are the fixed canonical/accepted-plan quantities; a provider must not realize a different hard quantity. Provider-specific cost units remain advisory under `OQ-QUOTA-001` and are not advertised as enforced. Successful realization commits planned usage. Failures release only after the provider outcome and cleanup are known; destroy releases only after absence is confirmed, while unknown or orphaned resources retain usage.

The `TASK-PROD-003` quota/lifetime slice implements these transitions on both SQL adapters. Shared advisory identity locks prevent policy installation or tightening from racing admission, including scopes with no pre-existing row. An invocation-free reservation is terminalized when either its hold or exact Instance TTL expires; eligible queued and expired-lease running jobs race worker reclamation through row locks, repeated predicates, and bounded serialization retry. The sweeper also emits exact-expiry-gated warnings and idempotent destroy requests. Worker apply checks DB-time expiry before validation, before every provider resource, and before ready finalization. Planned allocation claims are released when no invocation could have run; unknown outcomes retain quota/claims and set recovery-required.

Classroom batch admission composes the same per-Instance units without introducing a shared execution fate. The API validates and plans one immutable revision for 1–100 owners; the adapter then persists every independently fenced job and reservation in one serializable transaction. Owner ordering makes overlapping batch lock acquisition deterministic. A conflict returns the exact allocation keys for bounded whole-batch replanning; a quota, authorization, or persistence failure leaves no partial batch. After admission, workers claim and reconcile child Operations independently, so one participant's provider failure cannot stop unrelated jobs.

On takeover, a recovery-required claim carries the exact unknown invocation record. The worker calls provider `GetInvocation` with the original invocation ID and never redispatches the mutation. Incomplete, missing, unavailable, or corrupt journal lookup remains unknown and retained. A durable terminal result is persisted under the new fence, transitions claims once, and resumes apply/compensation/destroy from stored resource state. Lost-response integration proves one provider mutation and one recovery lookup.

## Events and audit

State transaction writes outbox rows with envelope: `eventID`, schema/version, type, occurredAt, aggregateType/ID, aggregateSequence, organization/project, actor, correlation/causation/operation IDs, safe payload. Delivery is at least once; consumers deduplicate event ID. Ordering is guaranteed only per aggregate sequence. Operational event retention is configurable (default proposal 90 days); immutable audit retention is policy-controlled and longer. Audit records authorization-relevant intent/result and redacted changes; events support progress/integration.

The first `TASK-PROD-003` implementation generalizes audit/event storage beyond the earlier job prototype. Audit and event streams have independent database-allocated monotonic positions; events also allocate a sequence per aggregate. Their versioned actor, scope, links, outcome/type, and deliberately redacted/safe payloads are immutable, while deletion exists only through bounded cutoff-based retention. Event cleanup skips every record with an undelivered outbox sink. Export enforces row and byte ceilings without loading an unbounded result set. Template mutations and classroom batch acceptance are fully integrated producers and atomically append audit, event, local outbox delivery, and their idempotency binding with the aggregate change. Authentication, authorization, target, operation, access and administrator producers are implemented under completed `TASK-PROD-003`.

The cross-adapter acceptance catalog is [`design/contracts/job-invariants.yaml`](../../design/contracts/job-invariants.yaml). It fixes the expected semantics for atomic outbox, fencing, unknown invocation recovery, idempotency conflicts, quota retention, leftover handling, SQLite/PostgreSQL claim equivalence, and at-least-once delivery before implementation selects SQL details.

The executable `TASK-JOBS-001` prototype binds every `TEST-JOB-*` ID to a Go test. Normal tests run the suite against a bounded single-writer SQLite adapter; `TestPostgreSQLJobConformance` runs the identical cases against PostgreSQL when `LABCENTRAL_TEST_POSTGRES_DSN` is set. Unknown payload versions remain unclaimable, expired unknown invocations yield `get_invocation`, and outbox retries retain event ID and aggregate sequence.

`TASK-DOCKER-001` composes those primitives into the first durable application path. A create transaction writes the instance, ordered resources, persisted provider plans, job, Operation, audit intent, event, outbox delivery, and idempotency binding atomically. The local worker revalidates the canonical Lab, uses the pinned plans, prepares one stable provider invocation per resource/action, and fence-checks every phase/result/finalization write. Apply compensation and explicit destroy traverse the reverse resource order and preserve cleanup failures as `partial`/`Degraded`. The original laptop runtime used one worker. ADR-0021 adds a bounded worker pool; graph-layer parallelism remains future scope.

`TASK-PROD-001` runs the same Store through independent PostgreSQL pools and worker identities. Concurrent claims have one winner, a replacement claim advances both attempt and fencing token, a partitioned old worker cannot publish, a prepared invocation is rebound to its original identity after takeover, and row-locked terminal finalization has exactly one winner. Concurrent equivalent create requests serialize by subject/action/key and return one original plus one replay instead of leaking a database serialization failure.

ADR-0015 separates target availability from core readiness. Worker claims are filtered to registered available targets before database leasing; unavailable work remains pending. A plugin failure does not fail unrelated jobs or release claims. Interrupted calls retain the existing unknown-invocation recovery path. ADR-0021 now admits up to the configured target limit across all replicas. PostgreSQL transaction advisory locks serialize admission, including targets without a saved discovery snapshot; SQLite has one owning process and connection. All running jobs occupy capacity, including expired leases and unknown outcomes. Recovery reuses a slot; decreasing a limit does not strand existing work. The worker pool includes temporarily unavailable targets in its startup capacity and cancels its local work context if lease renewal fails. Proxmox locks invocations and committed VMID/VLAN claims, allowing only independent claims to overlap.

The default deployment limit is one per target. Configure built-in providers with
`labd --provider-concurrency=proxmox=2,docker=4`, or set
`policy.maxConcurrentDeployments` (1–16) on an external installation’s target.
The CLI overrides matching provider policies. All replicas must use the same
limits. The pool has a global ceiling of 64 workers per process; target admission
is still enforced in the database. Limits include apply, cleanup and lifecycle
operations so those actions cannot bypass capacity.

While planning one lab, earlier resources’ tentative allocation claims join the
exclusion set for each later resource. Otherwise two VMs can both propose the
same still-unused VMID before the admission transaction runs. Exclusions are
copied and deduplicated; a provider returning an excluded claim produces a
validation error. SQLite/PostgreSQL regressions cover three VMs sharing an
allocation namespace, successful execution and owned cleanup.

Planning first snapshots occupied target allocation keys (held, committed and orphaned; released keys are reusable). The existing eight retries address newly concurrent claims, rather than sequentially discovering old allocations. The transaction remains authoritative. A bounded snapshot of 4096 keys fails explicitly above its supported size.
