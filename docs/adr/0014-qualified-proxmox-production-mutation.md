# ADR-0014: Signed exact-target authorization for Proxmox production mutation

- **Status:** Superseded
- **Date:** 2026-09-03
- **Supersedes:** None
- **Superseded by:** ADR-0017

This historical decision is superseded by ADR-0017. Consult the successor for current behavior; the original reasoning is retained below.

## Context and constraints

The fixed-node PVE 9.x QEMU implementation and its target-applicable T3 matrix have passed on the disposable qualification installation. The production constructor remains hard closed because neither `mutationEnabled`, a successful plugin handshake, nor signature validity alone proves that the running provider binary, current target, reviewed topology, least-privilege ACLs, and cleanup result are the qualified combination. Production also needs key rotation and revocation, bounded evidence lifetime, visible target status, and periodic capability refresh without turning target configuration into a write switch.

An independent deployment audit confirmed the signed testbed artifact is authentic and qualifying for its exact disposable target and candidate, while also confirming that it cannot authorize another installation or the current production process. The audit identified mandatory operational gaps: no production artifact loader, a mandatory Docker dependency, no target-status API/UI, no periodic refresh, and no Proxmox package/configuration path.

## Considered options

1. Enable writes whenever strict target configuration contains `mutationEnabled: true`. This is simple but makes the qualification artifact irrelevant and recreates a configuration-only bypass.
2. Verify evidence only in `labd` and pass a boolean to the provider. This lets a compromised or misconfigured host/provider boundary lose the exact build and live-target checks.
3. Construct the production provider only with an immutable signed-evidence authorizer and re-evaluate it against fresh target facts before capability advertisement and every provider mutation.

## Decision

Use option 3.

The normal provider constructor remains discovery-only. The only exported write-capable constructor requires a `MutationAuthorizer`; the package-private T0 constructor remains test-only. The production executable accepts a signed qualification document and a separate deployment-owned trust policy together or neither. Both are bounded, non-symlink regular files with no group/other write permission and are read once at startup. Partial configuration fails startup.

The closed `labcentral.proxmox-qualification-trust/v1` policy contains one or more unique, sorted Ed25519 key IDs, canonical public keys, validity intervals, explicit revocation state, and a deployment maximum evidence age no greater than the evidence format's 30-day maximum. Multiple active keys permit overlap during rotation. A revoked key is retained in policy history and never selected. Trust-policy changes require a provider restart so one process has one auditable authorization view.

Authorization verifies and binds all of the following:

- the trusted signature, passing case/probe/cleanup result, evidence validity interval, and deployment maximum age;
- target ID, installation ID, raw target-configuration SHA-256 fingerprint, capability/inventory/policy digests, and exact PVE release/build;
- the running provider executable's self-digest and a build-embedded deterministic source-tree digest;
- plugin version, protocol 1.1, and the required allocation/dependency feature set;
- exact destination node, VMID/VLAN ranges, template identities and digests, storage IDs/types/formats, active VLAN-aware bridges, and permission scopes.

The provider repeats live inventory and authorization checks before capability advertisement and before apply, destroy, or lifecycle writes. Existing ownership, plan, allocation, permission, template, journal, and optimistic-concurrency checks remain mandatory and independent. Missing, invalid, expired, revoked, stale, or mismatched evidence returns an unsupported capability or `qualification_gate_closed` before a Proxmox write. Target configuration alone never opens the gate.

`labd` refreshes registered target evidence periodically within the five-minute capability TTL and exposes redacted registration/discovery status through the HTTP API and static UI. Docker may be disabled so a Proxmox-only deployment does not require Docker-socket authority. A production distribution includes the Proxmox provider, checksum, qualification verifier, configuration examples, and a least-privilege service definition.

## Consequences

Every production target and candidate binary needs its own protected qualification run and reviewer signature. Provider upgrades, target-configuration changes, PVE build changes, template/config/content changes, relevant inventory drift, signer revocation, or evidence expiry close new writes until requalification. Routine utilization counters remain excluded from the mutation inventory digest.

Qualification renewal is an explicit operational procedure rather than an automatic self-approval path. The signing private key never resides on the LabCentral runtime host. Operators can stage overlapping reviewer keys, restart, verify the new target status, then revoke and remove an old key in a later restart.

The first production scope remains the qualified Linux/amd64, fixed-node, full-clone subset. Snapshot/restore/reset, image import, richer guest provisioning, access, dynamic placement, and multi-node scheduling remain separately capability-gated work; this decision does not imply their support.

## Security implications

An attacker must compromise both runtime deployment inputs and a trusted reviewer key to authorize a different configuration, and must still pass fresh live inventory, ACL, ownership, allocation, and journal checks. Filesystem permissions are defense in depth; production deployments must keep provider credentials, evidence, trust policy, and journal paths outside guest reach and use separate OS identities and management-network policy.

Failing closed after expiry or revocation can defer cleanup as well as creation. Operators therefore renew evidence before expiry and retain the protected qualification/cleanup workflow. A later emergency-cleanup design, if required, must be a distinct ownership-fenced and audited authorization path rather than weakening this gate.

## Compatibility implications

The plugin protocol and existing target-config v1.1 schema do not change. The new trust-policy document is independently versioned. Old provider binaries ignore no new configuration because the host passes the new arguments only to a matching packaged provider. Existing read-only deployments continue to use the discovery-only constructor without qualification inputs.
