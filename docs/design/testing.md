# Testing strategy

The production web export has a browser startup gate under `TASK-PROD-005`:
after `bun run build` in `web`, install the pinned Playwright browsers with
`bunx playwright install --with-deps chromium firefox webkit`, then run
`bun run test:browser`. The test serves the real export with a credential-free
API fixture, checks visible sign-in controls and runtime errors in all three
engines, and repeats with optional console video detection permanently stalled.
HTTP asset status checks alone cannot detect module initialization failures.

## Test layers

- Domain unit/property tests: typed values, state machines, quotas, authorization, scheduler determinism, reference/graph/cycle/topological/reverse ordering.
- Parser/render tests: YAML adversarial corpus, duplicate/alias/depth limits, CEL type/cost, component/loop limits, canonical golden vectors and digest stability.
- Schema compatibility: JSON Schema valid/invalid fixtures, OpenAPI breaking checks, Buf/Protobuf lint/breaking checks, event/provider-state fixtures.
- Repository conformance: identical behavior on SQLite and PostgreSQL for transactions, optimistic concurrency, lease/fencing, idempotency, quotas, outbox, migrations.
- Classroom batch conformance: identical 100-owner atomic admission, quota rollback, concurrent replay, per-owner authorization, and audit/event behavior on SQLite and PostgreSQL.
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

The same package runs all fourteen `TEST-ALLOC-*` cases from [`allocation-claims.yaml`](../../design/contracts/allocation-claims.yaml) against SQLite. `TestPostgreSQLAllocationConformance` runs the identical applicable functions against isolated PostgreSQL schemas; the restart-only and bounded-replan cases are storage-independent. The suite covers batch rollback, idempotent replay, target scoping, released reuse, commit/unknown/orphan transitions, absence-gated release, process restart, concurrent winners, input bounds, stale plan binding/fencing, and sorted conflict exclusions with an eight-attempt ceiling.

`TASK-PROD-001` adds full control-plane PostgreSQL gates under `internal/controlplane`. `TestPostgreSQLControlPlaneStoreConformance` runs the complete Example A create/idempotency/apply/destroy transaction path; `TestPostgreSQLProviderTargetStoreConformance` repeats target snapshot, allocation-replan, dependency, and absence-release behavior; `TestPostgreSQLMultiReplicaFencingAndCrashRecovery` uses two independent pools to prove claim exclusion, takeover, stale-writer fencing, invocation rebinding, and one terminal finalizer; and `TestPostgreSQLConcurrentIdempotentCreateHasOneDurableResult` proves concurrent request convergence. `TestPostgreSQLBackupRestoreCompatibility` performs an isolated custom-format `pg_dump`/drop/`pg_restore`, verifies every table count, reruns migrations, and reads the restored terminal Instance. It uses local PostgreSQL client tools by default or the explicitly named disposable tools container variables documented in the operator guide. Shared-filesystem tests open the same root twice and prove cross-store staging serialization and committed-object visibility.

`TASK-PROD-002` adds SQLite/PostgreSQL conformance for atomic bootstrap/mode selection, identity uniqueness, versioned concurrent administration, authority renewal/revocation, last-platform-administrator protection, cross-organization owner/scope refusal, the complete one-role-at-a-time permission matrix, active-membership evaluation, target grants, SQL-prefiltered instance visibility, session/recovery/OIDC one-use state, database-time concurrent authentication limits, rolling-key rate buckets, and authentication-mode downgrade/legacy-data refusal. HTTP/startup tests cover explicit auth selection at both process and server-construction boundaries, zero external activity before auth preflight, exact public origins, trusted-proxy spoofing and client-chain parsing, TLS 1.3 configuration, secure cookie/CSRF/logout behavior, typed bearer issuance/revocation, browser/bearer replay separation and mixed-credential refusal, target-granted refresh, account/global/IP rate limiting with `Retry-After`, local recovery, first-admin tenancy setup, object hiding, and non-loopback refusal. OIDC fake-provider tests bind code exchange, S256 PKCE, issuer, single/multiple audience plus `azp`, nonce, signature/JWKS rotation, browser state and parallel flows, redirect safety, identity pre-linking, response bounds, retained/retired keys, and replay. Password-worker tests prove the Argon2 concurrency cap and cancellation while queued.

