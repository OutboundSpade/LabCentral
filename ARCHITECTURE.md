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

`OpenWithOptions` accepts portable `EngineOptions` for image-build, machine-create
and machine-cleanup concurrency within each operation. Existing `Open` callers
retain defaults of three images, four creates and four deletions. Validation
rejects limits outside 1–32 before opening state; zero selects the default.
Waiting native operations check cancellation before invoking a provider.
The CLI accepts these limits as flags or private JSON defaults under the XDG
config directory, with explicit `--config`/`LABCENTRAL_CONFIG` selection and
command-line overrides. Saved paths must be absolute; automatic mutation approval
cannot be saved. Command help, interspersed options and positional validation do
not start the provider; local lists remain available without it. Structured JSON
results preserve version 2, while ordinary lists use aligned column headings.

The provider never opens the library database. It owns a separate mutation
journal for native subresources. The current provider executable routes both
platforms in one separate process; splitting it into platform-specific binaries
is possible without moving portable logic into adapters. The provider is trusted
operator code, not a sandbox for arbitrary third-party executables. The protocol
is generated from typed Go structures, uses explicit version 2 envelopes, bounds
message size, and rejects unsupported versions. Resource-specific opaque bytes
are adapter state, never author-supplied portable manifests.
Image file selections and machine hostname peers use appended protocol fields,
preserving existing field numbers. A source-validation round trip checks that
these restrictions survive transmission to the provider before image building.

## Source, environment and plans

`lab.yaml` version 3 replaces version 2 without an author-source compatibility reader. The format,
plugin protocol and stored-record versions are independent; the latter remain 2.
Within v3, changes add optional fields or accept previously unsupported values;
changes of meaning require a new format version. Strict YAML decoding is shared
with hosted instructor import and rejects unknown/duplicate fields, aliases and
multiple documents. Source snapshots reject symlinks and unsafe paths and have
bounded size/file count. The source digest binds file bytes to the parsed lab.

Portable objects declare named networks, NIC host offsets, OS family and egress.
Omission means one network `lab`, one NIC, Linux and egress `none`. Empty typed
AWS/Proxmox option structs import no provider SDK; `platforms` can restrict targets.
Namespaced extensions retain canonical JSON without interpretation, with 64 KiB
combined across the manifest. Builds receive lab/image extensions in generated
`labcentral_extensions` variables. Inputs have typed scalar defaults and optional
500-byte descriptions; required inputs have no defaults. Optional unsupplied values
without defaults are omitted. CLI strings are checked against their declared types.

Static platform capabilities reject Windows, author CIDRs, Internet egress,
forwarding and multiple networks/NICs before environment bindings or allocation.
Adapters repeat these checks before native calls. Format validation still checks
private IPv4 /16–/28 CIDRs, NIC references and unique usable offsets (4–250).
Explicit addresses reserve offsets first; remaining machines use offsets from 11,
skipping reservations in name order. `hostnamePeers` is `all` by default, a named
list for selected peers, or `[]` for localhost only, including after persistence
and plugin transport. This controls initial name resolution, not firewall policy.

Go YAML tags and a structural annotation table generate `schemas/lab-v3.json`
through `go run ./tools/schema`; a synchronization test rejects stale output. The
conformance corpus checks strict YAML plus JSON Schema 2020-12 alongside Go
validation/planning, distinguishing structural, source-reference and capability
failures, and includes examples and templates. YAML-only constraints run before
conversion because JSON cannot retain aliases or duplicate keys.

Plans bind source, effective inputs, environment, base identities and build policy.
An explicit `build.files` selection binds only those file bytes, the image definition
and lab extensions; omission binds the whole source. Modified plans are revalidated
before mutation. Deployment copies machine entries before adding native image
references, addresses and keys, so independent instances preserve the caller's plan.
AWS instance selection uses an operator allowlist and reports allocated capacity.
Proxmox reserves disjoint VMIDs and per-VLAN /24s.

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
image fence before checking retained references. Removal and abandonment persist
cleanup intent under the allocation fence, release it for native I/O, and briefly
retake it to atomically release reservations and records after confirmed absence.
Unknown cleanup blocks cache reuse until reconciled. Credential generation uses
the instance guard outside the global allocation fence. Deployment releases those fences
before native provisioning; after the network is ready, up to four machines provision
concurrently. Each successful sibling is persisted, even when another fails.
Deployment, recovery and destruction require an exclusive instance lifecycle lock.
Destruction waits cancellably for busy guests instead of requiring a busy retry.
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

### Private instructor format v3

The standalone `instructor` package owns private types, shared strict YAML decoding,
field-path/line errors, full template validation and guest payload assembly. It has
no hosted, identity, HTTP, store or provider dependency. `instructor.LoadTemplate`
excludes private material using `lc.LoadWithOptions`, leaving author files intact.
The CLI validates it when present; `--template` additionally requires README.md.
Go types generate `schemas/instructor-v3.json`; schema and Go share a conformance
corpus. Instructor v1/v2 author formats are removed. Stored history and confirmed
cleanup retain their original identities and durable storage keys.

Visibility explicitly selects students or coaches; only coaches may hide machines.
`practice.desktopMachine` identifies a visible desktop-enabled machine for stages,
scenarios, personal practice and meeting desktops. Visible image builds select
explicit files excluding lab.yaml and private image playbooks, and hostnamePeers
exclude hidden guests. Visible guest payloads are checked structurally: they cannot
select hidden guest identities, lab.yaml, or build files used only by hidden images.
File contents are not scanned.

Checks and `breaks` retain ordered guest steps and private extensions. Nonempty
resets are validated and breaks require a reset. Each step always includes its
playbook and selects additional `files`, portable `assets` under `template/`, and
optional `guestHosts` under `labcentral/hosts.json`. Generated
`labcentral/context.json` contains machine, lab/template extensions and the owning
check/break extensions; reset has no owner extensions. Author paths cannot enter
reserved namespaces; duplicate destinations and file/directory collisions fail.
Timeouts default to 90 seconds and accept 10–600, driving the guest subprocess,
SSH wrapper and aggregate deadline. Expired steps have unknown outcomes; uncertain
meeting mutations require an explicit reset and scenario mutations require cleanup.

