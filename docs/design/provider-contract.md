# Provider contract

## Required provider behavior

A provider plugin implements:

- `GetPluginInfo`, target configuration schema, `DiscoverTarget`, `GetCapabilities`, and `GetHealth`.
- `ValidateLab` for target inventory/topology/provider overrides without mutation.
- `PlanResource` and lifecycle support reporting with warnings/destructive markers.
- idempotent logical-resource apply/read/destroy; start/stop/pause/resume; snapshots/restore/clone/reset where advertised.
- capacity/placement reporting with timestamp, confidence, and `unknown` support.
- image inventory/import only when advertised.
- safe endpoint discovery, never reusable credentials.
- invocation query, progress, structured errors, ownership evidence, and opaque-state migration.

## Two-stage validation

```mermaid
flowchart LR
  L[Rendered Lab] --> C[Core structure / refs / graph / policy / quota]
  T[Target capability snapshot] --> C
  C -->|required capabilities satisfied| P[Provider inventory / overrides / limits / realizability]
  C -->|missing| E[Structured failure]
  P -->|valid| PL[Plan]
  P -->|invalid| E
```

Required capabilities are typed predicates such as `workload.vm == native`, `network.interface.max >= 3`, `network.semantics includes l2`, `isolation includes virtualMachine`, or `snapshot.modes includes crashConsistent`. Capability evidence includes discovery time, TTL, optional component dependency, constraints, and target configuration fingerprint. Plans pin its digest; stale capability facts trigger revalidation.

## Apply invariant

For each logical resource, plugin behavior is effectively:

```text
lookup recorded provider state and ownership tags
if desired generation already realized: read and return it
if a prior invocation with idempotency key exists: resume/query it
create/update primitives while recording their IDs locally
on failure: compensate primitives created by this attempt
return success OR error + every known primitive + compensation result
```

The core persists the response before unlocking. Plugin-local journals are mandatory when an upstream async task can outlive the RPC or process; they bridge the pre-response crash window and make `GetInvocation` durable, while committed core invocation/provider state remains authoritative. Journals are versioned, checksummed, bounded, and secret-free. A plugin must never adopt a name collision. Adoption, if later supported, is an explicit privileged operation with a resource identity proof.

## Provider allocation claims

Target-global scarce values cannot be assigned safely by hashing and provider plugins do not access the control-plane database. Under ADR-0013, a provider proposes bounded opaque `(namespace, value)` claims and the core enforces uniqueness by target. The core creates the Instance ID before planning, atomically commits the accepted plan and claims, and supplies committed dependency plans/states to later resources. Conflicts cause bounded replan with exclusions. A claim is not provider ownership; externally occupied values still fail closed. Claims remain held for unknown outcomes, leftovers, and orphaned resources and are released only after provider absence is confirmed.

## Ownership metadata

Where tags exist: `labcentral.installation`, `target`, `organization/project`, `instance`, `logicalResource`, `generation`, and `managed=true`. Where tags are limited, store a collision-resistant composite token plus full IDs in provider state. Destroy requires both the recorded immutable provider ID and matching ownership token; mismatch stops with `partial_cleanup` and emits an audit alert.

## Container-via-VM contract

Official Proxmox, AWS, and KubeVirt providers share a `container-vm-runtime` library plus a small versioned in-guest bootstrap protocol. Base image contract: verified image provenance/digest, supported CPU architecture, cloud-init or Ignition, read-only bootstrap component, OCI runtime version range, dedicated unprivileged workload user, log/readiness channel, and no inbound control-plane dependency. The provider attaches networks/volumes, injects a one-time bootstrap document, and observes readiness via provider-safe serial/guest-agent or outbound mutually authenticated status where policy permits.

The shared library creates runtime config, state schemas, lifecycle/reset semantics, and conformance tests; each provider owns VM/network/storage API calls. Snapshot includes VM disks/runtime state according to advertised consistency; OCI image upgrades are recreate by default. Shell access goes through normal Access Sessions. This avoids three divergent contracts without hiding provider VM differences.

