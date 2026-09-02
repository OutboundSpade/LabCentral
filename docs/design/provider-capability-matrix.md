# Provider capability model and matrix

## Schema

A target publishes `capabilities.labcentral.io/v1alpha1`:

```yaml
kind: ProviderCapabilities
provider: proxmox
targetID: pt_...
discoveredAt: 2026-09-02T00:00:00Z
validUntil: 2026-09-02T00:05:00Z
features:
  workload.vm:
    support: native       # native|emulated|constrained|unsupported
    parameters: {architectures: [amd64], maxInterfaces: 8}
    constraints: []
    evidence: {source: discovered, component: qemu-server}
```

Capabilities cover workload realization/isolation; lifecycle operations/modes; networking semantics, families and limits; volume/image types; access methods; and scheduling/capacity. Parameters use a core-known typed schema. Unknown feature IDs are preserved for diagnostics but cannot satisfy a requirement until the control plane knows their semantics. `constrained` only satisfies a request whose predicates accept the listed constraints; `emulated` always produces a plan warning.

### Core-known feature registry

| Namespace | v1 feature IDs and important parameters |
|---|---|
| Workload/isolation | `workload.vm`, `workload.container.native`, `workload.container.viaVM`; architectures, isolation classes (`process`, `sandboxedContainer`, `virtualMachine`), base runtime contract versions. |
| Lifecycle | `lifecycle.create`, `start`, `stop`, `pause`, `resume`, `snapshot`, `restore`, `clone`, `reset.snapshot`, `reset.recreate`, `update.inPlace`; snapshot consistency/modes, online/offline, scope, clone modes, mutable fields. |
| Interfaces/topology | `network.interface.multiple`, `network.segment.isolated`, `network.semantics.l2`, `network.semantics.l3Only`; maximum interfaces, same-segment attachment rules, optional components. |
| Addressing | `network.address.static`, `ipam.providerManaged`, `network.ipv4`, `network.ipv6`, `network.mac.custom`, `network.vlan`; address families, CIDR control, MAC policy, VLAN access/trunk ranges. |
| Connectivity/policy | `network.external`, `network.address.public`, `network.portPublication`, `network.egress.nat`, `network.egress.enforced`, `network.policy.enforced`; protocols, source filters, exposure types, deny/restricted evidence. |
| Storage/images | `volume.persistent`, `volume.ephemeral`, `volume.snapshot`, `image.inventory`, `image.import`, `image.ociPull`, `image.build`; storage types/classes, access modes, size ranges, snapshot consistency, architectures/digest algorithms. |
| Access | `access.ssh`, `vnc`, `spice`, `serial`, `rdp`, `containerExec`, `kubernetesExec`, `providerNativeURL`; proxy/direct modes, token TTL, revocation, browser compatibility. |
| Scheduling | `scheduling.capacity`, `scheduling.automaticPlacement`, `scheduling.constraints`, `scheduling.reservations`; dimensions, confidence/staleness, node/region/storage constraints, reservation lifetime. |

Capability snapshots also carry limits (resource counts/rates), deprecated features, discovery errors, and an optional `requiresOperatorAcceptance` flag. A Boolean-like feature still uses the same support enum so native, emulated, constrained, and unsupported are never collapsed.

## Target profiles

Legend: **N** native, **E** emulated, **C** constrained/discover, **—** unsupported. Every `C` is determined per target, not promised by provider name.

| Capability | Proxmox QEMU | K8s + KubeVirt | K8s, no KubeVirt | Fly Machines | Docker Engine | AWS EC2 |
|---|---:|---:|---:|---:|---:|---:|
| VM workload / VM isolation | N | N | — | — | — | N |
| Native logical container / process isolation | — | N | N | N | N | — |
| Sandboxed container isolation | — | C RuntimeClass | C RuntimeClass | C platform boundary, not requested VM | — | — |
| Container-via-VM | E | E | — | — | — | E |
| Start/stop | N | N | N (Pod recreate/scale semantics C) | N | N | N |
| Pause/resume | N | C | — | C | N/C Desktop | N (instance hibernate is target/image C) |
| VM/volume snapshot + restore | C storage | C CSI/feature | C CSI volume only | C volume snapshot, not full topology | C image/volume semantics only | C EBS crash/app consistency |
| Clone | C storage/template | C DataVolume/snapshot | C PVC | C Machine/volume semantics | E recreate | E AMI/EBS/instance create |
| Reset by recreate | N | N | N | N | N | N |
| Multiple workload NICs | N, max discover | C Multus/NAD | C Multus | — arbitrary NICs | — baseline | N, type limits |
| Isolated named segments | N bridge/VLAN C | C CNI/Multus/policy | C CNI/Multus/policy | C custom 6PN, L3 IPv6 | C user networks | N VPC/subnets |
| Honest L2 semantics | C bridge/VLAN | C secondary CNI | C secondary CNI | — | C Linux bridge; Desktop differs | — |
| Static IP / custom MAC | C | C CNI | C CNI | — / — | C | N / C |
| IPv4 / IPv6 | C / C | C / C | C / C | C / N private | C / C | N / N target config |
| Deny/restricted egress enforcement | C firewall/network | C CNI policy | C CNI policy | C custom network/services; exact policy limited | C; baseline not hostile boundary | N SG/NACL/routes |
| Public/published service | C | C Service/Gateway | C | N Fly Proxy/services | N published ports | N public IP/LB/SG C |
| Persistent volumes | N | N CSI | N CSI | N volumes C placement | N | N EBS |
| OCI pull | E via VM | N Pod / E VM | N | N | N | E via VM |
| Image inventory/import | N/C | C CDI/registry | OCI registry | OCI registry | OCI registry/build C | N AMI / import C |
| SSH/VNC/SPICE/serial | C/N/N/C | C/C/—/N | container exec | C provider SSH | exec | N/C via network/SSM |
| Native capacity/placement data | C | N scheduler + C inventory | N/C | C region capacity | C local | C service quotas/capacity |

## Precise provider notes

- **Proxmox:** discover cluster nodes, QEMU versions, storage content/format and snapshot/clone support, bridges/VLAN awareness, firewall, templates, console methods, guest agent, and per-node placement. Never infer snapshot from “Proxmox” alone because storage backends differ.
- **KubeVirt:** discover CRDs/API versions, KubeVirt readiness/features, CDI, VolumeSnapshot/CSI classes, Multus/NADs/bindings, RuntimeClasses, CNI NetworkPolicy enforcement evidence, live migration, and permitted MAC/interface modes. Absence of KubeVirt is explicit container-only.
- **Fly:** a Machine is accepted only as logical container realization. Custom 6PN offers L3 IPv6 segmentation; it does not satisfy L2, arbitrary multi-NIC, static lab CIDR/MAC, or VM requirements.
- **Docker:** baseline uses Engine REST containers, images, volumes, user-defined networks, and port publishing. Desktop runs Engine networking behind a VM/backend and differs from Linux bridge behavior; advanced namespace/iptables features require `docker.linuxAdvancedNetworking` and operator opt-in.
- **AWS:** one logical network normally maps to subnet/security controls, not L2. ENI count depends on instance type and AZ/subnet constraints. Full-instance snapshot/reset is composed from EC2/EBS and is reported emulated/constrained, including consistency.

Official source evidence and retrieval date are in [`docs/references.md`](../references.md).
