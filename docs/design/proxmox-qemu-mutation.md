# Proxmox QEMU mutation contract

`TASK-PROXMOX-002` fixes the safety and integration contract for the first write-capable Proxmox slice. `TASK-PROXMOX-003` implements the additive protocol-minor fields and core allocation repository prerequisites. `TASK-PROXMOX-004` implements the closed provider types and every T0 fake-API case behind a package-private test gate. `TASK-PROXMOX-005` now has the control-plane T0 target/secret/capability/allocation/dependency path and the signed qualification-evidence boundary, but the production constructor still refuses writes and reports `workload.vm` and every lifecycle capability as `unsupported` until the credentialed T3 gates below pass on the configured target and a separate enablement decision is accepted.

## Supported slice

The initial slice is deliberately narrow:

- Proxmox VE 9.x on Linux/amd64, one configured online destination node, and QEMU guests only.
- One full clone (`full=1`) from an administrator-catalogued Linux Cloud-Init template on that same node.
- One approved image storage/profile and up to 16 ordered VirtIO NICs attached only to administrator-approved, Linux VLAN-aware bridge profiles.
- IPv4 static addressing on isolated logical networks. Each network maps to an existing bridge plus a core-reserved VLAN; the provider creates no bridge, SDN object, subnet, DHCP service, route, firewall, or NAT object.
- Start, graceful stop, pause (`suspend` with `todisk=0`), resume, read, and destroy after realization.
- PVE-native Cloud-Init network settings derived from typed NIC addresses and logical-network CIDRs. The template already contains the Cloud-Init drive and enabled QEMU guest agent.

The slice does not support portable Volume or mount resources, root-disk resizing, external networks, trunks, IPv6, provider-managed IPAM, passwords, guest secrets, author-supplied Cloud-Init/Ignition/files, arbitrary `cicustom` snippets, image import, snapshot/restore/reset, user-requested clone, browser/SSH access, dynamic placement, live migration, HA, or classroom batches. Creating a VM internally from a template does not advertise portable `lifecycle.clone`. Unsupported input returns a stable validation error before any provider mutation; it is never ignored or degraded.

Example B remains gated because its router/guest provisioning and access requirements exceed this subset.

## Typed target and plan schemas

Mutation configuration is the additive closed `proxmox.target-config` v1.1 document. `mutationEnabled` defaults to false. When enabled it contains the release-supported PVE range, installation ID, fixed destination node and optional pool, disjoint VMID ranges, a template catalogue keyed by operator aliases, one or more storage profiles, approved bridge profiles with VLAN pools, hard VM/NIC/resource limits, positive intended-segment plus negative management/cross-instance evidence, exact qualification digests, exact non-root ACL paths, and the provider-journal directory. A template entry pins source node/VMID, expected PVE config digest and operator-approved content digest, cloned disks, architecture, Cloud-Init drive, guest-agent requirement, and forbidden-device policy. A provider override selects administrator aliases only through the closed validation plan; it cannot supply raw bridge names, VLANs, storage IDs, VMIDs, PVE arguments, or weaken isolation.

The plan is `proxmox.qemu-plan` v1.0. It is a closed typed document containing:

- plan schema/version, target/config/capability/inventory/policy/input digests, Instance ID, logical resource ID, and generation;
- proposed allocation claims for the QEMU VMID and each isolated-network VLAN, with namespace, decimal value, and source range;
- source template node/VMID, observed PVE SHA-1 config digest, operator-approved SHA-256 content digest, destination node/pool, storage ID/format, and full-clone flag;
- an ownership marker containing installation, target, instance, logical resource, generation, and a non-secret random 128-bit token;
- computed hardware: `sockets=1`, `cores=ceil(cpuMillis/1000)`, `cpulimit=cpuMillis/1000`, and memory rounded up to MiB, all bounded by target policy;
- cloned disk inventory, ordered network bindings, deterministic MACs, bridge/VLAN claims, typed IPv4 address/gateway settings, and `ciupgrade=0`;
- the finite ordered API step enum and structured warnings.

There is no generic map, raw QEMU command line, shell, hook, template language, or executable string. `args`, `hookscript`, host PCI/USB passthrough, VirtioFS/host paths, raw disks, physical interfaces, arbitrary boot media, and unknown template devices are forbidden.

ADR-0013 makes the core the authority for opaque target-scoped claims. Planning receives the stable Instance ID, already committed dependency plans/states, and bounded excluded claims. The core atomically commits all accepted claims with the plan. Apply receives only those committed claims and rejects a missing, changed, duplicate, or differently scoped value. A logical Network's provider state supplies the exact bridge/VLAN binding consumed by dependent VM plans.

