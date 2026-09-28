# LabCentral v2 architecture

This is the living architecture document. The v2 implementation is independent
of the previous implementation; Git retains its history. No legacy migration or
compatibility behavior is part of this design.

## Scope and boundaries

One Linux amd64 operator runs a Go library and optional CLI against AWS EC2 or
Proxmox. Initial guests are Ubuntu 24.04 amd64. Labs contain images, named machines,
one private IPv4 segment, access requirements, and optional typed Ansible inputs.
The same source directory is intended to work unchanged on both platforms.

```
author source + operator environment
                  |
           public Go library ---- SQLite intent journal + private credentials
                  |
        typed v2 gRPC over private Unix socket
                  |
          platform process ---- provider-owned recovery journal
            |           |
       native APIs    pinned Packer ---- guest-local Ansible
            |
       owned lab VMs ---- verified SSH / authenticated loopback browser relay
```

The CLI handles presentation and confirmation. The library accepts contexts,
returns typed results/errors, and emits structured progress. Root portable types
import no provider SDK. `platform/aws` uses the AWS SDK, `platform/proxmox` a bounded
HTTPS API client, `build` generates platform-owned Packer configuration, and `access`
handles SSH and desktop transports. The public process client also allows an
external application to use the same implementation.

The provider never opens the library database. It owns a separate mutation
journal for native subresources. The current provider executable routes both
platforms in one separate process; splitting it into platform-specific binaries
is possible without moving portable logic into adapters. The provider is trusted
operator code, not a sandbox for arbitrary third-party executables. The protocol
is generated from typed Go structures, uses explicit version 2 envelopes, bounds
message size, and rejects unsupported versions. Resource-specific opaque bytes
are adapter state, never author-supplied portable manifests.

## Source, environment and plans

Strict YAML rejects unknown/duplicate fields, aliases, multiple documents,
unsupported topology, out-of-bound resources, missing files, and undeclared inputs.
Source snapshots reject symlinks and unsafe paths and have bounded size/file count.
The digest binds file bytes to the parsed lab. Plans bind source, effective input
values, environment, base identities, and build toolchain; modified plans are
revalidated before mutation. Deployment copies machine entries before adding
native image references, addresses and keys, so one validated plan can deploy
independent team instances without changing the caller's plan.
AWS instance selection uses an operator allowlist and
reports allocated capacity. Proxmox reserves disjoint VMIDs and per-VLAN /24s.

Environment files contain target identifiers, credential references, base bindings,
network requirements, and toolchain pins. They belong outside tracked source.
State contains snapshots, effective plans, provenance, operation records, retained
resource references, and allocations. SSH private keys live separately from the
SQLite records. Input values are not a secret-storage interface.

Sharing is through Git or local directories. There is no package manifest service,
registry, template expression language, loop expansion, inheritance, or author
Packer extension. Ansible remains ordinary author code running inside build guests.

## Builds

Packer 1.14.3 with amazon 1.8.1, proxmox 1.2.3, and ansible 1.1.4 produces AMIs or
Proxmox templates. The fixed bootstrap installs pinned ansible-core in a guest
virtual environment. `ansible-local` receives a generated variables file; author
values do not enter host shell commands. Build infrastructure credentials remain
in the operator/provider/Packer processes. Builds have bounded logs and timeouts.
Source directories and logs are private operator records. The platform desktop
installation omits optional recommended packages to keep build and guest overhead
bounded. Desktop build-policy revisions participate in the cache key without
invalidating unchanged server images.

Each build reserves an owner identity and native identifiers before mutation.
Native tags, descriptions, explicit VMIDs, image names, and keypair identities
support discovery after interruption. A captured image is unpublished until a
disposable instance passes SSH/cloud-init readiness and declared desktop service
checks and cleanup succeeds. Validation checkpoints preserve progress across
cleanup interruption. Cached artifacts are checked for identity/existence before
reuse. Mutable package repositories prevent promises of byte-identical rebuilds.

Proxmox clone builds preserve the base root disk. The environment must provide a
base large enough for installed software and a working guest agent before Packer
needs IP discovery. Deployment can grow disks. No mandatory image export, disk
conversion, or nested virtualization is used.