The first `TASK-PROD-003` conformance slice runs Lab Template/revision and generalized audit/event tests against SQLite and the disposable PostgreSQL adapter. It covers scoped name uniqueness, sequential and concurrent mutation replay, conflicting idempotency reuse, one optimistic metadata-update winner, concurrent revision numbering, immutable revision/audit/event triggers, project-bound revision instance creation, atomic rollback when governance IDs conflict, independent stream/per-aggregate sequences, concurrent event writers, outbox insertion, cursor pagination, bounded NDJSON export/retention, undelivered-event preservation, malformed or duplicate JSON, and secret-bearing payload-key refusal. Authenticated HTTP tests exercise project/organization denial before persistence or artifact access, opaque typed cursor round trips, paginated audit serialization, bounded raw NDJSON headers/body, malformed scope rejection, and OpenAPI route presence.

The completing producer suite is indexed by
[`design/contracts/governance-audit-producers.yaml`](../../design/contracts/governance-audit-producers.yaml).
Shared SQLite/PostgreSQL cases require all seven categories—authentication,
authorization, target, template, operation, access, and administrator—and
fault-inject duplicate governance IDs to prove session, Instance/Operation, and
target state rolls back with its audit/event/outbox pair. Authentication
canaries are scanned from stored payloads, and audit/event pair insertion is
independently forced through a duplicate-event failure to prove there is no
retained audit half.

The first `TASK-PROD-004` suite is indexed by
[`production-observability.yaml`](../../design/contracts/production-observability.yaml).
It distinguishes process liveness from dependency-backed readiness, bounds all
parallel readiness checks to one deadline, preserves SSE flush and WebSocket
hijack behavior through HTTP instrumentation, and scans Prometheus exposition
and recorded OTLP span attributes for raw paths, queries, tenant IDs, tokens,
and error text. Startup configuration tests require TLS for every non-loopback
OTLP endpoint and a complete certificate/key pair for mTLS. The second slice
runs the same database-time operational snapshot assertions on SQLite and
PostgreSQL, including queue/expired-lease age, both outboxes, target staleness,
access/resource lifecycle, and cleanup ownership records. Metrics tests seed
unknown state and identifier canaries and prove that only closed labels are
exported; the full apply/destroy worker path proves one terminal duration/result
observation per completed operation.

[`access-conformance.yaml`](../../design/contracts/access-conformance.yaml) binds
all eighteen access cases to Go tests. Normal CI covers pre-plugin denial,
exact endpoint/state/audience binding, tight expiry, one-winner exchange on both
databases, authority loss, 30-second outage semantics, core-first/idempotent
revocation, lost responses, restart denial, protocol/schema downgrade,
secret-free sinks and URLs, exact Proxmox VNC calls, and unsupported SSH. A
real TLS RFB fake proves that the access process consumes the Proxmox VNC
password before the browser gets a no-auth RFB stream. The separate-process
host test proves access-only registration plus the launch-scoped private core
callback.

`TEST-ACCESS-018` is the protected black-box gate. With no access environment
it skips before files or network. Run it only against an already-qualified,
disposable authenticated installation containing a running owned VM and two
bounded bearer identities:

```console
LABCENTRAL_PROXMOX_ACCESS_EXECUTE=I_ACKNOWLEDGE_LABCENTRAL_PROXMOX_ACCESS_TEST \
LABCENTRAL_PROXMOX_ACCESS_BASE_URL=https://labcentral.example.edu \
LABCENTRAL_PROXMOX_ACCESS_OWNER_TOKEN_FILE=/secure/owner.token \
LABCENTRAL_PROXMOX_ACCESS_DENIED_TOKEN_FILE=/secure/other-tenant.token \
LABCENTRAL_PROXMOX_ACCESS_INSTANCE_ID=instance.example \
LABCENTRAL_PROXMOX_ACCESS_RESOURCE_ID=vm.example \
LABCENTRAL_PROXMOX_ACCESS_MANAGEMENT_ORIGIN=https://pve.example.internal:8006 \
go test -run '^TestProxmoxAccessSessionQualification$' -count=1 -v ./access/novnc
```

