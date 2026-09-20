# LabCentral project state

**Updated:** 2026-09-20

LabCentral is a Go control plane with a static SvelteKit UI, SQLite/PostgreSQL,
durable jobs and separate provider/access plugins. Core owns authorization,
quotas, lifetime and ownership; plugins translate infrastructure operations.

## Portable templates and collaboration

The accepted ADR-0026–ADR-0029 contracts and `TASK-TEMPLATE-001` through
`TASK-TEMPLATE-008`, `TASK-COLLAB-001` and `TASK-COLLAB-002` are implemented and
released on the authorized private testbed. Core retains typed semantics,
authorization, immutable revisions, ownership, quotas, durable operations and
Huma/OpenAPI. Template Library and Teams ship as independent feature packages;
Classroom is optional and is enabled on this installation. Core-only operation,
independent installation against unchanged prebuilt core/UI, typed bridge
permissions and compatibility routes passed their checks.

Portable packages contain build source and a pinned source dependency closure,
never VM disks or OCI archives. Deterministic packaging, composition, import
preview, export and local builder rebinding passed contract/API/CLI checks.
Export/import into a fresh installation with different bindings rebuilt both
Ansible and Dockerfile images, including shared-base dependencies. GitHub
collection synchronization supports atomic commit-pinned publication, unchanged
content reuse, retained upstream removals, schedules and authority revocation.
Its source plugin is installed; the owner deferred the repository/credential
binding. Synchronization alone never executes builds.

Linux amd64 builds run inside disposable VMs in a separate builder environment.
Pinned QEMU/Packer/Ansible produce qcow2 images; pinned BuildKit produces OCI
artifacts that the fixed runtime packager places in dedicated container VMs.
Dependency reuse, cancellation, limits, recovery and cleanup passed builder and
SQLite/PostgreSQL checks. Qualified Proxmox import uses reserved allocations,
ownership, per-resource image bindings, separate evidence and explicit approval.
Live participant preparation waited for approval, rejected participant
self-approval, and deployed a two-image Ansible lab and a Dockerfile HTTP lab.
In-guest checks verified both playbook configurations and the container content.
The reusable Ansible base includes the guest agent required for VM readiness.

Project groups enforce one active self-service enrollment per user/project without
changing organizational roles or memberships. Team-owned admission, concurrent
enrollment, isolation, pending-preparation departure and member lifecycle actions
passed authorization and database race/parity checks. Live users joined running
labs, left without deleting them, and retained team ownership independently of
the initiating user. Three authenticated console users shared the same display;
physical guest probes confirmed driver input and rejected raw viewer input.
Control handoffs, concurrent takeovers, release, departure, reconnect, revocation
and core/access-plugin outages passed. The native noVNC unit restarts after
heartbeat-triggered clean exits; reconnection never restores control automatically.

`TASK-TEMPLATE-008` is complete. Affected normal/race database, architecture,
protocol/OpenAPI, Chromium/Firefox/WebKit, vulnerability, independent-package,
migration/rollback and exact-target qualification gates passed. The final release
reproduced byte-for-byte and its installed files passed integrity verification.
Consistent database/artifact backups and prior packages were retained. Existing
lab records, organizational memberships and role assignments were preserved;
only test-owned VM/image resources and temporary sessions were cleaned up.
Host-bound evidence and rollback instructions remain in protected operator records.
Existing production load, recovery, failover and publisher gates remain open.
See [the implementation contract](docs/design/portable-template-collaboration.md).

## Feature page usability

`TASK-UI-011` repairs Template Library, Teams and Classroom with the shared core
theme, task-focused tabs, draft protection, accessible feedback and confirmations,
authorized group rosters and administrator oversight. Additive host version 4
provides permission-declared draft/navigation operations; older contributions
retain their host semantics. The reproducible release is deployed on the authorized private testbed.
Affected normal/race SQLite/PostgreSQL, API/architecture, three-browser,
independent-package and live role/enrollment checks passed. Consistent backups
and prior packages are retained, with qualified backend identities unchanged.
All existing lab, membership and role records were preserved; temporary
enrollment and browser sessions were cleaned up. See [the UI contract](docs/design/classroom-ui.md).

