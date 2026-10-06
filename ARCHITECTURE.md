# LabCentral v2 architecture

This is the living architecture document. The v2 implementation is independent
of the previous implementation; Git retains its history. No legacy migration or
compatibility behavior is part of this design.

## Scope and boundaries

One Linux amd64 operator runs a Go library and optional CLI against AWS EC2 or
Proxmox. Initial guests are Ubuntu 24.04 amd64. Labs contain images, named machines,
one private IPv4 segment, access requirements, and optional typed Ansible inputs.
The same source directory is intended to work unchanged on both platforms.

```
author source + operator environment
                  |
           public Go library ---- SQLite intent journal + private credentials
                  |
        typed v2 gRPC over private Unix socket
                  |
          platform process ---- provider-owned recovery journal
            |           |
       native APIs    pinned Packer ---- guest-local Ansible
            |
       owned lab VMs ---- verified SSH / authenticated loopback browser relay
```

The CLI handles presentation and confirmation. The library accepts contexts,
returns typed results/errors, and emits structured progress. Root portable types
import no provider SDK. `platform/aws` uses the AWS SDK, `platform/proxmox` a bounded
HTTPS API client, `build` generates platform-owned Packer configuration, and `access`
handles SSH and desktop transports. The public process client also allows an
external application to use the same implementation.

The provider never opens the library database. It owns a separate mutation
journal for native subresources. The current provider executable routes both
platforms in one separate process; splitting it into platform-specific binaries
is possible without moving portable logic into adapters. The provider is trusted
operator code, not a sandbox for arbitrary third-party executables. The protocol
is generated from typed Go structures, uses explicit version 2 envelopes, bounds
message size, and rejects unsupported versions. Resource-specific opaque bytes
are adapter state, never author-supplied portable manifests.

## Source, environment and plans

Strict YAML rejects unknown/duplicate fields, aliases, multiple documents,
unsupported topology, out-of-bound resources, missing files, and undeclared inputs.
Source snapshots reject symlinks and unsafe paths and have bounded size/file count.
The digest binds file bytes to the parsed lab. Plans bind source, effective input
values, environment, base identities, and build toolchain; modified plans are
revalidated before mutation. Deployment copies machine entries before adding
native image references, addresses and keys, so one validated plan can deploy
independent team instances without changing the caller's plan.
AWS instance selection uses an operator allowlist and
reports allocated capacity. Proxmox reserves disjoint VMIDs and per-VLAN /24s.

Environment files contain target identifiers, credential references, base bindings,
network requirements, and toolchain pins. They belong outside tracked source.
State contains snapshots, effective plans, provenance, operation records, retained
resource references, and allocations. SSH private keys live separately from the
SQLite records. Input values are not a secret-storage interface.

Sharing is through Git or local directories. There is no package manifest service,
registry, template expression language, loop expansion, inheritance, or author
Packer extension. Ansible remains ordinary author code running inside build guests.

## Builds

Packer 1.14.3 with amazon 1.8.1, proxmox 1.2.3, and ansible 1.1.4 produces AMIs or
Proxmox templates. The fixed bootstrap installs pinned ansible-core in a guest
virtual environment. `ansible-local` receives a generated variables file; author
values do not enter host shell commands. Build infrastructure credentials remain
in the operator/provider/Packer processes. Builds have bounded logs and timeouts.
Source directories and logs are private operator records. The platform desktop
installation omits optional recommended packages to keep build and guest overhead
bounded. Desktop build-policy revisions participate in the cache key without
invalidating unchanged server images.

Each build reserves an owner identity and native identifiers before mutation.
Native tags, descriptions, explicit VMIDs, image names, and keypair identities
support discovery after interruption. A captured image is unpublished until a
disposable instance passes SSH/cloud-init readiness and declared desktop service
checks and cleanup succeeds. AWS desktop validation also creates a virtual session
and waits for its running X display. An empty session-status response remains unknown;
startup polls it for a bounded period without creating another session. Validation
checkpoints preserve progress across cleanup interruption. Retrying failed validation
reuses the captured image only after confirmed guest cleanup, archives the prior
receipt and assigns a fresh validation owner and machine-intent record. Cached artifacts are checked for identity/existence before
reuse. Mutable package repositories prevent promises of byte-identical rebuilds.

Proxmox clone builds preserve the base root disk. The environment must provide a
base large enough for installed software and a working guest agent before Packer
needs IP discovery. Deployment can grow disks. No mandatory image export, disk
conversion, or nested virtualization is used.