Stages select exactly one `completion`: an answer with 1–10 accepted values and
explicit case policy, a pinned check or `instructor: true`. Answers trim whitespace,
optionally fold Unicode case and compare every normalized answer digest in constant
time. Stages may link a `break`. Scenario variants name `breaks` and `stages`, certify
compatibility explicitly and cover each distinct selected break set with guest
recovery checks. Library metadata and existing bounds remain enforced.

The configured SSH GitHub repository supplies templates. Hosted import validates
all directories independently and reports directory/field errors through the
administrator API. Each bundle/source/template/head revision commits in one SQLite
transaction; re-import preserves image and cleanup state. Runtime uses trusted
pinned bundles with file, instance, ownership and digest execution checks. Public
DTOs call exercises breaks; historical job/object keys stay readable. Example Branch
fallback programs and publication authoring/release/unpublish endpoints are removed;
stored attempts, releases, progress and cleanup remain readable.

**Format v3 was deployed to the hosted AWS service on 2026-10-14** over the idle
installation, after a fresh backup, with its retained state. Real administrator and
student OIDC sessions imported all three migrated templates without failures, kept
earlier revisions listed and hid the coach-only template from students. The
service-recovery images were rebuilt, and one student scenario attempt was prepared:
its reset and selected break ran through the v3 guest payload, and the student
opened the rendered DCV desktop and repaired the break by keyboard. The v3 guest
check failed before the repair and passed after it, completing the stage. The
allocation was stopped with confirmed native cleanup and the temporary identities
were removed. Example Branch v3 images, meetings, coach templates, Proxmox and larger
cohorts were not qualified in this rollout.

## Proposals after the foundation

Registries, additional hosted platforms, classroom governance beyond published
stages and meeting/team membership, and EC2 Image Builder are deferred. They should
use the public library without putting product policy into portable lab declarations.
This section describes proposals, not implemented contracts.

### Platform options

Environment files keep credentials, storage, pools, keys and subnets, and will add
allowlists for author-requested instance types, CPU types and egress. Typed platform
options cannot change topology, reachability or access; adapters ignore other
platforms’ blocks.

### Networks, gateway and guests

The portable declarations above reserve future routing and egress requests.
Networks will be isolated except through declared forwarding machines. Internet
egress will require operator enablement per network.

Lab machines receive no public addresses. Each lab has one platform-owned gateway,
outside the author inventory, that reaches each machine's first NIC for SSH, desktop
tunnels and checks. Its host key is pinned like a machine's. On AWS the gateway sits
in an edge subnet with the lab's only public IPv4 address; lab subnets reach it by VPC
routing, security groups admit management only from the gateway, and egress networks
route their default traffic to the gateway, which source-NATs only egress-network
sources. Machines need no management NIC. On Proxmox the gateway VM joins each lab VLAN.
At AWS us-east-1 on-demand prices, a t3.micro gateway plus one address costs about
as much as three per-machine public addresses and less than a NAT gateway. One
management path is preferred over separate public-address and gateway modes.

Windows guests use an OS family bound to environment bases, PowerShell build
scripts executed inside the build guest, EC2Launch or cloudbase-init bootstrap, and
an SSH/PowerShell check launcher. Ansible for Windows may later run from a disposable
Linux controller guest; author code never runs on the operator host.

Delivery order after the portable v3 foundation: instructor migration; platform
options; gateway and egress; multiple networks, NICs and routing; Windows. Each step
requires live AWS and Proxmox acceptance in a configured test environment.

## Hosted team service

The hosted milestone adds a separate application layer around the public library:
OIDC provider authentication, transactional team membership, a durable infrastructure
worker, and user-owned desktop workspaces. The library remains independent of
HTTP and identity policy. This milestone targets AWS; Proxmox remains a library
and CLI platform. The hosted AWS twenty-user acceptance is described above.

Retained hosted state written by the v2 server remains readable after the format
upgrade. Hosted JSON readers project instructor v1/v2 `exercises`, flat visibility,
legacy stage fields and scenario selections into current history views without
rewriting pinned source or guest programs. Publications, independent progress,
release gates, attempt ownership, team membership and scoreboard results retain
their saved identities. New attempts and guest actions on v2 source revisions
return HTTP 409 with a historical-revision message; scheduled and interrupted
guest work cannot execute those programs. Meeting and personal cleanup use the
original instance identities, and image removal uses saved build cache keys, never
the v3 planner to derive old keys. Unbuilt historical revisions can be listed
without planning; missing keys on a built historical revision block image removal.
Refresh imports new v3 revisions alongside the retained revisions. Local upgrade
tests open state written by `main` using production templates at `773d88f`, with
a fake engine; the small checked-in JSON excerpt omits guest file payloads. These
checks establish hosted state compatibility, not live platform, OIDC or desktop
acceptance.

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
Lucide icons. Its local SVG mark is a central node inside a lab frame. The layout
uses static Tailwind utilities for its shared panel surfaces and section labels and
favours a few quiet panels over ruled sections, short labels over explanatory
copy, and purple only for selection and principal actions. A sticky top bar has
three destinations: Practice (catalog, templates and player), Meeting (team choice
or the team lab, with a dot while a meeting is open) and Admin. On phones it
becomes role-appropriate bottom navigation; the header carries no backdrop filter,
so the fixed phone navigation stays anchored to the viewport. Administration has a
secondary Meeting and Templates switcher. The header shows
administrative activity only while operations run or need attention.
Practice defaults to the template library with category, difficulty, duration and
skill tags, baseline/scenario availability, pinned detail and owned attempt history. The guided player has a stage rail beside the stage and a single
workspace strip; local download and CLI commands open in a dialog. The temporary publication editor is removed; its guarded administrative route
redirects to Templates. Existing stored lessons and progress remain readable. The team lab page titles the team, and its desktop
panel precedes the handover on narrow screens while the handover stays on the left
on desktop.
Phone navigation reserves bottom and lateral safe areas; primary touch controls
remain at least 44 pixels high. Browser focus scrolling clears the mobile
navigation and sticky
exercise action bar. On short laptop screens the pinned activity feed has its own
keyboard-accessible scroll area. The desktop lesson panel is 320 pixels wide
from 1024 pixels and 352 pixels from 1280 pixels, with matching page padding to
retain room for the remote workspace.
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
to the lab or team list during an open meeting, and otherwise to the template library. Team changes require a named confirmation and recheck the
current membership before submitting. Desktop readiness transitions update the
page title and an accessible status region; the launch label stays fixed. Desktop
help describes idle reconnects and browser clipboard behavior. Instructor meeting
summaries use one compact stat panel (time left, labs ready, students joined and
active desktops); joined students count committed memberships once,
independently of pending seat reservations. Failed-team rows show preparation errors
and recovery.
The scoreboard is a horizontally scrollable teams-by-checks table with a pinned
header; its schedule selector initializes from saved data without overwriting an
unsaved choice during polling. Exercise selection precedes team selection and a
sticky action bar; confirmation counts teams and offers a collapsible list.
Template commit links use a sanitized administrator-only repository URL supplied
by the API. HTTP authorization, meeting policy, queue claims and native desktop
behavior remain unchanged for team meetings.

