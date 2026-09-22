# Primary references

Unless a row says otherwise, sources were retrieved **2026-09-02**. Provider
features remain target-discovered; these sources support design direction, not
universal target guarantees.

| Subject | Primary source | Supported claim |
|---|---|---|
| Proxmox encrypted WebSocket console | [QEMU API `vncproxy` and `vncwebsocket`](https://github.com/proxmox/qemu-server/blob/master/src/PVE/API2/Qemu.pm) | WebSocket mode returns a temporary port/ticket; `vncwebsocket` checks `VM.Console` and a ticket bound to user, VM and port. The provider can authenticate the WSS request with its server-side API token; it must not delegate that reusable token to a browser/access plugin. An authorized PVE 9.2.11 target was measured using verified TLS 1.3 and RFB 3.8 through WSS while its separate temporary TCP socket was plain RFB. Retrieved and measured 2026-09-21. |
| Proxmox architecture/API/network/storage | [Proxmox VE Administration Guide](https://pve.proxmox.com/pve-docs/pve-admin-guide.pdf) | PVE exposes REST through `pveproxy`; QEMU VMs, bridges/VLAN tags, clusters and storage capabilities are provider primitives. |
| Proxmox API paths and schemas | [Proxmox VE API Viewer](https://pve.proxmox.com/pve-docs/api-viewer/) | The current API exposes read-only `GET` schemas for `/version`, `/nodes`, and per-node storage, network, and QEMU inventory; storage accepts content/enabled/format filters. |
| Proxmox API-token authentication | [Proxmox VE user-management guide: API Tokens](https://pve.proxmox.com/pve-docs/chapter-pveum.html#pveum_tokens) | API tokens support separate ACLs and expiry; requests use `Authorization: PVEAPIToken=USER@REALM!TOKENID=UUID`; effective permissions are bounded by both the token and backing user. |
| Proxmox effective permissions and QEMU/task APIs | [Proxmox VE API Viewer](https://pve.proxmox.com/pve-docs/api-viewer/) | `/access/permissions` exposes effective privileges and their propagation state; child VMID/VLAN operations require applicable inherited grants. QEMU clone/config/lifecycle/delete operations return UPIDs whose task status must be polled; in-memory suspend uses `qmpause` and paused state appears in `qmpstatus`. `/cluster/nextid` can check a requested VMID but is not a durable LabCentral reservation. A PVE 9.2.11 target returned `500` for a missing QEMU config to a broad reader and `403` to a pool-scoped token; LabCentral confirms either ambiguous status with the exact typed `nextid` check and otherwise fails closed. Retrieved 2026-09-04. |
| Proxmox QEMU VNC proxy implementation | [Proxmox `qemu-server` QEMU API source](https://github.com/proxmox/qemu-server/blob/master/src/PVE/API2/Qemu.pm) | `POST /nodes/{node}/qemu/{vmid}/vncproxy` requires `VM.Console`, creates a ten-second accept-window TLS proxy, and returns a port, the cluster CA, and an explicit VNC password for non-serial displays. The TLS listener may use the configured `pveproxy-ssl` certificate instead, so LabCentral verifies with system roots plus target-configured and returned CA material. LabCentral calls it only after exact owned-resource checks and has the access process connect immediately. Retrieved 2026-09-04. |
| noVNC library API | [noVNC API documentation](https://github.com/novnc/noVNC/blob/master/docs/API.md) | The `RFB` constructor accepts an already-open WebSocket channel, allowing LabCentral to exchange its one-time authorization token before handing the authenticated socket to noVNC. v1.7.0 is pinned. Retrieved 2026-09-04. |
| Go WebSocket transport | [`coder/websocket` releases](https://github.com/coder/websocket/releases) | v1.8.15 is the current pinned server transport; it provides bounded reads, origin controls, and a stream-compatible connection used only after token exchange. Retrieved 2026-09-04. |
| Proxmox QEMU clone and Cloud-Init | [Proxmox QEMU/KVM documentation](https://pve.proxmox.com/pve-docs/chapter-qm.html) | Full clones are independent of the source template; Cloud-Init network/SSH settings are configured before first boot, and custom snippets require configured snippets storage. The first mutation slice therefore uses full clones and only typed native IP settings. |
| Proxmox storage variance | [Proxmox `pvesm` storage documentation](https://pve.proxmox.com/pve-docs/pvesm.1.html) | Snapshot/clone support depends on storage backend and format, so target discovery is mandatory. |
| Proxmox VE 9 qualification installer | [Proxmox VE 9.2 ISO download](https://www.proxmox.com/en/downloads/proxmox-virtual-environment/iso/proxmox-ve-9-2-iso-installer) and [official ISO verification instructions](https://enterprise.proxmox.com/iso/) | The disposable nested target uses x86-64 ISO 9.2-1, pinned to published SHA-256 `4e88fe416df9b527624a175f24c9aa07c714d3332afb1ee3dbf3879573ef2c6c` and a detached signature from Trixie release key fingerprint `24B30F06ECC1836A4E5EFECBA7BCD1420BFE778E`. |
| Debian qualification-runner image | [Debian Cloud Images, Trixie build 20260831-2587](https://cloud.debian.org/images/cloud/trixie/20260831-2587/) | The disposable runner and future provider test nodes use the official genericcloud amd64 qcow2 image pinned to its published SHA-512; this image choice is test-host infrastructure, not provider capability evidence. |
| KubeVirt VM networking | [KubeVirt interfaces and networks](https://kubevirt.io/user-guide/network/interfaces_and_networks/) | VM networks/interfaces pair explicitly; Multus enables secondary networks; binding/CNI constraints affect MAC, L2, ports, and migration. |
| KubeVirt snapshots | [KubeVirt Snapshot/Restore API](https://kubevirt.io/user-guide/storage/snapshot_restore_api/) | VM snapshots depend on KubeVirt APIs, CSI VolumeSnapshot support/classes, storage, and guest-agent consistency. |
| Kubernetes runtime isolation | [Kubernetes RuntimeClass](https://kubernetes.io/docs/concepts/containers/runtime-class/) | RuntimeClass selects an operator-installed runtime and can represent a hardware-virtualized sandbox with scheduling/overhead constraints. |
| Kubernetes policy limits | [Kubernetes NetworkPolicy](https://kubernetes.io/docs/concepts/services-networking/network-policies/) | L3/L4 policy requires a network plugin that actually enforces NetworkPolicy; API presence alone is insufficient evidence. |
| Kubernetes production workload controls | [StatefulSets](https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/), [Pod disruptions](https://kubernetes.io/docs/concepts/workloads/pods/disruptions/), and [Pod Security Standards](https://kubernetes.io/docs/concepts/security/pod-security-standards/) | StatefulSets provide stable replica identity and rolling-update controls; a PDB constrains only eviction-aware voluntary disruptions and does not govern a StatefulSet rollout; the Restricted standard requires explicit non-root, no-escalation, seccomp, capability, and volume controls. Retrieved 2026-09-04. |
| Kubernetes secret and shared-volume boundaries | [Secret good practices](https://kubernetes.io/docs/concepts/security/secrets-good-practices/) and [Persistent Volumes](https://kubernetes.io/docs/concepts/storage/persistent-volumes/) | Kubernetes Secrets require explicit encryption-at-rest/RBAC policy and should be mounted only into containers that need them. RWX is a scheduling/mount capability rather than proof of write protection or the coherent `flock`, atomic rename, and `fsync` behavior LabCentral requires. Retrieved 2026-09-04. |
| Fly private networking | [Fly private networking](https://fly.io/docs/networking/private-networking/) and [custom private networks](https://fly.io/docs/networking/custom-private-networks/) | Fly 6PN is private IPv6/WireGuard L3 networking; custom 6PNs isolate groups but do not provide arbitrary L2 lab topology. |
| Fly workload model | [Fly Machines API](https://fly.io/docs/machines/api/machines-resource/) | Machines are configured from container images with Machine/service/volume semantics; LabCentral maps only logical containers. |
| Docker portable API | [Docker Engine API](https://docs.docker.com/reference/api/engine/), [API version history](https://docs.docker.com/reference/api/engine/version-history/), and [bridge driver](https://docs.docker.com/engine/network/drivers/bridge/) | Baseline uses versioned REST Engine container/image/volume/network APIs; the thin provider requires API 1.44 or newer and uses user-defined bridges. |
| Docker port publication | [Docker port publishing and mapping](https://docs.docker.com/engine/network/port-publishing/) | Published ports expose a container through host addresses; the laptop provider requests `127.0.0.1` explicitly and does not infer egress denial from that ingress binding. |
| Docker Desktop differences | [Docker Desktop networking](https://docs.docker.com/desktop/features/networking/) | Desktop routes Engine/container networking through a lightweight VM/backend, so Linux host bridge assumptions are not portable. |
| Docker multi-stage images | [Docker multi-stage builds](https://docs.docker.com/build/building/multi-stage/) | Multiple `FROM` stages allow build artifacts to be copied into a minimal final stage without carrying build toolchains into the runtime image. |
| Docker host networking | [Docker host network driver](https://docs.docker.com/engine/network/drivers/host/) | Host mode shares the host network namespace on Linux; Docker Desktop support requires 4.34 or newer and explicit enablement. |
| Docker Compose networking | [Docker Compose networking](https://docs.docker.com/compose/how-tos/networking/) | Compose normally creates an application network; the laptop deployment deliberately overrides this with host mode for loopback semantics. |
| CycloneDX SBOM format | [CycloneDX specification](https://github.com/CycloneDX/specification) | CycloneDX publishes versioned JSON schemas for component inventories; native releases emit the broadly supported 1.6 JSON format. |
| OSV dependency scanning | [OSV-Scanner supported lockfiles](https://google.github.io/osv-scanner/supported-languages-and-lockfiles/) and [usage](https://google.github.io/osv-scanner/usage/) | OSV-Scanner v2 supports the text `bun.lock` format and fails its source-scan command when the exact dependency graph contains a known vulnerability. LabCentral pins v2.5.0, scans the committed Bun lockfile, and permits only one bounded retry for service transport failure. Retrieved 2026-09-04. |
| GitHub Actions supply-chain controls | [Secure use reference](https://docs.github.com/en/actions/reference/security/secure-use) and [deployment environments](https://docs.github.com/en/actions/reference/workflows-and-actions/deployments-and-environments) | Full commit SHAs are the immutable third-party action reference. Protected environments can require reviewers, prevent self-review, restrict deployment tags, and disallow bypass; these repository settings protect the publisher's OIDC authority. Retrieved 2026-09-04. |
| GitHub artifact attestations | [Using artifact attestations](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations) | GitHub Actions can create signed SLSA provenance for files and OCI image digests and verify it with repository, workflow, source-ref, and runner constraints. LabCentral uses attestations in addition to standalone Cosign signatures. Retrieved 2026-09-04. |
| Sigstore blob signing and identity verification | [Signing blobs](https://docs.sigstore.dev/cosign/signing/signing_with_blobs/), [Cosign verification](https://docs.sigstore.dev/cosign/verifying/verify/), [Fulcio GitHub identity configuration](https://github.com/sigstore/fulcio/blob/main/config/identity/config.yaml), and [Fulcio OIDC identities](https://github.com/sigstore/fulcio/blob/main/docs/oidc.md) | A Cosign bundle carries the keyless signature, certificate, and transparency evidence. GitHub's Fulcio subject binds the exact workflow reference, and verification must constrain both that identity and `https://token.actions.githubusercontent.com`. Retrieved 2026-09-04. |
| Cosign legacy-bundle security floor | [Cosign advisory GHSA-fx35-mq7g-6g98](https://github.com/sigstore/cosign/security/advisories/GHSA-fx35-mq7g-6g98) | Older vulnerable Cosign versions could skip identity checks for some legacy public-key bundles. Publisher and operator guidance therefore require the pinned fixed Cosign 3.0.6 line or newer while still enforcing exact certificate identity. Retrieved 2026-09-04. |
| AWS VPC isolation | [AWS VPC infrastructure security](https://docs.aws.amazon.com/vpc/latest/userguide/infrastructure-security.html) | VPC/subnets, routes, security groups, and NACLs provide L3 controls; private subnets are recommended for non-public instances. |
| AWS multiple NICs | [EC2 elastic network interfaces](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/using-eni.html) | ENIs attach within an Availability Zone and maximum interfaces varies by instance type. |
| AWS snapshots | [Amazon EBS snapshots](https://docs.aws.amazon.com/ebs/latest/userguide/ebs-snapshots.html) | EBS provides point-in-time volume snapshots/restores, not an automatic universal whole-lab snapshot. |
| CEL safety/model | [CEL overview](https://cel.dev/overview/cel-overview) | CEL is designed for safe embedded evaluation with declared environments and parse/check/evaluate phases; LabCentral still adds cost/expansion limits. |
| Huma contract generation | [Huma OpenAPI generation](https://huma.rocks/features/openapi-generation/) and [Huma releases](https://github.com/danielgtaylor/huma/releases) | Huma generates OpenAPI 3.1 from registered operation inputs/outputs; v2.39.1 is pinned for the laptop API. |
| gRPC Go transport | [gRPC-Go releases](https://github.com/grpc/grpc-go/releases) | v1.83.1 is pinned for local plugin transport and includes the current upstream HTTP/2 resource-exhaustion mitigation. |
| Static web build | [SvelteKit adapter-static changelog](https://github.com/sveltejs/kit/blob/version-3/packages/adapter-static/CHANGELOG.md), [daisyUI v5](https://daisyui.com/docs/v5/), and [Bun lockfile guidance](https://bun.sh/docs/pm/lockfile) | The laptop UI uses a pinned SvelteKit 2 static adapter, DaisyUI 5 with Tailwind CSS 4, and a committed text lockfile for reproducible Bun installs. |
| Go plugin framework comparison | [HashiCorp go-plugin](https://github.com/hashicorp/go-plugin) and [protocol internals](https://github.com/hashicorp/go-plugin/blob/main/docs/internals.md) | Established framework provides subprocess RPC/gRPC, handshake, health/TLS/reattach concepts; LabCentral selects a thinner shared local/remote protocol. |
| Protobuf evolution | [Protocol Buffer best practices](https://protobuf.dev/best-practices/dos-donts/) and [language guide](https://protobuf.dev/programming-guides/editions/) | Field numbers/names must not be reused; additive evolution and reserved fields underpin plugin compatibility rules. |
| Architecture audit: plugin service health | [gRPC health checking](https://grpc.io/docs/guides/health-checking/) | Standard health supports individual service status and client avoidance of unhealthy services. This is a transport primitive, not a replacement for plugin identity, restart, or durable invocation recovery. Retrieved 2026-09-04. |
| Protobuf lint/breaking configuration | [Buf v2 `buf.yaml`](https://buf.build/docs/configuration/v2/buf-yaml/), [lint usage](https://buf.build/docs/lint/usage/), and [breaking checks](https://buf.build/docs/breaking/) | `STANDARD` lint and `FILE` breaking rules provide the checked v1 protocol baseline; breaking compares current schema with a past descriptor/Git input. |
| Canonical JSON | [RFC 8785 JSON Canonicalization Scheme](https://www.rfc-editor.org/rfc/rfc8785.html) | JCS uses I-JSON/ECMAScript primitive serialization and deterministic property sorting for stable canonical bytes. |
| Go toolchain | [Go downloads](https://go.dev/dl/) | Go 1.27.1 was the current stable toolchain used to verify the implementation. |
| YAML parser | [`go.yaml.in/yaml/v4` package documentation](https://pkg.go.dev/go.yaml.in/yaml/v4) | The maintained v4 API exposes the node model and decoder used by the bounded, duplicate-aware parser. |
| CEL implementation | [`cel-expr/cel-go` releases](https://github.com/cel-expr/cel-go/releases) | CEL-Go v0.30.0 supplies parse/check/evaluate APIs and cost controls; LabCentral additionally enforces AST, collection, and expansion limits. |
| JSON Schema validator | [`santhosh-tekuri/jsonschema` releases](https://github.com/santhosh-tekuri/jsonschema/releases) | The v6 implementation supports the Draft 2020-12 authoring schema used by renderer conformance tests. |
| PostgreSQL queue claims and advisory locks | [PostgreSQL 18 `SELECT` locking clause](https://www.postgresql.org/docs/18/sql-select.html#SQL-FOR-UPDATE-SHARE) and [advisory locks](https://www.postgresql.org/docs/18/explicit-locking.html#ADVISORY-LOCKS) | `SKIP LOCKED` is suitable for avoiding contention among consumers of a queue-like table; transaction- and session-level advisory locks coordinate idempotency and migrations without representing provider ownership. |
| PostgreSQL logical backup/restore | [PostgreSQL 18 `pg_dump`](https://www.postgresql.org/docs/18/app-pgdump.html) and [`pg_restore`](https://www.postgresql.org/docs/18/app-pgrestore.html) | Custom-format dumps support selective restore through `pg_restore`; LabCentral rehearses an isolated schema with exit-on-error and post-restore migration/data checks. Retrieved 2026-09-03. |
| age encrypted-file implementation and format | [`filippo.io/age` package documentation](https://pkg.go.dev/filippo.io/age) and [age v1 file format](https://age-encryption.org/v1) | The pinned Go implementation provides native recipient/identity parsing, authenticated streaming file encryption, and hybrid recipient generation used by the recovery utility; the versioned file format is independently specified. Retrieved 2026-09-04. |
| SQLite transaction semantics | [SQLite transaction documentation](https://www.sqlite.org/lang_transaction.html) | SQLite permits only one simultaneous writer; the local adapter deliberately bounds the pool to one connection and uses atomic write statements. |
| PostgreSQL Go driver | [`pgx` changelog](https://github.com/jackc/pgx/blob/master/CHANGELOG.md) | pgx v5.10.0 is the pinned `database/sql` PostgreSQL driver used by the conformance adapter. |
| SQLite Go driver | [`modernc.org/sqlite` changelog](https://gitlab.com/cznic/sqlite/-/blob/master/CHANGELOG.md) | v1.57.0 provides the pure-Go SQLite driver and defensive DSN mode used by the conformance tests. |
| OpenTelemetry Go and OTLP exporters | [OpenTelemetry Go exporters](https://opentelemetry.io/docs/languages/go/exporters/) and [OTLP exporter configuration](https://opentelemetry.io/docs/specs/otel/protocol/exporter/) | Trace and metric SDKs are stable; production applications should send OTLP to a Collector. OTLP/HTTP uses per-signal endpoints and supports TLS/mTLS. LabCentral pins the Go SDK and OTLP/HTTP trace exporter at v1.46.0, requires remote TLS, and permits plaintext only to a loopback Collector. Retrieved 2026-09-04. |
| Prometheus instrumentation | [Prometheus client-library guidance](https://prometheus.io/docs/instrumenting/writing_clientlibs/) | Metrics and collectors must be thread-safe, use documented stable names, and include tests for exposition. LabCentral uses a private registry, bounded labels, and dedicated exposition tests with `client_golang` v1.24.1. Retrieved 2026-09-04. |

## Architecture simplification references (retrieved 2026-09-04)

- [Huma OpenAPI generation](https://huma.rocks/features/openapi-generation/) — the registered API exposes its OpenAPI document for offline export; product route registration remains authoritative.
- [openapi-typescript introduction](https://openapi-ts.dev/introduction) — OpenAPI 3.x schema-to-TypeScript generation. This repository pins generator 7.13.0 and checks generated DTO drift in CI.

## Native deployment hardening references (retrieved 2026-09-05)

- [systemd 257 execution settings](https://github.com/systemd/systemd/blob/v257/man/systemd.exec.xml) — `ProtectHome=tmpfs` permits explicit bind mounts beneath an otherwise hidden home directory; systemd manages runtime-directory ownership and modes before applying the service sandbox. Checked against the installed systemd 257 manual and actual service namespaces.
- [nftables reference](https://www.netfilter.org/projects/nftables/manpage.html) — `--check` validates without applying rules; `destroy table` accepts an absent table. The the private testbed profile replaces only its named table and tests UID/port filtering in a disposable network namespace.
- [Tailscale HTTPS configuration](https://tailscale.com/docs/how-to/set-up-https-certificates) — tailnet HTTPS certificates require an admin-console setting. File-based `tailscale cert` installations require operator-managed renewal; merely obtaining a certificate does not install recurring renewal.

## Browser startup reference (retrieved 2026-09-06)

- [noVNC 1.7.0 browser capability detection](https://github.com/novnc/noVNC/blob/v1.7.0/core/util/browser.js) — module evaluation awaits video-decoder detection. The optional console library therefore loads only inside the console flow, after the main application has rendered; browser regression tests include detection that never completes.

## Private classroom testbed references (retrieved 2026-09-07)

| Subject | Primary source | Decision supported |
|---|---|---|
| OIDC provider setup | [Official installation](https://pocket-id.org/docs/setup/installation) | Standalone binary installation is supported; passkeys require a secure HTTPS context. |
| OIDC provider configuration | [Environment variables](https://pocket-id.org/docs/configuration/environment-variables) | APP_URL, loopback HOST/PORT, encryption-key file, proxy trust and static setup API key are supported; retain local state and disable analytics for this test instance. |
| OIDC provider pinned binary | [v2.14.0 release](https://github.com/pocket-id/pocket-id/releases/tag/v2.14.0) | Official AMD64 asset and release-provided SHA-256 pin used for installation. |
| OIDC provider client/user API | [Pinned OIDC controller](https://github.com/pocket-id/pocket-id/blob/v2.14.0/backend/internal/controller/oidc_controller.go), [user DTO](https://github.com/pocket-id/pocket-id/blob/v2.14.0/backend/internal/dto/user_dto.go) | Exact callback, confidential PKCE client, separate client-secret endpoint and explicit user IDs. Live setup also required a test email address. |
| Tailscale private HTTPS | [Tailscale Serve](https://tailscale.com/docs/features/tailscale-serve), [official stable packages](https://pkgs.tailscale.com/stable/) | Serve proxies loopback services to tailnet clients using HTTPS. Debian trixie packages installed; account enrollment and tailnet HTTPS enablement are external prerequisites. No Funnel. |
| Proxmox guest-agent transfer | [QEMU agent API implementation](https://github.com/proxmox/qemu-server/blob/master/src/PVE/API2/Qemu/Agent.pm) | File writes accept at most 60 KiB content; bounded independent chunks and final archive verification allow installation without opening an ingress listener. |


Classroom readiness sources retrieved 2026-09-07:

| Topic | Primary source | Decision supported |
|---|---|---|
| Read-only image measurement | [Proxmox QEMU drive schema](https://github.com/proxmox/qemu-server/blob/master/src/PVE/QemuServer/Drive.pm) | SCSI `ro=1` allows measuring an unmounted logical disk without writing the template. Guest read-only status and source configuration were independently checked. |
| Isolated bridge allocation and apply | [Proxmox network API](https://github.com/proxmox/pve-manager/blob/master/PVE/API2/Network.pm) | `bridge_vids` restricts allowed VLANs. GET exposes pending changes; PUT applies interface changes through ifupdown2. A separate vmbr4 avoids editing the pilot bridge; pending diff and existing interfaces were checked. |

Console transport sources retrieved 2026-09-07:

| Topic | Primary source | Decision supported |
|---|---|---|
| Proxmox temporary VNC ports | [PVE Tools](https://github.com/proxmox/pve-common/blob/master/src/PVE/Tools.pm), [QEMU API](https://github.com/proxmox/qemu-server/blob/master/src/PVE/API2/Qemu.pm) | VNC port allocation is 5900–5999. WebSocket access checks both VM.Console and its VNC ticket. Live the private testbed 9.2.11 returned plain RFB on the temporary socket, unlike the newer upstream TLS-first path; transport must be verified on the actual target. |
| Tailscale peer encryption | [Node keys](https://tailscale.com/docs/concepts/node-keys), [Tailscale identity](https://tailscale.com/docs/concepts/tailscale-identity) | Peer node public keys authenticate encrypted WireGuard connections. The explicit access profile pins the peer and binds the socket to the local Tailscale interface. |
| Local peer lookup | [LocalAPI client](https://github.com/tailscale/tailscale/blob/main/client/local/local.go) | Whois uses the local privileged daemon over its Unix socket. The live access UID can read peer identity without Tailscale operator authority. |
| Serve mount paths | [Serve implementation](https://github.com/tailscale/tailscale/blob/main/ipn/ipnlocal/serve.go) | Serve strips a mount prefix and joins the configured backend path; the console mount forwards to the same path on the loopback backend. |

| Console static routing | [SvelteKit adapter-static](https://svelte.dev/docs/kit/adapter-static), [path configuration](https://svelte.dev/docs/kit/configuration#paths) | A generated fallback supports non-prerendered dynamic routes; root-relative assets preserve nested-route boot. Retrieved 2026-09-07. |
| OIDC provider enrollment links | [Pinned one-time access handler](https://github.com/pocket-id/pocket-id/blob/v2.14.0/backend/internal/onetimeaccess/handler.go), [official login-code dialog](https://github.com/pocket-id/pocket-id/blob/v2.14.0/frontend/src/lib/components/one-time-link-modal.svelte) | Admin-created single-use links support explicit TTLs. The 25 handoff links use 48 hours and are never consumed by automation or sent automatically. Retrieved 2026-09-07. |

## Classroom operations update — retrieved 2026-09-07

- [noVNC RFB API](https://novnc.com/noVNC/docs/API.html): existing scaleViewport,
  resizeSession, viewOnly, sendKey and sendCtrlAltDel operations; remote resize
  is a request to the server, while local fitting is a browser operation.
- [Proxmox VM status return schema discussion](https://lore.proxmox.com/pve-devel/2b7a5f73-66f0-40e5-a65c-cc015ccb1a51@proxmox.com/t/): cpu, cpus, mem and maxmem
  status fields. VM.Audit reads are measurements rather than quota reservations;
  live target comparison remains required for this provider update.


## TanStack request caching (retrieved 2026-09-08)

- [TanStack QueryClient](https://tanstack.com/query/v5/docs/reference/QueryClient) — fetchQuery freshness, query cancellation, invalidation and cache clearing; verified against installed `@tanstack/query-core` 5.102.8 source and regression tests.
- [Svelte QueryClientProvider](https://github.com/TanStack/query/blob/main/packages/svelte-query/src/QueryClientProvider.svelte) — shared client context and mount/unmount lifecycle; verified against installed `@tanstack/svelte-query` 6.1.48 source. No server-side UI runtime or cache persistence is added.
- [Query cancellation](https://tanstack.com/query/v5/docs/framework/react/guides/query-cancellation), retrieved 2026-09-21 and checked against installed query-core 5.102.8: QueryClient owns the query function's AbortSignal. Component-local cancellation must not cancel a shared query needed by other consumers.


Navigation continuity reference (retrieved 2026-09-08):

- [SvelteKit state management](https://svelte.dev/docs/kit/state-management) and [navigation hooks](https://svelte.dev/docs/kit/$app-navigation) — shared layouts preserve component state; afterNavigate handles client route changes. Confirmed against the installed SvelteKit 2.70.3 types and the built three-engine navigation fixture.

## Operations charts (retrieved 2026-09-15)

- [ApexCharts skill and lifecycle reference](https://github.com/apexcharts/apexcharts-skill/blob/main/SKILL.md): chart-specific series shapes, render/update/destroy lifecycle, missing values and per-type renderer imports. TASK-UI-005 uses a lazy bar renderer with keyboard support; this does not change provider measurement semantics.

## Classroom credential renewal (retrieved 2026-09-15)

- [Proxmox token management synopsis](https://github.com/proxmox/pve-docs/blob/master/generated/pveum.1-synopsis.adoc): token modification supports a Unix-epoch expiry and separate privileges. TASK-TESTBED-004 updates only the existing token's expiry; effective permissions are checked against the six accepted target scopes. Live API evidence confirms expired credentials returned HTTP 401 and renewed credentials permit authenticated readiness reads.

## Container VM runtime (retrieved 2026-09-15)

- [Proxmox QEMU Agent API source](https://github.com/proxmox/qemu-server/blob/master/src/PVE/API2/Qemu/Agent.pm): exec accepts a program/argument array, exec-status returns captured output; both require VM.GuestAgent.Unrestricted. ADR-0022 restricts its use to the fixed runtime entrypoint and exact pool.
- [Podman run reference](https://docs.podman.io/en/latest/markdown/podman-run.1.html): offline pull policy, rootless execution, host network namespace, capability removal and no-new-privileges options inform the dedicated-VM runtime.

## Extension browser isolation and device authorization (retrieved 2026-09-17)

- [MDN iframe](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/iframe): opaque-origin sandbox with scripts only, separate asset origin, and response CSP constrain browser contributions (ADR-0023).
- [MDN postMessage](https://developer.mozilla.org/en-US/docs/Web/API/Window/postMessage): validate sender window and message shape; use a fresh nonce and a transferred channel for the opaque-origin frame (ADR-0023).
- [RFC 8628](https://www.rfc-editor.org/rfc/rfc8628.html): browser approval, device/user codes, polling interval and slow-down responses inform gateway delegation (ADR-0024). LabCentral further restricts grants to current user authority and a 15-minute lifetime without refresh tokens.

## Portable templates and collaboration sources

Retrieved 2026-09-18. These sources inform ADR-0027–ADR-0029; live target capabilities still require discovery and qualification.

- [Packer QEMU builder](https://developer.hashicorp.com/packer/integrations/hashicorp/qemu/latest/components/builder/qemu) and [ansible-local](https://developer.hashicorp.com/packer/integrations/hashicorp/ansible/latest/components/provisioner/ansible-local): QEMU image construction and running the Ansible controller inside the build guest.
- [BuildKit OCI exporter](https://docs.docker.com/build/exporters/oci-docker/): OCI archives are internal build outputs, not portable template contents.
- [GitHub Git trees API](https://docs.github.com/en/rest/git/trees) and [commits API](https://docs.github.com/en/rest/commits/commits): resolve one commit before immutable tree/blob retrieval; reject truncated recursive trees (GitHub documents 100,000 entries/7 MB). Contents read permission suffices for configured private repositories. The source plugin pins REST version 2026-03-10.
- [GitHub metadata API](https://docs.github.com/en/rest/meta/meta): `/meta` supplies current service IP ranges, including `api`; documentation examples are not an operational allowlist. Native source-service egress uses the retrieved API ranges for HTTPS plus the installation's DNS resolver. Retrieved 2026-09-20; actual ranges remain in private operator configuration.
- [GitHub organization base permissions](https://docs.github.com/en/organizations/managing-user-access-to-your-organizations-repositories/managing-repository-roles/setting-base-permissions-for-an-organization): base access applies to all organization repositories; higher repository grants add access, while lower roles do not remove inherited access. Private visibility alone does not make build source instructor-only. Retrieved 2026-09-20.
- [Proxmox storage upload implementation](https://github.com/proxmox/pve-storage/blob/master/src/PVE/API2/Storage/Status.pm): import content requires matching storage support and validates uploaded image format; upstream support does not establish installed-target support.
- [noVNC API](https://novnc.com/noVNC/docs/API.html) and [RFB RFC 6143](https://datatracker.ietf.org/doc/rfc6143/): shared initialization and client input messages. Browser view-only settings are not an authorization boundary.

| Subject | Primary source | Supported claim |
|---|---|---|
| Disposable builder boundary | [QEMU system invocation](https://www.qemu.org/docs/master/system/invocation.html) | Restricted user networking isolates the guest except explicitly configured forwards. Each build exposes one fixed guestfwd proxy and a private virtio-serial channel; no shared filesystem or host management socket enters the guest. Retrieved 2026-09-18. |
| Builder watchdog | [systemd resource control](https://github.com/systemd/systemd/blob/main/man/systemd.resource-control.xml), [service execution](https://github.com/systemd/systemd/blob/main/man/systemd.exec.xml), [service lifecycle](https://github.com/systemd/systemd/blob/main/man/systemd.service.xml) | Transient units apply CPU/memory/task/file limits and an independent runtime deadline with control-group termination. Worker reconnection does not reset the deadline. Retrieved 2026-09-18. |
| Packer image construction | [QEMU builder](https://developer.hashicorp.com/packer/integrations/hashicorp/qemu/latest/components/builder/qemu), [ansible-local](https://developer.hashicorp.com/packer/integrations/hashicorp/ansible/latest/components/provisioner/ansible-local), [Packer releases](https://releases.hashicorp.com/packer/1.14.3/) | Generated QEMU image builds support an existing disk, explicit SSH forwarding, and local Ansible provisioning. Reviewed tool archives and the offline Ansible wheel closure are pinned in `tools/builders/toolchain.lock.json`; the installed guest image binds distribution/tool binaries by SHA-256. Retrieved 2026-09-18. |
| BuildKit artifact and dependency inputs | [BuildKit v0.33.0](https://github.com/moby/buildkit/releases/tag/v0.33.0), [buildctl reference](https://github.com/moby/buildkit/blob/v0.33.0/docs/reference/buildctl.md), [OCI exporter](https://docs.docker.com/build/exporters/oci-docker/) | OCI output remains an internal build artifact. Named OCI-layout contexts resolve dependency manifests without uploading them to a registry; the source package contains only recipes and source files. Retrieved 2026-09-18. |
| BuildKit source enforcement | [Source policy engine](https://github.com/moby/buildkit/blob/v0.33.0/sourcepolicy/engine.go), [policy contract](https://github.com/moby/buildkit/blob/v0.33.0/sourcepolicy/pb/policy.proto) | Last-matching allow/deny rules permit local/dependency sources and declared SHA-256 image identities, rejecting unpinned inherited image sources as well as direct Dockerfile references. Retrieved 2026-09-18. |
| Docker Hub egress | [Docker Hub release notes](https://docs.docker.com/docker-hub/release-notes/), [official allowlist](https://docs.docker.com/desktop/setup/allow-list/) | Registry pulls also use authentication and CDN hosts, including `production.cloudfront.docker.com`. A configured registry binding must explicitly allow its required public endpoints; redirects do not expand the proxy policy. Retrieved 2026-09-18. |
| Image sanitization | [virt-sysprep operations](https://libguestfs.org/virt-sysprep.1.html) | Custom deletion requires the `customize` operation when operations are explicitly selected. Builders verify removal of cloud-init/build state and SSH keys inside the disposable guest before exporting qcow2. Retrieved 2026-09-18. |

## Qualified image import references (retrieved 2026-09-18)

- [Proxmox storage upload implementation](https://github.com/proxmox/pve-storage/blob/master/src/PVE/API2/Storage/Status.pm) — import uploads support checksum and untrusted-image validation; upload tasks use `imgcopy` with an empty object ID.
- [Proxmox QEMU API implementation](https://github.com/proxmox/qemu-server/blob/master/src/PVE/API2/Qemu.pm) — typed VM creation accepts storage volume references with `import-from`; creation and template conversion remain provider mutations.
- [Proxmox task API](https://github.com/proxmox/pve-manager/blob/master/PVE/API2/Tasks.pm) — bounded listings support active/archive selection and user/type/VM filters; task logs provide upload ownership evidence after response loss.
- [QEMU qcow2 format](https://www.qemu.org/docs/master/interop/qcow2.html) — header fields identify backing files, virtual size, encryption, snapshots and external-data features; version 3 permits a 112-byte header with zero optional fields.

## OCI runtime packaging (retrieved 2026-09-18)

- [OCI image layout](https://github.com/opencontainers/image-spec/blob/main/image-layout.md), [image manifest](https://github.com/opencontainers/image-spec/blob/main/manifest.md), and [configuration](https://github.com/opencontainers/image-spec/blob/main/config.md): indexes address manifests; manifests address configuration and layer blobs. Configuration identity includes uncompressed layer identities and is distinct from the archive and manifest digests.
- [Podman load](https://docs.podman.io/en/latest/markdown/podman-load.1.html): OCI archives can be loaded into local container storage. LabCentral performs that operation as the rootless runtime user inside a disposable VM, then starts the verified local image ID using the existing offline pull policy.

- [RFB protocol extensions](https://github.com/rfbproto/rfbproto/blob/master/rfbproto.rst), retrieved 2026-09-18: client message framing, extended pointer and clipboard, Fence, XVP, desktop resize and QEMU extended key messages inform the bounded noVNC input filter.

- [Proxmox streaming upload parser](https://github.com/proxmox/pve-http-server/blob/master/src/PVE/APIServer/AnyEvent.pm), retrieved 2026-09-19: multipart upload fields must precede the file in parser order: content, checksum-algorithm, checksum, filename. A standards-compliant multipart encoder alone does not establish compatibility with this streaming parser.

- [Proxmox storage volume deletion](https://github.com/proxmox/pve-storage/blob/master/src/PVE/API2/Storage/Content.pm), retrieved 2026-09-19: deleting import staging volumes requires `Datastore.Allocate` on the storage. Upload permission alone cannot complete import cleanup.

- [Pinned Debian 13 genericcloud package manifest](https://cloud.debian.org/images/cloud/trixie/20260831-2587/debian-13-genericcloud-amd64-20260831-2587.json), retrieved 2026-09-19: the example's pinned base includes cloud-init but omits `qemu-guest-agent`; the reusable Ansible recipe installs the agent required by Proxmox guest readiness.

- [systemd service restart semantics](https://github.com/systemd/systemd/blob/main/man/systemd.service.xml), retrieved 2026-09-19: `Restart=always` covers clean process exits as well as failures; explicit service stops do not trigger restart. The noVNC service uses this policy to recover after a heartbeat-triggered clean exit.

## Qualified lab egress and additional build guest compatibility

Retrieved 2026-09-20:

- [Debian cloud image variants](https://wiki.debian.org/Cloud): `generic` uses Debian's standard kernel; `genericcloud` uses its reduced cloud kernel. Graphical desktop recipes select the generic image and verify its published checksum before pinning it.
- [cloud-init hosts-file management](https://docs.cloud-init.io/en/latest/reference/modules.html#update-etc-hosts): when enabled, the update module regenerates `/etc/hosts` from its distribution template; persistent branch aliases belong in that template.
- [NetworkManager DNS configuration](https://networkmanager.dev/docs/api/latest/NetworkManager.conf.html): `dns=none` leaves resolver-file management to the configured branch resolver service.
- [Linux network namespaces](https://man7.org/linux/man-pages/man7/network_namespaces.7.html): namespace-local interfaces, routing and firewall state support overlapping guest CIDRs behind separate trusted routing contexts (ADR-0030); actual isolation remains a live qualification requirement.
- [AlmaLinux generic cloud images](https://wiki.almalinux.org/cloud/Generic-cloud) and [AlmaLinux 10 release notes](https://wiki.almalinux.org/release-notes/10.0.html): NoCloud image selection, default Python 3.12, x86-64-v2 variant and signing identity. The selected ext4 image checksum signature and bytes are verified in protected build records.
- [QEMU CPU models](https://www.qemu.org/docs/master/system/qemu-cpu-models.html): the disposable inner TCG builder selects `max` for the additional AlmaLinux CPU baseline; deployment CPU compatibility is checked separately.
- PyPI release metadata for [CFFI 2.1.1](https://pypi.org/pypi/cffi/2.1.1/json), [MarkupSafe 3.0.3](https://pypi.org/pypi/markupsafe/3.0.3/json) and [PyYAML 6.0.3](https://pypi.org/pypi/pyyaml/6.0.3/json): additive CPython 3.12 wheel hashes, verified against downloaded bytes and offline installations alongside the existing Python 3.13 closure.
- [Proxmox CPU configuration](https://raw.githubusercontent.com/proxmox/qemu-server/master/src/PVE/QemuServer/CPUConfig.pm): the REST CPU type default remains `kvm64`; v1.5 imports explicitly bind the operator-selected model when a newer guest baseline requires it. Retrieved 2026-09-20.

## Dynamic filesystem capacity (retrieved 2026-09-21)

- [OpenZFS Linux statvfs implementation](https://github.com/openzfs/zfs/blob/master/module/os/linux/zfs/zfs_vfsops.c): reported total blocks derive from referenced plus available bytes, so total capacity is not universally an immutable storage identity.
- [Proxmox directory storage status](https://github.com/proxmox/pve-storage/blob/master/src/PVE/Storage/Plugin.pm): status returns filesystem total/available/used values. ADR-0032 binds structural facts and explicit minimum capacity instead of exact dynamic totals for the new target version.

- [Proxmox virtual machine disk controllers and I/O threads](https://github.com/proxmox/pve-docs/blob/master/qm.adoc), retrieved 2026-09-21: VirtIO SCSI single supports a dedicated disk I/O thread; separating storage handling from the main event loop is recommended for performance. This informs the control-plane VM configuration, not a change to qualified workload-provider behavior.

### Shared VNC startup (retrieved 2026-09-21)

- [Proxmox `qm` source](https://github.com/proxmox/qemu-server/blob/master/src/PVE/CLI/qm.pm): `vncproxy` sets the VM VNC password and a 30-second authentication deadline before proxying. This supports serializing resource-scoped endpoint discovery through upstream authentication; it does not imply an established console expires after 30 seconds. See ADR-0037.

- [Proxmox datacenter bandwidth schema](https://github.com/proxmox/pve-cluster/blob/master/src/PVE/DataCenterConfig.pm) and [QEMU clone implementation](https://github.com/proxmox/qemu-server/blob/master/src/PVE/API2/Qemu.pm): clone bandwidth is expressed in KiB/s and inherited from storage/datacenter configuration unless explicitly overridden. Retrieved 2026-09-21. Limits must leave enough throughput to complete within the provider task budget; tuning does not replace SSD-backed control-plane storage.

- SQLite WAL checkpoint concurrency and commit latency: https://www.sqlite.org/wal.html (retrieved 2026-09-21). PASSIVE checkpointing can run on a separate connection; FULL continues to sync each committed WAL transaction.
