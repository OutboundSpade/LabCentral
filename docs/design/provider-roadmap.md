# Implementation and provider roadmap

The thin Docker path optimizes onboarding, not production priority.

## Phase 0A — architecture and contract gate (complete)

Finalize executable Lab schema/canonical vectors, domain/application boundary specification, Protobuf plugin protocol and descriptor baseline, capability predicates, machine-readable fake-provider/job/security contracts, and the repeatable design verifier. This gate is complete at `MILESTONE-DESIGN-READY`; it proves implementation readiness, not runtime behavior.

## Phase 0B — architecture foundation implementation (complete)

Compile the domain/application ports; implement renderer/schema/canonical conformance; build SQLite/PostgreSQL job/outbox prototypes; implement the fake provider and shared failure contract runner. `TASK-DOMAIN-001`, `TASK-RENDER-001`, `TASK-JOBS-001`, and `TASK-PROVIDER-001` are complete. The Phase 0B exit is satisfied; real provider code remained out of scope.

## Phase 1 — packaged local vertical slice (complete)

`TASK-DOCKER-001` built the `labd` modular monolith, Huma API/OpenAPI checks, SQLite/filesystem adapters, no-auth loopback mode, durable validate/plan/apply/destroy path, static SvelteKit shell, separately supervised thin Docker plugin, and live Example A gate. `TASK-RELEASE-001` added deterministic native archives with checksums/SBOM/provenance plus a minimal Docker image and persistent host-networked Compose deployment, then repeated the live Example A lifecycle through that deployment. Hostile images and unenforced denied egress remain explicitly gated behind the trusted-laptop acknowledgement. Advanced Linux networking remains later provider scope and is not implied by this milestone.

## Phase 2 — Proxmox production path

The `TASK-PROXMOX-001` foundation is complete: a separate provider now performs strict HTTPS/API-token `GET` discovery of version, nodes, QEMU-list reachability/templates, VM-image storage/formats/capacity, and bridges/VLAN awareness, with expiring deterministic evidence and no advertised mutations. It is not yet integrated into `labd` target registration or a credentialed Proxmox environment.

Next specify and gate the first mutation slice before implementation: target registration/supervision, exact QEMU/template/volume/network plan schemas, privilege and ownership tokens, task polling/unknown outcomes, cleanup, and T3 fixtures. Then implement QEMU VM, multi-NIC isolated router labs, placement, template clone, cloud-init/Ignition, snapshot/restore/reset, access integration, classroom batches, and Example B at scale. Firewall, snapshot, access, and placement support must be discovered independently. Container-via-VM follows the shared contract after the VM path is reliable.

## Phase 3 — production control plane

PostgreSQL, OIDC/local auth, organizations/projects/teams/RBAC, quotas/reservations, expanded audit/outbox, access proxy plugins, TLS/backup/HA deployment, TTL/renewal/idle signals where honest, and operator workflows.

## Phase 4 — Kubernetes/KubeVirt

Dynamic CRD/component/CNI/CSI/RuntimeClass discovery; KubeVirt VM and container-via-VM; native Pods and optional sandbox RuntimeClass as distinct isolation; Multus/NAD and NetworkPolicy evidence; snapshots and access. A cluster without KubeVirt remains explicitly container-only.

## Phase 5 — additional providers and sites

Fly portable L3 container subset and custom 6PN; Docker feature completion; deliberately small AWS EC2/VPC/ENI/SG/EBS plus container-via-VM; outbound-mTLS Site Agent with offline leases. Example C must produce provider-specific plan warnings rather than equivalence claims.

## Phase 6 — ecosystem

Git/commit provenance, OCI bundle distribution, image resolver/importer, isolated build service, plugin/bundle checksums/signatures and registries, third-party SDK maturity, policy/event/audit extensions, and educational metadata consumers.

Windows support follows OQ-GUEST-001 and is not implicit in these phase exits. Each phase requires threat-model/test/state/checklist updates before implementation is called complete.
