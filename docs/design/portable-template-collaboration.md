# Portable templates and collaborative labs

This is the accepted target contract under ADR-0026–ADR-0029. The task checklist
reports implementation and validation separately; this document is not evidence
that an integration is deployed. Implement tasks in the checklist's dependency order.

## Kernel and feature packages

Core owns typed lab semantics, rendering, immutable revisions, authorization,
project/team relationships, ownership, quotas, expiration, durable work, artifacts,
atomic admission and Huma/OpenAPI. Basic lab management and administration remain
built in. Source, builder, provider and access backends retain separate identities.
Template Library, Teams and Classroom are independently installed browser packages.
Standard distributions include Library and Teams; Classroom is opt-in. Groups and
collaborative access do not depend on Classroom. Existing classroom URLs route to
its installed contribution; missing packages cannot prevent core readiness.

Top-level `page` contributions use the existing isolated frame host. A page is
identified by installation/contribution, not an arbitrary URL. Its manifest declares
known typed bridge permissions. The host binds the active project, validates each
request and uses existing session/CSRF handling only outside the frame. Canonical
HTTP handlers repeat object authorization. There is no general fetch proxy, database
access, session-cookie delivery or arbitrary navigation. Immutable assets, origin
isolation, frame nonces, bounded concurrency/messages and channel teardown remain.

## Source portability and composition

A portable package includes lab YAML, scripts, Dockerfiles, Ansible playbooks,
configuration and a digest-locked vendored source dependency closure. VM disks,
OCI archives, secrets, target IDs, provider state and approval are excluded. Builds
and provider imports are separate internal workflows. Base image references and
required local capabilities remain declarations, resolved at the destination.

Packaging is deterministic; safe normalized regular-file paths, bounded archive
size/expansion/count/depth and exact manifest digests are required. Publication
resolves components and recipes to immutable content, rejects dependency cycles and
name collisions, and selects explicit representation/renderer semantics. Local
builder bindings satisfy logical recipe requirements without rewriting source.
Old YAML, bundle and rendering versions retain their meaning.

Import preview lists package metadata, dependency identities, required capabilities
and unresolved bindings without running code or downloading images. Export uses
the verified immutable revision and its source closure. Copying a synchronized
template creates an independent template; editing does not alter upstream history.

## Library synchronization

The GitHub source plugin resolves a configured repository, branch and library index
to one commit and bounded collection. Core validates every entry before publishing
the collection in one fenced transaction. Stable library entry keys bind to local
templates. Content identity, not fetch time, determines whether a revision is new.
Entries removed upstream become unavailable there; revisions, lab references and
running instances survive. Sync never triggers author code or image builds. The implementation uses
`labcentral.library/v1`, optional `source-collection-v1`, and migration
`0018_template_libraries`; old migration checksums remain unchanged. See
[GitHub library operations](../plugins/github-library.md).

Manual and scheduled sync use the same durable operation and original invocation
recovery. Default polling is fifteen minutes, one active invocation per binding,
with bounded backoff. Configuration records project, author and source binding;
publication rechecks authority. A revoked author pauses schedules. Credentials are
invocation-scoped; only configured repositories are reachable. Repository hooks,
submodules, workflow execution and arbitrary URL image import are disabled.

## Build and image lifecycle

The first builders support Linux amd64. QEMU/Packer with ansible-local produces
standalone qcow2. BuildKit produces OCI; a fixed packaging stage preloads the exact
OCI image into the approved container VM runtime. No runtime workload restriction
is relaxed. Author code and Ansible controller-side execution run inside disposable
VM boundaries with externally enforced CPU, memory, disk, time, output and network
limits. No provider/control credentials enter build guests. Toolchains and base
images are pinned, configured registries/package mirrors are permitted, and
provenance records effective inputs. Package-repository changes mean rebuilds are
not promised byte-identical. Guest identity and build credentials are removed.

Core executes bounded recipe dependency graphs through typed durable child work.
Deduplication includes effective context, resolved bases, architecture, toolchain,
configuration and declared inputs, within a project. Cancellation by one waiter
cannot cancel shared work. Large image outputs use streamed scoped artifact
transfer, never bundle archives or relational byte fields.

