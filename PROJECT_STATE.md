# LabCentral project state

**Updated:** 2026-09-15

LabCentral is a Go control plane with a static SvelteKit UI, SQLite/PostgreSQL,
durable jobs and separate provider/access plugins. Core owns authorization,
quotas, lifetime and ownership; plugins translate infrastructure operations.

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

The private PVE1 classroom runs **classroom-usability-20260915**. The provider,
runtime profile and core/UI were deployed at **2026-09-15T22:08:29Z**; the final
core access fix was deployed at **2026-09-15T22:22:23Z**. All six services are
healthy. All 95 pre-deployment records remain; verification labs and
allocation claims were cleaned up. The separate iad1 pilot remains ready.
Provisioning concurrency remains two; no target VM quota was imposed.

Qualification is valid until **2026-09-21T22:07:47Z**; the new pool-scoped,
privilege-separated runtime token expires **2026-09-22T21:41:38Z**. See the
[classroom record](docs/operations/classroom-testbed.md) for artifact bindings,
rollback and catalogue details.

## Verification

Affected provider/runtime, quota, allocation and access tests passed; database
conformance covers SQLite and PostgreSQL 18, including more than eight labs and
VM-backed container access. Go compilation, Svelte check/build, API contracts and
affected Chromium/Firefox/WebKit browser tests passed. Exact-build T3 lifecycle,
replay, recovery, container runtime and isolation checks passed before signing
fresh qualification. Live checks verified both examples, console switching/reload,
private container HTTP, short hostnames and Classroom bulk cleanup. Detailed
artifacts remain in ignored `bin/classroom-usability-20260915/`.

## Next action and production gates

Renew qualification before **2026-09-21T22:07:47Z**, then the runtime token before
September 22. Continue `TASK-PROD-004` and `TASK-PROD-005`: load/soak, cross-node
locking/failover, encrypted off-host recovery, TLS renewal, monitoring delivery and
reviewed release controls. Private testbed validation does not close these gates.
Within-lab parallel branches need multi-invocation recovery before implementation.
