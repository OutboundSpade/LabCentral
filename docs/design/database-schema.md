# Database schema and migration sketch

Current architecture follows ADR-0015, ADR-0016 and ADR-0017. See [architecture](architecture.md), [installation and cleanup authority](../operations/plugin-installation.md), and [compatibility](../compatibility.md). Historical task checkpoints below remain evidence, not additional implementation requirements.

This design refines ADR-0006/ADR-0011 without committing to ORM or migration tooling. SQL names are stable conceptual names; the first migrations may adjust physical spelling while preserving these invariants.

## Core tables and keys

| Table group | Required columns and constraints |
|---|---|
| `installations` | singleton `id`, created time, version; installation ID never changes on ordinary restore. |
| `users`, `auth_identities`, `sessions`, `password_recovery_tokens`, `oidc_flows`, `authentication_attempts` | opaque IDs; user resource version/disabled state; issuer+subject unique; browser/bearer credential type; token/CSRF/state/browser/nonce values stored only as keyed hashes; encrypted PKCE verifier; key ID; expiry/revocation/consumption; database-time bounded durable rate windows and GC indexes. |
| `organizations`, `projects`, `teams` | opaque IDs, scoped unique names, lifecycle/resource version; project belongs to one org, team to one org. |
| `memberships`, `role_assignments` | subject/scope/role, validity interval and resource version; uniqueness preserves one renewable/revocable logical authority tuple. |
| `provider_targets`, `provider_target_grants` | global target, plugin binding/config fingerprint/credential ref, scoped grant/quota policy, optimistic version. |
| `capability_snapshots` | target, schema/plugin versions, discovered/valid timestamps, canonical payload digest/bytes, discovery errors. |
| `lab_templates`, `template_revisions` | template metadata is mutable; `(template_id, revision_number)` unique; revision bundle/canonical provenance immutable. |
| `artifacts`, `bundle_files` | digest primary identity, measured size/media/status; normalized path unique within bundle. No blob column. |
| `lab_instances` | revision/one target/owner scope, desired generation/lifecycle, canonical/policy/capability digests, phase, TTL, resource version. |
| `instance_batches`, `instance_batch_items` | immutable revision/project/actor/target/count plus ordered unique owner/Instance/Operation bindings; 1–100 items. |
| `logical_resources` | `(instance_id, logical_id)` unique, kind, desired/observed generation, phase/conditions, canonical spec bytes/digest. |
| `provider_states` | resource unique, plugin ID/version/state schema, opaque bytes/digest, migration flag; hard size limit. |
| `provider_ownership` | resource, provider primitive type+immutable ID+ownership token, generation, cleanup status; immutable identity unique per target. |
| `provider_allocation_claims` | `(target_id, namespace, value)` primary identity; Instance/resource/plan digest binding; state `held|committed|orphaned|released`; monotonic fencing token; bounded values and owner index. Released values alone may be rebound. |
| `provider_targets` | Global target identity/provider kind; non-secret configuration fingerprint; negotiated protocol/features; redacted discovery capability document and bounded validity interval. Credential and target-config envelopes are never stored. |
| `operations`, `jobs`, `operation_steps` | actor/command/aggregate/correlation, operation state; job payload version/attempt/availability/lease/fence; ordered checkpoints. |
| `plugin_invocations` | invocation/operation/resource, idempotency key, request digest, plugin/target, deadline/state/task ref/result digest. |
| `snapshots`, `access_sessions` | provenance/consistency/compatibility and session subject/resource/method/token hash/expiry/revocation/plugin state. |
| `quotas`, `quota_reservations`, `quota_usage` | dimension/scope/limit; reservation operation+expiry+planned/committed; usage observed/reconciled timestamp. |
| `aggregate_sequences`, `events`, `outbox_deliveries`, `audit_records` | per-aggregate sequence uniqueness; immutable event/audit IDs; sink delivery attempt/lease; redacted payload versions. |
| `idempotency_records` | `(subject_id, route_action, key)` unique, request digest, result resource/operation, expiry. |

All security-sensitive child tables carry or can join through an indexed organization/project/instance scope. Foreign keys are enabled in both adapters. User-visible deletes are soft lifecycle transitions where audit/provenance requires retention; artifact/outbox/session staging rows use explicit GC states.

## Lease and fencing transactions

PostgreSQL claim uses a short transaction with `FOR UPDATE SKIP LOCKED` (or equivalent), sets owner/expiry, and increments fence. SQLite serializes the same transition under `BEGIN IMMEDIATE` with a bounded busy timeout and one writer pool. The commit predicate for every checkpoint/result is conceptually:

```sql
UPDATE jobs
SET checkpoint = ?, lease_until = ?, resource_version = resource_version + 1
WHERE id = ? AND lease_owner = ? AND fencing_token = ? AND desired_generation = ?;
```

