# Deployment

## Forms

- **Release archive/native:** `labd`, official provider/access executables, static UI, schemas, migrations, checksums. `labd` supervises configured children.
- **Docker:** one image may contain executables but starts them as separate processes. Docker provider access uses an explicitly mounted socket/endpoint only in acknowledged laptop mode; the socket never reaches guests.
- **Compose:** `labd` + optional PostgreSQL/object-store-compatible service for evaluation; SQLite/filesystem remains the simplest default.
- **Kubernetes:** `labd` Deployment, PostgreSQL/object storage external or managed, plugins as sidecar/separate pods under dedicated identities where local IPC/network policies preserve boundaries. Static assets are served by `labd` or a normal static server.

Laptop defaults: listen `127.0.0.1`, no-auth warning, SQLite, filesystem artifacts, Docker target auto-registration only after explicit socket selection, conservative quotas/TTL, and non-hostile-workload warning. No external broker or Site Agent is required.

## Implemented laptop distribution

`TASK-DOCKER-001` provides the native-binary development path. `make build` performs a frozen Bun install, Svelte type check/static build, builds `labd` and `lab-provider-docker`, and writes `bin/lab-provider-docker.sha256`. `make run` starts `labd` on `127.0.0.1:8080` with state under `./labcentral-data`. The server verifies the provider executable digest before launch, negotiates protocol v1 over a private per-launch Unix socket, and initializes SQLite WAL plus the filesystem artifact root before accepting work.

The server refuses a non-loopback no-auth listener unless `--unsafe-listen-non-loopback-without-auth` is supplied exactly. That override changes the startup/API/UI warning; it does not add authentication or TLS. The thin Docker provider reads `DOCKER_HOST`, defaults to `/var/run/docker.sock`, accepts plaintext TCP only on loopback, and does not yet configure a remote TLS Docker endpoint.

`TASK-RELEASE-001` adds these supported laptop artifacts:

- `make release VERSION=<version>` writes `dist/labcentral-<version>-<os>-<arch>.tar.gz` and its adjacent checksum. Archive headers, ordering, ownership, modes, gzip metadata, build IDs, and timestamps are normalized. `SOURCE_DATE_EPOCH` selects the timestamp and otherwise defaults to the Git commit time.
- The archive contains separate `labd` and `lab-provider-docker` executables, the provider checksum, a v1 plugin manifest, archive-wide `SHA256SUMS`, a CycloneDX 1.6 SBOM, release metadata, and source/build provenance. The provenance records revision and dirty state but is neither signed nor a SLSA attestation. Linux amd64 is the verified target; Windows IPC remains gated by `OQ-GUEST-001`.
- `make image VERSION=<version> IMAGE=<tag>` uses digest-pinned Go and Bun build stages, gives SvelteKit the requested deterministic application version, and creates a `scratch` runtime containing only the two static executables and provider checksum. Native development web builds use the stable `WEB_VERSION=dev` default. Bun, Go, a shell, and package-manager files are absent at runtime. `labd healthcheck` validates the local server-info contract without adding a runtime HTTP client.
- `docker compose up -d --build` persists `/var/lib/labcentral` in the named `labcentral-data` volume, mounts the explicitly selected `${DOCKER_SOCKET_PATH:-/var/run/docker.sock}`, and uses host networking. `LABCENTRAL_PORT`, `LABCENTRAL_IMAGE`, and `LABCENTRAL_DATA_VOLUME` override the loopback port, image, and volume name. `docker compose down` preserves state; `docker compose down --volumes` deliberately removes it.

Host networking is part of the laptop correctness boundary: it preserves a true host-loopback no-auth listener and lets the containerized control plane probe provider-published `127.0.0.1` ports. It is supported directly by Docker Engine on Linux; Docker Desktop 4.34 or newer requires the host-networking setting to be enabled. This Compose form is not declared supported on other platforms.

The image runs as root because common Linux Docker sockets are root-owned. A read-only socket bind prevents changing the socket inode through that mount; it does **not** make Docker Engine commands read-only. Anyone controlling `labd` or its provider through this socket effectively controls the Docker host. Compose also uses a read-only root filesystem, drops Linux capabilities, applies `no-new-privileges`, bounds PIDs, and mounts only the selected socket and data volume, but none of those settings reduce Docker socket authority. The socket never reaches created lab guests.

Kubernetes manifests remain a roadmap deployment form and are not produced by the laptop slice.