Token files must be absolute private regular files. Set the optional
`LABCENTRAL_PROXMOX_ACCESS_PUBLIC_CA_FILE` for a private public-origin CA. The
test proves cross-tenant denial, no management-origin/token URL disclosure,
same-origin one-time exchange, downstream RFB, core-first revocation, and
active-stream closure. It does not create or destroy the VM.

The quota/lifetime slice binds [`governance-quotas.yaml`](../../design/contracts/governance-quotas.yaml) on SQLite and PostgreSQL. It proves atomic multi-scope admission, concurrent over-limit winners, post-admission policy backfill, DB-time defaults/maxima, exact renewal replay, optimistic preconditions, transaction-local authority revocation, one-shot warning predicates, convergent manual/timer destruction, invocation-free quota/allocation release, retained unknown outcomes, and queued/expired-lease sweeper races. Real pre-`0009` fixtures verify committed versus retained legacy backfill and restart idempotency. A lost-response gRPC integration executes a Docker mutation once, expires the worker lease, requires `GetInvocation`, and reaches ready without redispatch. Huma tests pin the closed OpenAPI paths, conditional headers, malformed scopes, exact-scope authorization-before-store, narrow lifetime replay DTO, and `no-store` dynamic usage response.

The default suite also runs all 18 `TEST-PROVIDER-*` cases against the deterministic fake. `pkg/plugin-sdk-go/contracttest.Run` is the shared T1 runner; an official provider supplies an RPC-backed public `contract.Factory` and may add cases but cannot remove the required IDs. The fake suite requires no provider credentials and performs no external mutations.

`TASK-DOCKER-001` adds a T0 fake-Engine suite plus an in-process gRPC/local-SQLite vertical test. It proves required risk acknowledgement, deterministic planning, request-digest idempotency, atomic instance/resource/job/audit/event/outbox enqueue, durable worker apply, loopback output projection, reverse destroy, and ownership cleanup. HTTP tests pin the implemented OpenAPI surface and security/static-routing behavior; plugin-host tests cover executable checksums and private registration; filesystem tests cover measured staging, checksum commit, restrictive modes, and symlink refusal.

The credentialed laptop smoke test is intentionally opt-in because it pulls images and mutates the configured Docker Engine. Build with `make build`, start `bin/labd --auth-mode=none`, submit `examples/docker-laptop/lab.yaml` with the risk acknowledgement, wait for the Operation, request the returned loopback URL, then destroy and verify that no objects with the instance ownership label remain. The recorded `TASK-DOCKER-001` gate used Docker Engine 29.7.2 / API 1.55 on Linux amd64 and observed the Example A JSON response before a clean reverse destroy.

`TASK-RELEASE-001` adds deterministic archive unit/integration checks and a credentialed Compose gate. The release check builds twice from one `SOURCE_DATE_EPOCH`, compares the compressed bytes, extracts the archive, verifies `SHA256SUMS` and the provider-specific checksum, and executes the packaged `labd --help`. Archive tests independently pin lexical entry order, fixed ownership/timestamps, directory/file modes, and byte-for-byte repeatability.

The container gate builds from digest-pinned Go 1.27.1 and Bun 1.3.14 stages, inspects the `scratch` filesystem for `labd`, all three official runtime binaries and their checksum/source files, plus the system CA bundle, and checks the configured health command. No shell or build toolchain is present. The Compose gate confirms a read-only root filesystem, dropped capabilities, `no-new-privileges`, host networking, and distinct live `labd`/provider processes. It applies Example A, waits for `ready`, observes `{"service":"LabCentral Example A","visits":1}` through the provider-selected loopback URL, destroys the instance, finds zero labeled containers/networks/volumes, restarts the service to prove the destroyed record persisted, and finally removes the isolated test volume.