Proxmox imports use discovered upload/import capability, bounded staging storage,
reserved VMIDs, ownership and original invocation recovery. Each resource selects
its own immutable image binding; mixed-image labs are supported. Static-template
behavior is preserved. Infrastructure qualification remains signed and bound to
the actual provider/target/profile; imported image evidence binds artifact digest,
architecture, VM configuration and runtime contract separately. Import completion
never means approval. Runtime never signs its own authority. Images are retired
explicitly and only removed after proving retained references no longer need them.

First launch can queue missing build/preparation work and wait visibly for approval.
Admission rechecks authority, explicit owner, quotas, image evidence and target
qualification. Participants can prepare launchable published templates without
receiving author/import administrator authority. User and team single-owner launches
are distinct from instructor batch requests.

## Project groups

Administrators create one or a bounded batch of groups and open/close enrollment.
Users already authorized in a project can list eligible groups and join or leave
themselves. A unique project/user active-enrollment constraint permits only one
self-service group in each project. This is a project-limited ownership relation,
not organization membership or a role assignment; joining must never activate
privileged roles attached to an existing organization team. Existing manually
managed teams retain their old semantics.

Open groups may be joined while labs run, giving access to those labs. Closing
enrollment blocks new joins, not existing members. Any member may launch and use
otherwise-permitted lifecycle actions, with destructive UI confirmation. Labs stay
team-owned; creator remains the audit actor. Multiple labs obey normal quotas.
Leaving invalidates new access immediately and revokes active sessions within the
existing closure bound. Even the last member leaving does not delete/transfer labs;
TTL and administrator cleanup still operate. Pending admission and sensitive
operations recheck membership; a former member cannot admit a queued lab.

## Shared console

Every viewer has an individual one-time token and authorization-renewed access
session for the same VM. Shared RFB connections never evict other viewers. One
session per instance/resource/generation holds a fenced input lease. All others
watch. Take control initiates direct transfer; activation waits for old input
withdrawal acknowledgement or lease expiry. There is never an overlap of two
valid drivers, including replica races. Release, disconnect, group departure,
revocation, instance-generation change and expiry release control. Reconnection
starts without control. Presence includes only authorized user display information.

The access plugin parses bounded client messages and enforces keyboard, pointer,
clipboard-write and other guest-changing message authority. Unknown messages fail
closed. Browser viewOnly reflects this state but is not security enforcement.
Input leases cannot outlive authorization leases, and core/plugin failure cannot
revive authority. Handoffs and memberships are audited without keystrokes/screens.

## Verification and delivery

Test export/import and rebuild on a fresh installation with different bindings;
source-only archives, dependencies, cycles, malformed input and version rejection;
atomic sync, pinned commits, removal, authority loss, schedule races and recovery;
isolated builds, shared cancellation, limits, provenance and cleanup; import
failures, ownership, image approval/revocation and reference-aware retirement;
one-group races, closed enrollment, project isolation, no privilege escalation,
joining active labs, last-member departure and membership loss during admission;
shared display, hostile viewer input, handoff races, reconnect and outage closure.

Run affected Go/race/SQLite/PostgreSQL checks, contract/OpenAPI/Protobuf gates,
architecture checks, migration/rollback and three-browser feature scenarios.
Independent packages must install against unchanged prebuilt core/UI. Verify
representative multi-image allocation plans before live staging, then qualify
changed provider/access/import paths on the exact target.

Work on main. Settle and test implementation before release artifacts. Preserve
existing labs/enrollment and the separate pilot, take consistent database/artifact
backups and retain prior packages. Push verified work and deploy exact tested bytes,
smoke-test both builders and a multi-user group console, and clean only test-owned
resources. Host-specific records and evidence stay protected outside tracked source.
Production load/soak, failover, recovery and publisher gates remain open.

Windows, ARM, Docker-provider imports, post-launch Ansible, webhooks, image-containing
template exports, recording and general plugin workflow engines are out of scope.

Builder execution details and the typed recipe contracts are in [disposable VM builders](../plugins/vm-builders.md). The dedicated worker uses a private virtio-serial channel, mutually authenticated typed RPC relay, restricted egress proxy and independent systemd guest deadlines. These implement ADR-0028 without giving builder guests provider or control-plane credentials.
