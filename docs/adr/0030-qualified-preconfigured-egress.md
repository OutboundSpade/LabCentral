# ADR-0030: Qualified preconfigured IPv4 lab egress

- **Status:** Accepted
- **Date:** 2026-09-20
- **Supersedes:** None
- **Superseded by:** None

## Context

TASK-LAB-001 needs four isolated VMs with Internet access. The first Proxmox
mutation subset supported only denied egress. Allowing a guest to enforce its
own boundary cannot satisfy ADR-0004 when students administer that guest.

## Decision

Extend the closed Proxmox target configuration with version v1.5. An optional
network-profile `egress` object selects `mode: allow`, an exact IPv4 CIDR and
gateway, external policy and measured probe digests, and explicit successful
Internet and failed management/cross-instance reachability results. Signed
exact-target qualification binds those settings and the infrastructure profile.
Configuration alone never authorizes writes or establishes isolation evidence.
Old target versions and denied-egress plans retain their existing semantics.

The provider selects an administrator-preconfigured profile matching the lab's
explicit egress mode and CIDR, allocates its VLAN normally, and configures the
gateway through typed cloud-init addressing. It does not create routes, NAT,
firewalls, router VMs or bridges. Gateway, network and broadcast addresses are
not workload addresses. Allowed and denied profiles use disjoint VLAN pools;
multiple profiles may share a bridge only with disjoint VLAN allocations.
Unsupported modes, CIDRs and missing evidence fail validation.

A trusted infrastructure boundary outside all student-controlled workloads
provides outbound IPv4 and stateful replies. It denies management, private,
link-local, metadata and cross-lab destinations and prevents source spoofing
from selecting another lab's routing context. No inbound Internet exposure is
implied. IPv6 is denied unless separately implemented and qualified. Guest
administration, guest firewall changes and extra VLAN tags must not bypass this
boundary. Internet reachability is not permission to reach management addresses.

A deployment may use an operator-owned router with one isolated network namespace
per VLAN, separate transit addressing and stateful filtering/NAT. This is an
operator implementation, not portable lab syntax. Identical guest CIDRs in
different labs remain isolated. Router configuration, ownership, restart
behavior, deny ranges and recovery instructions remain protected operator
records. The router has no core/provider credentials or student login path.

## Imported guest CPU compatibility

The same target-config v1.5 permits an optional closed `imageImport.cpuModel`
choice of `host` or `x86-64-v2-AES`. Omission retains the previous import behavior.
The operator selects this bound target fact, never a guest-authored arbitrary
QEMU argument. Imported VM configuration and approval bind the selected model.
Older configuration versions reject this field. The fixed-node provider does not
promise live migration portability for `host`; a changed model or target requires
new image evidence and infrastructure qualification. This lets RHEL-compatible
x86-64-v2 images run on a verified capable host rather than relying on the
Proxmox REST CPU default.

## Validation and operation

Before enabling a changed profile, verify intended same-lab communication,
public Internet/DNS access, negative management and cross-lab access, spoofed
sources, VLAN-tagged traffic, IPv6 denial and restart persistence. Test at least
two allocated VLANs with overlapping guest CIDRs. Retain measured transcripts
and policy identities, then execute and review the exact provider/configuration
qualification. Reconfiguration or expiry invalidates bound evidence; runtime
never signs its own authority. Existing denied-egress labs must remain denied.

Infrastructure failure closes egress; it must not attach guests to a management
network. Preserve existing allocation ownership and state compatibility. This
extends the supported subset under ADR-0004 and ADR-0013 without weakening their
requirements. The same private-testbed delegated review established in
ADR-0018/ADR-0020 applies to this owner-requested deployment; all technical gates
remain, and it is not independent human review or production approval.
