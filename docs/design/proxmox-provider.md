# Proxmox provider discovery foundation

`TASK-PROXMOX-001` implements the first bounded Phase 2 slice as the separate `lab-provider-proxmox` process. It performs target-scoped, read-only discovery only. It is not wired into the Phase 1 single-Docker-target `labd`, and it implements no validation, planning, apply, read, destroy, lifecycle, image, invocation, or access RPC. Those RPCs return gRPC `Unimplemented`; their portable capabilities remain `unsupported`.

## Versioned envelopes

The plugin advertises `proxmox.target-config` v1.0, `proxmox.api-token-credentials` v1.0, and `proxmox.capacity` v1.0. JSON envelopes reject unknown or duplicate fields, trailing JSON values, empty input, and oversized input.

Target configuration fields are:

| Field | Type/default | Constraint |
|---|---|---|
| `endpoint` | required string | HTTPS origin only, normally `https://pve.example:8006`; no user information, path, query, or fragment. |
| `caCertificatePEM` | optional PEM string | Adds one or more private CA certificates to system trust; invalid PEM is rejected. TLS verification is never disabled. |
| `requestTimeoutSeconds` | optional integer, default 10 | Maximum 30 seconds. |
| `maxResponseBytes` | optional integer, default 4 MiB | Between 1 KiB and 16 MiB for each upstream response. |

The separate credential envelope has exactly `tokenID` and `tokenSecret`. `tokenID` uses `USER@REALM!TOKENID`; `tokenSecret` is the value Proxmox displays once when it creates the token. The provider constructs the documented `Authorization: PVEAPIToken=USER@REALM!TOKENID=UUID` header in memory for each request. It does not persist or log either envelope, include upstream error bodies in errors, or copy credentials into capability/capacity output.

## Discovery and evidence

One discovery invocation performs only these Proxmox JSON API `GET` requests:

1. `/version` and `/nodes` establish the target version and bounded node set.
2. For each node whose returned status is exactly `online`, `/nodes/{node}/storage?content=images&enabled=1&format=1` discovers enabled VM-image storage and formats.
3. `/nodes/{node}/network?type=bridge` discovers bridges, ports, and VLAN-awareness flags.
4. `/nodes/{node}/qemu?full=0` proves QEMU-list API reachability and discovers records explicitly marked as templates.

Provider-returned node names are validated before they enter a URL path. Node count, aggregate inventory items, nested items, field lengths, per-response bytes, and the final capacity document are bounded. Results are normalized and lexically sorted before a SHA-256 inventory digest is calculated. Offline nodes are retained as capacity facts but are not queried further. A node-level permission, availability, transport, response, or limit failure becomes a stable typed issue when the rest of the inventory remains usable.

`DiscoverTarget` returns `proxmox.capacity` v1.0 JSON containing the version, nodes, QEMU reachability, templates, storage content/formats/capacity, bridges, issues, and inventory digest. Capability evidence cites that digest, the observed Proxmox version, online-node count, and issue count. The target configuration fingerprint is included only when it is a canonical lowercase `sha256:` digest. Evidence expires after five minutes.

Discovery facts do not establish mutation support. In particular, QEMU-list reachability does not advertise `workload.vm`; bridge presence does not advertise portable segment isolation or VLAN allocation; storage/format presence does not prove snapshot/clone semantics; and this slice does not probe firewall enforcement, access, reservations, or placement. Provider-specific `proxmox.discovery.*` facts remain diagnostic unknown features to older cores.

## Transport and operator boundary

The client uses TLS 1.2 or newer, system roots plus the optional target CA, a fixed target origin, no environment proxy, refused redirects, bounded dial/handshake/request timeouts, and bounded response bodies. It sends no CSRF token because every call is a `GET`. Health reports that the process is ready to accept discovery calls; per-target reachability is represented by `DiscoverTarget`, not global health.

Create a dedicated Proxmox user and a privilege-separated, expiring token for this target. The token and backing user both need read/audit access to the intended cluster inventory. The built-in `PVEAuditor` role at `/` is the simplest read-only cluster-wide discovery grant; narrower ACLs are valid but can produce `permission_denied` issues for storage, network, or QEMU inventory. Do not grant a mutation role to this discovery-only version. Keep the Proxmox management endpoint unreachable from lab guests and expose it only to the provider process or its deployment identity.

This is a T0 fake-API/TLS foundation, not a production provider release. A production gate still requires credentialed T3/T4 testing against supported Proxmox versions, least-privilege permission verification, target registration/supervision, mutation ownership/orphan conformance, and upgrade/state-migration rehearsal.

The accepted next-step contract is [`proxmox-qemu-mutation.md`](proxmox-qemu-mutation.md). It keeps every mutation capability disabled while fixing the PVE 9.x full-clone subset, core-owned VMID/VLAN claims, typed plan/state/journal schemas, exact API/permission surface, ownership and UPID recovery rules, and credentialed T3 matrix.
