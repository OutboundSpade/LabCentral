# LabCentral project state

**Updated:** 2026-09-15

LabCentral is a Go control plane with a static SvelteKit UI, SQLite/PostgreSQL,
durable jobs and separate provider/access plugins. Core owns authorization,
quotas, lifetime and ownership; plugins translate infrastructure operations.

## Latest completed work

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

The private PVE1 classroom runs **classroom-flow-20260915**, with final core
from `37a8acbeb2d81deffe83e82439ba1df298edabc9` deployed at **2026-09-15T19:38:07Z**.
Provider rollout and fresh qualification were installed at **2026-09-15T19:31:44Z**.
The rollout retained rollback binaries and integrity-checked database snapshots.
All 56 pre-existing lab records were unchanged. Both new VM exercises
launched with overlapping jobs, reached ready with four VMs, and were destroyed;
independent inventory confirmed cleanup. Temporary verification sessions were
retired. The separate iad1 pilot remains ready.

Qualification is valid until **2026-09-21T19:31:31Z**; the existing scoped
token expires **2026-09-22T18:40:20Z**. See the
[classroom record](docs/operations/classroom-testbed.md) for exact build hashes,
evidence, catalogue and rollback details.

## Verification

The full Go race suite passed with disposable PostgreSQL 18. Subsequent focused
race tests cover the final planner fix on both stores. Vet, Svelte check/build,
all four browser suites in Chromium/Firefox/WebKit, nine cache tests and
schema/protocol/design/link checks passed. Fresh exact-build Proxmox lifecycle,
recovery and isolation qualification passed. Two separate provider processes
submitted clone tasks concurrently and both cleaned up successfully.
See [verification](design/implementation-verification.yaml). Raw evidence remains
in ignored `bin/navigation-flow-20260915/` and `bin/classroom-flow-20260915/`.

## Next action and production gates

Renew qualification before **2026-09-21T19:31:31Z**, then renew the scoped
token before September 22. Continue `TASK-PROD-004` and `TASK-PROD-005`: live load
and soak, cross-node locking/failover, encrypted off-host recovery, trusted TLS
renewal, monitoring delivery and reviewed release controls. Private testbed
validation does not close these production gates. Within-lab parallel branches
need an extension to multi-invocation recovery before implementation.
