# LabCentral project state

**Updated:** 2026-09-15

LabCentral is a Go control plane with a static SvelteKit UI, SQLite/PostgreSQL,
durable jobs and separate provider/access plugins. Core owns authorization,
quotas, lifetime and ownership; plugins translate infrastructure operations.

## Current implementation

`TASK-UI-006`, `TASK-EXAMPLES-001` and `TASK-SCHEDULING-001` simplify sidebar
navigation and timed feedback, add readable catalogue names and teaching examples,
and bound independent deployments per target under ADR-0021. Browser, Go race
(including PostgreSQL), vet and design checks passed. Fresh exact-build PVE1
qualification is in progress; live two-process VM clone overlap passed. Rollout
and catalogue publication remain before these tasks can be marked complete.

## Latest completed work

- `TASK-MAINT-001`: build output removed from Git and ignored; build prerequisites
  corrected; [documentation](docs/README.md) separated by audience. Prior state is
  preserved in the [September 8 archive](docs/history/implementation-through-2026-09-08.md).
- `TASK-UI-005`: workspace/organization administration, provider discovery and
  workspace target grants, consistent Classroom navigation, and operations charts.
  Workspace names support readable Unicode display text; stable IDs are unchanged.
- `TASK-TESTBED-003`: deployed `classroom-admin-20260915` to the private PVE1
  classroom; live administration, charts and both Classroom workspaces verified.
- `TASK-TESTBED-004`: renewed the scoped classroom token and signed exact-target
  qualification after fresh lifecycle, recovery, isolation and cleanup checks.
- `TASK-PROD-004` and `TASK-PROD-005` remain separate production validation work.

## Implemented

- Typed declarative templates, immutable revisions and evaluated launch inputs.
- Durable provisioning, ownership-bound cleanup, quota reservations and expiry.
- Local/OIDC authentication, scoped roles, classroom batches and short-lived consoles.
- Verified external-provider installations, target-specific capabilities and bounded
  per-plugin recovery. Installation remains operator-managed and restart-based.
- Generated OpenAPI/TypeScript contracts; persistent workspace navigation and
  identity-scoped request caching.

See the [architecture](docs/design/architecture.md),
[classroom interface](docs/design/classroom-ui.md) and
[machine state](design/state.yaml) for details. Accepted ADRs and machine contracts
remain authoritative; no accepted decision exists only in task conversation.

## Deployment and remaining gates

The private PVE1 classroom runs `classroom-admin-20260915`, deployed from
`9733c6af128136d813e0bda886a1c93e29e697dc` on September 15 at 18:34:09 UTC.
The core/UI update retained provider/access executables and configuration, with
an integrity-checked database snapshot and previous binary for rollback. All 50
existing lab records were unchanged, and the temporary instructor verification
sessions were retired. See the [classroom record](docs/operations/classroom-testbed.md)
and [separate pilot record](docs/operations/pve1-pilot.md).

The subsequent TASK-TESTBED-004 renewal restored target refresh and native VM,
VNC and lifecycle capabilities. Qualification is valid until **2026-09-21T18:51:05Z**;
the existing privilege-separated token expires **2026-09-22 18:40:20 UTC**.
All 50 user lab records were unchanged and all qualification guests were removed.

Production still requires live target load/soak, shared-journal cross-node
locking/failover/disaster recovery, publisher controls and the first signed release
ceremony. The native topology additionally needs encrypted off-host recovery,
renewable trusted TLS, monitoring delivery and reviewed release controls.
Local tests and private testbed evidence do not satisfy these gates.

## Verification

The full Go race suite passed with disposable PostgreSQL 18, alongside vet,
Svelte check/build, all four browser suites in Chromium/Firefox/WebKit, nine cache
tests, API generation, design/protocol/schema/link checks and the dependency audit.
A clean source-only checkout built a native release and all archive checksums
verified. The release tool now rebuilds ignored UI assets itself. See the
[verification record](design/implementation-verification.yaml). Raw local logs are
in ignored `bin/maintenance-20260915/`; browser screenshots remain under `bin`.

## Next action

Renew the classroom qualification before **2026-09-21T18:51:05Z**, then renew the
scoped token before its September 22 expiry. Continue production validation
through `TASK-PROD-004` and `TASK-PROD-005`; private testbed renewal does not
close those gates.
