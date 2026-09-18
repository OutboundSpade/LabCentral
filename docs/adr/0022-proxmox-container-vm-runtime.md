# ADR-0022: Offline containers in dedicated Proxmox VMs

- **Status:** Accepted
- **Date:** 2026-09-15
- **Supersedes:** None
- **Superseded by:** ADR-0028 (only the scope explicitly identified there)

## Decision

Implement the owner's requested Proxmox container-inside-VM path with one dedicated
VM per Container. Require explicit `isolation: virtualMachine` and
`realizationPreference: [viaVM]`. Count both a container and a VM against quotas;
CPU and memory describe the whole guest, including runtime overhead.

An optional `proxmox.target-config/v1.3` runtime profile selects an immutable,
approved base template and digest-pinned OCI images preloaded into its rootless
Podman store. Discovery advertises this realization only for a qualified target
with that profile. Existing v1.1/v1.2 configurations and VM plans remain valid.
`proxmox.qemu-plan/v1.1` adds the closed `labcentral.container-vm/v1` bootstrap
contract; its binding covers installation, instance, logical resource, generation,
image, literal environment and argv. Physical provider state remains a QEMU VM.

Use the existing isolated VLAN, static IPv4, denied-egress and ownership checks.
The first subset rejects mounts, published ports, secret references, other
realizations, and mixed base-template profiles. Images must already be present;
there is no runtime pull or guest route to management. VM recreation resets state.

The trusted image installs a fixed guest entrypoint and systemd service. The
provider invokes only that entrypoint through QGA with bounded typed configuration,
never an author-selected executable on the guest host. The entrypoint atomically
binds the guest once, executes Podman as the dedicated unprivileged workload user,
drops capabilities and prevents new privileges. Author command/args execute inside
the container. Host networking means this dedicated VM's isolated network only.
A matching replay is idempotent; a different binding fails. Systemd restores the
workload after a VM restart. Readiness requires the workload to be running.

## Security and compatibility

This narrows the previously planned shared runtime in the provider contract; it
extends the initial QEMU subset's prohibition on guest execution only for the
fixed runtime entrypoint. PVE requires `VM.GuestAgent.Unrestricted`: permit it only
on the exact configured pool when the optional runtime profile is enabled. It
remains forbidden otherwise. No credentials, authorization decisions or database
access move into guests. Compromise of a guest can falsify its readiness but cannot
change provider ownership or allocation authority. The outer VM and qualified
network are the isolation boundary; rootless containers provide defense in depth.

The provider binary, target profile, template, permissions and runtime are changed
qualification inputs. Fresh T3 and real container start/restart/isolation/cleanup
checks precede the delegated the private testbed rollout. This does not claim independent
production qualification. Public plugin RPC messages remain unchanged.

Qualification evidence v1.1 adds the exact runtime profile and a required transcript
digest for container-specific checks. The existing signed-envelope version and
signature domain remain unchanged; the signed canonical payload includes its
version. Existing v1 evidence remains valid only without a runtime profile.

Adding this profile must preserve cleanup of existing labs. Destroy may refresh
only qualification metadata in an old plan, after its canonical resource,
physical profiles, allocations and prior owned state still match the current
allowlist. Fresh current-target authorization and remote ownership checks remain
mandatory. Creates cannot use this refresh; removed or changed physical bindings
continue to fail closed. Journals retain the original request identity.