Primary sources, retrieved 2026-09-27:
[amazon-ebs](https://developer.hashicorp.com/packer/integrations/hashicorp/amazon/latest/components/builder/ebs),
[proxmox-clone](https://developer.hashicorp.com/packer/integrations/hashicorp/proxmox/latest/components/builder/clone),
[ansible-local](https://developer.hashicorp.com/packer/integrations/hashicorp/ansible/latest/components/provisioner/ansible-local),
[pinned Proxmox clone configuration](https://github.com/hashicorp/packer-plugin-proxmox/blob/v1.2.3/builder/proxmox/clone/config.go).

## Native networking and access

Each AWS lab owns its VPC, subnet, route table, Internet gateway, and security group.
Public IPv4 is ephemeral. Operator CIDRs limit SSH/DCV ingress; same-group rules
permit lab communication; default general egress is removed. IMDSv2 is required.
Non-desktop guests have no instance profile. Desktop profiles must contain a
single inline regional DCV-license read permission and no managed policies; the
adapter adds a narrowly scoped S3 endpoint. Build networking belongs to the operator
and is checked separately from runtime networking.

AWS DNS and metadata traffic are special cases outside ordinary security-group
filtering. This is not an air gap. Role contents and host/network administration
remain trusted operator responsibilities, including changes made after validation.
Sources retrieved 2026-09-27:
[security-group behavior](https://docs.aws.amazon.com/vpc/latest/userguide/vpc-security-groups.html),
[DCV licensing](https://docs.aws.amazon.com/dcv/latest/adminguide/setting-up-license.html),
[short-lived AWS CLI authentication](https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-sso.html).
Console browser login can feed both SDK and Packer through a separate
credential-process profile. Source retrieved 2026-09-28:
[AWS console login and process credentials](https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-sign-in.html).

Proxmox uses one configured node/storage/pool, an exclusive VMID range, and existing
isolated VLANs. VM rules default to DROP with intra-lab and operator SSH allowances.
The cluster firewall and VLAN-aware bridge are checked; physical isolation and
operator routes require external verification. The adapter never changes host
bridges, routing, or global firewall policy. A disabled cluster firewall is a hard
preflight failure. HTTPS verifies server identity by default; an explicit environment
opt-out is reserved for operator-authorized targets.
Source retrieved 2026-09-27:
[Proxmox firewall documentation](https://pve.proxmox.com/wiki/Firewall).

SSH uses per-instance keys and pinned host identities, no agent forwarding, terminal
resize and remote exit status. Machine-name mappings are written at first boot.
DCV runs system authentication and named per-access virtual sessions. Both session
ownership and the desktop process user are set to the temporary guest account.
Plans disclose the regional S3 licensing exception. Its certificate
is fetched over verified SSH and pinned by the loopback TLS proxy. Image capture
removes generated DCV certificates so guests generate their own identity. Proxmox uses a
bundled noVNC 1.6.0 client and encrypted upstream console connection; management
tokens remain in the provider process. Both paths create temporary guest login
accounts and disclose passwords only in explicit access output.

Loopback relays enforce a single-use launch token, authenticated cookie, exact
Host/Origin checks, expiry, and closure of active connections. Closing access
removes its temporary guest session/account; DCV revocation terminates the user's
remaining processes before deleting the account and verifies session absence.
After a process crash, recovery uses
the durable access intent. The CLI browser relay is local. The hosted service wraps it with team authorization
and a separate desktop origin, described below. It does not provide an AWS boot console.
Source retrieved 2026-09-27:
[DCV TLS certificate handling](https://docs.aws.amazon.com/dcv/latest/adminguide/manage-cert.html)
(retrieved 2026-09-28),
[DCV virtual-session users](https://docs.aws.amazon.com/dcv/latest/adminguide/managing-sessions-start.html)
(retrieved 2026-09-28),
[DCV packages and published checksums](https://www.amazondcv.com/)
(retrieved 2026-09-28),
[noVNC source and licensing](https://github.com/novnc/noVNC/tree/v1.6.0).

## Durability and cleanup

SQLite uses WAL and FULL synchronization. A state-root file lock serializes infrastructure mutations;
per-instance locks coordinate access and destruction without blocking unrelated
running labs during builds. Status queries remain available. Native create intent is recorded before calls.
Native idempotency keys are used where supported. Provider journals persist
subresource intent and results with fsync and atomic rename. An uncertain create
is discovered through its exact recorded ownership identity; timeout is never
interpreted as nonexistence. Ambiguous/missing results retain allocations.

Successful earlier deployment steps remain after a later failure. Recovery resumes
the original operation; destroy first reconciles uncertain creates and then removes
owned resources in reverse dependency order. Image removal is separate and checks
retained instance references. AMI snapshots and Proxmox template disks are included
in image cleanup. Failed cleanup leaves a recoverable record. The CLI has no scheduler; operator failure can leave resources and charges active.
The hosted worker enforces persisted meeting deadlines while its host runs and
immediately queues overdue cleanup after restart.

One state root exclusively owns its configured allocation ranges. Cross-operator
locking and reservation are deferred. Deletion or corruption of local state is
outside automatic recovery guarantees; backups must include provider journals and
credentials. Uncertain mutations that cannot be authoritatively reconciled require
operator investigation instead of releasing possibly live resource identities.

## Acceptance

The implementation includes the library, CLI, typed process protocol, SQLite
journal, AWS/Proxmox adapters, Packer configuration generation, SSH, browser relays,
three portable examples, and an independent Go consumer. Local tests exercise
schema rejection, ownership, response loss, interrupted cleanup, allocation
collisions, concurrent access, plugin transport, role policy and relay authentication.
Real pinned Packer configuration validation is available as an opt-in test.

**Release acceptance remains incomplete.** The unchanged single-server and
workstation examples have completed live AWS Packer/Ansible builds, disposable
image validation and deployment. Focused checks passed for verified SSH, nginx,
required IMDSv2, and denied public HTTP ingress and egress. Live DCV checks cover
display, guest authentication, keyboard input, launch-token replay, foreign origins,
disconnect and revocation. An expired AWS login interrupted validation cleanup;
recovery resumed that cleanup and reused the image without duplicating the build.
Temporary build and validation VMs and networks were removed; the requested labs,
images and operator build infrastructure remain retained. This does not establish
cross-lab isolation or full lifecycle acceptance. Initial Proxmox discovery
found missing target prerequisites. Detailed evidence remains outside tracked source.
The following still require live verification:

- All three unchanged examples build/deploy/access/destroy on both platforms.
- Real provider interruption, response loss, stale ownership and cleanup scenarios.
- Network probes for intra-lab traffic, operator ingress, egress and cross-lab denial.
- Live Proxmox/noVNC display, keyboard, guest authentication, replay, disconnect and revocation.
- Full template/AMI/snapshot/disk cleanup and a zero-unreported-leftovers inventory.
- Packaging of the exact live-verified Linux amd64 binaries and toolchain checksums.

Service readiness checks are not browser acceptance. Adapter tests are not live
provider evidence. `make package` produces a development archive; it is not a
release qualification step. Do not label it a stable release until these checks pass.

## Proposals after the foundation

Registries, additional hosted platforms, Windows, routed/multiple NIC topologies,
classroom governance beyond meeting/team membership, and EC2 Image Builder are deferred. They should use the public library without putting product policy
into portable lab declarations. These are proposals, not implemented contracts.

## Hosted team service

The hosted milestone adds a separate application layer around the public library:
OIDC provider authentication, transactional team membership, a durable infrastructure
worker, and user-owned desktop workspaces. The library remains independent of
HTTP and identity policy. This milestone targets AWS; Proxmox remains a library
and CLI platform. The hosted service and twenty-user acceptance are not yet qualified.

Native CloudFormation under `tools/hosted/` defines one t3.medium control host,
encrypted retained EBS, an encrypted private S3 bucket with seven-day backup
retention, HTTPS ingress, and account spending alerts. The host uses an EC2 role;
GitHub Actions exchanges its `main` branch identity for temporary maintenance
credentials. The maintenance role permits a fixed SSM document against hosts
marked as this service and release uploads to the installation bucket. No permanent
AWS keys or browser-login refresh dependency are introduced. Infrastructure setup
still requires an administrator; runtime cannot pass its own management role to labs.

Hosted deployment, recovery, cleanup and backup use `Hosted maintenance` workflow
dispatches. Installation identifiers belong in protected operator records and
GitHub repository variables, not source. Application rollout consumes an exact,
checksum-verified release archive. The trusted `main` maintenance workflow has
GitHub Contents write permission because draft releases require push access; it
uses that access to download staged artifacts, without publishing them
([GitHub release visibility](https://docs.github.com/en/rest/releases/releases#list-releases),
retrieved 2026-09-28). Production DNS remains an explicit cutover step.

Primary sources, retrieved 2026-09-28:
[AWS role guidance](https://docs.aws.amazon.com/IAM/latest/UserGuide/best-practices.html),
[GitHub OIDC on AWS](https://docs.github.com/en/actions/how-tos/secure-your-work/security-harden-deployments/oidc-in-aws),
[DCV Web SDK](https://docs.aws.amazon.com/dcv/latest/websdkguide/getting-started.html),
[SvelteKit static generation](https://svelte.dev/docs/kit/adapter-static).


The `hosted` package owns typed users, server-side OIDC sessions, meetings, teams,
memberships, template revisions, jobs and desktop ownership. SQLite transactions
reserve destination capacity before a team switch. Access is blocked while the
switch is pending; guest revocation must succeed before committing membership.
Per-user locks serialize those changes with that user's launch. Provider plugins
receive explicit principal, expiry and workspace-retention options, never team
policy or database access. CLI access remains disposable by default.

OIDC uses authorization code, PKCE, state and nonce with verified issuer, audience,
signature and expiry. An explicit HTTPS backchannel origin can route machine
requests directly to the IDP while retaining canonical issuer verification and
browser authorization URLs. HTTP-only secure host cookies contain opaque session IDs;
tokens remain server-side. Exact-origin CSRF checks protect writes. Userinfo checks
refresh group authorization every four minutes; desktop policy checks run every
20 seconds and fail closed on revocation or unverifiable identity. Checks run
concurrently across users and cancel unauthorized transports immediately; durable
guest cleanup proceeds independently through the worker. API credentials
used to bootstrap OIDC provider and temporary acceptance users are not runtime inputs.

One durable worker executes provisioning and cleanup. It preserves the reserved
engine instance ID across retries, reconciles incomplete work, and prioritizes
meeting destruction over ordinary queued work. Ending a meeting interrupts active
provisioning; failed cleanup remains reported and retryable. Page reads and
unrelated desktop launches do not share that worker's infrastructure lock.
Provider journals distinguish explicit AWS authorization rejection from uncertain
transport outcomes. A rejected create can be retried; a timeout still requires
discovery. AWS creation permissions cover tagged outputs and separately authorize
their already-owned parent resources ([EC2 authorization reference](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html), retrieved 2026-09-28).

Repository import uses a dedicated read-only deploy key and verified GitHub host
keys. Each import discovers `templates/*/lab.yaml`, pins its commit, validates and
snapshots source, then builds native images once for reuse. Imports never update
running labs. Markdown disables raw HTML. The UI is prerendered route HTML with
SSR enabled during generation, no SPA fallback, and full-page navigation.

Desktop launch tickets are one-use and expire after one minute. The separate
origin exchanges them for host-only cookies bound to the application session,
user, team, instance and expiry. Every HTTP request is authorized; established
WebSockets are cancelled after revocation. The pinned official DCV Web Client SDK
uses transient guest credentials without a second sign-in. A student's deterministic
workspace identity permits reconnects and retains the home directory. Revocation
closes sessions, kills guest processes and locks the account; destroying the VM
removes retained workspaces. Teammates have training administration of shared
machines, so their workspaces are not a security boundary.

Hosted release acceptance still requires real OIDC/passkeys, twenty simultaneously
connected desktops across five groups for sixty minutes, application exercises,
network isolation, reconnect/switch/revocation, cleanup inventory and backup restore.
A local browser fixture establishes layout and UI flow only. AWS quota approval
must precede five-team provisioning. Budgets provide alerts, not a spending cap.