`TASK-UI-012` implements desktop group management, named shared labs, retained
Classroom launch history and console instructions. Participants receive only the
Teams feature; additive host version 5 lets packages declare scoped visibility
and safe Lucide icons. Administrators can prepare group-owned labs without joining
and delete groups after all their labs have ended. Deletion retains audit/ownership
identities, removes enrollment and fences pending admission. Existing organizational
teams retain their meaning. The reproducible release is deployed on the authorized
private testbed. Authorization, SQLite/PostgreSQL races, three-browser workflows,
exact-artifact plugin independence and the migration/prior-core rollback rehearsal
passed. A live administrator and two students verified group launch without admin
enrollment, Teams-only participant features, shared console handoff/instructions,
instructor roster/history and group deletion. Prior lab/group/membership/role
records and qualified backend bytes were preserved. The test VM, enrollments and
temporary browser sessions were cleaned up; backup and rollback evidence is kept
in protected operator records. Existing production gates remain open.

## Independently installed extensions

`TASK-EXT-001` through `TASK-EXT-007` implement the contracts and working orchestration under ADR-0023–ADR-0025. Independently installed provider, access, source, builder and browser contributions use explicit bindings and separate backend identities. Source publication, durable builds/imports/preparation, gateway device approval, isolated browser slots and descriptor-based workload/network access are implemented.

External fixtures exercise source recovery, concurrent build deduplication, image approval, preparation and owned lifecycle against a prebuilt core/UI. Chromium, Firefox and WebKit exercise installed stream views, sandbox isolation and browser-approved gateway grants. SQLite/PostgreSQL race tests cover work fencing, shared cancellation, delegated access revocation and atomic prepared batch admission.

`TASK-EXT-008` is complete: native release `extensions-20260917` from commit `91c9ef0` reproduced byte-for-byte and passed the independent installation suite using only its packaged public SDK and unchanged core/UI. Normal/race SQLite/PostgreSQL, contracts, OpenAPI/Protobuf, three-browser, vet and vulnerability gates passed. The prior-core migration and matching database/artifact rollback rehearsal passed. `TASK-EXT-009` is complete: the authorized private rollout passed. Authenticated administration, provider apply/read/destroy, topology action discovery and the retained noVNC console/reload passed; all pre-existing lab records and qualified plugin/configuration bytes remained unchanged. Test resources, allocation claims and temporary browser sessions were cleaned up. The protected publisher ceremony and existing production-validation gates remain separate. Production Fly, WireGuard, SSH, AWS and Fedora CoreOS integrations remain outside this implementation. The newer GitHub and image-builder implementations are deployed on the private testbed as described above; repository binding remains owner-deferred. Existing production-validation gaps remain open.

See [extension contracts](docs/design/extensions.md) and [author examples](docs/plugins/extensions.md).

## Latest completed work

- `TASK-UI-010`: administrators can permanently delete API clients with audited
  revocation and confirmed UI actions. Validation displays specific provider
  rejection reasons. Existing Proxmox support for one VM per container passed
  live two-machine, network, console and cleanup checks. Docker-only templates
  retain their distinct requirements. Host-bound records/scripts moved out of
  tracked source; reusable guidance and synthetic deployment examples remain.

- `TASK-UI-009`: Integrations groups Environments, Plugins and API clients;
  legacy provider/access services appear alongside installed contributions.
  Corrected registration, one-time credentials, error/retry and disable flows
  passed Blueprint and three-browser checks and run on the private testbed.

- `TASK-UI-008`: Classroom and My labs share confirmed bulk cleanup, including
  per-lab failure recovery. The console's Machine selector obtains fresh authorized
  sessions when switching guests; VM-backed containers support the same access flow.
- `TASK-ALLOC-002`: plans include occupied target claims, removing the accidental
  eight-lab allocation ceiling while retaining transactional conflict protection.