The `TASK-PROD-004` observability checkpoint repeats a truly empty named-volume
start after adding the private operations listener. It verifies the image's
`0700` state mountpoint, container health through `/ready`, public server-info,
metrics after live traffic, and a healthy restart using the same volume.

[`production-topology.yaml`](../../design/contracts/production-topology.yaml)
indexes the Kubernetes checkpoint. `TEST-PROD-TOPOLOGY-001` through `006`
verify fail-closed image/config/network sentinels, stable two-node placement and
disruption settings, distinct restricted container identities, isolated secret
mounts, protected attached-plugin IPC in both startup orders, sibling
artifact/journal ownership, complete-mutation cross-process locking, and
default-deny policy with explicit dependency classes. The phase verifier
parses every Kubernetes YAML document with duplicate-key rejection and checks
the static topology. A live disposable k3s API additionally accepted the
rendered Namespace, ServiceAccount, Services, PVC, StatefulSet, PDB, and
NetworkPolicies under server-side dry-run. This admission check does not prove
CNI policy enforcement, RWX durability, multi-node scheduling, provider
qualification, or the open 100-instance resilience gates.

`TEST-PROD-TOPOLOGY-007` and `TEST-PROD-TOPOLOGY-008` bind the native
multi-service checkpoint. The static verifier fixes three distinct systemd
users, two non-overlapping IPC groups shared only with core, private systemd
credential projections, separate mode-`0700` state roots, attached socket/token
configuration, fail-closed IP policy, state-path denial, syscall allowlists, and
release inclusion. `systemd-sysusers --dry-run`, `systemd-analyze verify`, and
offline `systemd-analyze security` are release evidence commands. The existing
host-loss subprocess test proves an attached plugin exits after its heartbeat
channel disappears, so an external service manager can reconstruct the one-use
handshake.

[`production-recovery.yaml`](../../design/contracts/production-recovery.yaml)
indexes the offline recovery checkpoint. The normal suite proves protected
non-overwriting hybrid age and Ed25519 key generation, encrypted canary
absence, domain-separated manifest authentication, signer revocation,
component hashes and bounds, traversal/link/type refusal, exact release and
offline confirmation, native SQLite lock/integrity/watermark checks, and
pre-staging of both native components before either destination is committed.
With `LABCENTRAL_TEST_POSTGRES_DSN` and
`LABCENTRAL_TEST_POSTGRES_TOOLS_CONTAINER`, the same suite creates a migrated
source database, invokes real containerized `pg_dump` and `pg_restore`,
decrypts/verifies the set, requires an empty target, compares signed
audit/event/job watermarks, and opens the restored database and shared state.
The artifact-maintenance case adds shared runtime/exclusive offline filesystem
leases with linked-file refusal, exact migration-set and database-reference enumeration, pre-deletion
digest/mode/link validation, abandoned-staging cleanup, dry-run output, and a
second exact deletion confirmation. Native and PostgreSQL integration tests
prove referenced objects survive, unreferenced objects are removed, live
runtimes and unknown future schemas refuse collection, and unsafe inventories
cause no deletion. The test-only insecure flag accepts only a loopback database
and is absent from the production procedure.

[`production-resilience.yaml`](../../design/contracts/production-resilience.yaml)
indexes the 100-Instance control-plane checkpoint. With an isolated
PostgreSQL schema, one atomic two-container-per-Instance batch is processed by
four independent Store pools. One replica abandons an acquired lease before
provider work; the pool must advance its attempt and fencing token, converge
all 100 Instances to `ready` within 120 seconds, then destroy all 100 within
120 seconds. The test verifies 400 unique provider ownership tokens, exact
team-owner persistence, 200 successful Operations, 600 destroyed logical
resources, and zero queued/running/recovery work, cleanup failures, orphaned
allocations, active quota, active allocation claims, or provider objects.