### Template library and practice attempts

The hosted learning layer adds `/labs/`, `/learn/?id=PUBLICATION_ID` and
`/admin/labs/` to the existing static routes. `/labs/` is the template-first library,
with revision detail links at `/labs/?template=TEMPLATE_ID`; the old admin publication
route redirects to `/admin/templates/` after authorization.
Authenticated `GET /api/templates` and `GET /api/templates/{id}` return explicit
public projections: revision metadata, description, student handover, declared
machine sizes/access, safe stage instructions, library metadata, scenario summaries and IDs eligible for
baseline practice. Variant IDs, selected break IDs, private programs, checks and
answers never enter these projections. They never serialize the private `Template` or `InstructorBundle`.
The saved `imagesBuilt` flag is informational; reads do not inspect providers,
and cloud admission still validates readiness. Browsing never provisions guests.

Version 3 instructor manifests own reusable stages and optional standardized
`library` metadata: category, beginner/intermediate/advanced difficulty, estimated
minutes and up to twelve unique tags. The standard authored source library lives
under `templates/`. Its service-recovery template provides a Linux workstation,
branch services, baseline stages and independently composable web/scheduler breaks.
It is imported through the configured private template repository; source presence
and strict-import tests do not establish native image or guest acceptance.

Authenticated, CSRF-checked `POST /api/templates/{id}/attempt` accepts an optional
`scenarioID` and a required random 24-hex `requestID`. A per-user lock and transaction
save an owned publication snapshot and private attempt together, without provisioning.
The request key binds identity, template and mode. Retrying the same key returns the
same attempt; changing its meaning is rejected. The static client retains only the
owner, template/scenario keys and random request IDs in per-tab session storage until
a confirmed outcome, preserving unknown requests across reloads and transient session
refresh failures. A confirmed sign-out or different identity clears them; unavailable
storage falls back to in-memory retry keys and attempt history. Baseline attempts select eligible
authored stages in template order. Another attempt starts new progress while retaining
prior progress. All student reads, answers, checks, solutions, downloads and cloud
starts enforce attempt ownership; catalogs batch only that user's attempt progress.
Active cloud cleanup must finish before creating another attempt.

Legacy shared publications and instructor-paced lessons retain their stored API
behavior and progress. Their editor and navigation have been removed, with no
name-based deletion or mutation of live publications. Administrative authoring,
release and unpublish endpoints are removed. New attempts snapshot template stages
and exclude break-linked stages and instructor checkpoints from the baseline.

A publication has a title, description, estimated duration,
self-paced or instructor pacing, and 1–20 ordered stages. Each stage uses a
server-matched answer, a check from that pinned revision's private instructor
bundle, or an instructor checkpoint. Template validation precedes import; attempts snapshot trusted pinned definitions.
Answer matching trims whitespace, checks all alternatives in constant time and
uses the authored case policy. Stored single-answer lessons retain case sensitivity.
Accepted answers, check references and solution text stay in administrator records
and views; ordinary student DTOs contain instructions, prompts, completion rules,
an optional solution-availability flag and their own progress. An authenticated,
CSRF-checked `POST /api/learning/{id}/solution` deliberately reveals an author's
solution from that publication snapshot only for the student's current or
completed released stage. It does not change progress or run checks. Unknown,
future, unreleased, solution-less and unpublished stages are denied. The player
renders solution Markdown with raw HTML disabled and discards revealed text on
stage, publication or identity changes. Downloads and desktop lesson reads do
not contain solution text. Correct answers, check results and progression are persisted. User
locks prevent answer replay from advancing twice, and check completion commits
its result and progression in one transaction. Stored instructor publications keep their release count; completion cannot bypass
an unreleased stage. New content requires a new publication instead of changing existing progress.
Student instructions and the stage outline remain visible; release gates completion.
Unpublishing hides lessons and denies new starts/downloads without removing the
history or preventing cleanup of an existing allocation.

Authenticated downloads contain only the retained portable source and a separate
student instruction document. Instructor files, accepted answers, provider
configuration and credentials are excluded. Local operation means the existing
CLI with a student-owned configured AWS or Proxmox environment; no laptop
hypervisor, Docker or QEMU adapter is implemented. Cloud action verification is
not claimed for local runs. Students can submit answers through the hosted UI
while following the downloaded lesson; local infrastructure cleanup is the
operator's responsibility.