- `TASK-CONTAINER-VM-001`: qualified Proxmox targets support pinned, preloaded
  containers in dedicated VMs under [ADR-0022](docs/adr/0022-proxmox-container-vm-runtime.md).
  Guests use short names. Two runnable examples are published on the private testbed.
- Earlier work unified classroom students/labs and launch options, added schema
  forms, workspace switching, plugin settings, capacity charts and console reconnect,
  removed tracked builds, and separated [docs by audience](docs/README.md).

Independent lab operations run concurrently; resources within a lab remain
sequential in dependency order under [ADR-0021](docs/adr/0021-bounded-independent-deployments.md).
The container subset uses one VM per container, approved offline images and a
static private network. Mounts, secrets and published ports are not supported.
See [runtime guidance](docs/operations/container-vm-runtime.md).

## Deployment records

Host-specific deployment details and evidence are maintained in protected operator
records outside this repository. See [private testbed guidance](docs/operations/classroom-testbed.md).
No source-controlled example or prior result establishes current target qualification.

## Verification

API client deletion passed the full Go suite, SQLite/PostgreSQL race and
concurrency checks, API authorization, generated contracts, Svelte type/build,
cache and three-browser regressions. Blueprint approved the rendered UI. Native
firewall examples passed in a disposable namespace. The release reproduced
identically and passed external device-flow fixtures; live client deletion,
validation feedback and VM-backed container checks passed. Deployment evidence,
rollback bindings and temporary resource/session cleanup records remain private.


The integration administration update passed Blueprint (no issues/warnings),
Svelte check/build, cache tests, affected backend/vet and race tests. All three
browsers passed administration, responsive, extension isolation and device-flow
scenarios. Its native archives reproduced identically; the packaged SDK/core
passed source/build/import and browser fixtures. Authenticated the private testbed checks
confirmed inventory, forms, unchanged records, readiness and session cleanup.
Evidence remains in ignored `bin/integrations-ui-20260917/`.

The preceding extension foundation release passed the following broader gates:

Normal Go and race suites passed with SQLite/PostgreSQL 18, including durable
leases/fencing, concurrent deduplication, shared cancellation, delegated authority,
prepared atomic batch admission and artifact retention/recovery. Architecture,
schema, OpenAPI/Protobuf compatibility and canonical vectors passed. Svelte
check/build, existing browser regressions and extension/gateway scenarios passed
Chromium, Firefox and WebKit; vet and vulnerability gates passed. gRPC was updated
to its patched release before the final native build.

Two native archives reproduced identically. Independently compiled fixtures used
only the extracted release SDK and left its core/UI unchanged while exercising
source/build/import recovery, lifecycle, streams, credential replay/revocation and
bounded traffic closure with core suspended. Prior-core migration/rollback was
rehearsed before deployment. Live the private testbed checks and final database/cleanup/readiness
checks passed. Evidence remains in ignored `bin/extensions-20260917/`; earlier
provider qualification evidence remains in `bin/classroom-usability-20260915/`.

`TASK-UI-013` and `TASK-DEPLOY-001` are complete. Host 6 coordinates full-page
feature dialogs; desktop launches use two columns, rosters omit redundant IDs,
and tabs/bulk controls share one opaque sticky header. Teams supports bounded
multi-group launch, enrollment and deletion, plus multi-lab cleanup with per-item
progress and failed-only retries. Container images are the recommended distribution.
The exact tested image now runs core on the private testbed with preserved data
ownership and unchanged qualified backend services. Three-browser, exact-image,
independent-package and live administrator/two-student checks passed, including
Firefox dimming/scrolling, shared control, deletion guards and owned cleanup.
Consistent backups and rollback packages remain in protected operator records;
existing records were preserved. Existing production gates remain open.

## Next action and production gates

Configure the GitHub library binding when the owner supplies its repository and
credential details. Existing production work remains open: `TASK-PROD-004` and `TASK-PROD-005`: load/soak, cross-node
locking/failover, encrypted off-host recovery, TLS renewal, monitoring delivery and
reviewed release controls. Private testbed validation does not close these gates.
Within-lab parallel branches need multi-invocation recovery before implementation.