This is a database/scheduler/provider-protocol capacity gate using the
deterministic in-process Docker Engine substitute. It does not claim that an
operator's Proxmox storage, template, bridge, RWX filesystem, CNI, or guest
boot path can sustain 100 VMs. Those deployment-specific T4 load, long-soak,
cross-node RWX failover, and disaster-recovery gates remain mandatory before
admitting a production classroom.

[`production-release.yaml`](../../design/contracts/production-release.yaml)
indexes the release-supply-chain checkpoint. Static verification rejects
mutable third-party action references, normal CI permissions beyond repository-content read, a publisher
without the exact repository/protected environment, missing PostgreSQL/race/
vulnerability gates, a single native build, publication before same-run
signature verification, or release packaging without the compatibility and
operator-verification documents. `TestBootstrapAndStaticAssetsCarrySecurityWarnings`
also proves that server-info exposes the linked release version rather than a
hard-coded product value. The first real tag run, protected-environment
readback, and independent reviewer acceptance remain external release evidence;
the static suite cannot manufacture those GitHub controls.

`TASK-PROXMOX-001` adds the normal-CI T0 HTTPS discovery fake. `TASK-PROXMOX-004` extends T0 with closed-schema planning/state/journal tests, a stateful mutation fake, and an HTTPS method/path/form fake. Tests pin the API-token header, private-CA verification, redirect refusal, envelope/response/inventory bounds, provider-returned paths, deterministic evidence, every allowed mutation endpoint and safe field, committed claims/dependency state, ownership, optimistic config, UPID/task bounds, compensation, absence-confirmed destroy, lifecycle idempotency, restart at every checkpoint, and credential/guest/upstream canary absence. The default constructor is asserted closed; separate production-constructor tests require a non-nil authorizer and prove authorization on discovery plus fail-closed reviewer rejection. Build the binary with `make build-proxmox-provider`; the disposable testbed T3 evidence package and read-only cross-build T4 rehearsal pass.

`TEST-PROXMOX-023`, the `TASK-PROXMOX-005` control-plane T0 integration test, runs a protocol-v1.1 provider through real in-process gRPC and the migrated SQLite store. It persists expiring discovery evidence and visible freshness status, proves scoped credential/config delivery, atomically commits Network/VM plans and claims, forces a second instance through conflict exclusions, passes committed Network state/claims into the VM mutation, routes apply/destroy by persisted target after enqueue, releases claims only on successful absence, and proves an `unsupported` refreshed capability blocks create. Normal CI still contacts no external target. The protected exact-profile lifecycle, guest-connectivity, and failure/recovery runs passed, and their closed testbed artifact verifies `qualifies=true` for its exact target/candidate.

`TEST-PROXMOX-024` covers the signed T3 evidence handoff without contacting a target. It proves deterministic RFC 8785 bytes, Ed25519 domain separation, duplicate/unknown-field rejection, complete ordered T3-case and guest-probe records, exact target/configuration/build rebinding, expiry, and tamper rejection. Authentic failed evidence remains recordable but returns non-qualifying reasons; a failed management probe, unconfirmed provider absence, leftover, or retained claim cannot be consumed as a pass. The isolated `tools/proxmox-qualification` command has no execute mode and accepts a private signing key only from a bounded non-symlink file with no group/other permissions.

`TEST-PROXMOX-025` adds the package-private credentialed smoke runner. With no T3 environment it skips before opening any file or client; setting only part of the environment, using an inexact destructive sentinel, reusing an input path, or failing to confirm the profile's exact target ID is an error. The closed [smoke profile schema](../../design/contracts/proxmox-t3-smoke-profile.schema.json) and [non-secret example](../../schemas/fixtures/proxmox/t3-smoke-profile.valid.json) select one expected disposable VMID/VLAN, one target-configured template/storage/bridge alias, static guest address, and bounded compute shape. Before writing, the runner must pass real discovery/capability/permission/template preflight and observe exactly those planned claims. It then runs create, stop/start/pause/resume, ownership-checked reverse destroy, and a fresh API absence check; independent five-minute cleanup contexts still run after test failure or the main 15-minute context expires.

