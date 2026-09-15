# Archived project state through 2026-09-08

Historical evidence and deployment versions below are not current qualification.

# LabCentral project state

**Updated:** 2026-09-08
**Completed:** `TASK-ARCH-002` architecture simplification and `TASK-PILOT-001` authenticated Tailscale pilot.

**Current work:** `TASK-UI-004` is complete: the persistent workspace layout fixes navigation flashes and is deployed as `classroom-navigation-20260908`. TanStack caching and earlier classroom operations work remain complete. The next production release work remains `TASK-PROD-004`/`TASK-PROD-005`, separate from this testbed.

**Previous milestone:** `TASK-UI-001` is complete; `TASK-TESTBED-001` private classroom deployment is complete. `TASK-PROD-004` and `TASK-PROD-005` retain their separate production validation gaps.

LabCentral is a Go modular monolith with a Huma/OpenAPI API, static SvelteKit UI, SQLite/PostgreSQL persistence, durable database jobs/outbox, and separate provider/access processes. Portable resource types remain closed and typed. One instance belongs to one target. Core owns authorization, quota, lifetime and ownership decisions; plugins own infrastructure translation and execution journals.

The owner approved substantial simplification for independently distributed infrastructure providers implementing the existing resource types. ADR-0015, ADR-0016 and ADR-0018 replace the relevant earlier decisions. See [architecture](../../docs/design/architecture.md), [plugin installation](../../docs/operations/plugin-installation.md), and [compatibility](../../docs/compatibility.md).

## Current implementation

- Application-owned transactional interfaces replace the unused repository/unit-of-work abstraction. Provider and access gRPC mapping lives in an adapter; orchestration has no wire imports. SQL transactions and existing state envelopes are preserved.
- Explicit versioned installation files bind verified provider manifests to targets. Existing official CLI profiles remain supported. Production external plugins use operator-isolated attached processes; trusted local development can launch children.
- Bounded per-plugin recovery preserves a stable RPC client. Unavailable targets stop taking work while the API and other providers continue. Unknown mutations retain their original invocation IDs.
- Revisions preserve canonical parameterized source separately from resolved instances. Typed API/UI inputs are evaluated at instance creation under the revision's renderer semantics. Migration `0012_template_representations` preserves existing revisions.
- Provider source identity hashes actual dependency files and module locks. Ordinary Proxmox writes retain exact-target signed qualification. Explicit, short-lived signed grants provide separately reviewed cleanup authority for exact owned resources.
- The checked OpenAPI document generates frontend DTOs. HTTP handlers are grouped by feature.

## Evidence and remaining gates

The [architecture audit](../../docs/design/architecture-audit-2026-09-04.md) is the historical rationale, not the current implementation inventory. Earlier implementation/testbed evidence is preserved in the [historical log](../../docs/history/implementation-through-2026-09-04.md). [Verification contracts](../../design/contracts/architecture-conformance.yaml) describe the architecture acceptance checks.

`TASK-PROD-004` still requires live target load/soak, cross-node shared-journal locking/failover/disaster-recovery evidence, publisher-control approval, and the first signed release ceremony. Local/fake-provider tests do not satisfy these gates. New provider binaries require fresh exact-build qualification; earlier testbed evidence is not relabeled.

The full race-enabled Go suite passed with disposable PostgreSQL 18. A CI-exposed lease-renewal conflict was reproduced and fixed with bounded database-only retries; three deterministic conflict runs and five 100-instance runs passed under the race detector. Targeted normal tests, Go vet, design/protocol/schema/link checks, generated OpenAPI/TypeScript, Svelte checks/build, and dependency scans passed. See the [verification record](../../design/implementation-verification.yaml) for scope and exclusions.

A [PVE1 pilot](../../docs/operations/pve1-pilot.md) now runs authenticated HTTPS with core `main-cd2bffd-browser-fix` and the unchanged, previously qualified rc6 provider. The earlier embedded asset omission is fixed. A later blank-page report was reproduced in WebKit and fixed by deferring the optional console client's asynchronous initialization; browser regression checks cover Chromium, Firefox and WebKit. The isolated Debian example expired on schedule and is now reported destroyed by the product API. The owner selected Tailscale access. This pilot does not qualify current-main provider binaries or complete any remaining production milestone gate.

Pilot CI also exposed audit-write contention exhausting the existing PostgreSQL retry window during classroom teardown. The shared policy now uses at most 16 attempts with capped randomized exponential backoff. A deterministic eight-conflict regression, five repetitions of the 100-instance scenario, and the full Go suite passed with PostgreSQL under the race detector; provider calls remain outside database retries. The deployed SQLite pilot is unaffected by PostgreSQL transaction conflicts.

The owner selected the Tailscale deployment and withdrew the Kali VM 101 access requirement on 2026-09-05. `TASK-PILOT-001` is complete; the remaining production gates are unchanged.

**Next action:** use the private classroom through Tailscale and distribute each student's individual enrollment link. The instructor uses the enrolled OIDC provider passkey; student consoles log in automatically. Renew the currently installed exact-target qualification before 2026-09-13T20:36:44Z. No deployment permission, VM password, scheduled backup or off-host recovery input is pending for this disposable testbed. Production gates remain separate.

