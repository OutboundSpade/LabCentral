# Classroom interface and private testbed

Owner-approved on 2026-09-07 under TASK-UI-001 and TASK-TESTBED-001.

The static SvelteKit/DaisyUI interface uses a single charcoal theme, restrained
purple emphasis, compact navigation and role-appropriate task views. Students
discover authorized projects and published templates, launch labs, follow
operations, open short-lived browser consoles and explicitly end ephemeral
labs. Authoring supports schema-generated fields and YAML/JSON source. Instructors
see students with their labs in one roster, with launch options in a dialog.
The Huma API remains authoritative; navigation visibility grants no authority.

`TASK-UI-007` uses a shared native dialog for launch options, target settings and
operation details. Notifications are compact floating cards outside document flow;
existing timed expiry, focus/hover pause and navigation dismissal remain.

`GET /lab-authoring-schema` exposes the canonical Lab JSON Schema to authenticated
clients. `POST /lab-authoring-documents` converts a bounded single YAML document
using the existing strict parser; it does not validate, publish or execute a lab.
Form projection supports local references, typed fields, arrays, optional objects,
enums and the lab schema’s object compositions. Depth/reference budgets bound
projection. Unsupported constructs retain a JSON editor and server validation
remains authoritative. Provider options use discovered `override_json_schema`.
Source comments are not retained after a form edit; values outside projected
properties are preserved.

Administrators configure target concurrency and VM quotas from Plugins or
Operations. Capacity renders the versioned Proxmox discovery report and its time,
including per-node CPU/memory and per-node storage without summing shared storage.
It is observational, not a reservation. Target quota usage and limits remain
separate and apply to any registered provider. Missing upstream capacity is not
interpreted as unlimited or zero.

On console reload or transport failure the client obtains the previous session’s
resource identity through the authenticated API and creates a new authorized
session with a fresh one-use token. It updates the URL and retires the old grant
where possible. Retry is bounded to five attempts with backoff, cancellation on
navigation, and no retries after revocation or authorization denial. Transport
and session tokens remain out of URLs and persistent browser storage.

Authentication follows the configured local or OIDC mode. The example OIDC
provider is a separate OIDC provider installation with passkey enrollment, exact
callback matching and authorization-code/PKCE. Test identities are pre-linked
to explicit LabCentral memberships and roles; OIDC does not grant roles.

the private testbed is exclusively a testbed. The separate control-plane VM is provisioned
through the Proxmox API and reached through Tailscale HTTPS, with no public
ingress or Funnel. Core, provider and access identities and durable state are
isolated. New target configuration and console permissions require reviewed
exact-target qualification under ADR-0018. The existing separate pilot is preserved. Before qualification, the existing plugin
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

## Workspace administration and charts (TASK-UI-005)

The persistent shell uses one route catalog and a separate navigation component.
Lab details and administration have focused components. Global Workspaces, Plugins
and Operations views do not require a selected project or load a project's
catalogue merely to render administration. Workspaces and People are available
in authenticated deployments; no-auth keeps its synthetic local workspace.

Workspaces lists organizations and projects, including empty organizations,
creates and renames them through existing versioned commands, and links to the
selected project's limits. Organization/project names now accept bounded human
readable display text; opaque IDs and scope bindings remain unchanged.

Classroom is always discoverable. Missing batch authority produces an explanation
of instructor and environment requirements, with an administrative link to target
access. The server's evaluated project actions still gate batch forms and roster
reads. Creating a workspace never grants roles or target access implicitly.

Plugins lists registered provider environments globally, discovery validity and
protocol features. It refreshes targets and grants/revokes project environment
access. Expired/revoked grants renew their existing identity using If-Match.
Revocation asks for confirmation of the exact scope and environment. Inherited
organization grants remain visible. Operator-managed manifests, isolated services,
restart-based installation and separate access-plugin configuration are unchanged.
No upload, arbitrary executable launch or live installation mutation is introduced.

Operations charts use durable queued/running/failed counts and current CPU/memory
samples for the visible VM page. They make no historical-trend claim. Missing,
failed, invalid-timestamp or stale samples remain unavailable. Active-lab counts
include all workload kinds; VM pagination and measurements remain VM-specific.
Tables provide exact accessible values. The bar renderer is loaded only when
Operations mounts, updates in place, escapes untrusted chart labels and is destroyed
on unmount. Project/filter changes discard old results before fetching the new scope.


Historical UI deployment and credential-renewal acceptance is recorded under
TASK-TESTBED-003 and TASK-TESTBED-004. Site inventory, installed builds and
qualification expiry belong in [private operator records](../operations/classroom-testbed.md).

## Navigation and feedback (TASK-UI-006)

My labs owns the launch action and lab details. Launch has no separate sidebar
entry; both nested views use a My labs breadcrumb. Top-level destinations use
one page heading. Every authorized role can switch workspace using the sidebar
selector, which lists only accessible projects. Identity and sign-out also live
in the sidebar; the desktop top bar is removed. Mobile retains a drawer button,
and selecting a workspace closes the drawer.

Transient success/error notifications float above document flow, can be dismissed,
and expire after five/eight seconds. Hover or keyboard focus pauses expiry.
Navigation and workspace changes clear them. Form-local validation remains next
to the field or dialog. Accepted launches navigate back to My labs for progress.

Catalogue template names are readable Unicode display text (1–63 characters),
separate from stable template IDs and declarative resource names. Invalid names
produce a validation response. Create, rename and persisted readback are tested.

Classroom and My labs share confirmed bulk deletion, per-lab outcomes, four bounded requests and stable retry keys. Classroom offers selection beside each lab and all-active selection. The console has a ready-machine selector and a link back to that lab; switching creates a fresh authorized session and retires the old grant.


## Integration administration (TASK-UI-009)

The existing `/plugins` route is labelled **Integrations**, with URL-addressable
Environments, Plugins and API clients tabs. Environments retain deployment limits,
discovery and workspace grants. Plugins lists provider, console/network access,
template source, image builder and UI roles together, including legacy CLI-managed
services. Technical IDs/contracts and operator configuration stay in expandable
details. This is presentation terminology; ADR-0023's process/credential isolation
and versioned provider/access/source/builder/browser contracts remain unchanged.

API clients are external applications requesting user-approved access through
ADR-0024's gateway protocol; registration is not plugin installation or permission
to act as a user. Registration uses a labelled dialog, then a one-time panel with
client ID and masked/revealable/copyable secret. Secrets stay in component memory
and disappear on dismissal/navigation. Loading errors are explicit, list refresh
failure cannot discard returned credentials, and disable requires confirmation.
User settings calls delegated grants **Connected applications**; device approval
uses the same application terminology. Existing API paths and grant semantics
remain compatible. Keyboard tabs, mobile layout, failure paths, and Blueprint's
rendered/source inspection form the UI acceptance checks.

## API client deletion and validation feedback (TASK-UI-010)

Administrators can delete a client from Integrations → API clients after a named
confirmation. Deletion is permanent; current grants, pending approvals and client
credentials become unusable, existing streams close under the same authorization
deadlines, and audit history remains. Disable/enable remains the reversible option.
Failures keep the confirmation open for retry; no credentials are persisted by the UI.

Validation notifications retain bounded API field explanations so unsupported
isolation or runtime features are visible. Submitted values are not echoed.
Docker templates and dedicated-VM container templates retain distinct semantics;
see [template requirements](../users/templates.md).
