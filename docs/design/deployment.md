# Deployment

## Forms

- **Release archive/native:** `labd`, official provider/access executables, static UI, schemas, migrations, checksums. Local mode supervises configured children; the production Proxmox units attach three separately sandboxed systemd services.
- **Docker:** one image may contain executables but starts them as separate processes. Docker provider access uses an explicitly mounted socket/endpoint only in acknowledged laptop mode; the socket never reaches guests.
- **Compose:** `labd` + optional PostgreSQL/object-store-compatible service for evaluation; SQLite/filesystem remains the simplest default.
- **Kubernetes:** `labd` Deployment, PostgreSQL/object storage external or managed, plugins as sidecar/separate pods under dedicated identities where local IPC/network policies preserve boundaries. Static assets are served by `labd` or a normal static server.

Laptop defaults: listen `127.0.0.1`, no-auth warning, SQLite, filesystem artifacts, Docker target auto-registration only after explicit socket selection, conservative quotas/TTL, and non-hostile-workload warning. No external broker or Site Agent is required.

## Implemented laptop distribution

`TASK-DOCKER-001` provides the native-binary development path. `make build` performs a frozen Bun install, Svelte type check/static build, builds `labd` and `lab-provider-docker`, and writes `bin/lab-provider-docker.sha256`. `make run` starts `labd` on `127.0.0.1:8080` with state under `./labcentral-data`. The server verifies the provider executable digest before launch, negotiates protocol v1 over a private per-launch Unix socket, and initializes SQLite WAL plus the filesystem artifact root before accepting work.

The server always refuses a non-loopback no-auth listener. No-auth is an explicit loopback-only development mode, and an installation that has selected local or OIDC authentication cannot later start in no-auth mode. The thin Docker provider reads `DOCKER_HOST`, defaults to `/var/run/docker.sock`, accepts plaintext TCP only on loopback, and does not yet configure a remote TLS Docker endpoint.

`TASK-RELEASE-001` adds these supported laptop artifacts:

- `make release VERSION=<version>` writes `dist/labcentral-<version>-<os>-<arch>.tar.gz` and its adjacent checksum. Archive headers, ordering, ownership, modes, gzip metadata, build IDs, and timestamps are normalized. `SOURCE_DATE_EPOCH` selects the timestamp and otherwise defaults to the Git commit time. Production release creation refuses a dirty Git worktree so embedded source provenance matches compiled code.
- The archive contains `labd`, the Docker and Proxmox providers, the noVNC access plugin, the Proxmox qualification utility, provider checksums and v1 manifests, the build-embedded Proxmox source-tree digest, Kubernetes and three-service systemd assets, deployment examples/runbooks, archive-wide `SHA256SUMS`, a CycloneDX 1.6 SBOM, release metadata, and source/build provenance. Provenance is neither signed nor a SLSA attestation. Linux amd64 is the verified target; Windows IPC remains gated by `OQ-GUEST-001`.
- `make image VERSION=<version> IMAGE=<tag>` uses digest-pinned Go and Bun build stages, gives SvelteKit the requested deterministic application version, and creates a `scratch` runtime containing only `labd`, the three official runtime executables/checksums, the Proxmox source digest, and the system CA bundle. Native development web builds use the stable `WEB_VERSION=dev` default. Bun, Go, a shell, and package-manager files are absent at runtime. `labd healthcheck` validates the local server-info contract without adding a runtime HTTP client.
- `docker compose up -d --build` persists `/var/lib/labcentral` in the named `labcentral-data` volume, mounts the explicitly selected `${DOCKER_SOCKET_PATH:-/var/run/docker.sock}`, and uses host networking. `LABCENTRAL_PORT`, `LABCENTRAL_IMAGE`, and `LABCENTRAL_DATA_VOLUME` override the loopback port, image, and volume name. `docker compose down` preserves state; `docker compose down --volumes` deliberately removes it.

The scratch image creates `/var/lib/labcentral` as mode `0700` before declaring
it as a volume, so Docker's first-use copy-up preserves the private mountpoint
required by `labd`. A clean named-volume start and restart are executable gates;
the data layout remains at the original mount root.

Host networking is part of the laptop correctness boundary: it preserves a true host-loopback no-auth listener and lets the containerized control plane probe provider-published `127.0.0.1` ports. It is supported directly by Docker Engine on Linux; Docker Desktop 4.34 or newer requires the host-networking setting to be enabled. This Compose form is not declared supported on other platforms.

