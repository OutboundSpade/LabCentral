# Proxmox provider foundation and gated QEMU mutation

`TASK-PROXMOX-001` implements target-scoped discovery in the separate `lab-provider-proxmox` process. `TASK-PROXMOX-004` adds the closed validation, planning, state, journal, apply, read, destroy, `GetInvocation`, and start/stop/pause/resume implementation for the accepted fixed-node Linux QEMU subset, plus its complete T0 fake-API suite. Image import, access, cancellation, state migration, and every out-of-subset operation remain unimplemented.

The normal `NewService` constructor used by the production binary has an unconditionally closed mutation-execution gate. Only the package-private T0 constructor used by local tests can execute writes or advertise the qualified mutation subset. Therefore protocol v1.1 compatibility, a `mutationEnabled` target document, or discovery evidence alone never enables production writes. `TASK-PROXMOX-005` now integrates target registration, secret-backed discovery, capability persistence, allocation/dependency delivery, and worker routing in T0; exact-target credentialed T3 qualification and its real guest probes remain outstanding.

## Versioned envelopes

The plugin advertises discovery-compatible `proxmox.target-config` v1.0, additive target-config v1.1, API-token credentials v1.0, capacity v1.0, validation-plan v1.0, QEMU plan/state v1.0, and invocation-journal v1.0. JSON envelopes reject unknown or recursively duplicate fields, trailing JSON values, empty input, and oversized input.

Target configuration fields are:

| Field | Type/default | Constraint |
|---|---|---|
| `endpoint` | required string | HTTPS origin only, normally `https://pve.example:8006`; no user information, path, query, or fragment. |
| `caCertificatePEM` | optional PEM string | Adds one or more private CA certificates to system trust; invalid PEM is rejected. TLS verification is never disabled. |
| `requestTimeoutSeconds` | optional integer, default 10 | Maximum 30 seconds. |
| `maxResponseBytes` | optional integer, default 4 MiB | Between 1 KiB and 16 MiB for each upstream response. |

Mutation remains false by default. A v1.1 mutation target additionally fixes an installation ID, the exact supported PVE 9.x range and destination node/pool, disjoint VMID ranges, approved template aliases with SHA-1 config and SHA-256 content digests and cloned-disk inventory, storage aliases/formats, bridge aliases and VLAN pools, VM/NIC/compute limits, positive intended-segment and negative management/cross-instance probe evidence, exact-target qualification digests, non-root `0700` journal directory, and least-privilege ACL paths. The configuration rejects `/` write scope, missing privileges, `Sys.Modify`, and `VM.GuestAgent.Unrestricted`. The initial planner may select only those aliases; raw VMIDs, storage IDs, bridges, VLANs, QEMU fields, and force flags cannot come from Lab input.

The separate credential envelope has exactly `tokenID` and `tokenSecret`. `tokenID` uses `USER@REALM!TOKENID`; `tokenSecret` is the value Proxmox displays once when it creates the token. The provider constructs the documented `Authorization: PVEAPIToken=USER@REALM!TOKENID=UUID` header in memory for each request. It does not persist or log either envelope, include upstream error bodies in errors, or copy credentials into capability/capacity output.

## Discovery and evidence

One discovery invocation performs only these Proxmox JSON API `GET` requests:

1. `/version` and `/nodes` establish the target version and bounded node set.
2. For each node whose returned status is exactly `online`, `/nodes/{node}/storage?content=images&enabled=1&format=1` discovers enabled VM-image storage and formats.
3. `/nodes/{node}/network?type=bridge` discovers bridges, ports, and VLAN-awareness flags.
4. `/nodes/{node}/qemu?full=0` proves QEMU-list API reachability and discovers records explicitly marked as templates.

Provider-returned node names are validated before they enter a URL path. Node count, aggregate inventory items, nested items, field lengths, per-response bytes, and the final capacity document are bounded. Results are normalized and lexically sorted before a SHA-256 inventory digest is calculated. Offline nodes are retained as capacity facts but are not queried further. A node-level permission, availability, transport, response, or limit failure becomes a stable typed issue when the rest of the inventory remains usable.