## Capability and preflight gate

`workload.vm=native` and the lifecycle subset may be advertised only when all of these facts are fresh and bound into one capability digest:

1. The observed target is in the configured PVE 9.x range and exposes the required API schemas.
2. `/access/permissions` proves the invocation token's effective privileges at every configured source, destination, pool, storage, bridge/VLAN, node, and task scope.
3. The fixed node is online; template config/digest, Cloud-Init drive, guest agent, storage profile/format, and VLAN-aware bridge still match configuration.
4. Management-path isolation is operator-attested and has passed the target's negative connectivity probe.
5. Core allocation claims and dependency-plan protocol features were negotiated.
6. The durable provider journal is writable, version-compatible, and passes restart/recovery self-tests.
7. The exact PVE release/storage/network combination has passed the credentialed T3 cases in `design/contracts/proxmox-qemu-mutation.yaml`.

Missing or ambiguous evidence leaves the capability unsupported. Discovery alone never unlocks it. Apply rechecks configuration, capability, inventory, template, committed claims, and current provider objects immediately before its first write; a stale digest returns `stale_plan` with zero mutations.

The initial least-privilege role is limited to `VM.Audit`, source `VM.Clone`, destination-pool `VM.Allocate`, `VM.Config.CPU`, `VM.Config.Memory`, `VM.Config.Network`, `VM.Config.Cloudinit`, `VM.Config.Options`, `VM.PowerMgmt`, `VM.GuestAgent.Audit`, target-storage `Datastore.Audit` and `Datastore.AllocateSpace`, configured bridge/VLAN `SDN.Use`, and node/task `Sys.Audit`. Scope assignment matters as much as the privilege list and is verified from effective permissions. Do not grant `VM.GuestAgent.Unrestricted`, `Sys.Modify`, direct datastore deletion, or cluster-wide administrative roles. The write-enabled target uses a dedicated privilege-separated, expiring token; the discovery-only release continues to use an audit-only token.

## Ownership and provider state

The clone request sets a bounded, structured LabCentral ownership marker in the VM description at creation time. A later configuration step also applies a deterministic safe tag. The marker contains full stable IDs and the random ownership token; names and tags alone never establish ownership. Provider state `proxmox.qemu-state` v1.0 records the immutable node/VMID, ownership marker digest/token, logical identity/generation, cloned volume identities, exact network bindings/MACs, last PVE config digest, and current task UPID/phase.

Read and destroy address only the recorded node and VMID, then require the marker identity and token to match. A missing/mismatched marker or an unexpected object is `ownership_mismatch`: do not adopt, update, stop, or delete it; retain claims and emit an audit alert. Display-name equality is irrelevant.

## Apply, tasks, and unknown outcomes

The ordered apply algorithm is:

1. Validate the plan/capability/config digests, committed claims, dependency states, template config, destination absence, and ownership marker; write the invocation journal before any request.
2. Treat `GET /cluster/nextid?vmid=...` only as a time-of-check preflight, never a reservation.
3. Submit `POST /nodes/{source}/qemu/{template}/clone` with the exact claimed `newid`, `full=1`, destination node, approved storage/format/pool, bounded name, and ownership description; persist the returned UPID before polling.
4. Poll `GET /nodes/{node}/tasks/{upid}/status` with bounded exponential backoff until `status=stopped`; only `exitstatus=OK` succeeds.
5. Read the destination config, then update only the planned safe fields using its PVE `digest`: CPU, memory, ordered `netN`/`ipconfigN`, safe tag, `ciupgrade=0`, and `onboot=0`.
6. Read back and compare every managed field, marker, forbidden-device condition, and config digest.
7. Start the VM, poll its task, wait for running state and bounded guest-agent ping, then return observed state.

UPIDs are parsed into a bounded typed structure and must match the configured node/action context. Task logs and upstream error bodies are not returned or logged. If a task fails, times out, disappears, or returns malformed identity/status, the invocation remains queryable or fails with typed safe diagnostics; success is never inferred from an HTTP response alone.

Before every side effect the provider persists a checksummed journal record in an operator-owned directory mode `0700`, with data files mode `0600`. The versioned record maps invocation ID to target/action/request digest, node/VMID, ownership marker digest, UPID, phase, and last safe observation. It contains no API token, secret, raw Cloud-Init input, task log, or upstream body. `GetInvocation` reads this journal after process restart. A corrupt or unknown journal version fails closed.

A lost clone response is reconciled by reading the exact destination VMID: absence permits the same idempotent invocation to retry; a matching marker resumes; any mismatch stops. A PVE config-digest conflict is reread: if all desired managed fields and ownership match, the step is complete; otherwise return drift without overwriting. Start/stop/pause/resume are read-before-action and accept already-satisfied state. No force, skip-lock, purge, or unreferenced-volume flag is used.