The image runs as root because common Linux Docker sockets are root-owned. A read-only socket bind prevents changing the socket inode through that mount; it does **not** make Docker Engine commands read-only. Anyone controlling `labd` or its provider through this socket effectively controls the Docker host. Compose also uses a read-only root filesystem, drops Linux capabilities, applies `no-new-privileges`, bounds PIDs, and mounts only the selected socket and data volume, but none of those settings reduce Docker socket authority. The socket never reaches created lab guests.

The release also packages the fail-closed Kubernetes base described below; it
is excluded from the laptop Compose path.

For the isolated `TASK-PROXMOX-005` qualification environment, [`tools/provider-testenv`](../../tools/provider-testenv) provides an operator-side Debian/libvirt harness. It creates a private NAT management network and a separate IP-less, non-forwarded layer-2 trunk, uses digest-pinned base media, exposes nested VT-x through `host-passthrough`, leaves all guests disabled at host boot, and marks every disposable domain before allowing deletion. The protected runner and fixed Proxmox VE 9.2 target are installed; the target has an isolated VLAN-aware bridge, a dedicated least-privilege expiring token, and a digest-bound Debian qualification template. The lifecycle smoke and one three-guest source/peer/cross isolation probe have passed on this target. These runs cannot emulate Fly or AWS faithfully, advertise a production provider capability, or replace the remaining exact-target matrix.

`TASK-PROXMOX-005` adds an opt-in native target path without changing the default Docker process. `--proxmox-plugin` selects a separately digest-pinned `lab-provider-proxmox`; `--proxmox-target-config` supplies the strict non-secret target envelope; `--proxmox-credential-file` identifies an allowlisted, non-symlink mode-protected secret; and `--proxmox-target-id` fixes the stable instance binding. Docker can be disabled for a Proxmox-only installation. Startup negotiates protocol v1.1, performs discovery, persists only redacted expiring evidence, exposes target freshness through the API/UI, and refreshes every two minutes. The plugin host copies a protected opened executable into its private runtime directory while verifying the configured digest, then executes the mode-`0500` snapshot.

`make build-proxmox-qualification` builds `lab-proxmox-qualification`, which is also included in native release archives as an offline review tool. `sign` accepts a completed closed unsigned T3 artifact plus an unpadded-base64url Ed25519 private key from a non-symlink file with no group/other permissions and writes the canonical signed document to standard output. `verify` requires the trusted public key/key ID plus exact target, installation, target configuration, current capability/inventory/policy, PVE release/build, and provider binary/source-tree expectations; it exits unsuccessfully when authentic evidence does not qualify. Neither command contacts a target or changes provider state. The production provider independently consumes the signed artifact only when paired with a closed trust policy and re-verifies exact live bindings before every mutation. Signing-key custody, protected T3 execution, artifact retention, and reviewer separation remain deployment responsibilities.

The native Proxmox package includes three systemd services: core, provider,
and access use distinct non-root users; provider and access each have a separate
IPC group shared only with core. systemd credential projections deliver each
registration token only to its pair and the Proxmox credential only to core.
Core and provider state roots are separate mode-`0700` directories, and explicit
inaccessible paths prevent the plugin sandboxes from traversing core state.
Every unit starts with all non-loopback IP traffic denied; the operator must add
the exact qualified Proxmox address to the two plugin units and retain a
UID-scoped TCP/8006 host firewall rule. The services also use a strict read-only
system view, an `@system-service` syscall allowlist, empty capabilities, and
kernel/device/namespace/process hardening. The exact install, authentication
bootstrap, TLS proxy, qualification, verification, backup, update, rotation,
and emergency-cleanup procedure is
[`../operations/proxmox-deployment.md`](../operations/proxmox-deployment.md).

`make build-proxmox-probe-agent` builds a separate qualification fixture that is also excluded from production archives and images. An operator must bake that exact binary into the digest-pinned disposable qualification template, issue a short-lived server identity for its exact guest IPv4 address, and supply a dedicated CA that trusts only the protected runner identity. The agent requires three distinct absolute certificate/key paths, a mode-protected non-symlink server key, one exact non-loopback IPv4 listen address, and the operator-attested qualification image digest. It uses TLS 1.3 mutual authentication and has no shell or provider client. The dedicated CA and server identity must not be shared with participants or retained in classroom images; stop and remove the fixture after qualification. Its configured image digest is evidence binding, not runtime attestation, so the runner must separately rebind the approved template/content digest.

The `TEST-PROXMOX-027` isolation runner uses a separate v1 profile and environment namespace so the original one-VM lifecycle smoke remains stable. It requires six distinct absolute inputs: profile, target config, Proxmox credential, agent CA, runner certificate, and runner key. Credential and runner-key files must have no group/other permissions; every input must remain a bounded non-symlink regular file. The profile reserves three VMIDs and two VLANs and identifies the source/peer/cross guests and TCP destinations exactly. The source agent and peer/cross listener services are properties of the disposable qualification template, not installed through provider guest execution. Use a dedicated PKI and address/range reservation, revoke it after the run, and never point the profile at a production target or a management endpoint whose availability probe is unsafe.