Personal cloud practice is independent of team membership and meeting cleanup.
The pinned revision must have built images and its named `practice.desktopMachine`.
One active personal allocation is allowed per student across all publications.
A SQLite transaction records quota reservation, a fresh exact library instance
identity and a `practice-prepare` job before native mutation. Preparation validates
and reuses that identity on recovery, without treating a lost response as a
failed create. Native preparation inherits the saved allocation deadline;
interruption at expiry persists cleanup for that same identity even if policy
revalidation is busy. `practice-end` retains cleanup intent through uncertainty and
uses the reserved cleanup lane. Pending allocations protect shared image cache
keys before a native instance exists. Scheduler claims serialize preparation,
guest verification and destruction for a given allocation while preserving
independent meeting work. Meeting destruction skips personal desktop records.

`hosted.Config.weeklyCloudMinutes` defaults to 180 per student, with UTC Monday
resets. Usage is allocation wall time from reservation through confirmed native
destruction, including provisioning, failures and pending cleanup. Each week's
usage includes only its overlap with those intervals, rounded up to the next
second. Admission reserves at most the remaining quota and ends at the next UTC
week boundary; a reset cannot admit a second run while old cleanup is uncertain.
An ending allocation immediately loses desktop authorization. Deadline intent
is persisted and retried after worker restart; the host must run to perform
cleanup, so an offline control host can leave resources and charges active.
Stopping destroys cloud workspace files; progress survives separately.

Quota reads use a covering `(user_id,ended,created)` index and separate searches
for pending cleanup and recent ended intervals. A conservative two-date lookback
retains fractional UTC-week boundaries and supported timestamp offsets; exact
overlap and rounding remain in Go. Template catalog reads join pinned metadata
without copying or base64-decoding source/program files. Public fields still pass
through explicit DTO projections. Practice catalogs decode publication summaries
and batch only the authenticated user's progress keys, at most 128 per query.
No identity, progress or native readiness is cached across requests.

The subsequent backend review passed authorization/persistence/failure regressions
and race checks. Local fixtures measured quota reads with 10,000 expired personal
allocations at 10.2–10.6 ms before and 27.8–28.3 µs after; sixteen large template
revisions reduced allocations from about 31 MB to 105 KB per catalog request.
Learning catalog reads batch progress instead of querying once per publication.
Three blocked personal preparations run concurrently while student reads/answers
remain responsive, and the fourth waits for the existing bulk budget. These
changes are not part of the preceding deployed release's live qualification.
Unpaginated catalogs, per-poll lesson planning and administrator/native inventory
reads remain scaling limits; these local fixtures do not qualify a larger cohort.

Released action stages in ready personal labs are observed with a 20-second base
cadence through the bounded guest-local Ansible launcher. After five minutes
without an answer, check-now request or authenticated desktop lesson read, the
base interval becomes 60 seconds. Persisted activity and next-due records are
separate from automatic progress updates; per-allocation jitter spreads checks.
Check now bypasses the periodic due time and deduplicates active work. Policy
iteration only queues guest work; it never executes slow observations inline. Only a
confirmed successful check advances progress; failures preserve the stage and
unavailable results never count as passes. The worker rechecks allocation, pinned
source and current stage before execution and rechecks allocation expiry when
saving a passing result. Students may also queue a check explicitly. Desktop
launches use the existing separate-origin, single-use-ticket DCV transport with
allocation ownership in place of meeting membership. The desktop origin exposes
the owner's lesson through authenticated `/lesson` reads and answer/check writes;
writes require its exact Origin, JSON content type and a custom header. Every
lesson request also binds to the run ID rendered into that desktop window, so a
cookie replaced by another desktop tab cannot redirect an answer to another lab. It serves
the same built UI styles locally, without infrastructure credentials or third-party
assets. This supports a lesson panel beside the actual remote desktop; the static
player also provides local-run guidance and cloud lifecycle controls.

These learning and quota changes have local authorization, persistence, race and
failure-case coverage. The requested hosted maintenance deployment on 2026-10-08
passed live acceptance with real administrator/student OIDC sign-in and one
personal AWS allocation independent of the existing ended meeting. A rendered
native DCV session accepted guest keyboard input and sidebar answers, automatically
advanced a pinned guest action check after instructor release, completed the
instructor checkpoint and retained completed progress on reconnect. Both pacing
modes, private student DTOs, portable download filtering and charged quota were
checked through the live service. Desktop and mobile lesson layouts were inspected.
Larger personal cohorts, full three-hour exhaustion and UTC-week reset remain
unqualified live; prior meeting load acceptance does not qualify personal load.
Native cleanup of the exact owned test allocation was confirmed. Temporary
publications were unpublished and test identities removed; existing meeting/team
records were preserved.
The subsequent template explorer and version 2 stage extension passed local
strict-import, authorization, cross-student solution-access, snapshot persistence
and hosted race checks, along with the Go suite, generated-contract verification,
UI type/build checks and browser fixtures. Template exploration, stage publishing
and freely selectable meeting stages were inspected at 1366×768, 1024×768 and
390/360-pixel phone widths. Browser regressions cover retained stage selection on
same-session refresh, unlinked instructor breaks, stage-only meetings, unavailable
revision pins without silent substitution, and late solution/template responses
after navigation or identity changes. The extension and Example Branch version 2
migration were deployed through verified hosted maintenance on 2026-10-09. Real
administrator/student OIDC sessions verified template card/search/detail/stage
exploration and pinned publication authoring at desktop and phone widths, private
DTO/download filtering, current/completed solution reveals, future/unknown/CSRF
denials and unchanged progress after reveal. A temporary owned personal AWS lab
completed all six authored baseline stages using real guest checks. Rendered DCV,
actual guest keyboard input, workstation web access, charged quota and reconnect
with retained progress/files passed. The six-stage Example Branch baseline
publication remains available; retained meetings and prior publications keep
their original revisions. The exact temporary personal allocation's provider
cleanup was confirmed, its acceptance publication was unpublished and both
temporary OIDC identities were deleted with confirmed 404 reads. New
incident-stage meeting controls retain local
browser coverage and prior acceptance of the unchanged guest programs; no new
live meeting incident run or randomized execution is claimed.
The 2026-10-09 proportion refinements passed type/build, affected browser flows
and negative keyboard-focus regressions, plus live sign-in and a new owned
personal desktop lesson flow. Rendered native DCV was inspected at 1024×768,
1366×768 and 390×900, including mobile lesson collapse and guest keyboard input.
Instructor progression, automatic guest verification and reconnect were rechecked.

