# LabCentral project state

**Updated:** 2026-09-17

LabCentral is a Go control plane with a static SvelteKit UI, SQLite/PostgreSQL,
durable jobs and separate provider/access plugins. Core owns authorization,
quotas, lifetime and ownership; plugins translate infrastructure operations.

## Independently installed extensions

`TASK-EXT-001` through `TASK-EXT-007` implement the contracts and working orchestration under ADR-0023–ADR-0025. Independently installed provider, access, source, builder and browser contributions use explicit bindings and separate backend identities. Source publication, durable builds/imports/preparation, gateway device approval, isolated browser slots and descriptor-based workload/network access are implemented.

External fixtures exercise source recovery, concurrent build deduplication, image approval, preparation and owned lifecycle against a prebuilt core/UI. Chromium, Firefox and WebKit exercise installed stream views, sandbox isolation and browser-approved gateway grants. SQLite/PostgreSQL race tests cover work fencing, shared cancellation, delegated access revocation and atomic prepared batch admission.

`TASK-EXT-008` is complete: native release `extensions-20260917` from commit `91c9ef0` reproduced byte-for-byte and passed the independent installation suite using only its packaged public SDK and unchanged core/UI. Normal/race SQLite/PostgreSQL, contracts, OpenAPI/Protobuf, three-browser, vet and vulnerability gates passed. The prior-core migration and matching database/artifact rollback rehearsal passed. `TASK-EXT-009` is complete: that exact release runs on PVE1. Authenticated administration, provider apply/read/destroy, topology action discovery and the retained noVNC console/reload passed; all 102 pre-existing lab records and qualified plugin/configuration bytes remained unchanged. Test resources, allocation claims and temporary browser sessions were cleaned up. The protected publisher ceremony and existing production-validation gates remain separate. Production Fly, WireGuard, SSH, Git, Ansible, Dockerfile, AWS and Fedora CoreOS integrations remain outside this implementation. Existing production-validation gaps remain open.

See [extension contracts](docs/design/extensions.md) and [author examples](docs/plugins/extensions.md).

## Current interface work

`TASK-UI-009` simplifies administration under Integrations → Environments, Plugins
and API clients. It corrects client registration/error/credential presentation and
includes legacy provider/access services in the installed plugin inventory.
Blueprint passed with no issues or warnings; backend/race and three-browser
checks passed. The authorized PVE1 core/UI rollout and publication to main
remain pending. The extension contracts and isolation remain
unchanged.

## Latest completed work

- `TASK-UI-008`: Classroom and My labs share confirmed bulk cleanup, including
  per-lab failure recovery. The console's Machine selector obtains fresh authorized
  sessions when switching guests; VM-backed containers support the same access flow.
- `TASK-ALLOC-002`: plans include occupied target claims, removing the accidental
  eight-lab allocation ceiling while retaining transactional conflict protection.
- `TASK-CONTAINER-VM-001`: qualified Proxmox targets support pinned, preloaded
  containers in dedicated VMs under [ADR-0022](docs/adr/0022-proxmox-container-vm-runtime.md).
  Guests use short names. Two runnable examples are published on PVE1.
- Earlier work unified classroom students/labs and launch options, added schema
  forms, workspace switching, plugin settings, capacity charts and console reconnect,
  removed tracked builds, and separated [docs by audience](docs/README.md).

Independent lab operations run concurrently; resources within a lab remain
sequential in dependency order under [ADR-0021](docs/adr/0021-bounded-independent-deployments.md).
The container subset uses one VM per container, approved offline images and a
static private network. Mounts, secrets and published ports are not supported.
See [runtime guidance](docs/operations/container-vm-runtime.md).

## Current deployment

The private PVE1 classroom runs **extensions-20260917**, deployed at
**2026-09-17T02:45:21Z** from the verified native build at commit `91c9ef0`.
All six services are healthy. All 102 pre-deployment lab records remain unchanged;
one temporary verification lab was provisioned, accessed and destroyed, with its
allocation claims released. The prior provider/access binaries, configuration,
qualification and separate iad1 pilot were preserved. Provisioning concurrency
remains two; no target VM quota was imposed.

Qualification is valid until **2026-09-21T22:07:47Z**; the new pool-scoped,
privilege-separated runtime token expires **2026-09-22T21:41:38Z**. See the
[classroom record](docs/operations/classroom-testbed.md) for artifact bindings,
rollback and catalogue details.

## Verification

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
rehearsed before deployment. Live PVE1 checks and final database/cleanup/readiness
checks passed. Evidence remains in ignored `bin/extensions-20260917/`; earlier
provider qualification evidence remains in `bin/classroom-usability-20260915/`.

## Next action and production gates

The extension implementation and authorized testbed rollout are complete. Renew
qualification before **2026-09-21T22:07:47Z**, then the runtime token before
September 22. Continue `TASK-PROD-004` and `TASK-PROD-005`: load/soak, cross-node
locking/failover, encrypted off-host recovery, TLS renewal, monitoring delivery and
reviewed release controls. Private testbed validation does not close these gates.
Within-lab parallel branches need multi-invocation recovery before implementation.
