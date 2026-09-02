# Interpreted requirements

This document is the durable interpretation of the product brief. “Must” requirements are release constraints; “should” entries are design goals whose omission needs an ADR.

## Mission and scale

- **REQ-PROD-001:** Authors can publish immutable, reusable Lab Template revisions and instantiate independent Lab Instances.
- **REQ-PROD-002:** An instance supports validate, plan, apply/create, start, stop, pause/resume where capable, reset, snapshot/restore/clone where capable, refresh, retry, cancel where safe, and destroy.
- **REQ-PROD-003:** The initial production envelope is 20–100 concurrent instances and hundreds to low thousands of logical resources, with users, teams, quotas, TTLs, and auditability.
- **REQ-PROD-004:** Laptop mode must realize a Docker lab with minimal configuration; its UI must identify its security limitations.
- **REQ-PROD-005:** Provider depth priority is Proxmox, KubeVirt/Kubernetes, Fly.io, Docker, AWS; a thin Docker onboarding path may precede production providers.

## Core and extensibility

- **REQ-CORE-001:** One Go control-plane process owns portable semantics, orchestration, authorization, state, scheduling, quotas, and lifecycle coordination.
- **REQ-CORE-002:** Huma HTTP/OpenAPI is canonical; handlers contain transport adaptation only. UI/CLI are ordinary clients.
- **REQ-CORE-003:** Provider and access plugins run out of process and communicate through versioned, language-neutral contracts.
- **REQ-CORE-004:** Plugins cannot access persistence, unrelated tenant state/credentials, or core authorization, and cannot define v1 portable top-level resource kinds.
- **REQ-CORE-005:** Provider Targets are registered centrally; each v1 Lab Instance binds to exactly one target.
- **REQ-CORE-006:** Stage 1 core validation precedes Stage 2 selected-provider validation and returns field/resource-addressed structured errors.

## Declarative behavior

- **REQ-DECL-001:** YAML documents use `labs.labcentral.io/v1alpha1`, typed inputs, immutable revision content, and versioned provider overrides.
- **REQ-DECL-002:** Rendering is deterministic and bounded; CEL is restricted to a declared environment with explicit `when`/`forEach`. Shell, Go templates, JavaScript, and network/file access are forbidden.
- **REQ-DECL-003:** Canonical JSON and its SHA-256 digest are independent of YAML formatting, comments, anchors, and map order.
- **REQ-DECL-004:** References infer dependency edges; explicit `dependsOn` only adds non-obvious ordering. Cycles are invalid.
- **REQ-DECL-005:** Bundles are content-addressed artifacts with manifests and checksums; large VM images are references/import jobs, never relational fields.

## Providers and isolation

- **REQ-PROV-001:** Capability claims are target-discovered and versioned, with support levels `native`, `emulated`, `constrained`, and `unsupported`, plus parameters and evidence.
- **REQ-PROV-002:** A requested isolation class is never silently weakened. KubeVirt absence yields container-only Kubernetes; Fly/Docker reject VM workloads.
- **REQ-PROV-003:** Proxmox portable VMs use QEMU. Logical containers may use the shared container-via-VM contract, not PVE LXC.
- **REQ-PROV-004:** Docker baseline uses Engine/network APIs and works through Docker Desktop; Linux host networking is a separately advertised extension.
- **REQ-PROV-005:** AWS v1 is EC2/VPC/subnet/ENI/security group/EBS; containers use container-via-VM.
- **REQ-PROV-006:** Provider ownership metadata and recorded provider IDs, never names alone, guard mutation and deletion.

## Security, tenancy, and access

- **REQ-SEC-001:** Guests, images, bundles, traffic, and third-party plugins are malicious by assumption. Guest networks cannot reach control/provider management planes by default.
- **REQ-SEC-002:** Lab egress is denied by default. Exceptions are explicit, planned, capability-checked, auditable, and policy-constrained.
- **REQ-SEC-003:** No-auth mode maps to one local administrator, binds loopback, shows a persistent warning, and requires an explicit unsafe flag for non-loopback.
- **REQ-SEC-004:** Secrets are references; values never appear in templates, canonical specs, logs, events, audit records, or reusable provider-state blobs.
- **REQ-SEC-005:** Authorization is scoped over platform, organization, project, team, target, template, and instance relationships; team isolation is deny-by-default.
- **REQ-ACCESS-001:** Access separates endpoint discovery, authorization, ephemeral session/token issuance, proxying, and audit. Provider credentials are never returned to clients.

## Durability and operations

- **REQ-JOB-001:** Long-running HTTP actions return durable Operation IDs and survive control-plane/plugin restarts without an external broker.
- **REQ-JOB-002:** Jobs use leases, fencing, heartbeats, step checkpoints, timeouts, cancellation requests, bounded retry/backoff, locks, and idempotency keys.
- **REQ-JOB-003:** The logical resource is the normal retry/status/compensation boundary. A failed resource is cleaned up while earlier successful resources remain.
- **REQ-JOB-004:** Destroy attempts every owned resource in reverse dependency order and cannot be `succeeded` while known owned resources remain.
- **REQ-EVENT-001:** State changes and durable events are atomically joined through a transactional outbox; audit is immutable and distinct from operational events.

## Persistence, UI, and delivery

- **REQ-DATA-001:** Persistence and artifact storage are interfaces. SQLite/filesystem serve local/single-node mode; PostgreSQL/object storage serve production.
- **REQ-UI-001:** SvelteKit/DaisyUI built with Bun exports static route entry points and requires no Node.js production runtime; runtime config contains no secrets.
- **REQ-DEPLOY-001:** Releases support binaries/archive, Docker, Compose, and Kubernetes. Bundled official plugins remain separate OS processes.
- **REQ-COMPAT-001:** HTTP, Lab schema/bundle/canonical forms, plugin/access protocols, capability/override/provider-state/database/SDK versions evolve independently.

## Explicit non-goals for v1

Cross-provider instances, continuous autonomous drift repair, arbitrary plugin resource kinds, a general secrets product, mandatory Git/OCI distribution, a production message broker, provider-equivalent networking, education/scoring, and production application implementation during this design phase are excluded.