`TASK-PROXMOX-005` adds an opt-in native target path without changing the default Docker process. `--proxmox-plugin` selects a separately digest-pinned `lab-provider-proxmox`; `--proxmox-target-config` supplies the strict non-secret target envelope; `--proxmox-credential-file` must identify an allowlisted, non-symlink regular file with no group/world permissions; and `--proxmox-target-id` fixes the stable instance binding. Startup negotiates protocol v1.1, performs read-only discovery, and persists only redacted expiring evidence. A discovery failure leaves cached evidence authoritative and does not stop the Docker path. The shipped production Proxmox binary still reports mutation capability unsupported and refuses writes, even when these options and `mutationEnabled` are present.

`make build-proxmox-qualification` builds the isolated `lab-proxmox-qualification` review utility; it is not included in the laptop release archive or runtime image. `sign` accepts a completed closed unsigned T3 artifact plus an unpadded-base64url Ed25519 private key from a non-symlink file with no group/other permissions and writes the canonical signed document to standard output. `verify` requires the trusted public key/key ID plus exact target, installation, target configuration, current capability/inventory/policy, PVE release/build, and provider binary/source-tree expectations; it exits unsuccessfully when authentic evidence does not qualify. Neither command contacts a target or changes the provider gate. Signing-key custody, protected T3 execution, artifact retention, and reviewer separation are deployment responsibilities.

`make build-proxmox-probe-agent` builds a separate qualification fixture that is also excluded from production archives and images. An operator must bake that exact binary into the digest-pinned disposable qualification template, issue a short-lived server identity for its exact guest IPv4 address, and supply a dedicated CA that trusts only the protected runner identity. The agent requires three distinct absolute certificate/key paths, a mode-protected non-symlink server key, one exact non-loopback IPv4 listen address, and the operator-attested qualification image digest. It uses TLS 1.3 mutual authentication and has no shell or provider client. The dedicated CA and server identity must not be shared with participants or retained in classroom images; stop and remove the fixture after qualification. Its configured image digest is evidence binding, not runtime attestation, so the runner must separately rebind the approved template/content digest.

The credentialed smoke executor is not a shipped executable. It is compiled only into the `providers/proxmox` Go test binary and remains skipped unless the exact destructive sentinel, three distinct absolute files, and matching target confirmation are all present. Run it from a protected worker that can reach only the disposable Proxmox target, keep its candidate journal on durable mode-`0700` storage, reserve the profile's VMID/VLAN outside other automation, and retain the journal plus operator-visible leftovers after any ambiguous result. The command and environment contract are documented in [testing](testing.md); completing the smoke never changes a deployed provider's capability.

Production defaults: TLS (or trusted TLS-terminating proxy with strict header trust), OIDC/local auth, PostgreSQL, durable shared artifacts, separate provider credentials, management network routes/firewalls, plugin sandbox identities, encrypted backups, audit retention, metrics/traces, and deny-egress policy. Multi-replica requires PostgreSQL, shared artifacts, stable encryption/session keys, and compatible job leasing.

## Configuration and secrets

Precedence is flags → environment → config file only for non-secret values, with startup output showing sources but redacting values. Secrets use environment/file refs locally and external providers in production. No-auth non-loopback requires the exact unsafe option and produces startup/API/UI warnings. Public base URLs and trusted proxy CIDRs are explicit; forwarded headers from other peers are ignored.

## Observability

Use OpenTelemetry-compatible traces/metrics and structured JSON logs. Required dimensions: request/operation/instance/resource/target/plugin/invocation IDs, error class, lifecycle timings; tenant/user IDs are access-controlled and bounded to avoid metric cardinality. Metrics cover API latency/errors, queue depth/lease age, operation duration/result, plugin health/restarts, target capability age, resource lifecycle, outbox lag, access sessions, and cleanup leftovers. Trace/log field allowlists and secret tainting prevent values from entering telemetry.

Health endpoints distinguish liveness (process), readiness (database/migrations/artifacts/worker eligibility), and degraded dependencies (individual plugins/targets do not necessarily make all API unready).

## Upgrade

Upgrade order: back up → expand database schema → roll `labd` within compatibility matrix → upgrade/drain plugins per target → verify capabilities/state readability → backfill/contract later. Static UI declares server range. Rollback is supported until an explicitly irreversible database/provider-state migration, which is gated and reported.