The simplified navigation, practice/player layouts and lab-frame mark were deployed
through verified hosted maintenance on 2026-10-09 after Blueprint source/rendered
review, Svelte type/build checks, generated-contract verification, Go checks/race
checks and all five frontend plus hosted/access browser suites. Eight deployed
route bodies and the logo matched the exact tested build. Real administrator and
student OIDC sessions passed desktop/mobile UI and authorization checks. An owned
personal AWS desktop rendered at both widths, accepted guest keyboard input and
wrong/correct lesson-sidebar answers. One separate preparation attempt failed with
an EC2 route-table lookup error; its cleanup was confirmed before a later successful
allocation. All three owned test allocations and temporary identities were confirmed
removed. Existing meeting, team and publication records remained unchanged. This
does not add cohort/load or meeting-exercise qualification. Detailed evidence and
installation identifiers remain outside source.

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
job. Bulk work (build, preparation, refresh and image removal) defaults to three slots, with
at most two image-build jobs. Guest work (checks and exercises) defaults to twelve separate
slots, so bulk saturation cannot consume its capacity. An additional slot is reserved
for meeting destruction or authorization cleanup. Resource claims still apply
across all lanes. Observational jobs share a team/allocation claim and exclusively
claim known target machines; reset/break and lifecycle work retain whole-team
exclusion. Steps within a job stay sequential and use the guest flock. Verified
SSH clients are reused per instance/machine for the job, with pinned identity
checks on every use. Enqueue and completion wake the dispatcher; a five-second
tick provides fallback. Immutable queued claims are cached, transferred on
admission, and discarded when jobs leave the queue.
Hosted SQLite has one write connection with BEGIN IMMEDIATE transactions and
four read-only, query-only WAL readers; WAL and FULL synchronization remain.
Compound quota, seat, import, template and enqueue decisions use the writer.
Bounded request bodies are read before transaction admission and mutation
responses are buffered until commit, so clients never retain that connection.
Meeting check enqueue uses batched statements; administrator run pointers,
job timing/labels, revision heads/operations and personal image references are
batched rather than read once per student or team.
Desktop/ticket maps have their own mutex used only for short memory operations.
Desktop revalidation performs durable reads and cleanup enqueueing outside it.
User intent locks are cancellable and reference counted; desktop generations
persist opening/closing intent before native calls, release the user lock during
I/O and revalidate authorization before publishing a relay. Cleanup rechecks the
current generation, so stale expiry snapshots cannot close a new open.
Personal destruction relies on persisted ending intent and scheduler claims,
without retaining a user lock through native calls. Authorization cleanup
continues across independent record failures, retains uncertain intents and returns their combined error;
one unreachable guest cannot prevent later records from being visited.
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
A prepare job defaults to at most
three team deployments concurrently. A shared, cancellable provisioning gate also
includes personal practice, so team and personal preparations together cannot
exceed `worker.maxProvisioningConcurrency`. Ready teams need no gate or native work;
waiting teams recheck meeting state after admission. Personal preparations acquire
the gate before their durable preparing transition, preserving an intervening stop.
Cleanup never acquires the provisioning gate. Each team persists its own readiness or failure;
one failure does not cancel peers. Meetings with both ready and failed teams report
`partial`; all-team failures report `failed`. Aggregated job errors name the failed teams, and
retry reconciles their reserved instance IDs while keeping ready teams accessible.
The protected service config exposes startup-only `worker` limits for provisioning,
bulk, guest and image-build jobs and job timeout, plus portable `engine` limits for
native work within each operation. Service-wide `aggregate` budgets supplement
those limits: eight native machine create/delete operations and 24 active guest
SSH steps/terminal sessions/desktop operations, shared across jobs. Image build
and abandonment reserve native capacity conservatively for their internal guests.
Adapter creates/builds also reserve SSH admission for guest readiness/validation;
desktop operations acquire it before library machine guards to avoid lock order
inversion with checks. The server wraps its provider before opening the engine
and shares that budget with hosted SSH work; portable engine types carry no hosted policy. Aggregate
limits accept 1–64 and are reported by --check-config alongside worker/engine
policy. Defaults are three bulk/twelve guest/two build jobs and 120
minutes; concurrency accepts 1–32 and timeout 1–1440 minutes. Image-build jobs cannot
exceed bulk capacity; an omitted image limit adapts to the smaller bulk budget.
The total worker ceiling is bulk plus guest capacity plus one reserved cleanup slot.
Allocation deadlines still override job timeout. The server validates static
limits before starting the provider, and `--check-config` reports effective policy
without OIDC, provider startup or secrets. Config changes require restart and are
not exposed in public DTOs. Local tests cover configured limits, shared admission,
cancelled waiters, cleanup admission, reopened readiness and private CLI defaults.
The earlier worker/engine settings deployed on 2026-10-13 and completed native
AWS meeting/personal preparation and cleanup with two guest slots. The twelve
slots, aggregate budgets and concurrency changes have local failure/race/load
coverage only. Configured capacity ceilings and larger concurrent cohorts remain
unqualified live.
The provider permits unrelated image builds, networking and machine operations
concurrently. Owner fences serialize networking, image and access lifecycle work;
distinct machines share the owner fence and have their own name fences. Create,
discovery and response-lost retries for the same machine cannot overlap its journal.
Networking deletion waits for all machine operations in that owner. Cancellation drains overlapping
meeting operations before deadline cleanup proceeds; unrelated image builds continue. It preserves the reserved
engine instance ID across retries, reconciles incomplete work, and prioritizes
meeting destruction over ordinary queued work. Ending a meeting interrupts active
meeting provisioning, exercises and checks; failed cleanup remains reported and
retryable. Ending uses a bounded pool of eight teams; authorization cleanup uses eight
independent access records concurrently. Each resource retains ending/closing
intent and commits its own confirmed outcome; errors are joined and uncertain
outcomes remain retryable. Ending visits every team even when a peer fails.
Page reads and unrelated desktop launches do not share its resource claims.
Provider journals distinguish explicit AWS authorization rejection from uncertain
transport outcomes. A rejected create can be retried; a timeout still requires
discovery. AWS creation permissions cover tagged outputs and separately authorize
their already-owned parent resources ([EC2 authorization reference](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html), retrieved 2026-09-28).