`DiscoverTarget` returns `proxmox.capacity` v1.0 JSON containing the version, nodes, QEMU reachability, templates, storage content/formats/capacity, bridges, issues, and inventory digest. Capability evidence cites that digest, the observed Proxmox version, online-node count, and issue count. The target configuration fingerprint is included only when it is a canonical lowercase `sha256:` digest. Evidence expires after five minutes.

Discovery facts do not establish mutation support. The production constructor therefore leaves `workload.vm` and every lifecycle capability unsupported. Under the T0-only gate, support becomes native only when the pinned inventory digest, PVE 9.x node/template/storage/bridge facts, exact ACL paths, template config/forbidden-device checks, connectivity evidence, protocol-compatible configuration, and durable journal all match. Snapshot, clone-as-user-action, firewall, access, reservation, and placement capabilities remain unsupported.

## Gated mutation behavior

Validation accepts only isolated static-IPv4 Network resources and Linux/amd64 VM resources without mounts, arbitrary provisioning, snapshots, reset, IPv6, author MACs, or provider-managed addressing. Its closed validation plan carries only administrator profile aliases and target/capability/inventory/policy/lab digests. Per-resource planning deterministically proposes one core-owned VLAN or VMID claim, consumes dependency Network plans, derives locally administered MACs, computes bounded CPU/memory, and emits the finite full-clone step sequence.

Apply rebinds every decoded plan field to the canonical resource and configured VMID, template, storage, bridge, VLAN, hardware, disk, and ownership profiles. It rejects any stale digest, missing/extra committed claim, mismatched dependency state, substituted provider-state address, template drift, foreign destination, or permission/inventory regression before its first write. It uses only the allowlisted clone, safe config, status, guest-agent, task, lifecycle, and exact-VM delete APIs. Full ownership is placed in the clone description and checked on every later operation. Config writes use the observed PVE digest; lost responses are reconciled by exact VMID, marker, lock, task, and readback state. Cleanup never uses force, skip-lock, purge, or unreferenced-volume deletion and reports an owned leftover when absence is ambiguous.

The invocation journal uses an operator-created `0700` directory, atomic/checksummed `0600` records, file and directory sync, bounded closed JSON, and hashed filenames. It stores no token, upstream body, task log, or guest data. Submitted clone/lifecycle/destroy UPIDs survive restart and are polled before any action can be reissued; unknown versions, corruption, identity reuse, malformed UPIDs, ownership drift, or a terminal-result persistence failure fail closed.

## Transport and operator boundary

The client uses TLS 1.2 or newer, system roots plus the optional target CA, a fixed target origin, no environment proxy, refused redirects, bounded dial/handshake/request timeouts, and bounded response bodies. API-token calls do not use a CSRF token. Internal methods construct only the accepted fixed paths and typed form/query fields; provider values are validated before entering paths. Health reports process readiness and the still-closed production mutation gate; per-target reachability remains a discovery result.

Create a dedicated Proxmox user and a privilege-separated, expiring token for this target. Discovery tokens need only read/audit access. The future mutation token must use the exact configured template, pool, storage, bridge/SDN, and node/task ACL paths documented in the mutation contract; cluster-root write ACLs are rejected. Until `TASK-PROXMOX-005` qualifies and integrates a real target, do not grant the production process a mutation role. Keep the management endpoint unreachable from lab guests and expose it only to the provider process or its deployment identity.

This is a complete provider and control-plane T0 foundation, not a production provider release. The original 22 `TEST-PROXMOX-*` cases have executable provider bindings, including restart at every persisted mutation checkpoint and exact HTTP method/path/form assertions; `TEST-PROXMOX-023` additionally proves target supervision inputs, protocol/features, persisted capabilities, atomic claims, dependency delivery, and absence-gated release through the control plane. A production gate still requires credentialed T3/T4 testing against supported Proxmox versions and profiles, real isolation probes, cleanup/orphan conformance, signed qualification review, and upgrade/state-migration rehearsal.

The authoritative contract is [`proxmox-qemu-mutation.md`](proxmox-qemu-mutation.md); its exact T0/T3 case bindings are in [`proxmox-qemu-mutation.yaml`](../../design/contracts/proxmox-qemu-mutation.yaml).