Primary sources, retrieved 2026-09-27:
[amazon-ebs](https://developer.hashicorp.com/packer/integrations/hashicorp/amazon/latest/components/builder/ebs),
[proxmox-clone](https://developer.hashicorp.com/packer/integrations/hashicorp/proxmox/latest/components/builder/clone),
[ansible-local](https://developer.hashicorp.com/packer/integrations/hashicorp/ansible/latest/components/provisioner/ansible-local),
[pinned Proxmox clone configuration](https://github.com/hashicorp/packer-plugin-proxmox/blob/v1.2.3/builder/proxmox/clone/config.go).

## Native networking and access

Each AWS lab owns its VPC, subnet, route table, Internet gateway, and security group.
Public IPv4 is ephemeral. Operator CIDRs limit SSH/DCV ingress; same-group rules
permit lab communication; default general egress is removed. IMDSv2 is required.
Non-desktop guests have no instance profile. Desktop profiles must contain a
single inline regional DCV-license read permission and no managed policies; the
adapter adds a narrowly scoped S3 endpoint. Build networking belongs to the operator
and is checked separately from runtime networking.

AWS DNS and metadata traffic are special cases outside ordinary security-group
filtering. This is not an air gap. Role contents and host/network administration
remain trusted operator responsibilities, including changes made after validation.
Sources retrieved 2026-09-27:
[security-group behavior](https://docs.aws.amazon.com/vpc/latest/userguide/vpc-security-groups.html),
[DCV licensing](https://docs.aws.amazon.com/dcv/latest/adminguide/setting-up-license.html),
[short-lived AWS CLI authentication](https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-sso.html).
Console browser login can feed both SDK and Packer through a separate
credential-process profile. Source retrieved 2026-09-28:
[AWS console login and process credentials](https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-sign-in.html).

Proxmox uses one configured node/storage/pool, an exclusive VMID range, and existing
isolated VLANs. VM rules default to DROP with intra-lab and operator SSH allowances.
The cluster firewall and VLAN-aware bridge are checked; physical isolation and
operator routes require external verification. The adapter never changes host
bridges, routing, or global firewall policy. A disabled cluster firewall is a hard
preflight failure. HTTPS verifies server identity by default; an explicit environment
opt-out is reserved for operator-authorized targets.
Source retrieved 2026-09-27:
[Proxmox firewall documentation](https://pve.proxmox.com/wiki/Firewall).

SSH uses per-instance keys and pinned host identities, no agent forwarding, terminal
resize and remote exit status. Machine-name mappings are written at first boot.
DCV runs system authentication and named per-access virtual sessions. Both session
ownership and the desktop process user are set to the temporary guest account.
Plans disclose the regional S3 licensing exception. Its certificate
is fetched over verified SSH and pinned by the loopback TLS proxy. Image capture
removes generated DCV certificates so guests generate their own identity. Proxmox uses a
bundled noVNC 1.6.0 client and encrypted upstream console connection; management
tokens remain in the provider process. Both paths create temporary guest login
accounts and disclose passwords only in explicit access output.

Loopback relays enforce a single-use launch token, authenticated cookie, exact
Host/Origin checks, expiry, and closure of active connections. Closing access
removes its temporary guest session/account; DCV revocation terminates the user's
remaining processes before deleting the account and verifies session absence.
After a process crash, recovery uses
the durable access intent. The CLI browser relay is local. The hosted service wraps it with team authorization
and a separate desktop origin, described below. It does not provide an AWS boot console.
Source retrieved 2026-09-27:
[DCV TLS certificate handling](https://docs.aws.amazon.com/dcv/latest/adminguide/manage-cert.html)
(retrieved 2026-09-28),
[DCV virtual-session users](https://docs.aws.amazon.com/dcv/latest/adminguide/managing-sessions-start.html)
(retrieved 2026-09-28),
[DCV packages and published checksums](https://www.amazondcv.com/)
(retrieved 2026-09-28),
[noVNC source and licensing](https://github.com/novnc/noVNC/tree/v1.6.0).

## Durability and cleanup

SQLite uses WAL and FULL synchronization. Cache-key file locks coordinate image
build, reuse, abandonment and removal across library clients. Up to three independent
images build concurrently. Native builds never hold the allocation lock. Deployment
builds/reuses its images first, then acquires sorted image locks and the short
allocation fence to persist image references and reservations. Removal uses the same
image fence before checking retained references. Deployment releases those fences
before native provisioning; after the network is ready, up to four machines provision
concurrently. Each successful sibling is persisted, even when another fails.
Deployment, recovery and destruction require an exclusive instance lifecycle lock.
Desktop open/close and bounded SSH operations share that lock and take an exclusive
machine lock, permitting a workstation launch during checks on another machine in
the same lab. File locks coordinate independent clients/processes; cancelled waits
release acquired guards, and lifecycle changes cannot overlap active machine work.
Unrelated running labs remain available during builds. Status queries remain
available. Native create intent is recorded before calls.
Native idempotency keys are used where supported. Provider journals persist
subresource intent and results with fsync and atomic rename. An uncertain create
is discovered through its exact recorded ownership identity; timeout is never
interpreted as nonexistence. Ambiguous/missing results retain allocations.
AWS default-route creation verifies the recorded route table's owner and VPC,
reconciles an existing matching route, and rejects conflicting routes. Explicit
authorization denials are retried for up to 31 seconds to tolerate propagation of
tag-conditioned authorization; persistent denials retain the original error and
recovery intent. Transport failures are not retried as confirmed rejections.
Operator recovery skips preparation for obsolete, ending or expired meetings;
maintenance status includes each current team's readiness.

Successful earlier deployment steps remain after a later failure. Recovery resumes
the original operation; destroy first reconciles uncertain creates and then removes
owned resources in reverse dependency order. Up to four independent machines are
deleted concurrently; each mutation retains its own durable intent. Networking
waits until every machine is confirmed absent. AWS termination is polled every
two to five seconds. Guest-session cleanup is attempted first, but an unreachable
guest cannot block owned VM termination; access records remain until the VM and
its dependencies are confirmed removed. Image removal is separate and checks
retained instance references. AMI snapshots and Proxmox template disks are included
in image cleanup. Failed cleanup leaves a recoverable record. The CLI has no scheduler; operator failure can leave resources and charges active.
The hosted worker enforces persisted meeting deadlines while its host runs and
immediately queues overdue cleanup after restart.

One state root exclusively owns its configured allocation ranges. Cross-operator
locking and reservation are deferred. Deletion or corruption of local state is
outside automatic recovery guarantees; backups must include provider journals and
credentials. Uncertain mutations that cannot be authoritatively reconciled require
operator investigation instead of releasing possibly live resource identities.

## Acceptance

The implementation includes the library, CLI, typed process protocol, SQLite
journal, AWS/Proxmox adapters, Packer configuration generation, SSH, browser relays,
three portable examples, and an independent Go consumer. Local tests exercise
schema rejection, ownership, response loss, interrupted cleanup, allocation
collisions, concurrent access, plugin transport, role policy and relay authentication.
Real pinned Packer configuration validation is available as an opt-in test.

CI verifies pull requests and `main` pushes, ignores Markdown-only changes, and
cancels superseded verification runs. Browser installation and integration checks
run only on an explicit workflow dispatch with the `browsers` input enabled.
Maintenance remains manual and never cancels an in-progress installation operation.
Local UI changes retain desktop/mobile browser inspection requirements.

**The hosted AWS milestone passed live acceptance on 2026-09-28.** Twenty real
OIDC identities used independent DCV desktops across five four-machine Example
Bank labs for 3,602 continuous seconds with keyboard and pointer input. All twenty
workspace files survived reconnects. Warm launches completed within five seconds;
authenticated page reads remained available during the run. Checks covered
transactional team capacity, switching and old-team access denial, launch replay,
foreign origins, CSRF, logout and group revocation. Group removal closed active
access within two minutes.

Live exercise checks covered banking, ledger transactions, FTP publishing and
workstation handover files. Native probes verified intra-lab traffic, cross-lab
denial, control-host-only management ingress, denied general Internet egress and
DCV licensing access. The acceptance labs were destroyed and their exact owned
VM, disk and VPC inventories reconciled. Native images remain explicitly retained
for reuse. Consistent backup restoration passed state integrity, service startup,
meeting reads and the OIDC redirect. The deployed archive contains the exact
verified Linux amd64 binaries and pinned native toolchain with checksums.
Detailed evidence, installation identifiers and cost records remain outside source.

The scheduler and template-check changes passed one-team hosted AWS acceptance on
2026-10-05. A retained revision deployed and was destroyed while an unrelated native
image build continued. The new revision's image validation recovered using its retained
AMI and a fresh disposable guest after an initially empty DCV status response. Real
OIDC student access rendered a DCV desktop with keyboard and pointer input; student
requests to instructor APIs were denied. Guest Ansible detected the stopped web
service, database-port mismatch, and FTP-root/publication changes; each pinned reset
restored a 100/100 snapshot. A saved schedule produced a fresh passing check and was
then paused. The deployed UI was inspected at desktop and mobile widths. Owned test
labs and temporary OIDC identities were cleaned up; native images remain retained.
Detailed run evidence remains outside source.

The meeting-control update passed live UI and real OIDC checks on 2026-10-05.
Desktop and mobile navigation preserved the document and authenticated layout;
server-owned sign-in completed a real OIDC exchange, and student requests to
administrative UI and APIs remained denied. Destructive confirmation cancellation
preserved the existing meeting, whose teams, capacity, pinned revision and deadline
survived deployment unchanged. Larger team/seat settings and exact eight-hour
extensions passed local real-handler persistence and race checks; no new large-class
native load qualification is claimed. Temporary acceptance identities were removed.

The ready-team scheduling fix passed hosted authorization, persistence and race
regressions and was deployed on 2026-10-05. Real OIDC administration and five AWS
teams' guest Ansible checks passed after rollout; the existing meeting's pinned
revision, deadline, team identities and capacities were preserved. The regression
verifies checks finish while other teams remain in native preparation, including
restored claims after reopening storage. All live teams had already finished
preparation, so a fresh live provisioning overlap was not exercised over the active
user meeting. Unchanged UI and desktop runtime retain their prior acceptance.
Temporary acceptance identities were removed.

The machine-level access fix passed full library, hosted and storage race checks
and was deployed on 2026-10-05. Cross-client regressions verify a workstation
launch and close during a gated web check, same-machine exclusion, cancelled waits,
lifecycle exclusion and retained uncertain cleanup. A real OIDC student launched
a native DCV desktop during the team's FTP guest check; the launch request returned
in 1.001 seconds while that team's check run remained active. The desktop rendered
and keyboard/pointer input was sent, and guest Ansible completed confirmed service
results. The existing meeting and labs were preserved; the temporary desktop was
closed, its old access denied, its team seat released and both test identities removed.
UI assets and guest image revisions were unchanged.

The FTP defacement revision was imported into the hosted service on 2026-10-05
and became ready by reusing all four existing native images. The retained archive's
four data files match the clean pinned sources, and configured importer validation
passed under the race detector. Real OIDC administration verified the private
walkthrough and ready revision at desktop and mobile widths; student administrative
requests were denied and their responses excluded instructor material. The prior
meeting had ended and remained unchanged. No replacement meeting or break was
started, as requested; the revised guest break, archive transfer and student recovery
have not yet been exercised in a native lab. Temporary identities were removed.

The UI refinement was deployed through the restricted GitHub/AWS maintenance
workflow on 2026-10-05 after one browser-only advisory review by Claude Opus 5.5
at medium reasoning; Codex implemented the changes. Svelte type/build checks,
full Go checks/race checks, browser regressions, and the Blueprint source/rendered
review passed. Local UI fixtures covered twelve role/state/viewport combinations
and 36 axe WCAG A/AA page checks. Fourteen deployed page checks used real OIDC
student/admin sessions at desktop and mobile widths, including student API denial
and contrast checks. A user-created one-team meeting remained intact. The occupied
single seat was preserved; no fresh native desktop session was opened. Native
desktop code and toolchain are unchanged and retain their prior live acceptance.
Temporary review identities were removed and absence confirmed. Detailed evidence
and the bounded advisory transcript remain outside source.

The guest-capacity and recovery update was deployed on 2026-10-05 through the
restricted GitHub/AWS maintenance workflow. One tool-disabled Opus 5.5 review
provided architecture advice; Codex made all changes. Gated scheduler tests verify
that bulk saturation permits two independent guest jobs while retaining cleanup
capacity; this is local scheduling evidence, not a native AWS load test. Durable
retry, authorization, persistence and race tests passed, including concurrent
retry deduplication and retention of obsolete failures and unresolved cleanup.
All hosted browser checks and Svelte type/build checks passed. Desktop/mobile
inspection, 24 local axe page checks and Blueprint review passed. Fourteen deployed
page checks used fresh real OIDC student/admin sign-ins, verified historical retry
eligibility and student API denial, and preserved the current occupied one-team
meeting. No native build, new lab, break or desktop session was started for this
release; unchanged native inputs retain prior acceptance. Temporary review
sessions were logged out and exact owned identities confirmed absent. Detailed
operator evidence remains outside source.

The wider portable-library release remains incompletely qualified. Live AWS
single-server and workstation builds, deployment, verified SSH and browser access
have passed, including interrupted cleanup recovery. The following still require
live verification:

- All three unchanged portable examples completing their lifecycle on Proxmox.
- The full provider interruption, response-loss, stale-ownership and cleanup matrix.
- Proxmox isolation and noVNC display, keyboard, authentication and revocation.
- Explicit removal of retained image artifacts with complete native inventory checks.

Service readiness and local adapter tests do not establish live platform acceptance.
The hosted AWS result does not establish AWS/Proxmox portability or qualify a
standalone cross-platform release.

## Proposals after the foundation

Registries, additional hosted platforms, Windows, routed/multiple NIC topologies,
classroom governance beyond meeting/team membership, and EC2 Image Builder are deferred. They should use the public library without putting product policy
into portable lab declarations. These are proposals, not implemented contracts.

## Hosted team service

The hosted milestone adds a separate application layer around the public library:
OIDC provider authentication, transactional team membership, a durable infrastructure
worker, and user-owned desktop workspaces. The library remains independent of
HTTP and identity policy. This milestone targets AWS; Proxmox remains a library
and CLI platform. The hosted AWS twenty-user acceptance is described above.

Native CloudFormation under `tools/hosted/` defines one t3.medium control host,
encrypted retained EBS, an encrypted private S3 bucket with seven-day backup
retention, HTTPS ingress, and account spending alerts. The host uses an EC2 role;
GitHub Actions exchanges its `main` branch identity for temporary maintenance
credentials. The maintenance role permits a fixed SSM document against hosts
marked as this service and release uploads to the installation bucket. No permanent
AWS keys or browser-login refresh dependency are introduced. Infrastructure setup
still requires an administrator; runtime cannot pass its own management role to labs.

Hosted deployment, recovery, cleanup and backup use `Hosted maintenance` workflow
dispatches. Installation identifiers belong in protected operator records and
GitHub repository variables, not source. Application rollout consumes an exact,
checksum-verified release archive. The trusted `main` maintenance workflow has
GitHub Contents write permission because draft releases require push access; it
uses that access to download staged artifacts, without publishing them
([GitHub release visibility](https://docs.github.com/en/rest/releases/releases#list-releases),
retrieved 2026-09-28). Production DNS remains an explicit cutover step.
Trusted maintenance may import a fenced standalone engine journal into a fresh
private namespace for cleanup using the host role. Imports allow only the checked
SQLite schema and hashed provider JSON journals, never executable files or keys.
They cannot overwrite hosted state, and every subsequent deletion rechecks native
ownership. Identical import retries are idempotent.

Primary sources, retrieved 2026-09-28:
[AWS role guidance](https://docs.aws.amazon.com/IAM/latest/UserGuide/best-practices.html),
[GitHub OIDC on AWS](https://docs.github.com/en/actions/how-tos/secure-your-work/security-harden-deployments/oidc-in-aws),
[DCV Web SDK](https://docs.aws.amazon.com/dcv/latest/websdkguide/getting-started.html),
[SvelteKit static generation](https://svelte.dev/docs/kit/adapter-static).


The static UI keeps SvelteKit, the dark-purple daisyUI theme, Instrument Sans and
Lucide icons. A local SVG L-shaped corner surrounding a central square identifies
LabCentral in the header and favicon. A compact desktop navigation bar becomes
role-appropriate bottom navigation on phones. The desktop panel precedes the
handover on narrow screens, while the handover stays on the left on desktop.
Administrator meeting tools use keyboard-accessible Teams, Exercises and Scoreboard
tabs, selected by the URL fragment; panels remain mounted to retain pending state
and exercise selections. Live activity sits alongside those tools on desktop and
below them on phones. Status labels pair readable text with small icons and service
state colors. Activity filters expose failure counts and mark the selected filter
explicitly; failed operations retain recovery feedback and blocking reasons.
Template history initially shows recent, pinned and working revisions, with older
revisions available on demand. Cached images remain distinct from confirmed builds.
Schedule controls name starting, updating and pausing explicitly; exercise selection
explains unavailable teams. Primary controls have larger touch targets, flat sections
reduce visual noise, and purple identifies selection and principal actions. Shared deadline displays
count down using the browser clock and warn at fifteen
minutes that workspace files will be deleted. Signed-in home navigation redirects
to the lab or team list. Team changes require a named confirmation and recheck the
current membership before submitting. Desktop readiness transitions update the
page title and an accessible status region; the launch label stays fixed. Desktop
help describes idle reconnects and browser clipboard behavior. Instructor meeting
summaries use stat tiles; joined students count committed memberships once,
independently of pending seat reservations. Failed-team rows show preparation errors
and recovery.
The scoreboard is a horizontally scrollable teams-by-checks table with a pinned
header; its schedule selector initializes from saved data without overwriting an
unsaved choice during polling. Exercise selection precedes team selection and a
sticky action bar; confirmation counts teams and offers a collapsible list.
Template commit links use a sanitized administrator-only repository URL supplied
by the API. HTTP authorization, meeting policy, queue claims and native desktop
behavior remain unchanged.

The `hosted` package owns typed users, server-side OIDC sessions, meetings, teams,
memberships, template revisions, jobs and desktop ownership. SQLite transactions
reserve destination capacity before a team switch. New meetings default to five
teams with six seats each and four hours until cleanup. The UI and API allow
1–100 teams, 1–100 seats per team, and 1–8 hours. Creation uses one timestamp for
its start and deadline; selected extensions cannot exceed eight hours total.
Extensions clamp sub-millisecond clock skew from older meeting records to the
exact limit rather than rejecting a valid whole-hour extension.
Live desktop load acceptance remains twenty users (four per team); larger per-team
desktop counts have not yet been load-qualified. Access is blocked while the
switch is pending; guest revocation must succeed before committing membership.
Per-user locks serialize those changes with that user's launch. Provider plugins
receive explicit principal, expiry and workspace-retention options, never team
policy or database access. CLI access remains disposable by default.

OIDC uses authorization code, PKCE, state and nonce with verified issuer, audience,
signature and expiry. An explicit HTTPS backchannel origin can route machine
requests directly to the IDP while retaining canonical issuer verification and
browser authorization URLs. HTTP-only secure host cookies contain opaque session IDs;
tokens remain server-side. Exact-origin CSRF checks protect writes. Userinfo checks
refresh group authorization every four minutes; token refresh and persistence use a
bounded context independent of browser cancellation. At most four refreshes contact
the IDP concurrently; its dedicated HTTP transport checks HTTP/2 connection health.
Operator status exposes only fixed failure categories, connection phases and HTTP
status codes, never identity responses or tokens; desktop policy checks run every
20 seconds and fail closed on revocation or unverifiable identity. Checks run
concurrently across users and cancel unauthorized transports immediately; durable
guest cleanup proceeds independently through the worker. API credentials
used to bootstrap OIDC provider and temporary acceptance users are not runtime inputs.

One process holds the durable scheduler fence. Resource claims form execution
dependencies: template/cache-key claims coordinate builds, removals and preparation;
preparation, exercises and checks share a meeting claim while ending holds it
exclusively. Preparation claims only unfinished teams and releases each team's
claim after its native operation and durable readiness checkpoint. Already-ready
teams are not inspected, recovered or rewritten by preparation retries. Team claims
serialize provisioning, exercises and checks for the same team; checks on a ready
team can run while other teams are preparing. Unrelated queued work can bypass a blocked
job. Bulk work (build, preparation, refresh and image removal) has three slots, with
at most two image-build jobs. Guest work (checks and exercises) has two separate
slots, so bulk saturation cannot consume its capacity. A sixth slot is reserved
for meeting destruction or authorization cleanup. Resource claims still apply
across all lanes; same-team checks and exercises remain exclusive.
Queued dependencies, start/finish times,
per-template image counts, team readiness and active desktops are visible in the
administrator's live activity view, polled every two seconds.
Recovery eligibility is computed from durable meeting, latest-run and job records
and shared by the admin API and retry endpoint. Ended, expired, superseded or
recovered guest failures remain in Recent with their original error and reason;
they no longer count as actionable failures. Unresolved cleanup remains visible.
Retries show pending, accepted or rejected feedback inside Live activity; selected
filters use an explicit check mark and selection color separate from error counts.
The complete active
queue remains visible even when its jobs predate the bounded recent history.
A prepare job runs at most
three team deployments concurrently. Each team persists its own readiness or failure;
one failure does not cancel peers. Meetings with both ready and failed teams report
`partial`; all-team failures report `failed`. Aggregated job errors name the failed teams, and
retry reconciles their reserved instance IDs while keeping ready teams accessible.
The provider permits unrelated image builds, networking and machine operations
concurrently. Owner fences serialize networking, image and access lifecycle work;
distinct machines share the owner fence and have their own name fences. Create,
discovery and response-lost retries for the same machine cannot overlap its journal.
Networking deletion waits for all machine operations in that owner. Cancellation drains overlapping
meeting operations before deadline cleanup proceeds; unrelated image builds continue. It preserves the reserved
engine instance ID across retries, reconciles incomplete work, and prioritizes
meeting destruction over ordinary queued work. Ending a meeting interrupts active
meeting provisioning, exercises and checks; failed cleanup remains reported and retryable. Ending visits every
team even if an earlier team fails, retaining only unresolved cleanup for retries. Page reads and
unrelated desktop launches do not share that worker's infrastructure lock.
Provider journals distinguish explicit AWS authorization rejection from uncertain
transport outcomes. A rejected create can be retried; a timeout still requires
discovery. AWS creation permissions cover tagged outputs and separately authorize
their already-owned parent resources ([EC2 authorization reference](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html), retrieved 2026-09-28).

Previously imported Example Branch revisions retain three fixed hosted exercises: stop Apache,
change its listener and virtual host to 8080, and perform a controlled SSH login
with a synthetic support-account key and harmless homepage/file changes. Exercise
names, walkthroughs, records and mutation endpoints are administrator-only;
student API responses contain none of them. Guest administrators can inspect the
effects and evidence inside their own VM.

A transaction reserves selected ready teams and persists each exercise intent
with its durable job. The worker rechecks the meeting, deadline and latest team
operation on every replay. Superseded jobs cannot reapply a break after a reset;
completed jobs do not repeat mutations. A failed team does not prevent other
selected teams from running. End-of-meeting cleanup cancels exercises and retains
priority. The library's caller-owned SSH callback holds the selected machine fence
and a shared lifecycle guard; destruction waits for all guests without serializing
access to unrelated machines. Hosted policy stays outside the library.

Fixed guest programs receive pinned template files as data through verified SSH.
They restore Apache configuration, the homepage and the synthetic support account,
remove the exercise key/artifact, and verify baseline web access from the
workstation before applying a break. Guest commands have a timeout and a guest
lock so an interrupted transport cannot cause overlapping scripts. Exercise SSH
private keys stay in protected application storage, outside guest commands and
API output, and are removed after VM destruction. Intrusion logins generate real
SSH authentication events; reset preserves historical logs, bank data, published
documents and desktops. This targeted reset does not promise to repair arbitrary
package or SSH-daemon damage. No generic remote-command HTTP endpoint or author
execution on the control host is introduced.

New templates may supply `instructor.yaml` (version 1) and an `instructor/` folder
in their pinned repository revision. The hosted importer extracts these **before**
loading portable source. Notes, walkthroughs and guest programs are stored separately;
they never enter native images, portable build snapshots or student API responses.
Changing only instructor material can reuse the existing native images. The
Example Branch FTP exercise leaves an older tar.gz archive in the FTP archive
login root, with clean FTP configuration, publication source, homepage and cached
page. It defaces the source and visible website. Restoring only the cache cannot
repair publishing; reset restores the baseline and preserves the recovery archive.
The manifest contains notes, exercises (ID, name, description, walkthrough, ordered
machine/playbook steps), reset steps and checks (ID, name, points, steps). Optional `assets` name pinned
portable-source files made available under `template/` inside the guest bundle. Strict
field, duplicate-key, alias, file, size and machine-reference validation precedes
import. Instructor notes and manifest data are never sent to a guest; only programs
and selected assets are transferred. Students administer guests and can inspect
program effects; this does not make those programs a guest-side security boundary.

Each selected team's exercise runs its pinned reset steps before applying its break.
Fixed guest launchers stage private temporary files and invoke pinned guest-local
Ansible over verified SSH while holding the instance fence; author code never runs
on the control host. Guest locks and timeouts bound interrupted executions. Template
authors define the reset scope and implement observational check playbooks. A reset
is a targeted repair, not a VM restore.

Administrators can run checks once or persist a 60–3600-second schedule for the current
meeting. Schedules start paused, stop at meeting end, and resume their next due time
after restart without replaying missed ticks. Check intents, jobs, per-team current
results and next due time commit together. Active checks are deduplicated; busy teams
wait for a later tick. Each team's checks report individual pass/fail/unavailable
results and weighted points. Guest connectivity or unconfirmed execution is shown as
unavailable, never a confirmed failed service. Results and schedules are admin-only.
The scoreboard is a service-health snapshot, not accumulated competition scoring.

Repository import uses a dedicated read-only deploy key and verified GitHub host
keys. Each import discovers `templates/*/lab.yaml`, pins its commit, validates and
snapshots source, then builds native images once for reuse. Imports never update
running labs. Imports retain commit dates and subjects and record the current GitHub
head per template; a refresh backfills metadata for older imported revisions.
The administrator timeline sorts the current head first, then by commit date.
Each revision shows durable build/removal jobs and counts from the local image
journal; native image existence is revalidated by the engine before reuse.
Image operations persist resolved cache keys and an operation identity before
effects. Removal blocks new meetings on that revision, rejects active or retained
lab references, retains images shared by another built revision, and separately
cleans published images and unpublished build attempts through the public library.
Partial or uncertain cleanup keeps its intent and disables rebuilding until
reconciled. Superseded jobs cannot remove subsequently rebuilt images. Source
revisions and snapshots remain available for rebuilding after image removal.
Markdown disables raw HTML. The UI is prerendered route HTML with
SSR enabled during generation and no SPA fallback. Internal links use SvelteKit
client navigation, preserving the layout and authenticated session during route
changes; server-owned login and logout still use full document navigation.
SvelteKit route announcer is visually hidden by external CSS, preserving its
screen-reader announcements when the strict CSP blocks inline styles in Firefox.
Document titles remain descriptive without appearing beneath the page content.
The administrative pages share an in-memory view scoped to user and CSRF session,
cleared on sign-out, identity changes, role loss, or unauthorized administration
reads (401/403). Transient transport/server failures retain the last successful view;
two consecutive failures display a separate polling warning, and activity identifies
stale data by its successful-update timestamp. Action failures remain distinct.
One layout-owned two-second poll supplies both administrator pages and the header
activity link; concurrent refreshes share an in-flight request. Recent operations
initially show twelve entries with an explicit count and expansion control; active
operations are never truncated. Late responses cannot populate a different session’s
view. The root declares the
dark theme and color scheme before hydration. A dark
neutral theme with purple actions keeps the interface focused on team and lab tasks.

The student overview includes only the current user's desktop availability for the
same application session, team and instance. Availability means an uncancelled,
unexpired authenticated in-memory relay with an unexpired durable access record;
it does not assert a connected browser or perform a fresh guest probe. Retained
cleanup intents without a relay are inactive and are never deleted by a view read.
The administrator view reports aggregate and per-team relay counts without native
credentials, connection URLs or user identities. Desktop guidance enables disconnect
only for the user's available session. The repository link accepts the configured
GitHub SSH repository path and emits a canonical HTTPS link; unsupported or
credential-bearing values yield no link.

Desktop launch tickets are one-use and expire after one minute. The separate
origin exchanges them for host-only cookies bound to the application session,
user, team, instance and expiry. Every HTTP request is authorized. Desktop
launch forms retain the application Origin through a `strict-origin` referrer
policy, which excludes paths and query parameters. Established
WebSockets are cancelled after revocation. The pinned official DCV Web Client SDK
uses transient guest credentials without a second sign-in. A student's deterministic
workspace identity permits reconnects and retains the home directory. Launches
wait for DCV to report a running X display while holding the workstation's machine
lock and a shared instance lifecycle guard. They bypass the hosted job queue;
server checks on another machine do not delay launch or revocation. Launches and
checks on the same workstation remain serialized.
A failed asynchronous launch is retried once only after confirmed session absence;
unknown outcomes remain errors. Reconnects recheck the native session and replace
a failed session and relay while preserving the home directory. The browser shows
Connected only after the SDK reports its first rendered frame, and sizes the
remote display from a viewport independent of SDK-managed elements. If the server
retains a larger resolution, client-side scaling keeps the entire display visible;
the SDK maps pointer coordinates through that scale. The SDK captures native
clipboard paste events after its clipboard channel is established and synchronizes
supported local clipboard data when the desktop regains focus.
A user-initiated Copy from desktop action calls `syncClipboards()` for browsers
that require a gesture; browser permissions and supported formats still apply.
Firefox and Safari use a Paste to desktop dialog: a user-initiated native paste
event feeds the SDK clipboard handler, then the student pastes into the guest.
This text-only fallback requires no background browser clipboard-read permission.
The dialog clears on paste, cancellation and disconnect; clipboard content is
not persisted or logged by the application.
[DCV browser clipboard differences](https://docs.aws.amazon.com/dcv/latest/userguide/using-copy-paste.html)
(retrieved 2026-09-28).
Sources retrieved 2026-09-28:
[DCV virtual-session startup](https://docs.aws.amazon.com/dcv/latest/adminguide/investigating-linux-virtual-session-creation-failure.html),
[SDK connection callbacks](https://docs.aws.amazon.com/dcv/latest/websdkguide/establish-connection.html),
[SDK display scaling](https://docs.aws.amazon.com/dcv/latest/websdkguide/connection-class.html).
DCV retains its default sixty-minute idle-client timeout. Reconnecting resumes
the workspace; the meeting deadline controls VM and disk removal. Active-use
acceptance must send actual keyboard and pointer input throughout the hour.
Source retrieved 2026-09-28:
[DCV idle-client timeout](https://docs.aws.amazon.com/dcv/latest/adminguide/manage-disconnect.html).
Revocation
closes sessions, kills guest processes and locks the account; destroying the VM
removes retained workspaces. Teammates have training administration of shared
machines, so their workspaces are not a security boundary.

Hosted acceptance uses real OIDC/passkeys and rendered DCV sessions; local browser
fixtures establish layout and UI flow only. Five-team provisioning requires the
configured region to have at least twelve VPCs and sixty-four standard vCPUs of
quota. Budgets provide alerts, not a spending cap. Nightly backups briefly stop
the service, closing transports; users reconnect to their retained workspaces.

A typed maintenance `cancel-build` request fences the hosted worker before cancelling a specific template build and queued duplicates. Meeting assignments and other jobs remain intact; native build journals remain available for explicit cleanup.
