# Implementation and provider roadmap

The thin Docker path optimizes onboarding, not production priority.

## Phase 0A — architecture and contract gate (complete)

Finalize executable Lab schema/canonical vectors, domain/application boundary specification, Protobuf plugin protocol and descriptor baseline, capability predicates, machine-readable fake-provider/job/security contracts, and the repeatable design verifier. This gate is complete at `MILESTONE-DESIGN-READY`; it proves implementation readiness, not runtime behavior.

## Phase 0B — architecture foundation implementation (complete)

Compile the domain/application ports; implement renderer/schema/canonical conformance; build SQLite/PostgreSQL job/outbox prototypes; implement the fake provider and shared failure contract runner. `TASK-DOMAIN-001`, `TASK-RENDER-001`, `TASK-JOBS-001`, and `TASK-PROVIDER-001` are complete. The Phase 0B exit is satisfied; real provider code remained out of scope.

## Phase 1 — packaged local vertical slice (complete)

`TASK-DOCKER-001` built the `labd` modular monolith, Huma API/OpenAPI checks, SQLite/filesystem adapters, no-auth loopback mode, durable validate/plan/apply/destroy path, static SvelteKit shell, separately supervised thin Docker plugin, and live Example A gate. `TASK-RELEASE-001` added deterministic native archives with checksums/SBOM/provenance plus a minimal Docker image and persistent host-networked Compose deployment, then repeated the live Example A lifecycle through that deployment. Hostile images and unenforced denied egress remain explicitly gated behind the trusted-laptop acknowledgement. Advanced Linux networking remains later provider scope and is not implied by this milestone.

## Phase 2 — Proxmox production path

The `TASK-PROXMOX-001` foundation is complete: a separate provider now performs strict HTTPS/API-token `GET` discovery of version, nodes, QEMU-list reachability/templates, VM-image storage/formats/capacity, and bridges/VLAN awareness, with expiring deterministic evidence and no advertised mutations. `TASK-PROXMOX-002` fixes the first mutation design contract: PVE 9.x fixed-node Linux QEMU full clone, approved storage and VLAN-aware bridge profiles, core-owned VMID/VLAN claims, typed IPv4 Cloud-Init networking, exact ownership/task recovery, least privilege, and a 22-case T0/T3 matrix. `TASK-PROXMOX-003` implements the additive v1.1 allocation/dependency wire fields, stable pre-plan Instance identity, transactional/fenced SQL allocation claims, and SQLite/PostgreSQL conformance. None of these tasks enables writes or integrates a credentialed target.

Next implement the strict QEMU plan/state/journal and all 22 T0 fake-API mutation cases behind a disabled-by-default gate, then integrate target registration/supervision and run exact-profile credentialed T3 qualification. Rich guest artifacts, router provisioning, dynamic placement, snapshot/restore/reset, access integration, classroom batches, and Example B follow as separate slices. Firewall, snapshot, access, and placement support must be discovered independently. Container-via-VM follows the shared contract after the VM path is reliable.

## Phase 3 — production control plane

PostgreSQL, OIDC/local auth, organizations/projects/teams/RBAC, quotas/reservations, expanded audit/outbox, access proxy plugins, TLS/backup/HA deployment, TTL/renewal/idle signals where honest, and operator workflows.

## Phase 4 — Kubernetes/KubeVirt

Dynamic CRD/component/CNI/CSI/RuntimeClass discovery; KubeVirt VM and container-via-VM; native Pods and optional sandbox RuntimeClass as distinct isolation; Multus/NAD and NetworkPolicy evidence; snapshots and access. A cluster without KubeVirt remains explicitly container-only.

## Phase 5 — additional providers and sites

Fly portable L3 container subset and custom 6PN; Docker feature completion; deliberately small AWS EC2/VPC/ENI/SG/EBS plus container-via-VM; outbound-mTLS Site Agent with offline leases. Example C must produce provider-specific plan warnings rather than equivalence claims.

## Phase 6 — ecosystem

Git/commit provenance, OCI bundle distribution, image resolver/importer, isolated build service, plugin/bundle checksums/signatures and registries, third-party SDK maturity, policy/event/audit extensions, and educational metadata consumers.

Windows support follows OQ-GUEST-001 and is not implicit in these phase exits. Each phase requires threat-model/test/state/checklist updates before implementation is called complete.
