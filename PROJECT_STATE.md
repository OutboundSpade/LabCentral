# LabCentral project state

**Updated:** 2026-09-05
**Completed:** `TASK-ARCH-002` architecture simplification and `TASK-PILOT-001` authenticated Tailscale pilot.

**Current work:** `TASK-PROD-004` production qualification and `TASK-PROD-005` native deployment hardening remain in progress.

LabCentral is a Go modular monolith with a Huma/OpenAPI API, static SvelteKit UI, SQLite/PostgreSQL persistence, durable database jobs/outbox, and separate provider/access processes. Portable resource types remain closed and typed. One instance belongs to one target. Core owns authorization, quota, lifetime and ownership decisions; plugins own infrastructure translation and execution journals.

The owner approved substantial simplification for independently distributed infrastructure providers implementing the existing resource types. ADR-0015, ADR-0016 and ADR-0017 replace the relevant earlier decisions. See [architecture](docs/design/architecture.md), [plugin installation](docs/operations/plugin-installation.md), and [compatibility](docs/compatibility.md).

## Current implementation

- Application-owned transactional interfaces replace the unused repository/unit-of-work abstraction. Provider and access gRPC mapping lives in an adapter; orchestration has no wire imports. SQL transactions and existing state envelopes are preserved.
- Explicit versioned installation files bind verified provider manifests to targets. Existing official CLI profiles remain supported. Production external plugins use operator-isolated attached processes; trusted local development can launch children.
- Bounded per-plugin recovery preserves a stable RPC client. Unavailable targets stop taking work while the API and other providers continue. Unknown mutations retain their original invocation IDs.
- Revisions preserve canonical parameterized source separately from resolved instances. Typed API/UI inputs are evaluated at instance creation under the revision's renderer semantics. Migration `0012_template_representations` preserves existing revisions.
- Provider source identity hashes actual dependency files and module locks. Ordinary Proxmox writes retain exact-target signed qualification. Explicit, short-lived signed grants provide separately reviewed cleanup authority for exact owned resources.
- The checked OpenAPI document generates frontend DTOs. HTTP handlers are grouped by feature.

## Evidence and remaining gates

The [architecture audit](docs/design/architecture-audit-2026-09-04.md) is the historical rationale, not the current implementation inventory. Earlier implementation/testbed evidence is preserved in the [historical log](docs/history/implementation-through-2026-09-04.md). [Verification contracts](design/contracts/architecture-conformance.yaml) describe the architecture acceptance checks.

`TASK-PROD-004` still requires live target load/soak, cross-node shared-journal locking/failover/disaster-recovery evidence, publisher-control approval, and the first signed release ceremony. Local/fake-provider tests do not satisfy these gates. New provider binaries require fresh exact-build qualification; earlier testbed evidence is not relabeled.

The full race-enabled Go suite passed with disposable PostgreSQL 18. A CI-exposed lease-renewal conflict was reproduced and fixed with bounded database-only retries; three deterministic conflict runs and five 100-instance runs passed under the race detector. Targeted normal tests, Go vet, design/protocol/schema/link checks, generated OpenAPI/TypeScript, Svelte checks/build, and dependency scans passed. See the [verification record](design/implementation-verification.yaml) for scope and exclusions.

A [PVE1 pilot](docs/operations/pve1-pilot.md) now runs authenticated HTTPS with core `main-8a01cb0-native` and the unchanged, previously qualified rc6 provider. A live HTTP check caught missing embedded SvelteKit `_app` assets; the fix and regression test cover the actual browser boot URLs. Its isolated Debian example VM 910000 is ready and expires on 2026-09-06 at 02:45 UTC. The owner selected Tailscale access. This pilot does not qualify current-main provider binaries or complete any remaining production milestone gate.

Pilot CI also exposed audit-write contention exhausting the existing PostgreSQL retry window during classroom teardown. The shared policy now uses at most 16 attempts with capped randomized exponential backoff. A deterministic eight-conflict regression, five repetitions of the 100-instance scenario, and the full Go suite passed with PostgreSQL under the race detector; provider calls remain outside database retries. The deployed SQLite pilot is unaffected by PostgreSQL transaction conflicts.

The owner selected the Tailscale deployment and withdrew the Kali VM 101 access requirement on 2026-09-05. `TASK-PILOT-001` is complete; the remaining production gates are unchanged.

**Next action:** obtain the off-host backup destination and recovery-key custody, rehearse encrypted recovery, enable trusted renewable Tailscale HTTPS, and complete monitoring and reviewed release controls. Retain the existing scale/failover qualification gates.

The approved native migration under `TASK-PROD-005` moved active state to `/var/lib`, split core/provider UIDs, enforced provider port/DNS egress, and masked the retired pilot. Live login, instance readiness, native qualification discovery and filesystem boundaries passed. The general systemd profile now supplies setgid IPC directories and owner-only provider secrets. Encrypted off-host recovery, trusted renewable TLS, monitoring delivery and reviewed release controls remain required; see the deployment record for operator inputs and exact evidence.