The approved native migration under `TASK-PROD-005` moved active state to `/var/lib`, split core/provider UIDs, enforced provider port/DNS egress, and masked the retired pilot. Live login, instance readiness, native qualification discovery and filesystem boundaries passed. The general systemd profile now supplies setgid IPC directories and owner-only provider secrets. Encrypted off-host recovery, trusted renewable TLS, monitoring delivery and reviewed release controls remain required; see the deployment record for operator inputs and exact evidence.


The [classroom interface](../../docs/design/classroom-ui.md) now separates My labs,
launch, details/consoles, authoring, classroom batches, people and policy in one
responsive dark theme. Authorized project and roster discovery use server-side
scope checks; published revision content supplies typed input declarations.
Generated OpenAPI includes authenticated administration. Three-engine browser
workflow fixtures, the full Go race suite with disposable PostgreSQL, vet and
Blueprint inspection passed.

The [new PVE1 testbed](../../docs/operations/classroom-testbed.md) has its own VM
920000 and OIDC provider v2.14.0 with a confidential PKCE client and 26 disposable
identities. All 26 real OIDC code-flow sign-ins passed. The owner enrolled the
instructor passkey; individual students still enroll their own passkeys.
Classroom memberships, target grant, a project limit of 25, one lab per student
and four-hour expiry are installed. The setup API key is retired.

Owner-delegated smoke, failure/recovery and typed mTLS isolation qualification
passed against the exact provider build and target. The signed artifact is
installed and verifies through 2026-09-13T07:01:38Z. All qualification clones,
runner and temporary relay were removed with independent absence checks.
The required measured probe template 920003 remains stopped in the catalogue;
920001 is the student starter. Native VM/VNC capabilities are enabled, the
starter is published, and the first real product lab provisioned and expired
with owned VM cleanup. The existing iad1 pilot is preserved.

Live VNC exposed a target transport mismatch: its temporary socket presents
plain RFB. ADR-0019 adds explicit peer-pinned, interface-bound Tailscale transport
in the access plugin while retaining default TLS and provider credential
separation. Exact provider source and binary are unchanged. The renewed console soak passed for 3615 seconds with all 25 independently owned labs and browser consoles healthy at every sample. Individual console sessions were reopened at 24 minutes to respect their 30-minute limit; this was an idle-console soak with keyboard interaction, not a workload benchmark. The failed initial-lease attempt is retained separately. Explicit access expiry and revocation closed the browser stream; a full control-VM reboot preserved all 25 labs and each console reconnected. Student self-service launch, automatic login, default four-hour lifetime and confirmed deletion passed. Final product cleanup and independent Proxmox inventory found no test clones or disks, zero quota usage and released allocations; both measured templates remain stopped and unchanged. Temporary browser sessions and unused discovery credentials are retired. The existing iad1 pilot remains ready.

The owner explicitly delegated classroom testbed review and signing to the
implementation agent. ADR-0018 records this target-specific authorization;
signing remains outside runtime services and all measured technical gates remain.

TASK-TESTBED-002 follows [ADR-0020](../../docs/adr/0020-classroom-update-delegated-review.md) for the same owner-delegated private target review; all earlier acceptance evidence remains historical.


The September 7 operations update is deployed as `classroom-ops-20260907`.
Exact-target smoke, recovery and isolation qualification passed for the changed
provider; the qualification expires September 13 at 20:36:44 UTC. Backend race
checks (including PostgreSQL), vet, browser workflows in Chromium/Firefox/WebKit,
Blueprint inspection and dependency audits passed. Live checks observed queued
and running work, matched CPU/memory measurements to Proxmox, exercised the real
console toolbar, and deleted only two explicitly selected verification labs.
Separate administrator deletions during this task are retained in the audit
record; no claim is made that all discovery-baseline labs remained running.
Test clones/disks, synthetic-user access and temporary test sessions were retired;
onboarding links, templates, private routing and the iad1 pilot were preserved.
Current signed evidence is under `bin/classroom-ops-20260907/`; earlier acceptance
and its hour-long console soak remain historical, not repeated for this revision.


The September 8 UI update uses TanStack Query for shared in-memory GET caching,
request deduplication, short live-data freshness and mutation invalidation.
Identity changes and authorization denial clear cached data; cross-tab logout
reloads other tabs. Authentication, console tokens and command results are not
cached. Eight cache regression tests, Svelte check/build, three-engine browser
workflows with request-count assertions, HTTP tests and dependency/design
checks passed. Live navigation reused catalogue and revision data, explicit
refresh fetched again, and all existing lab records were unchanged. Only the
core/UI binary changed; provider/access binaries and signed qualification were
preserved. Evidence is under `bin/classroom-query-20260908/`.


The navigation regression reproduced shell replacement and startup/loading
flashes in the previous request-cache build. The fixed shared layout preserves
the workspace and refreshes data in place. Nine cache tests and three-engine
browser tests cover delayed/rapid navigation, history, identity changes and
session expiry, alongside existing classroom and console workflows. Live tests
visited eight destinations with 150ms added read latency, then back/forward:
no shell replacements or startup/workspace-skeleton flashes occurred, and all
48 existing lab records were unchanged. Core/UI only was updated; provider,
access, qualification, private routing, enrollment links and iad1 readiness were
preserved. Temporary test login retired; evidence and delegated acceptance are
under `bin/classroom-navigation-20260908/`. Existing browser tabs need one reload
to load the new bundle. Production validation gates remain separate.