If apply fails after creating a VM, compensation first verifies exact ownership, gracefully stops when possible, sends `DELETE /nodes/{node}/qemu/{vmid}` with `purge=0` and `destroy-unreferenced-disks=0`, polls its UPID, and confirms absence. Failure or ambiguity returns every known leftover and retains allocations. Explicit destroy follows reverse dependencies, uses the same ownership and task rules, and releases claims only after confirmed absence.

Observed provider JSON exposes only typed VMID, node, power/readiness state, planned MACs/static IP addresses, safe configuration digest, and non-secret drift/leftover metadata. It exposes no credential, task log, arbitrary guest data, or access URL; the core projects portable `Output` values.

## Credentialed integration matrix

T0 fake-API tests deterministically cover every case in [`proxmox-qemu-mutation.yaml`](../../design/contracts/proxmox-qemu-mutation.yaml), including all generic provider cases. The catalog records the exact Go test binding for each stable ID. T3 runs remain isolated and opt-in, record exact PVE `pve-manager` version and storage/network configuration, use a disposable VMID/VLAN range, and verify at minimum:

- the minimum supported 9.x release and current supported 9.x release;
- full clone on `dir`/qcow2 and `lvmthin`/raw when the target advertises each profile;
- VLAN-aware bridge isolation, static addressing, and negative management/cross-instance reachability;
- missing permission, stale template/storage/bridge/config, VMID collision, and allocation conflict failures before unsafe writes;
- crash/restart and lost-response recovery at each clone/config/start/delete task boundary;
- marker mismatch, compensation failure, leftover retention, task failure/redaction, and clean reverse destroy.

No target combination is enabled merely because another combination passed. T4 later covers upgrade, load, and production-like failure rehearsal.

## Signed qualification evidence boundary

An isolated T3 runner emits the closed `labcentral.proxmox-qualification-evidence/v1` document defined by the machine contract. It records the stable run/target/installation IDs; exact target configuration, capability, inventory, and policy digests; full `pve-manager` release/build; provider binary/source-tree digests; negotiated protocol/features; destination node; `dir`/qcow2 or `lvmthin`/raw storage; VLAN-aware bridge and ranges; template VMID/config/content digests; effective ACL paths; disposable VMID ranges; every required T3 case outcome; and cleanup claim disposition. It contains only bounded identifiers and SHA-256 transcript/endpoint/agent-image digests, never an API token, raw upstream body, task log, guest secret, management address, or arbitrary test output.

`TEST-PROXMOX-024` implements normal-CI validation of the evidence boundary. The signer uses RFC 8785 canonical evidence bytes, a schema-version domain separator, Ed25519, an explicit key ID, and unpadded base64url. Verification requires a configured trusted public key and exact expected target/installation IDs, target-configuration/capability/inventory/policy digests, PVE release/build, and provider binary/source-tree digests. Evidence validity is at most 30 days; a consuming deployment may require less.

The three probe records are ordered and measured from a designated guest agent: all intended-segment attempts must succeed, while all management and cross-instance attempts must fail. Each has at least three attempts and binds the source VMID, endpoint digest, probe-agent image digest, and transcript digest. A signed document remains an auditable artifact when a case or probe fails, cleanup is ambiguous, a leftover exists, or a claim is retained/orphaned, but verification returns `qualifies=false`. Only provider-confirmed absence, zero leftovers, and every disposable claim marked released can qualify.

`go run ./tools/proxmox-qualification sign ...` signs a completed unsigned artifact using a mode-protected, non-symlink Ed25519 private-key file. Its `verify` command is the fail-closed CI/review primitive. The tool never connects to Proxmox, executes a test, changes a claim, or opens the provider gate. The credentialed executor and a separate production-enablement ADR/implementation remain outstanding; the latter must consume verified evidence rather than a target-config boolean or the package-private T0 constructor.

`TEST-PROXMOX-025` provides the first credentialed executor slice as a Go package test, keeping the mutation-opening constructor unavailable to production code. Its closed v1 profile names one exact target plus one expected disposable VMID/VLAN and approved aliases. Execution requires the exact destructive sentinel, three distinct absolute input files, and a separate target-ID confirmation. It performs real HTTPS discovery/preflight, one Network/VM plan and apply, stop/start/pause/resume, reverse ownership-checked cleanup, and provider absence confirmation. Default and partial activation make zero provider calls. Cleanup uses an independent bounded context. This smoke does not satisfy or populate the required T3 matrix and contains no guest-probe shortcut; it must be extended and run with the real probe fixture before evidence can qualify.
