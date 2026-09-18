# ADR-0028: Isolated image builders and qualified Proxmox import

- **Status:** Accepted
- **Date:** 2026-09-18
- **Supersedes:** ADR-0025 (production builders); ADR-0022 (fixed preloaded image set); ADR-0018 (imported-image qualification binding only)
- **Superseded by:** None

## Decision

The first production builders target Linux amd64. The Ansible builder uses pinned QEMU/Packer and ansible-local within disposable VM guests to produce standalone qcow2 images. The Dockerfile builder uses pinned BuildKit to produce OCI artifacts. Author code, including Ansible controller-side plugins, executes only within disposable VM boundaries; provider/control credentials never enter them. Enforce CPU, memory, disk, time, output and egress limits externally to author code. Operator-configured registries and package mirrors supply controlled downloads; bases and source dependencies are pinned and provenance records toolchain and effective inputs. Package repositories can change, so source portability does not promise byte-identical image rebuilds.

Core orchestrates bounded recipe dependencies with existing durable child work, fencing, original invocation recovery, project-scoped deduplication and shared cancellation rules. Build identity includes effective context, base artifact identities, architecture, toolchain and declared inputs. The Ansible output is cleaned of temporary credentials and machine identity. Dockerfile output is packaged into the fixed container VM runtime without broadening its workload restrictions.

The Proxmox provider alone imports images using discovered API capabilities, bounded staging storage, reserved VMIDs, durable ownership and restart-safe recovery. No arbitrary URL fetches or shell-command API is added. Per-resource bindings support multiple images in one lab. Existing static-template profiles and old state retain their semantics.

Imported bindings require artifact, target, architecture, exact VM configuration and applicable runtime evidence. Separate signed infrastructure qualification from imported-image evidence and explicit approval. Imports start unapproved; runtime cannot sign its own qualification. A changed provider/import profile is qualified before live admission. Retire images explicitly and delete only when no retained deployment references need them.

Preparation may wait for approval, then rechecks current actor authority, explicit user/team owner, quota, bindings and qualification. Participants may prepare only published templates they may launch without gaining author or image-administrator permissions. A team member leaving during preparation cannot admit a deployment using their former authority.

## Compatibility and validation

Preserve old wire fields, immutable revision identities, provider state and checksummed migrations. Use additive migrations and negotiated contracts. Test authorization, crash recovery and SQLite/PostgreSQL parity at changed boundaries. See [implementation contract](../design/portable-template-collaboration.md) and the dependency-ordered tasks in `design/checklist.yaml`.