The smoke is invoked only from a protected, isolated environment. Place the strict target config, non-secret smoke profile, and credential envelope in three distinct absolute non-symlink files; the credential must have no group/other permissions. Then set `LABCENTRAL_PROXMOX_T3_EXECUTE=I_ACKNOWLEDGE_LABCENTRAL_PROXMOX_T3_DESTRUCTIVE`, the three corresponding `LABCENTRAL_PROXMOX_T3_*` path variables, and `LABCENTRAL_PROXMOX_T3_CONFIRM_TARGET` equal to the profile target ID before running `go test -run '^TestCredentialedProxmoxT3Smoke$' -count=1 -v ./providers/proxmox`. Do not use a production target or a VMID/VLAN range shared with other automation. This smoke is not the complete matrix, does not run guest-origin isolation probes, emits no qualifying evidence, and cannot enable the production constructor.

The failure/recovery runner reuses the closed smoke profile but has an independent activation namespace: `LABCENTRAL_PROXMOX_T3_FAILURE_EXECUTE`, `LABCENTRAL_PROXMOX_T3_FAILURE_PROFILE`, `LABCENTRAL_PROXMOX_T3_FAILURE_TARGET_CONFIG`, `LABCENTRAL_PROXMOX_T3_FAILURE_CREDENTIAL_FILE`, and `LABCENTRAL_PROXMOX_T3_FAILURE_CONFIRM_TARGET`. Invoke only `go test -run '^TestCredentialedProxmoxT3FailureRecovery$' -count=1 -v ./providers/proxmox`. It substitutes bounded reads or failures at the package-private `MutationAPI` boundary, while response-loss cases call PVE first and stale-digest rejection is produced by PVE itself. It counts accepted mutation calls, uses fresh invocation identities, rechecks exact ownership before cleanup, rehearses every durable checkpoint without duplicate mutation, and independently confirms VM/storage absence. Reports must distinguish these injected boundary conditions from native PVE failures.

The host preparation in [`tools/provider-testenv`](../../tools/provider-testenv) was exercised on bare-metal Debian 13 with KVM, libvirt, and nested VT-x. A temporary Debian guest proved the runner profile and was disposed. The installed PVE 9.2.2 target exposes only private management plus an IP-less VLAN-aware trunk, and has a dedicated token, `local-lvm`/raw and `local`/qcow2 profiles, and a customized Debian template. Guarded smoke attempts exercised real discovery, clone, asynchronous config, guest-agent readiness, lifecycle, task recovery, and owned cleanup paths while exposing several fail-closed corrections; runs `qualification.smoke-20260902-r8` and `qualification.smoke-20260902-secondary-r1` passed the two storage profiles with VMID `910001`, VLAN `3901`, and fresh absence confirmation. Exact-revision failure/recovery run `qualification.failure-20260903-r4` passed every non-isolation target-applicable case in 292.02 seconds. Isolation run `qualification.isolation-20260903-r7` then passed 3/3 intended and 0/3 management/cross-instance probes plus reverse cleanup. Independent reads confirmed all disposable VMIDs free, both storage profiles clean, and all recent tasks terminal. The resulting testbed evidence verified `qualifies=true` with canonical digest `sha256:6943002263b2b2f35f04aeec21ec45bb91b1eab30aaba50d01e1903dbc62244d`.

`TEST-PROXMOX-026` adds the closed guest-origin probe protocol, a minimal agent, and the pinned-mTLS runner client. The [request](../../design/contracts/proxmox-qualification-probe-request.schema.json) and [response](../../design/contracts/proxmox-qualification-probe-response.schema.json) schemas have executable fixtures and reject unknown or duplicate fields. Normal CI proves exact three-target ordering, effort and body bounds, single-flight behavior, intended-positive/management-negative/cross-instance-negative observations, endpoint redaction, nonce/target/image response binding, canonical transcript hashing, TLS 1.3 server trust, and client-certificate trust. Those normal-CI tests remain local and synthetic; protected run `qualification.isolation-20260903-r7` separately exercised the same client and agent from the real source guest.