On the Debian qualification image, the probe unit is enabled under `cloud-init.target` and ordered after `cloud-final.service`. Debian orders `cloud-final.service` after `multi-user.target`, so enabling that dependent unit directly under `multi-user.target` creates a cycle and can cause systemd to drop the probe's boot job.

The credentialed smoke executor is not a shipped executable. It is compiled only into the `providers/proxmox` Go test binary and remains skipped unless the exact destructive sentinel, three distinct absolute files, and matching target confirmation are all present. Run it from a protected worker that can reach only the disposable Proxmox target, keep its candidate journal on durable mode-`0700` storage, reserve the profile's VMID/VLAN outside other automation, and retain the journal plus operator-visible leftovers after any ambiguous result. The command and environment contract are documented in [testing](testing.md); both target-advertised `local-lvm`/raw and `local`/qcow2 profiles have passed on the disposable target, and completing either smoke never changes a deployed provider's capability.

The credentialed failure/recovery executor has the same test-only packaging and an independent exact-sentinel environment namespace. Its fault controller is constructed only by the package test: no provider configuration, protocol request, or production executable can select it. Keep its VMID exclusive for the whole run, because deliberate compensation failure temporarily leaves the exactly marked VM for the harness to reconcile and remove.

Production defaults: TLS (or trusted TLS-terminating proxy with strict header trust), OIDC/local auth, PostgreSQL, durable shared artifacts, separate provider credentials, management network routes/firewalls, plugin sandbox identities, encrypted backups, audit retention, metrics/traces, and deny-egress policy. Multi-replica requires PostgreSQL, shared artifacts, stable encryption/session keys, and compatible job leasing.

`TASK-PROD-001` implements the storage/fencing prerequisite. `--deployment-mode=single-node` is the default and permits only SQLite plus `--artifact-mode=local-filesystem`. `--deployment-mode=multi-replica` requires a unique DNS-label-like `--replica-id`, a non-symlink PostgreSQL URL file with verified TLS, `--artifact-mode=shared-filesystem` with an absolute non-root shared path, and local or OIDC authentication. Startup migrates under a PostgreSQL advisory lock and self-tests shared staging locks before serving. When Proxmox is enabled, its signed target configuration must use the exact `proxmox-journal` sibling of the artifact directory on the same shared mount. The provider self-tests advisory exclusion there and holds one journal-wide kernel lease for the complete mutating RPC, so a process crash releases execution to a peer that resumes from the last checksummed checkpoint. Keeping the core-owned artifact directory and provider-owned journal as siblings supports distinct runtime UIDs without widening either directory from mode `0700`. A node-local or differently located journal is refused before plugin launch. Shared-RWX failover evidence remains part of `TASK-PROD-004`; the qualified single-node topology is unchanged. See [`../operations/postgresql-control-plane.md`](../operations/postgresql-control-plane.md).

## Configuration and secrets

The current executable is configured with explicit flags; secret values are read from protected regular files rather than flag or environment contents. `--auth-mode` is mandatory. Explicit no-auth is loopback-only and produces startup/API/UI warnings. Authenticated service requires an exact HTTPS public origin and either direct TLS or an explicitly trusted loopback reverse proxy; forwarded headers from other peers are ignored. The database and authentication-mode/legacy-data preflight run before any provider process starts or OIDC discovery is attempted, and first-administrator creation records the mode atomically. A no-auth database with existing instances or jobs must remain in no-auth for cleanup or be replaced/migrated through an operator-reviewed owner/actor mapping; startup never invents authenticated ownership for legacy rows.

## Observability

Use OpenTelemetry-compatible traces/metrics and structured JSON logs. Required dimensions: request/operation/instance/resource/target/plugin/invocation IDs, error class, lifecycle timings; tenant/user IDs are access-controlled and bounded to avoid metric cardinality. Metrics cover API latency/errors, queue depth/lease age, operation duration/result, plugin health/restarts, target capability age, resource lifecycle, outbox lag, access sessions, and cleanup leftovers. Trace/log field allowlists and secret tainting prevent values from entering telemetry.

Health endpoints distinguish liveness (process), readiness (database/migrations/artifacts/worker eligibility), and degraded dependencies (individual plugins/targets do not necessarily make all API unready).