## Contract tests

Every provider must pass idempotent replay, stale generation, wrong ownership, crash-after-create, timeout-with-unknown-outcome, partial-create compensation, compensation failure, reverse destroy, provider-state old/new/unknown schema, capability downgrade, cancellation, redaction, and deterministic plan tests. The fake provider offers programmable faults and a virtual clock. The authoritative machine-readable cases are in [`design/contracts/provider-conformance.yaml`](../../design/contracts/provider-conformance.yaml); official-provider suites may add cases but cannot waive these IDs.

`TASK-PROVIDER-001` implements the public harness contract under [`pkg/plugin-sdk-go/contract`](../../pkg/plugin-sdk-go/contract), the reusable 18-case runner under [`pkg/plugin-sdk-go/contracttest`](../../pkg/plugin-sdk-go/contracttest), and a mutex-safe programmable fake under [`internal/adapter/fakeprovider`](../../internal/adapter/fakeprovider). The fake has no credentials or external provider access; it uses a virtual clock and durable restart snapshot to exercise failure windows deterministically. Official provider test adapters implement the same public `Harness` without importing core internals.

## Thin Docker v1 implementation

`TASK-DOCKER-001` implements `PluginRuntime`, `ProviderService`, and the advertised lifecycle subset in `providers/docker`. Discovery negotiates Engine API v1.44 or newer and returns target-specific version/OS/architecture facts. Validation rejects VM resources, unpinned images, unsupported networking, and missing trusted-laptop acknowledgement. Planning produces typed, persisted provider plans; apply/read/destroy use stable invocation/idempotency identities and opaque provider state.

Physical object names and labels are deterministic. Deletion first inspects the immutable Engine ID/name and requires every expected ownership label to match; it never deletes by a name collision alone. Image pulls require a `@sha256:` reference. Published ports are loopback-only, published TCP services must accept a bounded connection before readiness, and container creation applies capability drop, `no-new-privileges`, CPU, and memory settings. Unit tests cover negotiation, pinning, loopback publication/readiness, replay, and ownership refusal; the local control-plane test covers complete Example A apply and reverse destroy through gRPC.

## Read-only Proxmox discovery implementation

`TASK-PROXMOX-001` adds the separately runnable `lab-provider-proxmox` process and only implements runtime health plus `DiscoverTarget`. Strict versioned JSON envelopes carry an HTTPS origin/private CA and an ephemeral target API token. The client disables proxies and redirects, verifies TLS, bounds time/responses/inventory, sends only `GET`, validates provider-returned node identifiers before using them in paths, and redacts configuration, credential, transport, and upstream-body details from gRPC errors.

The deterministic capacity document returns typed version, node, QEMU/template, storage/format, bridge/VLAN-awareness, and partial-issue facts with an inventory digest. Its evidence expires after five minutes. Portable mutation, snapshot, firewall, access, reservation, and placement capabilities remain unsupported; provider-specific discovery features are diagnostic only. The complete contract, operator permissions, and release boundary are in [`proxmox-provider.md`](proxmox-provider.md).

## Proxmox QEMU mutation design gate

`TASK-PROXMOX-002` defines, but does not implement, the first write slice: fixed-node PVE 9.x Linux QEMU full clones, approved storage, core-claimed VMIDs/VLANs, approved VLAN-aware bridges, PVE-native typed IPv4 Cloud-Init networking, and bounded start/stop/pause/resume. The plan, ownership marker, durable invocation journal, UPID polling/recovery, compensation, least-privilege scopes, and T0/T3 cases are closed contracts. Mutation stays unsupported until every prerequisite and target-specific gate in [`proxmox-qemu-mutation.md`](proxmox-qemu-mutation.md) and [`proxmox-qemu-mutation.yaml`](../../design/contracts/proxmox-qemu-mutation.yaml) passes.
