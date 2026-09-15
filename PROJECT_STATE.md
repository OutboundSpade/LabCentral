# LabCentral project state

**Updated:** 2026-09-15

LabCentral is a Go control plane with a static SvelteKit UI, SQLite/PostgreSQL,
durable jobs and separate provider/access plugins. Core owns authorization,
quotas, lifetime and ownership; plugins translate infrastructure operations.

## Latest completed work

- `TASK-MAINT-001`: build output removed from Git and ignored; build prerequisites
  corrected; [documentation](docs/README.md) separated by audience. Prior state is
  preserved in the [September 8 archive](docs/history/implementation-through-2026-09-08.md).
- `TASK-UI-005`: workspace/organization administration, provider discovery and
  workspace target grants, consistent Classroom navigation, and operations charts.
  Workspace names support readable Unicode display text; stable IDs are unchanged.
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

The last recorded private PVE1 classroom core/UI deployment is
`classroom-navigation-20260908`. Its qualified provider and access executables
were retained. This session has not deployed a replacement or renewed qualification.
The latest recorded qualification expired on September 13; verify the actual
installed evidence before ordinary writes. Do not treat the old next-renewal date
as future work that is still within validity. See the
[classroom record](docs/operations/classroom-testbed.md) and
[separate pilot record](docs/operations/pve1-pilot.md).

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

For a live classroom rollout, deploy the verified core/UI with rollback and
retained-lab checks. Renew exact-target provider qualification before testing
ordinary infrastructure writes. Continue production validation through
`TASK-PROD-004` and `TASK-PROD-005`; this maintenance work does not close those gates.
