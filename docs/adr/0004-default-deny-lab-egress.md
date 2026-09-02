# ADR-0004: Isolated networks and denied egress by default

- **Status:** Accepted
- **Date:** 2026-09-02
- **Supersedes:** None
- **Superseded by:** None

## Context and constraints

Cybersecurity guests and traffic are hostile. Providers differ sharply in L2, network-policy, and egress controls.

## Considered options

1. Provider default egress: portable but unsafe and unpredictable.
2. Deny by default; require explicit allow policy and enforceability.
3. Permit egress but block known management ranges.

## Decision

Use option 2. `Network.kind: isolated` and `egress.mode: deny` are defaults. An explicit `allow` or `restricted` request becomes a required capability. If a target cannot prove enforcement, validation fails; it does not degrade silently. External networks and published/proxied services are explicit. Control/provider management CIDRs and metadata endpoints are always deployment-policy denies, even where lab egress is allowed.

## Consequences

Some providers, especially portable Docker Desktop mode, cannot realize strong denial and must report constrained isolation plus a blocking policy error for hostile workloads. Router labs explicitly attach the router to a controlled egress network.

## Security implications

This reduces command-and-control, scanning, exfiltration, and management-plane attacks. Operators still must enforce physical/VLAN/VPC/CNI separation; the declarative model is not itself a firewall.

## Compatibility implications

Changing defaults requires a new Lab schema version and migration warning. Existing revisions preserve resolved values.