Zero affected rows means lost lease/stale generation; the worker cannot publish state or events. Database time, not worker wall time, evaluates leases. Plugin side effects never occur inside a database transaction.

## Atomic state and outbox

One transaction updates aggregate state, compares expected resource version/fence, allocates the next aggregate sequence, inserts event, audit record/intent, and outbox delivery. Unique `(aggregate_type, aggregate_id, sequence)` and `event_id` make retries safe. Sink delivery happens later and is at least once. Payloads reference artifacts/secrets; they never embed secret values.

## Quota reservation transaction

The adapter locks the applicable quota identities and usage rows in deterministic scope+dimension order, computes every platform/organization/project/team/user/target limit, and inserts one operation reservation in the same serializable transaction as Instance/job creation. The implemented hard dimensions are instances, VMs, containers, CPU millicores, memory bytes, storage bytes, public IPs, published ports, and snapshots. These quantities are fixed by the accepted canonical resource and provider plan; providers may not silently realize a different hard quantity. Ready realization moves reserved values to committed; unknown/partial cleanup moves them to retained. Destroy or failed admission releases quota and opaque allocation claims only after provider absence or proof that no invocation was dispatched.

## Migration policy

Each migration has ID, checksum, minimum/maximum compatible application version, reversibility, online/offline classification, and data-backfill status. Application startup:

1. refuses a database newer than its supported maximum;
2. applies only known forward migrations with an operator-configured policy;
3. records checksum and completion transactionally;
4. prevents old replicas from running after the compatibility floor advances.

Multi-replica changes follow expand → deploy dual-compatible code → bounded resumable backfill → verify → later contract. SQLite and PostgreSQL use separate SQL where needed but share migration IDs and postcondition fixtures. Provider opaque-state migration is never hidden inside a database migration.

## First repository conformance fixtures

[`design/contracts/job-invariants.yaml`](../../design/contracts/job-invariants.yaml) is the executable acceptance catalog for leasing, fencing, idempotency, atomic outbox, unknown outcomes, quota retention, and adapter equivalence. The first implementation adds crash-injection tests at every transaction boundary before choosing an ORM/query generator.

`TASK-JOBS-001` implements the focused prototype under [`internal/adapter/jobsdb`](../../internal/adapter/jobsdb). Migration `0001_jobs_outbox_prototype` is checksummed and transactional. SQLite is held to one database connection and uses an atomic `UPDATE ... RETURNING` claim; PostgreSQL uses an ordered `FOR UPDATE SKIP LOCKED` CTE. Both derive lease timestamps from database time, allocate aggregate event sequences inside the state/audit/event/outbox transaction, and require lease owner, fencing token, desired generation, and attempt on checkpoints. This is the conformance foundation, not the full production schema listed above.

`TASK-PROXMOX-003` adds checksummed migration `0002_provider_allocation_claims` to the same adapter. Reservations sort keys before a serializable transaction, reject partial batches, return exact conflicting keys, and replay only the same non-released plan binding. State transitions validate the full binding and refuse a fencing token older than the latest retaining worker. Unknown/committed and orphaned claims remain unique; released claims can be reused only by a new plan. SQLite and PostgreSQL execute the same conformance functions, while a file-backed SQLite restart case proves persistence.

`TASK-PROXMOX-005` adds local migration `0003_local_provider_target_binding`. It preserves old rows on `target.local-docker`, stores each new instance's target and effective non-secret override, and persists redacted target discovery snapshots. The local create transaction writes plans and `committed` claim rows together with the instance, resources, durable job, Operation, audit intent, event, outbox delivery, and idempotency binding; any allocation conflict rolls the whole transaction back before bounded replan.

`TASK-PROD-001` implements those same three migration IDs and every composed control-plane transaction on PostgreSQL. A cluster-wide advisory migration lock serializes concurrent replica startup. Job claims retain ordered `FOR UPDATE SKIP LOCKED` selection and database-time lease calculation. Fenced state transactions lock the current running job row before touching Instance, resource, invocation, audit, event, or outbox rows, and the terminal job update repeats the running-state predicate so concurrent finalizers cannot both commit. Transaction-scoped idempotency locks plus bounded SQLSTATE `40001`/`40P01` retry make simultaneous equivalent creates converge on one durable result.

Provider invocation digest v2 includes the target, Instance, logical resource,
desired generation, action, canonical resource, persisted plan, and prior state.
It is therefore stable across lease takeover but cannot collide when a
classroom creates identical resources for different owners. A worker that
finds an invocation prepared by a v1 worker retries only with the exact legacy
digest, preserving rolling-upgrade recovery without using v1 for new work.