`TEST-PROXMOX-027` adds the separate [isolation-run profile](../../design/contracts/proxmox-t3-isolation-profile.schema.json) and package-test-only executor. Normal CI validates the closed fixture and proves the exact plan shape: a source and peer in one Lab Instance/VLAN, a third guest in a distinct Lab Instance/VLAN, three distinct reserved VMIDs, and two distinct reserved VLANs. The credentialed binding independently requires the destructive sentinel, matching target confirmation, and six distinct absolute inputs; it reads the Proxmox credential and runner private key only from mode-protected non-symlink files. After exact discovery/plan checks it creates all resources, waits boundedly for the source agent, runs one fresh-nonce probe, maps all observations into typed signed-evidence records, and performs reverse cleanup with a fresh API absence read for every VM. It registers each planned address for cleanup before apply and, if apply returns without provider state, waits any exact journaled task to stop and re-verifies the plan's ownership marker before destroy. Default CI executes the guard and planning contract only; no Proxmox endpoint or guest is contacted.

Protected run `qualification.isolation-20260903-r7` passed on PVE 9.2.2 using VMIDs `910001`–`910003` and VLANs `3901`–`3902`. The source guest reached its intended peer on 3/3 attempts but not the PVE management endpoint or the cross-instance guest on any of three attempts; the canonical probe transcript digest was `sha256:2a0333652ab13e525000a25288583aafa31404dc548c01bb57d0a7deb0ab481d`. Reverse cleanup completed, and independent PVE reads confirmed all three VMIDs free, both storage scopes clean, and every recent task terminal.

Run the isolation binding only after the qualification image starts `lab-proxmox-probe-agent` on the profile's exact source address and exposes reviewer-selected TCP listeners on the peer and cross guest ports. Set `LABCENTRAL_PROXMOX_T3_ISOLATION_EXECUTE` to the same exact destructive sentinel; set `LABCENTRAL_PROXMOX_T3_ISOLATION_PROFILE`, `LABCENTRAL_PROXMOX_T3_ISOLATION_TARGET_CONFIG`, `LABCENTRAL_PROXMOX_T3_ISOLATION_CREDENTIAL_FILE`, `LABCENTRAL_PROXMOX_T3_ISOLATION_AGENT_CA`, `LABCENTRAL_PROXMOX_T3_ISOLATION_RUNNER_CERT`, and `LABCENTRAL_PROXMOX_T3_ISOLATION_RUNNER_KEY` to six distinct absolute files; and set `LABCENTRAL_PROXMOX_T3_ISOLATION_CONFIRM_TARGET` to the profile target. Invoke `go test -run '^TestCredentialedProxmoxT3Isolation$' -count=1 -v ./providers/proxmox`. The VMID/VLAN ranges, PKI, target, and destination services must be disposable and dedicated to the run. A failed/unknown cleanup or unexpected object requires investigation and continued claim exclusion.

`TEST-PROXMOX-028` is a separate read-only T4 upgrade rehearsal. Its exact sentinel, protected absolute journal directory, target ID, and comma-separated prior-build invocation IDs must all be present; otherwise it skips or fails before journal access. It opens no provider client. The protected `qualification.upgrade-20260903-r1` run used three terminal records produced by the earlier `99fb78f` candidate and proved that the current consumer validates their checksums and reads Network and VM `proxmox.qemu-state/v1.0` plus a destroyed `proxmox.invocation-journal/v1.0` tombstone without replaying a mutation.

