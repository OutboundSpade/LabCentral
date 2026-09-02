# LabCentral project state

**Phase:** Design complete and implementation-ready; production application implementation has not started.
**Updated:** 2026-09-02

LabCentral is a modular-monolith Go control plane with a Huma/OpenAPI HTTP contract, a statically exported SvelteKit/DaisyUI frontend, SQLite/PostgreSQL persistence adapters, database-backed durable operations/outbox, and separately supervised provider/access plugin processes using versioned Protobuf/gRPC. A Lab Instance is bound to one centrally registered, initially global Provider Target. Portable resources are typed `Network`, `Volume`, `VM`, `Container`, `AccessEndpoint`, and `Output`; images, artifacts, secrets, and attachments are typed references or embedded subresources.

Security defaults are isolation-first: lab networks have denied egress unless explicitly requested and supported; provider/control management paths must be unreachable from guests; no-auth mode is loopback-only unless an explicit unsafe override is set; ordinary containers are never represented as hostile-workload isolation.

The design-only Phase 0 exit criteria are accepted: executable schema/canonical fixtures pass, the plugin v1 descriptor passes Buf format/lint/`FILE` breaking checks, provider failure-window contracts are machine-readable, and every identified threat maps to owners, controls, and tests.

## Latest completed work

- Interpreted requirements and traceable architecture baseline.
- Accepted ADR-0001 through ADR-0012.
- Domain, declarative format, capability model, jobs, API, RBAC, access, persistence, deployment, testing, threat model, and provider roadmap documented.
- Three example bundles, an executable authoring JSON Schema, strict parser/invalid fixtures, and canonical JCS golden vectors added.
- Plugin registration direction and per-major negotiation finalized; v1 Protobuf descriptor baseline and behavior fixtures accepted.
- Domain/application ports, relational schema/migration invariants, and renderer/job/provider/security conformance catalogs completed.
- `tools/verify_phase0.py` passes all design gates without contacting a provider.
- Current official provider behavior recorded with primary sources retrieved 2026-09-02.

## Open questions and risks

Five questions remain under documented temporary defaults: Windows timing, trusted image sources, user-supplied guest secrets, browser console implementation, and spend enforcement. Primary risks are provider capability variance, hostile-workload network escape/misconfiguration, plugin/state upgrade compatibility, and destructive cleanup under partial provider failure. See `docs/design/open-questions.md`.

## Next concrete actions

1. Begin `TASK-DOMAIN-001`: create the minimal Go module and compile the typed domain/application ports against the accepted schema.
2. Implement `TASK-JOBS-001`: SQLite/PostgreSQL repository conformance tests for the accepted lease/outbox invariants.
3. Implement `TASK-PROVIDER-001`: fake provider and shared contract runner from `design/contracts/provider-conformance.yaml`.
4. After those gates pass, start the thin Docker local vertical slice; do not start a real production provider earlier.

Authoritative index: [`design/state.yaml`](design/state.yaml), [`design/checklist.yaml`](design/checklist.yaml), [`docs/design/architecture.md`](docs/design/architecture.md), [`docs/adr/`](docs/adr/), and [`docs/references.md`](docs/references.md).
