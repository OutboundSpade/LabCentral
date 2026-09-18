# LabCentral project state

**Updated:** 2026-09-18

LabCentral is a Go control plane with a static SvelteKit UI, SQLite/PostgreSQL,
durable jobs and separate provider/access plugins. Core owns authorization,
quotas, lifetime and ownership; plugins translate infrastructure operations.

## Portable templates and collaboration

The owner accepted ADR-0026–ADR-0029 and the dependency-ordered
`TASK-TEMPLATE-001`–`TASK-TEMPLATE-008`, `TASK-COLLAB-001` and `TASK-COLLAB-002`.
Contract decisions and consistency checks are complete (`TASK-TEMPLATE-001`);
the feature host (`TASK-TEMPLATE-002`) is complete. Installed pages, project-bound
typed API calls and denied cross-project bridge requests passed manifest/API,
Svelte type/build, independent-package and three-browser navigation checks.
Source packaging (`TASK-TEMPLATE-003`) is complete: deterministic locked source
archives, component/recipe composition, logical builder rebinding, authorized
export and import preview passed package/renderer/API/CLI tests, including import
into a fresh installation. GitHub synchronization (`TASK-TEMPLATE-004`) is
implemented: optional collection resolution, pinned recovery, atomic publication,
retained upstream removals, schedules and revocation passed source/API and
SQLite/PostgreSQL race tests. Isolated builders (`TASK-TEMPLATE-005`) are implemented: Linux amd64 Ansible qcow2 and BuildKit OCI builds, dependent builds, effective-input reuse, cleanup verification, worker process-loss recovery and external deadlines passed local VM acceptance. Typed dependency/protocol, race/database, generated API and independent-package checks passed. Proxmox import/preparation (`TASK-TEMPLATE-006`) is next. Portable exports contain build source and pinned source dependencies,
never VM/OCI images. The target release adds
GitHub synchronization, isolated builders, Proxmox imports, optional feature
packages, project groups and fenced shared-console control. The new packages have not yet been deployed; live build/import and collaboration
acceptance remain part of the release gate. See
[the implementation contract](docs/design/portable-template-collaboration.md).

## Independently installed extensions

`TASK-EXT-001` through `TASK-EXT-007` implement the contracts and working orchestration under ADR-0023–ADR-0025. Independently installed provider, access, source, builder and browser contributions use explicit bindings and separate backend identities. Source publication, durable builds/imports/preparation, gateway device approval, isolated browser slots and descriptor-based workload/network access are implemented.

External fixtures exercise source recovery, concurrent build deduplication, image approval, preparation and owned lifecycle against a prebuilt core/UI. Chromium, Firefox and WebKit exercise installed stream views, sandbox isolation and browser-approved gateway grants. SQLite/PostgreSQL race tests cover work fencing, shared cancellation, delegated access revocation and atomic prepared batch admission.

`TASK-EXT-008` is complete: native release `extensions-20260917` from commit `91c9ef0` reproduced byte-for-byte and passed the independent installation suite using only its packaged public SDK and unchanged core/UI. Normal/race SQLite/PostgreSQL, contracts, OpenAPI/Protobuf, three-browser, vet and vulnerability gates passed. The prior-core migration and matching database/artifact rollback rehearsal passed. `TASK-EXT-009` is complete: the authorized private rollout passed. Authenticated administration, provider apply/read/destroy, topology action discovery and the retained noVNC console/reload passed; all pre-existing lab records and qualified plugin/configuration bytes remained unchanged. Test resources, allocation claims and temporary browser sessions were cleaned up. The protected publisher ceremony and existing production-validation gates remain separate. Production Fly, WireGuard, SSH, AWS and Fedora CoreOS integrations remain outside this implementation. The newer GitHub and image-builder implementations are described above; their production rollout remains pending. Existing production-validation gaps remain open.

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

## Next action and production gates

Continue the dependency-ordered template/collaboration tasks, starting with
`TASK-TEMPLATE-006`. Existing production work remains open: `TASK-PROD-004` and `TASK-PROD-005`: load/soak, cross-node
locking/failover, encrypted off-host recovery, TLS renewal, monitoring delivery and
reviewed release controls. Private testbed validation does not close these gates.
Within-lab parallel branches need multi-invocation recovery before implementation.
