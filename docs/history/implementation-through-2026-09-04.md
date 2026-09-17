# Historical implementation record through 2026-09-04

This is an archived evidence log, not current status or deployment guidance. See [current project state](../../PROJECT_STATE.md) and [current architecture](../design/architecture.md). Statements about unfinished work describe their original checkpoint.

# LabCentral project state

**Phase:** Design, Phase 0B architecture foundation, the packaged Phase 1 Docker laptop vertical slice, `TASK-PROXMOX-001` through `TASK-PROXMOX-006`, and `TASK-PROD-001` through `TASK-PROD-003` are complete. Immutable reusable Lab Template revisions, generalized append-only audit/event production, scoped quota/lifetime governance, atomic 1–100-owner classroom batch creation, and the isolated controlled Proxmox/noVNC access runtime are implemented. `TASK-PROD-004` is active; its production-observability, shared Proxmox journal-coordination, fail-closed Kubernetes, native multi-service deployment, automated encrypted offline recovery and artifact maintenance, 100-Instance PostgreSQL control-plane resilience, and protected-publisher implementation checkpoints are complete, while live target load/soak, shared-RWX failover/disaster-recovery evidence, repository publisher-control approval, and the first signed release ceremony remain open.
**Updated:** 2026-09-04

LabCentral is a modular-monolith Go control plane with a Huma/OpenAPI HTTP contract, a statically exported SvelteKit/DaisyUI frontend, SQLite/PostgreSQL persistence adapters, database-backed durable operations/outbox, and separately supervised provider/access plugin processes using versioned Protobuf/gRPC. A Lab Instance is bound to one centrally registered, initially global Provider Target. Portable resources are typed `Network`, `Volume`, `VM`, `Container`, `AccessEndpoint`, and `Output`; images, artifacts, secrets, and attachments are typed references or embedded subresources.

Security defaults are isolation-first: lab networks have denied egress unless explicitly requested and supported; provider/control management paths must be unreachable from guests; no-auth mode must be selected explicitly and is loopback-only; ordinary containers are never represented as hostile-workload isolation.

The design-only Phase 0 exit criteria are accepted: executable schema/canonical fixtures pass, the plugin v1 descriptor passes Buf format/lint/`FILE` breaking checks, provider failure-window contracts are machine-readable, and every identified threat maps to owners, controls, and tests.

## Latest completed work

## Open questions and risks

Four questions remain under documented temporary defaults: Windows timing, trusted image sources, user-supplied guest secrets, and spend enforcement. Primary risks are provider capability variance, hostile-workload network escape/misconfiguration, plugin/state upgrade compatibility, and destructive cleanup under partial provider failure. See `docs/design/open-questions.md`.

## Next proposed phase

0. Review the architecture audit through `TASK-ARCH-002`, produce concrete ADR/implementation proposals, and explicitly decide their order relative to remaining production-validation work.
1. Follow `docs/operations/proxmox-deployment.md` to qualify and install this exact release candidate on each operator's own dedicated Proxmox target; no testbed artifact or reviewer key is transferable.
2. Execute the supported classroom production topology work in `TASK-PROD-004`.

Authoritative index: [`design/state.yaml`](../../design/state.yaml), [`design/checklist.yaml`](../../design/checklist.yaml), [`docs/design/architecture.md`](../../docs/design/architecture.md), [`docs/adr/`](../../docs/adr/), and [`docs/references.md`](../../docs/references.md).


Host-specific qualification bindings and historical deployment transcripts are
retained in protected operator records outside the repository. Current contracts
and stable task IDs are maintained in [the checklist](../../design/checklist.yaml).