`TASK-PROD-002` adds checksummed local migrations `0004_identity_tenancy_rbac`, `0005_authentication_hardening`, and `0006_authority_lifecycle` on both adapters. The first persists the selected authentication mode, users/external identities, rotating sessions, one-use password recovery and browser-bound OIDC flows, organization/project/team membership, scoped roles and target grants, and immutable instance/operation actors. The second adds atomic keyed authentication-attempt windows plus indexes used to prune expired sessions, recovery tokens, and OIDC flows. The third adds optimistic resource versions to users, memberships, role assignments, and target grants so renewal, revocation, enable/disable, and the last-platform-administrator invariant are atomic; it also makes browser and bearer credentials distinct durable types. Instance creation rechecks active creator and owner-to-project organization relationships inside the same serializable transaction that queues work.

The first `TASK-PROD-003` slice adds checksummed migrations `0007_classroom_governance_foundation` and `0008_governance_audit_events`. Migration `0007` adds project-scoped mutable Lab Template metadata, immutable monotonically numbered Template Revisions with content/canonical provenance, and an optional revision binding for legacy-compatible inline Instances. Database triggers reject revision update/delete; publication serializes per template on both adapters. Migration `0008` adds separate monotonic audit/event streams, per-aggregate event sequences, immutable audit/event rows, and one local outbox delivery per event. Template create/update/publication writes its aggregate change, successful redacted audit record, safe event, outbox row, and idempotency binding in one serializable transaction. Shared SQLite/PostgreSQL tests prove replay convergence, optimistic-concurrency winners, concurrent revision numbering, immutable rows, exact scope binding, stream sequencing, rollback on governance-record conflict, opaque cursor pagination/export bounds, and retention batches. Event retention skips records with any undelivered outbox sink so cleanup cannot erase pending at-least-once work.

Migration `0009_classroom_quota_expiration` adds scoped limits and usage, multi-scope reservation lines, versioned lifetime policies, Instance expiry/warning/renewal fields, and exact governance-mutation replay snapshots. Platform defaults bound new admissions to 100 instances/VMs/containers, 512,000 CPU millicores, 2 TiB memory, 20 TiB storage, 100 public IPs, 1,000 published ports, and 500 snapshots; deployments should tighten these per scope. A post-migration restart-safe backfill derives legacy usage from canonical resources. Ready work becomes committed, active admission remains reserved, and failed/destroyed rows with allocation claims, unknown cleanup, provider state, or ambiguous terminal work become retained for review. Re-running startup cannot duplicate reservations or usage. SQLite/PostgreSQL upgrade fixtures start from the real `0008` schema and prove identical postconditions.

Migration `0010_classroom_instance_batches` adds the durable batch aggregate and ordered child bindings without weakening the existing independent Instance/Operation model. Batch create takes one serializable transaction, acquires deterministic owner/quota locks, rechecks every owner authorization, and inserts all child reservations, claims, jobs, legacy operation envelopes, batch audit/event/outbox records, and the parent idempotency result. A 100-item shared SQLite/PostgreSQL conformance case proves an applicable limit of 99 leaves zero batches, Instances, or reservations; raising it to 100 admits exactly one batch even under two simultaneous equivalent requests.

Migration `0011_controlled_access_sessions` adds the exact subject/scope,
Instance/resource/method/target, provider-state generation/digest, access-plugin
identity/audience, endpoint and access-state schema/digest, keyed launch/lease
hash, expiry, revocation, closure-retry, and optimistic-version fields required
by controlled access. Endpoint bytes and provider credentials have no durable
column. Begin/finalize/exchange/recheck/end/revoke transactions use database
time, repeat current authority and owned-resource checks, clear consumed or
denied hash material, and atomically append redacted access audit/event/outbox
records. The same concurrency and restart cases pass on SQLite and PostgreSQL.

The final `TASK-PROD-003` producer pass retains the `0008` schema and completes
its transactional use. Authentication session/recovery/OIDC state,
administrator identity/tenancy/authority mutations, target discovery
snapshots, and Operation queue/finalization now append the generalized audit,
event, and outbox rows in the same transaction as state. Authorization has no
aggregate mutation, so the evaluator uses an atomic audit+event append boundary
for allowed, denied, and failed decisions. One authorization record is emitted
per boundary call rather than per SQL-prefiltered Instance; successful
`audit.read` decisions are deliberately not appended to the stream being paged
because doing so would create an inexhaustible cursor tail. Denied and failed
audit reads remain recorded. The executable producer catalog is
[`design/contracts/governance-audit-producers.yaml`](../../design/contracts/governance-audit-producers.yaml).

Migration `0012_template_representations` adds non-null `representation_json` with an empty default for legacy revisions. New revisions store canonical authoring JSON; old rows keep their digest/version/renderer fields and verification path. Migration checksums for prior versions are unchanged. See ADR-0016.
