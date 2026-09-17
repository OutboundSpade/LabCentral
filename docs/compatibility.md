# LabCentral release compatibility

This matrix is the compatibility contract for the first `v0.2.x` production
release line. A later release may narrow or extend it only through an accepted
ADR and updated executable compatibility fixtures. A package or image is
supported only when its version, digest, deployment mode, provider target, and
qualification evidence all match.

| Surface | `v0.2.x` reads/accepts | Write/emission behavior | Rollback boundary |
|---|---|---|---|
| Control-plane API | HTTP `/api/v1`; `server-info.apiVersion=v1` | Emits API v1 and the linked release version in `server-info.serverVersion` | Clients must ignore additive response fields. No API v2 client is accepted. |
| Lab declaration | `labs.labcentral.io/v1alpha1` authoring and `canonical.labcentral.io/v1alpha1` rendered Labs | Emits immutable source and `template.labcentral.io/v1` authoring representations; instances retain resolved canonical v1alpha1 bytes | No automatic downgrade to another schema. Retain the original authoring bundle and rendered revision. |
| Plugin transport | Protocol major 1. Docker provider minor 0; Proxmox provider minors 0–1; noVNC access minor 2 | Negotiates the highest supported common minor and gates minor features explicitly | Protocol major changes require side-by-side migration. Official profiles are tested together; independently installed plugins pin a manifest digest and negotiate compatible contracts. Identical product versions are not required. |
| Plugin state | Docker state major 1; `proxmox.qemu-state/v1.0`; `proxmox.invocation-journal/v1.0`; `novnc.access-state` major 1 | Preserves typed versioned state and refuses unknown majors | The Proxmox prior-build rehearsal proves current `v1.0` terminal journals and state remain readable. Never roll back across an emitted state major without a separately tested converter. |
| Proxmox target configuration | `proxmox.target-config/v1.1` and `v1.2`; browser console access requires `v1.2` | Writes QEMU plan/state v1.0 and journal v1.0 | Preserve the target JSON and journal together. A binary that does not list the configured schema is not a rollback candidate. |
| Persistence | PostgreSQL 18 for multi-replica production; SQLite for the qualified single-node native topology. Job migrations `0001`–`0002`; control-plane migrations `0002_local_vertical_slice` through `0012_template_representations` | Applies checksummed forward migrations under a database-wide migration lock; migrations are additive and never silently rewritten | There are no down migrations. Roll back only to a build independently proven to read the highest applied migration, or restore the pre-upgrade encrypted recovery set into a new database/state root. |
| Recovery sets | `labcentral.recovery/v1`, recovery tool major 1, exact release match by default | Emits signed, age-encrypted, checksummed database/state sets | Restore with the exact release recorded in the manifest. Cross-release restore requires a separately rehearsed compatibility exception. |
| Native artifact | Linux/amd64 | Reproducible `.tar.gz`, external Sigstore bundle, checksums, CycloneDX 1.6 SBOM, source facts, and GitHub SLSA provenance | Keep the old verified archive, checksum, Sigstore bundle, qualification evidence, configuration, and recovery set until the new release passes acceptance. |
| Container image | Linux/amd64, always selected by `sha256:` digest | Publishes a keylessly signed GHCR digest with a GitHub provenance attestation | Keep the previous digest. Tags are discovery aliases and are never rollback identities. |
| Proxmox VE | Only an exact target/build/profile with fresh signed qualification evidence. The disposable testbed exercised PVE 9.2.2; this is not a blanket support claim. | Mutation rechecks live inventory, executable/source digests, policy, permissions, and evidence before each write path | Any PVE update invalidates the old exact-build evidence until requalification. |

Windows compilation is a portability check, not a production support claim.
Provider capabilities remain target-discovered facts. In particular, a matching
PVE version does not establish storage, bridge, template, permission, guest
agent, or workload-capacity compatibility.

## Upgrade and rollback decision

Before an upgrade, verify the new archive or image by digest, take and verify an
encrypted recovery set, retain the old artifact and exact-target evidence, and
run the deployment-specific preflight. Upgrade one control-plane replica at a
time only when both builds support the applied database and plugin-state
versions. Do not run mixed provider builds against the same Proxmox journal.

Rollback is allowed only before an incompatible schema/state has been emitted.
If the compatibility check is uncertain, keep ingress and provider mutation
closed and restore the pre-upgrade set with its exact release into new storage.
The procedures in [recovery.md](operations/recovery.md) and the applicable
deployment runbook are authoritative.

ADR-0015, ADR-0016 and ADR-0017 govern the current architecture changes. Renderer semantics (`labcentral.renderer/v1alpha1`) are independent of product SemVer. Legacy revisions remain readable; unknown rendering semantics cannot create new instances. Explicit cleanup authority is described in the [installation guide](operations/plugin-installation.md); ordinary qualification remains required for every other write path. These changes do not make earlier provider-build qualification valid for a new executable.

## Independently installed extensions

ADR-0023, ADR-0024 and ADR-0025 define the extension implementation sequence.
The new host reads both installation/manifest v1 and the additive v2 contracts;
older hosts cannot load v2 contributions. Provider protocol negotiation and access
1.2 negotiation are unchanged. Source and builder services separately negotiate
1.0; bindings additionally require `artifact-transfer-v1` for orchestration. Browser contributions
use `labcentral.browser/v1` and bridge version 1. Executable versions need not
match the product version, but installation metadata must match the handshake.

| Evidence | Establishes | Does not establish |
| --- | --- | --- |
| Package digest and negotiated contract | Installed contribution identity and wire compatibility | Provider support, image trust, OS isolation |
| Current target discovery | Target-specific supported/emulated capabilities | Image approval or qualified isolation |
| Image approval | Approved content and usage | Import completion or deployment eligibility |
| Exact-target qualification | Eligibility under its bound target/build facts and expiry | Compatibility of a different executable |

Migration `0014_extensible_access` preserves existing records while permitting
namespaced methods and tracking one-time delivery claims. Older software that
rejects new method IDs must not be used against a database containing them; use
a consistent pre-upgrade database/artifact backup for rollback. Old migration
checksums and the original Protobuf compatibility baseline remain unchanged.
Single-document revisions remain byte-compatible; the new bundle publication
path is additive. Bundle build recipes select `labcentral.renderer/v1alpha1+builds/v1`; old revisions retain their original semantics and canonical vectors. Jobs migration `0003_typed_work_subjects` and core migrations `0015_extension_work`, `0016_gateway_delegation`, `0017_build_orchestration` add durable work and delegation. Roll back this release with a consistent pre-upgrade database/artifact set, never by editing migration history.

Provider `image-import-v1` and `artifact-transfer-v1` are optional features. A rebuilt provider has a different qualification identity even when its source behavior is unchanged. Core/UI-only upgrades may retain the exact qualified installed provider executable. Builder limits and binding configurations are captured in immutable work; restore the original identity/configuration to recover uncertain invocations after a binding change.
