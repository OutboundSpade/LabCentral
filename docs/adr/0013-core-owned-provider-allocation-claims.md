# ADR-0013: Core-owned opaque provider allocation claims

- **Status:** Accepted
- **Date:** 2026-09-02
- **Supersedes:** None
- **Superseded by:** None

## Context and constraints

Some provider values are globally scarce within a target. The first Proxmox mutation needs a unique QEMU VMID and may need a unique VLAN tag from an administrator-approved pool. A provider process cannot safely derive either value from a hash: collisions exist outside LabCentral, concurrent plans race, and a provider plugin must not access the control-plane database. The current protocol also plans before the core assigns an Instance ID and does not carry dependency plans or states, so a VM cannot safely consume the allocation selected for its logical Network.

## Considered options

1. Deterministically hash logical IDs into provider ranges and fail on collision.
2. Let each provider keep its own allocation database.
3. Let the core persist opaque, target-scoped allocation claims proposed by a provider.
4. Require every target to preassign all provider values in author input.

## Decision

Use option 3. The core owns a generic unique key `(targetID, namespace, value)` and treats namespace and value as bounded opaque strings. The provider owns their meaning, allowed ranges, selection order, and validation. A provider plan proposes claims; the core atomically reserves them, and a conflict causes bounded replanning with the conflicting values excluded. A committed plan and its claims are persisted together.

The core creates the stable Instance ID before provider planning. A backward-compatible plugin protocol v1 minor extension will carry instance identity, excluded claims, and dependency plans into planning; plan responses will carry proposed claims; apply will receive the committed claims and dependency states. Old plugins continue to negotiate the old minor and cannot advertise capabilities that require claims or dependency plan input.

Claims transition through `held`, `committed`, `orphaned`, and `released`. An operation may release an uncommitted hold only after it knows no provider side effect used it. Committed and orphaned claims remain unavailable until provider absence is positively confirmed. Lease expiry, a failed destroy, an unknown invocation, or a leftover never implies release.

The control plane does not infer provider capability, authorization, topology, billing, or safety from a claim. Providers still validate current inventory immediately before mutation, and an externally occupied value fails closed rather than being adopted.

## Consequences

Concurrent planning gains database-enforced uniqueness without expanding plugin trust. Plans and persistence gain explicit claim records and retry/replan behavior. The protocol, domain ports, relational schema, job checkpoints, and provider contract require additive implementation work before the Proxmox mutation can be enabled.

## Security implications

Plugins receive no database access. Namespace and value size/character limits prevent storage abuse, target scoping prevents cross-target collisions, and allocation does not establish ownership of an existing provider object. Destructive action still requires immutable provider identity plus an independently generated ownership token. Retaining claims for unknown or orphaned resources prefers resource leakage over collision and destructive reuse.

## Compatibility implications

The allocation schema and claim states are independently versioned. The plugin additions must be an additive protocol-minor change with new fields and feature negotiation; existing field numbers are not reused. Persisted jobs and plans record the selected protocol, claim schema version, and exact committed claims.
