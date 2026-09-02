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

The same default command runs every `TEST-JOB-*` invariant against in-memory SQLite. PostgreSQL conformance is explicit and credentialed: set `LABCENTRAL_TEST_POSTGRES_DSN` to an isolated test database URL and run `go test -count=1 -run TestPostgreSQLJobConformance ./internal/adapter/jobsdb`. The test creates and removes a unique schema for each invariant; it never uses the public schema or a production provider.

The default suite also runs all 18 `TEST-PROVIDER-*` cases against the deterministic fake. `pkg/plugin-sdk-go/contracttest.Run` is the shared T1 runner; an official provider supplies an RPC-backed public `contract.Factory` and may add cases but cannot remove the required IDs. The fake suite requires no provider credentials and performs no external mutations.

`TASK-DOCKER-001` adds a T0 fake-Engine suite plus an in-process gRPC/local-SQLite vertical test. It proves required risk acknowledgement, deterministic planning, request-digest idempotency, atomic instance/resource/job/audit/event/outbox enqueue, durable worker apply, loopback output projection, reverse destroy, and ownership cleanup. HTTP tests pin the implemented OpenAPI surface and security/static-routing behavior; plugin-host tests cover executable checksums and private registration; filesystem tests cover measured staging, checksum commit, restrictive modes, and symlink refusal.

The credentialed laptop smoke test is intentionally opt-in because it pulls images and mutates the configured Docker Engine. Build with `make build`, start `bin/labd`, submit `examples/docker-laptop/lab.yaml` with the risk acknowledgement, wait for the Operation, request the returned loopback URL, then destroy and verify that no objects with the instance ownership label remain. The recorded `TASK-DOCKER-001` gate used Docker Engine 29.7.2 / API 1.55 on Linux amd64 and observed the Example A JSON response before a clean reverse destroy.

`TASK-RELEASE-001` adds deterministic archive unit/integration checks and a credentialed Compose gate. The release check builds twice from one `SOURCE_DATE_EPOCH`, compares the compressed bytes, extracts the archive, verifies `SHA256SUMS` and the provider-specific checksum, and executes the packaged `labd --help`. Archive tests independently pin lexical entry order, fixed ownership/timestamps, directory/file modes, and byte-for-byte repeatability.

The container gate builds from digest-pinned Go 1.27.1 and Bun 1.3.14 stages, inspects the `scratch` filesystem for only `labd`, `lab-provider-docker`, and its checksum, and checks the configured health command. The Compose gate confirms a read-only root filesystem, dropped capabilities, `no-new-privileges`, host networking, and distinct live `labd`/provider processes. It applies Example A, waits for `ready`, observes `{"service":"LabCentral Example A","visits":1}` through the provider-selected loopback URL, destroys the instance, finds zero labeled containers/networks/volumes, restarts the service to prove the destroyed record persisted, and finally removes the isolated test volume.

`TASK-PROXMOX-001` adds a normal-CI T0 HTTPS fake-API suite with no real target or credential. It pins the exact API-token header, GET-only endpoint/query set, private-CA verification, redirect refusal, envelope/response/inventory bounds, provider-returned path validation, offline-node behavior, partial permission/unavailability issues, deterministic inventory ordering/digest and five-minute evidence, configuration-fingerprint validation, secret/error-body redaction, and `Unimplemented` status for every non-discovery provider RPC. Build the separate binary with `make build-proxmox-provider`. Credentialed Proxmox T3/T4, mutation, ownership, cleanup, and upgrade tests remain release gates for later slices.

## Release gates

The design-only Phase 0 exit is satisfied when examples validate against the executable schema, canonical vectors are stable, the plugin protocol passes compatibility checks, the fake-provider contract catalog covers failure windows, the threat model has owner/test mapping, and the verifier passes. The Phase 0B foundation additionally requires the implemented Go renderer, SQLite/PostgreSQL job suite, and fake-provider shared contract runner; those gates now pass. The trusted-laptop package additionally requires reproducible archive/checksum verification, SBOM/provenance presence, a toolchain-free runtime image, separate plugin execution, persistent state, and clean real-Engine lifecycle evidence. Production provider releases still require a target-specific integration tier, least-privilege credential documentation, capability evidence tests, cleanup/orphan tests, and upgrade/state-migration rehearsal.

At least one test deliberately proves each destructive path refuses name-only or mismatched ownership. Secret-redaction tests seed canary values through request, job, plugin, error, event, audit, log, and trace paths and fail if any sink contains them.