`TASK-PROXMOX-002` adds the catalog [`proxmox-qemu-mutation.yaml`](../../design/contracts/proxmox-qemu-mutation.yaml). Its original 22 stable cases map every `TEST-PROVIDER-*` failure window and add core-claim conflicts, template/config drift, foreign VMID refusal, lost clone/start/delete responses, durable-journal restart/corruption, UPID validation, task redaction, scoped permissions, and positive/negative VLAN-isolation evidence; `TASK-PROXMOX-005` adds `TEST-PROXMOX-023` for core target integration, `TEST-PROXMOX-024` for the signed evidence boundary, `TEST-PROXMOX-025` for guarded credentialed smoke readiness, `TEST-PROXMOX-026` for the bounded probe mechanism, `TEST-PROXMOX-027` for exact isolation-topology runner readiness, and `TEST-PROXMOX-028` for prior-build state/journal readability. `TEST-PROXMOX-029` proves journal-wide advisory exclusion with a real subprocess, bounded canceled acquisition with independent handles, strict lock modes, and startup rejection of a journal outside the shared mount's exact journal sibling. The disposable target passed both storage profiles, the exact T3 matrix, cleanup, signed testbed verification, and read-only upgrade rehearsal. No credentialed target is used by normal CI or the design verifier. ADR-0014 tests add closed reviewer trust, binary/source/live-profile rebinding, expiry/revocation behavior, and fail-closed production constructor coverage.

## Release gates

The design-only Phase 0 exit is satisfied when examples validate against the executable schema, canonical vectors are stable, the plugin protocol passes compatibility checks, the fake-provider contract catalog covers failure windows, the threat model has owner/test mapping, and the verifier passes. The Phase 0B foundation additionally requires the implemented Go renderer, SQLite/PostgreSQL job suite, and fake-provider shared contract runner; those gates now pass. The trusted-laptop package additionally requires reproducible archive/checksum verification, SBOM/provenance presence, a toolchain-free runtime image, separate plugin execution, persistent state, and clean real-Engine lifecycle evidence. The Proxmox mutation design gate additionally requires closed typed schemas, complete generic-case mapping, no pre-gate mutation capability, and explicit T3 target/profile evidence. The disposable PVE target exercised target integration, least-privilege capability evidence, cleanup/orphan handling, and v1.0 state/journal upgrade readability. The single-operator production package requires ADR-0014 authorizer tests, immutable plugin launch, target status/refresh tests, deterministic release contents, service-unit verification, a deployment-specific signed qualification artifact before writes can open, packaged API create/restart/destroy, and independent post-destroy absence. Exact candidate `0.2.0-rc5` passed those gates on the disposable PVE 9.2.2 target; every new installation must repeat qualification against its own exact build and target.

At least one test deliberately proves each destructive path refuses name-only or mismatched ownership. Secret-redaction tests seed canary values through request, job, plugin, error, event, audit, log, and trace paths and fail if any sink contains them.

## Dark classroom interface and private testbed

TASK-UI-001 uses production-export browser fixtures in Chromium, Firefox and
WebKit for startup, student launch payloads, deep navigation, console controls,
delete cancellation, polling outage recovery, role-specific pages, classroom
batch ownership, admin screens and mobile overflow. API tests check filtered
project/roster pages, revoked membership, scoped template content and embedded
static routes. Blueprint source inspection and desktop/mobile visual review
supplement these tests. These fixtures do not establish live OIDC or provider
behavior. TASK-TESTBED-001 requires the separate live acceptance in the
[classroom runbook](../operations/classroom-testbed.md).

The live classroom run exposed digest prefix conversion at the core/plugin
boundary and nested console routing issues. Regression checks now enforce raw
hex wire digests, the explicit static console route, root-relative assets,
same-origin relative/absolute launch URLs and foreign-origin rejection. The
pinned tailnet transport rejects mismatched target/name/key/address, offline
peers and identity-service failure; default TLS tests remain active. Real
console keyboard input is validated on the private target. The 25-seat soak
uses independent OIDC browser contexts with normal 30-minute console limits,
individual reconnection at 24 minutes, and continuous lab/session observations.

The live classroom soak exposed console closure at the initial authorization
lease. Bridge regression tests now preserve RFB traffic through repeated lease
renewals, close on denial or a stalled core, and enforce the fixed session
expiry. Core callback tests require renewal to be scheduled before lease expiry.

Access-store tests cover a caller clock ahead of the database, preservation of
a shorter launch deadline and rejection of already-expired launches. The
database TTL cap is checked without relaxing session expiry.
