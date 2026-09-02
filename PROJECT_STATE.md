# LabCentral project state

**Phase:** Design, Phase 0B architecture foundation, and the packaged Phase 1 Docker laptop vertical slice are complete. Production provider and multi-user control-plane work has not started.
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
- `TASK-DOMAIN-001` implemented the minimal Go module, sealed typed logical resources, validated value contracts, lifecycle transitions, application ports, secret redaction, and recursive import-boundary tests.
- `TASK-RENDER-001` implemented bounded strict YAML parsing, Draft 2020-12 validation, typed inputs/components/resources, bounded CEL, two-stage typed reference resolution, stable dependency ordering, native RFC 8785 canonicalization, framed digests, and executable example/contract fixtures.
- `TASK-JOBS-001` implemented a checksummed transactional job/outbox prototype, database-time leases, monotonic fencing, unknown-invocation recovery, idempotency binding, quota/leftover safeguards, and one shared eight-case conformance suite proven on SQLite and PostgreSQL.
- `TASK-PROVIDER-001` implemented a public provider harness, reusable 18-case contract runner, and programmable fake with virtual time, durable invocation restart, compensation/leftover behavior, ownership fencing, state migration, deterministic planning, cancellation, and secret-canary checks.
- `TASK-DOCKER-001` implemented `labd`, the Huma/OpenAPI laptop API, embedded SvelteKit/DaisyUI assets, durable SQLite operations, a content-addressed filesystem artifact adapter, a checksummed out-of-process Docker plugin, and the typed Docker Engine realization of Example A.
- The Docker slice enforces loopback-only no-auth and port publication, digest-pinned images, provider executable verification, resource limits, reduced container privileges, stable invocation replay, fenced persistence, and ownership-verified reverse destroy. It explicitly warns that ordinary containers and the degraded denied-egress path are trusted-laptop-only.
- Example A was built and exercised against Docker Engine 29.7.2 / API 1.55 on Linux amd64: the operation reached `ready`, its loopback URL returned the SQLite visit-counter JSON, destroy succeeded, and no instance-labeled container, network, or volume remained.
- `TASK-RELEASE-001` added byte-reproducible native tarballs with executable/archive checksums, a Docker plugin inventory manifest, CycloneDX SBOM, and source/build provenance; a digest-pinned multi-stage `scratch` image; and persistent, hardened, host-networked Compose packaging.
- The Compose distribution passed the real Example A apply/access/destroy gate with separate live `labd` and provider processes, zero ownership-labeled leftovers, and SQLite state retained across a control-plane restart. The image contained no Go, Bun, shell, or package-manager runtime.

## Open questions and risks

Five questions remain under documented temporary defaults: Windows timing, trusted image sources, user-supplied guest secrets, browser console implementation, and spend enforcement. Primary risks are provider capability variance, hostile-workload network escape/misconfiguration, plugin/state upgrade compatibility, and destructive cleanup under partial provider failure. See `docs/design/open-questions.md`.

## Next concrete actions

1. Propose and accept the first bounded Phase 2 task: read-only Proxmox target discovery and capability-evidence contracts before any mutating provider operation.
2. Keep production providers and multi-user deployment gated behind their target-specific integration, authorization, threat-control, and operational acceptance criteria.

Authoritative index: [`design/state.yaml`](design/state.yaml), [`design/checklist.yaml`](design/checklist.yaml), [`docs/design/architecture.md`](docs/design/architecture.md), [`docs/adr/`](docs/adr/), and [`docs/references.md`](docs/references.md).