The first `TASK-PROD-004` runtime checkpoint implements a separate operations
listener (loopback `127.0.0.1:9090` by default) with `/live`, `/ready`, and
`/metrics`. Readiness starts only after startup preflight, plugin handshakes,
initial target refresh, and worker construction, then checks database,
artifact-root, and configured-plugin health in parallel under one two-second
deadline. A target capability outage remains degraded target state rather than
making every API replica unready. Prometheus metrics use fixed method/status,
endpoint-family, component, plugin, and check labels; object, tenant, request
path/query, and error values are prohibited.

Opt-in OTLP/HTTP server spans use W3C trace context and the same fixed endpoint
families. Remote export requires TLS 1.3 or newer and may use private-CA mTLS;
plaintext is accepted only to a loopback Collector. Raw URL paths, queries,
tenant/object identifiers, and error strings are not trace attributes. The
operator contract, alert rules, and certificate rotation procedure are in
[`../operations/observability.md`](../operations/observability.md). The second
runtime checkpoint adds a database-time aggregate snapshot every 15 seconds for
queue and expired-lease age, both outboxes, every Operation/resource/access
lifecycle state, stale target evidence, and cleanup leftovers. Terminal worker
executions add fixed-kind/result counters and duration histograms. The exporter
rejects unknown lifecycle values rather than turning database content into new
labels; no object or tenant identity is present.

The recovery checkpoint packages `labcentral-recovery` in every native
release. It creates non-overwriting, domain-separated Ed25519-signed and
hybrid-age-encrypted offline sets. Multi-replica sets bind a PostgreSQL custom
dump to the complete shared artifact/journal root and stable database
watermarks; native sets bind the locked, integrity-checked SQLite core root to
the separately owned provider root. Restore authenticates and hashes every
component, stages filesystem contents before mutation, requires an exact
release and a new database/path, preserves numeric ownership by default, and
rechecks the signed watermark. Decryption identity and runtime secrets remain
off-workload and separately recoverable. The exact operator procedure is
[`../operations/recovery.md`](../operations/recovery.md), and the executable
contract is
[`../../design/contracts/production-recovery.yaml`](../../design/contracts/production-recovery.yaml).

## Kubernetes production base

The first Kubernetes checkpoint is a two-replica StatefulSet with stable Pod
names used as fencing-worker and plugin-instance identities. Required hostname
anti-affinity prevents both replicas from sharing one worker, a PDB retains one
Ready replica during eviction-aware maintenance, and rolling updates stop on an
unready ordinal. A PDB does not constrain the StatefulSet controller's own
rollout, so the operator must watch every ordinal and retain a compatible prior
image/evidence pair for rollback.

The Pod has three digest-identical but command-separated containers: `labd`
runs as UID 10000, the Proxmox provider as UID 11000, and noVNC as UID 12000.
All use the dedicated IPC/storage GID 20000 so either startup order creates
group-reachable sockets; mode-`0700` artifact/journal directories still enforce
the distinct owning UIDs.
Each has a read-only root, no added capability, no privilege escalation, a
RuntimeDefault seccomp profile, bounded resources/tmpfs, and no Kubernetes API
token. Core secrets and the Proxmox credential mount only into `labd`;
provider/access registration tokens and IPC volumes are separate. The official
plugins attach over group-restricted Unix sockets and never receive database
access or authorization policy.

One RWX mount contains sibling mode-`0700` directories: core owns `artifacts`
and the provider owns `proxmox-journal`. Startup verifies the shared artifact
locking primitive, while the provider initializes and verifies its journal.
NetworkPolicy defaults both directions to deny and lists DNS, PostgreSQL,
Proxmox, OIDC, ingress, and monitoring as explicit site-replaced classes. The
CNI must prove enforcement. Because containers in a Pod share its network
namespace, this topology supports only the bundled official plugins; it does
not claim per-plugin network isolation for third-party code.

The base intentionally references an all-zero OCI digest, RFC 5737 egress
addresses, and no ConfigMap or Secret resources. Operators must create the
inputs separately, enable Secret encryption/RBAC, replace every sentinel, and
qualify the exact image/target before rollout. The complete procedure and
rotation/backup boundaries are in
[`../operations/kubernetes-proxmox.md`](../operations/kubernetes-proxmox.md),
and the machine-readable checkpoint is
[`../../design/contracts/production-topology.yaml`](../../design/contracts/production-topology.yaml).

## Upgrade

Upgrade order: back up → expand database schema → roll `labd` within compatibility matrix → upgrade/drain plugins per target → verify capabilities/state readability → backfill/contract later. Static UI declares server range. Rollback is supported until an explicitly irreversible database/provider-state migration, which is gated and reported.
