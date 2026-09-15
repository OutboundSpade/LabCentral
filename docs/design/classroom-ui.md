# Classroom interface and private testbed

Owner-approved on 2026-09-07 under TASK-UI-001 and TASK-TESTBED-001.

The static SvelteKit/DaisyUI interface uses a single charcoal theme, restrained
purple emphasis, compact navigation and role-appropriate task views. Students
discover authorized projects and published templates, launch labs, follow
operations, open short-lived browser consoles and explicitly end ephemeral
labs. Authoring contains YAML and typed parameters; infrastructure details
remain administrative. Instructors see classroom rosters and batch progress.
The Huma API remains authoritative; navigation visibility grants no authority.

Authentication follows the configured local or OIDC mode. The example OIDC
provider is a separate OIDC provider installation with passkey enrollment, exact
callback matching and authorization-code/PKCE. Test identities are pre-linked
to explicit LabCentral memberships and roles; OIDC does not grant roles.

PVE1 is exclusively a testbed. The separate control-plane VM is provisioned
through the Proxmox API and reached through Tailscale HTTPS, with no public
ingress or Funnel. Core, provider and access identities and durable state are
isolated. New target configuration and console permissions require reviewed
exact-target qualification under ADR-0018. The existing iad1 pilot is preserved. Before qualification, the existing plugin
installation contract may expose administration with a discovery-only provider
and an independently scoped read-only credential. This does not admit VM
creation or console access.

The classroom target is 25 students, one active starter lab each, four-hour
expiration and an unprivileged Debian console exercise. Labs and test identity
data are disposable. SQLite, provider journals, service secrets and OIDC provider
state still survive ordinary restarts and upgrades. Scheduled backups, R2,
recovery-key infrastructure and restore rehearsals are outside this testbed
task. Reset requires completed owned cleanup; lost state requires operator
inventory reconciliation before allocation resumes. Broader production
recovery and release gates remain open and are not prerequisites to this
explicitly bounded testbed milestone.

Acceptance includes Blueprint quality inspection, three-engine browser checks,
real OIDC provider login, authorization/expiry/revocation, 25 concurrent lab and
console sessions, a one-hour soak, cleanup and restart recovery. Record observed
results and operator prerequisites in the testbed runbook.

For the owner-authorized private classroom testbed only, [ADR-0018](../adr/0018-owner-delegated-testbed-review.md) permits the implementation agent to execute, review and sign on the owner’s behalf from the protected operator workspace. This is delegated review, not independent human review. All exact-target technical evidence and runtime signature checks remain required. Other deployments retain the independent-review procedure.

[ADR-0019](../adr/0019-pinned-tailnet-console-transport.md) defines the explicit
peer-pinned Tailscale VNC transport for this Linux testbed. The access service
uses WireGuard encryption and validates the daemon-reported peer before binding
the outgoing socket to tailscale0. TLS remains the default elsewhere.

## Operations update (TASK-OPS-001, TASK-UI-002, TASK-TESTBED-002)

The owner requested bulk deletion, compact noVNC controls, less explanatory
copy and a purple theme. Bulk deletion snapshots the selected visible active
labs, confirms their names and count, then uses the existing individually
authorized, idempotent destroy commands with bounded concurrency. Accepted
commands remain visible as pending deletion; failures remain individually
retryable. Selection resets when the project changes.

An administrator-only product API exposes bounded durable operation pages and
per-instance VM measurements. Measurements use an optional typed field on the
existing provider ReadResource result, with sample time, CPU fraction of VM
capacity, vCPU count and used/maximum memory bytes. Missing fields mean
unavailable, never zero. Core forwards only these safe typed measurements,
checks authorization before and after provider reads, and never persists the
read result or changes desired state. Provider reads verify exact ownership.
The operations page distinguishes queued work, running provisioning, resource
progress and failures; student views retain only actionable messages.

The console wraps noVNC's RFB APIs for special keys and local scaling/remote
resize, plus the browser fullscreen API. Remote resize depends on the guest;
local scaling always remains available. This adds controls to the existing
session, preserving one-use tokens and authorization lease enforcement.

TASK-TESTBED-002 follows [ADR-0020](../adr/0020-classroom-update-delegated-review.md) for the same owner-delegated private target review; all earlier acceptance evidence remains historical.


## Request caching (TASK-UI-003)

TanStack Svelte Query 6.1.48 provides a shared QueryClient for the static
browser application. The existing typed API adapter uses fetchQuery to reuse
GET responses and deduplicate simultaneous reads across routes and components.
Catalogue, project and roster data have 30-second freshness; labs, operations
and batches one second; VM usage five seconds. Existing status/usage polling
continues. Explicit refresh bypasses freshness. Inactive queries expire from
memory after one minute; browser reloads start with an empty cache.

Keys include an identity epoch, subject/roles and the complete API path with
project/filter/cursor parameters. Session requests always recheck the server.
Identity changes, logout and authorization denials clear the cache; cross-tab
authentication changes reload other tabs after the command completes, clearing
component-local copies too. Pending old reads are canceled before mutation
invalidation. Successful and uncertain/failed mutations invalidate reads.
Authentication responses, access-session data, custom-credential requests and
all mutation responses bypass the cache. Console one-use tokens remain in the
existing one-use handoff only, never in QueryCache or MutationCache. There is
no persisted query cache, automatic command retry or change to server authority.


## Navigation continuity (TASK-UI-004)

The shared SvelteKit layout owns one workspace instance across ordinary routes.
Navigation updates the view and lab-detail query parameter without remounting
the authenticated shell or clearing project, catalogue and lab arrays. Session
revalidation and data refresh run in place; initial startup and explicit project
changes may show their loading states. Identity or role changes clear the old
view before loading the new scope, and expired sessions remove the shell.
Console routes retain their separate lifecycle and one-use launch behavior.

Revisited Operations and People views synchronously seed their tables from
complete identity-scoped TanStack Query pages; Settings seeds its current
values. Missing pagination pages never produce a partial cached collection.
Fresh queries still revalidate these values. Delayed-request browser regressions
assert DOM identity of the shell and absence of startup/workspace-skeleton
flashes across navigation, rapid route changes and browser history. This fixes
a rendering-lifecycle defect that request caching alone did not address.
