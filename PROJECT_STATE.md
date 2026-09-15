# LabCentral project state

**Updated:** 2026-09-15

LabCentral is a Go control plane with a static SvelteKit UI, SQLite/PostgreSQL,
durable jobs and separate provider/access plugins. Core owns authorization,
quotas, lifetime and ownership; plugins translate infrastructure operations.

## Latest completed work

- `TASK-UI-007`: classroom students and their labs share one view; launch dialogs
  include inputs, lifetime and plugin options. Template and override forms derive
  from JSON Schema. Target settings persist provisioning concurrency and expose
  existing VM quotas. Operations shows Proxmox capacity and modal progress.
  Console reload/reconnect obtains fresh authorized sessions; compact toasts retain
  automatic expiry and navigation dismissal.
- `TASK-UI-006`: Launch lab is under My labs; nested views have consistent
  breadcrumbs. Every role can switch workspace in the sidebar. The desktop
  top bar is removed; floating notifications expire and clear on navigation.
- `TASK-EXAMPLES-001`: four examples published on PVE1: two Debian networking
  exercises and two clearly labelled Docker catalogue examples. Catalogue names
  support readable Unicode. [Author guidance](docs/users/templates.md) explains
  resource requirements and additional isolation/capability constraints.
- `TASK-SCHEDULING-001`: configurable per-target deployment limits, default one;
  PVE1 uses two. Database admission spans replicas and retains unknown work.
  Proxmox locks independent claims. Multi-VM planning now excludes earlier
  tentative allocations instead of proposing duplicate VMIDs/VLANs.
- Earlier maintenance removed tracked build output and separated
  [documentation by audience](docs/README.md). Workspace/plugin administration,
  consistent Classroom navigation and Operations charts remain implemented.

Each lab still executes resources sequentially in dependency order. Parallelism
currently applies to independent lab operations, under [ADR-0021](docs/adr/0021-bounded-independent-deployments.md).
No Docker environment was provisioned on PVE1.

## Current deployment

The private PVE1 classroom runs **classroom-controls-20260915**, deployed at
**2026-09-15T21:09:36Z**. This core/UI update retains the existing qualified
provider, access plugin, credentials and target configuration. A pre-deployment
SQLite backup and prior binary support rollback. All 76 records in that snapshot
were preserved; the temporary console verification lab was created and destroyed.
Live console reload connected through a fresh session. The provisioning limit is
persisted at two. No target VM quota was imposed by verification.

Qualification is valid until **2026-09-21T19:31:31Z**; the existing scoped
token expires **2026-09-22T18:40:20Z**. See the
[classroom record](docs/operations/classroom-testbed.md) for exact build hashes,
evidence, catalogue and rollback details.

## Verification

Affected API, control-plane and database packages passed race tests with SQLite
and disposable PostgreSQL 18. Vet, Svelte check/build, startup and affected browser
suites in Chromium/Firefox/WebKit, cache/schema tests and design contracts passed.
Live checks covered target settings, capacity discovery, classroom launch options,
console reload, mobile layout and test-owned cleanup. Existing provider qualification
was reused because its bound facts and executables were unchanged. Detailed logs
remain in ignored `bin/classroom-controls-20260915/`.

## Next action and production gates

Renew qualification before **2026-09-21T19:31:31Z**, then renew the scoped
token before September 22. Continue `TASK-PROD-004` and `TASK-PROD-005`: live load
and soak, cross-node locking/failover, encrypted off-host recovery, trusted TLS
renewal, monitoring delivery and reviewed release controls. Private testbed
validation does not close these production gates. Within-lab parallel branches
need an extension to multi-invocation recovery before implementation.