Retained records describe earlier fixed Example Branch exercises. Their hosted
execution fallback is removed; new operations require pinned instructor programs.
Administrator reads preserve historical exercise records. Guest administrators
can inspect authored effects and evidence inside their own VMs.

A transaction reserves selected ready teams and persists each exercise intent
with its durable job. The worker rechecks the meeting, deadline and latest team
operation on every replay. Superseded jobs cannot reapply a break after a reset;
completed jobs do not repeat mutations. Interrupted resetting/applying states
become unknown after restart and cannot replay author mutations; explicit reset
is required. An error after entering mutation also retains unknown outcome.
Interrupted observational checks can run again. A failed team does not prevent other
selected teams from running. End-of-meeting cleanup cancels exercises and retains
priority. The library's caller-owned SSH callback holds the selected machine fence
and a shared lifecycle guard; destruction waits for all guests without serializing
access to unrelated machines. Hosted policy stays outside the library.

The fixed launcher delivers only the step’s pinned files and generated context,
and runs author Ansible inside the verified guest under a shared guest lock.
Timeouts leave unknown outcomes. Legacy exercise-key cleanup remains after
confirmed VM destruction. No generic remote-command HTTP endpoint or author
execution on the control host is introduced.

Templates supply `instructor.yaml` version 3 and optional private programs under
`instructor/`. The importer validates the complete portable/private pair before
provisioning and removes private files from its temporary snapshot. Bundles stay
separate from native images, portable build provenance and ordinary student APIs.
Step-local assets, files, identities, context and visibility follow the instructor
contract above. Guest administrators can inspect transferred programs and effects;
these files do not create a guest-side security boundary.

Example Branch’s v3 teaching revision retains nine stages: six baseline discovery
and verification stages and three recovery stages linked to stopped-web,
database-port and FTP-root/defacement breaks. Private solutions, notes and reset
behaviour are preserved. The v3 source and teaching migration imports as a new
pinned revision; existing meetings and publications retain their stored snapshots.
Local validation/import checks do not qualify native guests or images.

The private template repository additionally contains a separate `branch-services-ir`
template derived from that pinned v2 source. It preserves the original template
and adds a fifth, controlled support guest. Persistent UTC journals, native FTP
logging and targeted audit rules are provisioned in the new images. Runtime
preparation observes actual kernel audit readiness over pinned SSH and persists a fresh run
identity, victim clock timestamp, journal cursor and log offsets on the coach guest
before any attack effects. The support
guest uses only synthetic training credentials and guest-generated keys, performs
a real FTP upload and password SSH login, writes key persistence and a harmless
staging artifact, then holds a key-authenticated session for at most eight hours.
It pins the victim's prepared host public key and never automatically reconnects.

Private guest checks require native FTP account/source/PID records linked to audit
publication writes, SSH authentication linked through PAM audit sessions to key
and staging writes, a matching active connection and visible publication effects.
FTP privilege separation may leave the upload worker's loginuid unset; its native
upload PID supplies the account/source link rather than an invented audit-session
claim. Native vsftpd audit paths may be relative to its chroot; the publication
correlation accepts that exact path only with the native vsftpd executable,
authenticated upload PID, account and publication audit rule. Evidence receipts
cannot replace raw logs. Missing or truncated/rotated
logs, audit loss and stale-run records fail the gate. Observational recovery checks
require removed access/persistence, rotated training credentials, fresh publisher
authentication and clean source/visible content. Coaches assess the report.

Targeted reset stops the owned support service first, verifies its exact retained
key identity, removes only run-owned guest artifacts, and restores the synthetic
service baseline while preserving other keys, publications, bank data, desktops
and historical evidence. Unknown ownership or remaining sessions requires manual
review. The changed portable source binds all five image cache keys to new builds.
On 2026-10-12, all five images built and passed native AWS boot validation in the
explicitly configured hosted test environment. A disposable five-VM team on pinned
private revision `3d68fa8` passed real OIDC/student DCV keyboard access, native
logging and incident correlation, deliberate logging-misconfiguration rejection,
containment and credential/publisher recovery, and retained raw evidence after an
actual victim reboot. Targeted reset preserved unrelated keys/publications and the
actual student workspace; reapplication produced a distinct run identity/key and
passed the native evidence gate again.
One initial desktop launch failed with an unrecorded response; its cause remains
unverified. Retained access cleanup completed, and the bounded retry and subsequent
native desktop/keyboard/mobile checks passed.
A fresh five-VM team's first launch reproduced HTTP 503 after 51.75 seconds.
Native workstation logs showed Xdcv failing to initialize its display with GL
enabled. Virtual-session creation now explicitly uses `--gl off` on these
CPU-backed desktops, while still requiring an observed running X display.
The native diagnostic session reached a display in 9.362 seconds. After a
confirmed workstation reboot and deployment of that change, the first real
student launch returned HTTP 200 in 2.925 seconds; native keyboard input,
website access and both student documents were independently verified in the
guest. The exact owned desktop closed successfully. Affected DCV/access/build
and hosted race checks passed. This proves the observed cold launch on one AWS
team, not classroom concurrency or every future first launch.
That fresh team's actual incident application passed again: website, FTP,
logging readiness and native incident correlation passed, while altered-publication
and incomplete-containment failures remained the expected active-incident results.
The latest three original-template revision cards also confirmed their already
available four-image cache; no original template source or image was replaced.
AWS confirmed all three owned rehearsal allocations destroyed; temporary OIDC users and
sessions were removed. All eight pre-existing AWS images and all five IR images
were retained, with the original 43-file template and publication snapshots unchanged.
This is one-team AWS qualification; it does not establish a new
classroom load test or Proxmox qualification. The revision remains unpublished,
and the original Example Branch template and existing publications retain their
original source and snapshots.

