# Testing strategy

## Test layers

- Domain unit/property tests: typed values, state machines, quotas, authorization, scheduler determinism, reference/graph/cycle/topological/reverse ordering.
- Parser/render tests: YAML adversarial corpus, duplicate/alias/depth limits, CEL type/cost, component/loop limits, canonical golden vectors and digest stability.
- Schema compatibility: JSON Schema valid/invalid fixtures, OpenAPI breaking checks, Buf/Protobuf lint/breaking checks, event/provider-state fixtures.
- Repository conformance: identical behavior on SQLite and PostgreSQL for transactions, optimistic concurrency, lease/fencing, idempotency, quotas, outbox, migrations.
- Durable fault tests: crash at every checkpoint, lost response after provider mutation, expired lease, duplicate delivery, cancellation race, dead worker, outbox replay.
- Provider contract suite: fake provider plus all official providers; no real credentials in normal CI.
- Security: RBAC matrix and list filtering, cross-tenant IDs, CSRF/session/OIDC validation, bundle path/archive bombs, secret taint/redaction, ownership spoof/delete, fuzzing.
- UI: generated API contract, role-gated routes, no-auth banner, validation/plan/progress/access flows, static export and deep-link entry points, no secret bootstrap.
- End to end: one-command Docker Example A on supported Docker Desktop/Linux matrices, restart during apply, cleanup, and warning acknowledgement.

## Provider integration tiers

T0 pure translation/golden tests; T1 fake provider contract; T2 local/emulated API (Docker/Kubernetes test cluster where feasible); T3 credentialed ephemeral target in protected CI; T4 scheduled destructive/upgrade/scale tests on production-like target. Proxmox, AWS, Fly, and complex KubeVirt networking normally enter at T3/T4 and are opt-in.

Fixtures include multi-NIC routed topology, provider-managed/static IPAM, IPv4/IPv6, unsupported L2, snapshot modes, container-via-VM, partial resource creation, compensation failure, stale provider state, and target capability downgrade.

Phase 0 executable catalogs are:

- Lab/schema/parser/canonical fixtures under [`schemas/fixtures/`](../../schemas/fixtures/).
- Plugin descriptor baseline and behavior cases under [`api/plugin/v1/compat/`](../../api/plugin/v1/compat/).
- Renderer, job, provider, and security catalogs under [`design/contracts/`](../../design/contracts/).
- [`tools/verify_phase0.py`](../../tools/verify_phase0.py), which validates the catalogs, examples/manifests, canonical digests, Buf format/lint/breaking, links, and state consistency without running provider code.

The Go renderer is executable: `go test ./...` runs the strict parser and schema corpus, every `TEST-RENDER-*` contract mapping, deterministic example renders, graph ordering/cycles, bounded CEL evaluation, and native RFC 8785 vectors. `go test -race ./...` and `go vet ./...` are required before a renderer milestone is recorded complete.

## Release gates

The design-only Phase 0 exit is satisfied when examples validate against the executable schema, canonical vectors are stable, the plugin protocol passes compatibility checks, the fake-provider contract catalog covers failure windows, the threat model has owner/test mapping, and the verifier passes. This means implementation-ready, not that the future Go fake provider or database adapters already pass runtime tests. Provider releases require the implemented shared contract suite, target-specific integration tier, least-privilege credential documentation, capability evidence tests, cleanup/orphan tests, and upgrade/state-migration rehearsal.

At least one test deliberately proves each destructive path refuses name-only or mismatched ownership. Secret-redaction tests seed canary values through request, job, plugin, error, event, audit, log, and trace paths and fail if any sink contains them.