Instructor bundles may declare `audience: coaches` and `hiddenMachines`. The
IR revision uses these to exclude the whole revision from student template
catalogs, direct revision views, publications, practice admission, solution
reveals and source downloads. Publication requests for coach-only revisions are
rejected. Retained accidental publications are also filtered and denied; missing
or inconsistent privacy metadata fails closed. Admin template views retain notes,
stages, solutions and walkthroughs. The authenticated template explorer projects
coach-only revisions and the complete topology only for admin sessions, labels
them Coach-only, and omits the publication link. Its cached views are cleared on
role changes as well as identity changes. The student meeting handover contains only
bank access and an open investigation assignment. Student inventories omit hidden
guests, while administrator inventories include all machines.

Optional portable image `files` selects exactly which pinned source files reach
that image's build guest. Nonempty selections require the image playbook and
reject duplicate or unknown paths; omission preserves existing author inputs.
The IR bank/workstation images select only their own provisioning files, shared
operations tasks and required public assets. They never receive `lab.yaml`, the
support provisioning program or other image inputs. This prevents full topology
from leaking through retained or recoverable build files. Packer uses a fixed
source staging directory with explicit cleanup, and the final guest sanitizer
also removes that directory before capture ([Ansible Local provisioner reference](https://developer.hashicorp.com/packer/integrations/hashicorp/ansible/latest/components/provisioner/ansible-local), retrieved 2026-10-12).
Hidden-machine teaching metadata requires explicit student-image file selections
that exclude the full topology manifest and private image playbooks. Native
filesystem inspection of the newly built images remains an acceptance gate.

Optional portable `hostnamePeers` selects each machine's initial `/etc/hosts`
entries by declared peer name; omission preserves the original all-peer behavior.
Validation rejects duplicates and unknown peers before provisioning. This is
portable name-resolution configuration, independent of HTTP identity or roles;
it is not a network firewall. Hidden-machine instructor metadata requires explicit
student-guest peer lists excluding hidden guests. Students may discover the
source address through genuine logs or network observation, as intended, without
being told the source's coach role or receiving its login credentials.

Guest steps may select explicit `files` and request selected `guestHosts` public
addresses/host keys from the pinned instance. Coach-only templates require
explicit file selections for bank guests. The IR actor, verifier, generated keys,
expected correlations and durable run/evidence receipts reside solely on support.
Bank VMs receive ordinary baseline/service-check inputs, never the actor or
verifier bundle. No actor handshake or saved solution is written to bank storage.
The coach verifier uses the synthetic, publicly documented bankops training
account to observe bank services over pinned SSH; these operations create their
own distinguishable bankops sessions. No management or provider private key is
copied to a guest. The support image has no student operations account and permits
only management-key SSH for the library's lab account. Guest root access on a bank
VM does not grant access to coach storage; attack artifacts and raw logs remain
available for investigation. Unavailable observation or changed credentials fail
the checks and require coach review.

Administrators can select a ready meeting team and any declared SSH machine,
including support, in the meeting's Coach machine access picker. **Open SSH**
opens `/admin/terminal/` in a separate tab, with the meeting, revision, team,
machine and opaque library allocation ID pinned in its URL. The admin-only
machine endpoint projects those names, pinned context, status and access without
infrastructure addresses
or credentials. A separate interactive SSH WebSocket verifies the app Origin,
admin session and first-message CSRF/context/dimensions, then binds the team,
deadline, instance, source digest and declared machine to library-owned SSH material. It never accepts
a browser-supplied guest address, credential or remote-command job; interactive
keyboard input travels as binary frames.
Native ownership and the private lab address must match the plan; the management
endpoint may be the provider's distinct public address and remains host-key pinned.
Session/role and current meeting/team access are rechecked while connected;
revocation or expiry closes the channel. Bounded WebSocket Ping/Pong keepalives
release quiet half-open transports without delaying authorization checks.
Sessions last at most ten minutes and hold the machine/lifecycle fence, so coaches
must disconnect before running checks or resets on that machine. The locally
bundled xterm.js terminal renders ANSI output and raw keyboard bytes, with a
`xterm-256color` guest PTY. The server sends a ready control frame before accepting
interactive input; bounded resize controls update the guest PTY. Browser scrollback
and pending renderer writes are bounded and not persisted. Clearing access or
meeting context replaces the renderer, discarding queued output and selection
before delayed parsing can repaint old data. Excess output stops the connection
with a manual reconnect option. Output never controls browser navigation,
clipboard writes or the page title.
Transient transport failures use bounded backoff and repeat fresh session and
inventory authorization before each attempt. Recovery opens a new shell within the
same allocation, retains visible scrollback with a fresh-shell boundary and never
replays input. Normal shell exit, explicit disconnect, revoked access and expired
meetings stop recovery. The ten-minute channel limit requires a fresh shell too;
it is distinguished from meeting expiry. Cancellation releases the SSH connection
and library fence. A recovery boundary cancels an incomplete escape sequence and
saves the current cursor before leaving an alternate buffer and resetting terminal
modes, preserving normal-buffer history before the new shell's first output.
The xterm.js terminal was deployed on 2026-10-13. Live real-OIDC/native-AWS checks
covered the picker and separate tab, keyboard input, ANSI styles, actual selection
and clipboard copy, mobile PTY resize, offline/online recovery with retained
scrollback and no input replay, explicit disconnect/reconnect and normal shell exit.
Student access and invalid first-message CSRF were rejected; an active native SSH
WebSocket closed with authorization code 4003 after administrator logout.
This is an authenticated interactive guest transport, not a remote-command job
endpoint. The 2026-10-12 native AWS rehearsal verified administrator SSH access to
all five guests, the rendered coach console at desktop and phone widths, and
rejection of student access and an invalid first-message CSRF. Student inventories
and direct revision views hid the coach guest/private revision; actual bank-image
filesystem and hostname inspection found no private scenario programs or source
staging directory. The coach guest rejected the student training password, and a
deliberately wrong native SSH host pin was rejected. Local fixtures remain separate
from this native evidence, which is retained outside tracked source.

Meeting stage selection is a local instructor viewing choice, without a release
gate or guest mutation. Existing durable exercise, reset and service-check jobs
remain the execution mechanism. Instructors can freely revisit stage instructions
and solutions, apply a linked break to ready teams, or run meeting checks. A
template's unlinked break definitions remain independently selectable; adding
stages does not remove existing instructor operations. A
stage's `completion.check` does not turn the full meeting service scoreboard into a
stage-specific check. Stage-only templates can still provide a walkthrough even
without break definitions.

Randomized personal practice uses version 3 author-defined `scenarios` (at most twelve).
Each has a unique ID, briefing, difficulty, duration and 2–32 distinct variants.
A variant has its own ID, an explicit `compatible: true` certification, 1–8 existing
`breaks` in execution order and 1–20 self-paced `stages`. Every selected break
requires an action recovery stage, and linked stages must belong to selected breaks.
Duplicate combinations, unknown references, missing verification, coach-only scenarios,
invalid bounds and missing resets are rejected before provisioning. Authors must
qualify each combination: break programs cannot reset or undo another selected break.
The server does not infer compatibility from arbitrary Ansible or generate attacks.

Attempt creation draws a cryptographically random variant, excluding the immediately
previous saved variant for that student/template/scenario. The exact choice, full
private instructor snapshot and answer key are committed before allocation or guest
mutation. Retries and server restarts never rerandomize that attempt. Public views
include only the scenario briefing/summary and instructional stages; ordinary reads
omit selected program IDs and private variant details. Baseline downloads remain
portable; scenario downloads are denied because guest preparation is hosted-only.

After exact-instance deployment and source validation, the worker saves an `applying`
scenario journal, restores the baseline once and applies all chosen breaks. Only
confirmed completion saves `applied` before readiness. An applied checkpoint resumes
without repeating guest effects; an interrupted applying checkpoint remains unknown
and refuses reapplication. Failures retain the allocation, quota charge and cleanup
intent; students stop, wait for confirmed cleanup and create a new attempt. An ended
scenario allocation cannot restart the old attempt. Guest checks use the attempt's
private snapshot. Existing same-allocation scheduler claims serialize preparation,
verification and cleanup, while meeting operations remain independent.

These library/scenario changes were deployed on 2026-10-13. Local tests cover
strict import, compatibility/reference rejection, student ownership,
concurrent request deduplication, reopened persistence, independent progress, checkpoint
recovery, combined-break sequencing and uncertain guest/cleanup outcomes. Browser
fixtures cover desktop/mobile layouts, lost-response replay across reloads, actual
logout, delayed responses and unavailable browser storage. Separate live acceptance
used real administrator/student OIDC and the imported, pinned CCDC service-recovery
template with two completed native AWS images. A rendered student DCV session and
guest keyboard input confirmed both services were initially inactive, repaired
them, and passed both native recovery checks with saved progress. A second distinct
allocation confirmed a web-only outage, an active scheduler and independent zero
progress while the first attempt retained completion. Both allocations and the
owned meeting were stopped with confirmed cleanup. The legacy Published labs
navigation/editor was removed; stored legacy publication/history records were
retained. Initial cold desktop starts needed retries. The subsequent DCV startup
correction described below passed the first request on an untouched AWS
workstation. Larger cohorts and Proxmox scenario acceptance remain unqualified.
Host configuration, credentials and raw evidence stay outside source.

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
Before creating a virtual session, the shared DCV startup helper reads Xdcv,
the CPU graphics modules and their shared-library dependencies in the guest.
This moves fresh-image disk reads outside DCV's short display startup deadline;
it also applies to existing captured images. Preparation errors or cancellation
return before creating the session. The helper uses a fail-fast pipeline so a
failed module or dependency read cannot be hidden by a successful later command.
The 2026-10-13 cold-start fix reproduced a first-request failure on a fresh AWS
workstation: Xdcv exhausted its display-number deadline while initializing GLX.
Reading the graphics libraries before session creation allowed it to start.
The deployed fix passed a real-OIDC student's first request on an untouched
workstation in 60.4 seconds, including library preparation. DCV created one session,
reported a running display and rendered the desktop; keyboard input wrote a receipt
that was independently confirmed through pinned guest SSH. Local failure/race
checks cover preparation read failures, cancellation and unknown session outcomes.
A changed native boot ID confirmed a subsequent reboot; the original student
receipt survived before reconnection. Its first desktop request returned in
2.1 seconds and again rendered and accepted keyboard input without a user retry.
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
