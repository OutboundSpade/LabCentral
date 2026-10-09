# LabCentral v2

Portable virtual labs, built from small YAML files and ordinary Ansible playbooks.
LabCentral combines an embeddable Go library and CLI with a small hosted service
for CDC team practice. AWS EC2 and Proxmox VMs are library platforms; the hosted
service currently targets AWS. Images stay native to their platform.

The hosted AWS service is available at **https://labs.example.org**. Its live
acceptance passed with twenty independent desktops across five teams for one hour.
Cross-platform library qualification remains incomplete; see
[ARCHITECTURE.md](ARCHITECTURE.md#acceptance).

## CDC team labs

The hosted application lets competition members sign in with OIDC provider, choose a
team, read the Example Branch handover, and open an individual desktop on their
team's workstation. Files survive disconnects until the administrator's meeting
deadline destroys the lab. DCV disconnects clients after sixty minutes without
keyboard or pointer input; reopening the desktop resumes the retained workspace.
After copying locally, click inside the desktop and use Ctrl+V (⌘V on macOS)
to paste. Terminals use Ctrl+Shift+V. The **Copy from desktop**
button synchronizes its clipboard to your browser; allow clipboard permission when prompted.
In Firefox or Safari, first choose **Paste to desktop** and paste your text into
the box. After it closes, paste inside the desktop. This fallback transfers text only.
The lab and team pages show time remaining; at fifteen minutes they warn that
workspace files will be deleted. Desktop help appears beside **Open desktop**.
Navigation has three destinations: **Practice**, **Meeting** (team choice and your
team's lab) and, for administrators, **Admin**. Signed-in home navigation goes
directly to your lab or team selection during a meeting. Switching
or leaving a team requires a confirmation naming the affected teams. **Disconnect
desktop** is enabled only when your current login has an available desktop session;
it does not indicate whether the desktop browser tab is still connected.

`labcentral-users` admits students; `labcentral-admins` additionally
admits administrators. Switching teams revokes the old desktop before membership
changes. Administrators refresh the private template repository, build a pinned
revision once, then prepare teams from those images. Administrators choose
1–100 teams, 1–100 students per team, and a 1–8 hour meeting duration. Defaults are five teams, six students, and four hours. Time can
be added later up to eight hours total; ending a meeting requires confirmation.
Preparation runs up to three teams in parallel; a
failed team does not prevent others from becoming ready. Retry recovery preserves
ready labs and reconciles failed teams without replacing their instance identities.

**Lab templates** shows revisions newest first, with the latest GitHub revision,
commit dates and summaries, image counts, and build/cleanup jobs on each revision.
Build new images there, or remove unused images with confirmation. Removal keeps
the revision history, protects images referenced by retained labs, and preserves
images shared by another built revision. Failed cleanup stays retryable.

Independent image builds and old-template deployments can run together. The worker
uses resource dependencies and bounded capacity: three bulk operations (at most
two builds), two checks/exercises, and one reserved cleanup slot. **Live activity**
shows running/queued work, waiting dependencies, progress counts and active desktops.
Obsolete failures remain in Recent; Needs attention contains current failures.
Retry feedback appears beside the activity controls. Activity shows readable operation
names, elapsed durations and time since its last successful update. Recent history
starts at twelve entries with **Show more**; active operations are all shown. A
transient polling error preserves the last view; consecutive failures show a
separate stale-data warning. Sign-out and authorization loss clear private views.
Desktop launches bypass this queue and can run while checks or exercises use
another machine in the same ready team lab.

Pinned templates can include private `instructor.yaml` metadata and guest Ansible
playbooks under `instructor/`. They are extracted before image builds and are visible
only to administrators. Notes, break definitions and reset instructions stay with
the revision. **Service scoreboard** runs the revision's checks once or on a saved
schedule (one minute to one hour); it shows pass, fail and unavailable results per
team. The scoreboard uses a scrollable teams-by-checks table, with totals and last-run
times. Its selector reflects the saved schedule and offers one, two, five, ten,
fifteen, thirty and sixty minutes. Ready teams can be checked while other teams
are still preparing. Schedules start paused and stop when the meeting ends. See
[ARCHITECTURE.md](ARCHITECTURE.md#hosted-team-service) for the manifest contract.

The eight prerendered routes are `/`, `/labs/`, `/learn/`, `/teams/`, `/lab/`,
`/admin/`, `/admin/labs/`, and `/admin/templates/`. The SvelteKit UI uses daisyUI and Lucide; a Go service supplies
identity, APIs, jobs and desktop authorization. Live acceptance covers real OIDC
sign-in, retained workspaces, team switching, revocation, network isolation,
Example Branch exercises, cleanup and backup restoration.

**Admin → Meeting** summarizes time left, labs ready, students joined and active
desktop sessions; the header shows running or failed operations only when there
are any. Joined students count committed memberships once; team seat
counts also include pending reservations. Failed teams show the preparation
error and available retry.
New meetings default to the newest built revision and offer **Prepare labs now**;
time extensions confirm the new deadline. Exercise controls put the scenario and
walkthrough before team selection, with a sticky action bar and a counted team-list
confirmation.

Administrators can select ready teams under **Admin → Meeting → Exercises** to
apply the meeting revision's breaks. The current Example Branch template stops
Apache, moves the database listener, or disrupts FTP publishing and defaces the
homepage and branch-hours document. Every break first restores and verifies the
exercise baseline. Reset restores the authored baseline while preserving bank
data, other publications, desktops and historical logs. **Check services** runs
the revision's full checks. Controls, results and instructor walkthroughs require
administrator authorization. This is a targeted exercise reset, not a full VM
restore; unrelated package or configuration damage can require operator recovery.
Exercises use the meeting's pinned source and do not require new images. Older
meeting revisions retain their original exercise definitions.

## Explore templates and publish practice

**Practice** opens the published lab catalog; its **Templates** view is a
card-based template browser. Explore
a pinned revision to read its handover, inspect its machines and preview its
stages. Search helps find templates; revision selection keeps exploration
attached to the exact source. Browsing does not provision resources. Image status
reflects the saved build record; cloud readiness is checked when starting practice.

Hosted templates can own stages in `instructor.yaml` version 2. A stage contains
student instructions and an answer, a pinned action check, or an instructor
checkpoint. It can link an existing break and include an author-written solution.
The template owns this material once, alongside its existing reset and check
definitions. Accepted answers, guest programs and private references stay out of
ordinary student responses and downloads.

In **Admin → Meeting → Exercises**, instructors can choose any template stage,
read its instructions and solution, and apply its linked break to selected ready
teams. Other template breaks remain available separately. Selecting a stage does
not mutate the guests. Breaks, resets and meeting
checks remain explicit actions, so instructors can prepare labs ahead of time
and move around during a walkthrough without publishing or releasing a lesson.
Meeting checks run the template's full service-check set.

Example Branch's version 2 revision provides six baseline practice stages covering
the ledger dependency, banking page, FTP login root, publishing timer and published
branch hours. Three additional recovery stages link the stopped-web, database-port
and FTP-defacement breaks. Each stage includes a private reference solution.

Under **Admin → Published labs**, choose **New lab**, then a pinned revision and its reusable
practice stages. Publishing snapshots those definitions, including private
answers and solutions; later template imports cannot change an existing lesson.
Personal practice follows the selected stages in template order. Break-dependent
and instructor stages remain meeting-only: personal runs currently start from
the image baseline. Older revisions without stages retain the manual lesson
editor, and previously published instructor-paced lessons retain their release
controls. Saved progress is preserved. Where an author supplies a solution,
students can explicitly choose **View solution** for a current or completed
released stage. Viewing it does not complete that stage.

The practice catalog lists published lessons, progress and each student's
remaining cloud time.
Personal cloud labs default to **three hours per student per UTC week**, resetting
on Monday. Operators can set `weeklyCloudMinutes` in the protected hosted config.
Time counts from allocation reservation through confirmed cleanup, including
preparation and pending cleanup. Cloud runs require built images and a workstation
desktop. Only one personal cloud run can be active per student. It ends when its
reserved time expires; stopping deletes its workspace after cleanup confirmation.
Meeting cleanup and team membership are independent of these personal allocations.

The guided player shows the stage rail, instructions and answer/check controls,
with one workspace strip for starting, opening or stopping the cloud workspace;
**Run locally** opens the download and CLI commands. Active cloud action stages are checked periodically and advance
only after a confirmed pass. Previously published instructor-paced lessons still
require their instructor's release before completing later stages.
The cloud desktop can read and submit the current lesson on its own authenticated
origin, so lessons can accompany the actual remote display.

**Run on your own infrastructure** downloads pinned portable source and student
instructions. Use the standalone CLI with your own AWS or Proxmox environment;
a laptop hypervisor or Docker runner is not included. The download excludes
instructor files, accepted answers, provider configuration and credentials. Hosted
quota does not apply to your own resources, and you must clean those up yourself.
Cloud action verification is unavailable for local runs; answers can still be
submitted through the guided player.

Randomized scenario execution is not implemented. The intended small extension
is a set of author-defined variants that reuse a template's breaks, checks and
solutions. A future attempt must persist its selected variant and private answer
key before guest effects and keep progress per attempt. Choosing among validated
scenarios is preferable to generating arbitrary attacks or combining breaks whose
reset behavior is unknown.

The redesign and practice workflows were deployed on 2026-10-08, with mobile and
laptop proportion refinements on 2026-10-09. Live acceptance
covered real administrator/student OIDC sign-in, one personal AWS allocation,
rendered DCV with guest keyboard input, sidebar answers, automatic guest checks,
instructor releases, reconnect, saved progress and charged quota. Desktop and mobile
lesson layouts were inspected. Larger personal cohorts and full weekly quota
exhaustion/reset were not qualified live; local authorization, persistence, race
and failure tests cover those controls. The exact test allocation was confirmed
destroyed, temporary lessons were unpublished and the existing meeting was preserved.
Phone navigation reserves safe areas and keyboard focus clearance. Short laptop
screens have scrollable activity feeds and a narrower desktop instructions panel.
The template explorer, shared stages and Example Branch version 2 migration were
deployed on 2026-10-09. Live checks covered real administrator/student OIDC,
template exploration at desktop and phone widths, pinned publication snapshots,
solution access, all six baseline stages with native guest checks, rendered DCV,
guest keyboard input and retained progress/files on reconnect. The published
**Example Branch: baseline practice** remains available. The migration preserves
existing meeting revisions and publications; the three incident stages are for
new meetings using the migrated revision. The temporary test allocation was
confirmed destroyed, its acceptance lesson unpublished and test identities
removed. Randomized execution
remains unimplemented.

Operator setup uses the native CloudFormation templates in `tools/hosted/`.
`identity.yaml` requires the repository's exact `sub_claim_prefix` from
`gh api repos/OWNER/REPOSITORY/actions/oidc/customization/sub`; immutable GitHub
subjects contain IDs as well as names. `infrastructure.yaml` creates the host,
scoped runtime role, encrypted storage and budget alerts. Keep its output and all
configuration outside source. The initial administrator provisions this setup;
normal maintenance uses temporary GitHub OIDC and EC2 role credentials.

A protected service JSON config follows `hosted.Config`: state root, application
and desktop HTTPS origins, canonical OIDC issuer, client credentials, read-only
repository deploy key and trusted GitHub host keys, environment file, static web
root and verified DCV Web SDK root. Register the exact `/auth/callback` URL at the
IDP. Configure lab management ingress for the control host's public `/32` only.
When the public IDP hostname challenges machine traffic, set `backchannelOrigin`
to its trusted HTTPS origin (for CDC, `https://id.example.org`). Issuer and
audience verification remain enabled against the canonical advertised issuer.
The server never needs the OIDC provider administrative API key.

```sh
npm --prefix web ci
npm --prefix web run check
npm --prefix web run build
go run ./tools/webapi                 # regenerate the typed frontend contract
go test -race ./...
# Supply verified toolchain paths and an external staging directory:
tools/hosted/build.sh
# Test the resulting image, then package it without rebuilding:
tools/hosted/package.sh
```

Set repository variables `LABCENTRAL_MAINTENANCE_ROLE`, `LABCENTRAL_REGION`,
`LABCENTRAL_HOST_ID`, and `LABCENTRAL_ARTIFACT_BUCKET` from installation outputs. The **Hosted maintenance** workflow
supports status, deploying an uploaded SHA256-identified release, recovery, meeting
cleanup, and backup. A draft GitHub release can hold `SHA256.tar.gz`; supply its tag
as `release` to upload and deploy that verified archive without local AWS credentials.
Cleanup destroys the current meeting's labs. It preserves
images for reuse; image deletion remains an explicit library/CLI operation.

For standalone CLI resources, `recover` and `cleanup` also accept a verified
operator request named `SHA256.request.json` in the selected draft release:
`{"version":1,"state":"state/engine","kind":"instance","id":"INSTANCE_ID"}`.
State is relative to `/var/lib/labcentral` and must already exist. Kinds are
`instance`, `build`, or `image`; cleanup maps to destroy, abandon, or image removal.
Use the image cache key for build/image IDs. These requests cannot run arbitrary
commands and use the library's ownership and retained-image checks.
If the original operator is unavailable, fence it first. A `recover` request with
`kind: "restore-engine"`, a fresh `state/imports/<24-hex-ID>` state, and `files`
containing `{path, data}` entries restores base64-encoded `state.db` and hashed
`providers/aws/*.json` or `providers/proxmox/*.json` journals. It accepts no keys,
executables or existing-state overwrite. Subsequent cleanup uses the host role
and still verifies native ownership; preserve the original backup.
For a failed native build, a `recover` request with `kind: "build-log"`, `state`,
and its `owner` prints a bounded diagnostic tail and the build cache key, omitting
credential-related lines. Keep diagnostic workflow logs private.
For a hosted workstation, `kind: "desktop-log"` with `state` and instance `id`
reads bounded DCV startup logs through verified SSH. It accepts no custom command.
A `recover` request can also switch public origins with
`{"version":1,"kind":"origins","app":"https://labs.example.org","desktop":"https://desktop.example.org"}`.
It retains the previous configuration and redeploys the same verified artifact;
register the callback and prepare DNS before using it.

Nightly backups briefly stop the application and provider, archive state and
credentials together, then restart the service. This closes desktop transports
while retaining workspaces. Backups use encrypted private S3 and seven-day
retention. `tools/hosted/restore.py ARCHIVE SHA256 EMPTY_DIRECTORY` verifies and
restores an offline copy. Fence the original host before starting a restored
worker; two hosts must never mutate the same allocation namespace.
After a fresh backup with no active meeting or infrastructure jobs, a `recover`
request `{"version":1,"kind":"restore-check"}` downloads and verifies that backup,
checks the offline state, temporarily fences the original application, starts the
restored copy, verifies health and the OIDC redirect, then restarts the original.
The check briefly interrupts application access.

## Install from source

Linux amd64 and Go 1.27 are the initial operator baseline. Guests use Ubuntu 24.04.

```sh
make build
export PATH="$PWD/bin:$PATH"
labcentral init my-lab
labcentral validate my-lab
```

The CLI defaults to a provider executable beside itself. Keep both binaries.
Run `labcentral help` for flags. Flags precede positional arguments.

## Author a lab

```yaml
version: 2
name: web-investigation
images:
  workstation:
    base: ubuntu-24.04
    playbook: images/workstation.yml
machines:
  workstation:
    image: workstation
    cpu: 2
    memory: 4Gi
    disk: 20Gi
    access: [ssh, desktop]
```

One private IPv4 network is implicit. Machines resolve each other's declared names.
General Internet egress is denied at runtime. Build networking is separate.
`desktop` selects DCV on AWS and the VM's graphical console on Proxmox; these
have different session behavior. Images without a playbook use the bound base
unchanged for SSH labs. Desktop images require a playbook so the build installs
the platform access components.

Share the directory through Git or copy it. The library snapshots source and
records its identity automatically. Source cannot contain symlinks, hidden files,
provider configuration, expression templates, inheritance, or multiple networks.
Source paths use letters, numbers, underscores, dashes, dots, and slashes.
Ansible's own expressions execute inside the temporary build guest.

See [examples](examples/README.md) for a server, web/database pair, and workstation.
Inputs support `string`, `integer`, and `boolean`; input defaults and the CLI's
inputs JSON values are strings, then converted to their declared Ansible types.
Do not put secrets in inputs: source and build provenance are retained locally.

For a template in the hosted repository, place reusable teaching material in
`instructor.yaml`. For example, a baseline Ubuntu lab can offer an answer stage
and an independent meeting discussion:

```yaml
version: 2
stages:
  - id: identify-init
    title: Identify the service manager
    instructions: Run `ps -p 1 -o comm=` on the server.
    kind: answer
    prompt: Which service manager is running as process 1?
    answer: systemd
    solution: |
      Run `ps -p 1 -o comm=`. On this Ubuntu image, process 1 is `systemd`.
      Use `systemctl status SERVICE` to inspect a managed service.
  - id: discuss-recovery
    title: Review a recovery plan
    instructions: Explain how you would diagnose a stopped web service.
    kind: instructor
    prompt: Compare service status, listening ports and application logs.
    solution: Check the service status, inspect its logs and listener, then verify access from the workstation.
```

Action stages add `checkID` referring to a check in this same manifest. A stage
can add `exerciseID` referring to an existing break; the instructor explicitly
applies that break during a meeting. Check and break playbooks still belong under
`instructor/` and run only inside guests. The hosted importer removes this private
material before loading portable source. Keep accepted answers and solutions in
the private manifest, not in the student handover or portable playbooks. Solutions
are intentionally visible when a practice learner chooses **View solution**.

## Configure an environment

Keep environment files and credentials outside this repository:

```sh
mkdir -p "$HOME/.config/labcentral"
chmod 700 "$HOME/.config/labcentral"
labcentral environment init "$HOME/.config/labcentral/aws.yaml" aws
# Or: labcentral environment init "$HOME/.config/labcentral/proxmox.yaml" proxmox
```

Replace the placeholders before use. `bases.ubuntu-24.04` binds the logical name
to a pinned AMI (`id` and `identity` are the AMI ID) or Proxmox template (`id` is
VMID; `identity` is the API configuration digest). `diskGiB` describes the actual
base root disk. Operator ingress must be explicit IPv4 CIDRs, /24 or narrower.

Install Packer **1.14.3** from HashiCorp and verify its published checksum. Install
only the pinned plugins into a dedicated directory:

```sh
export PACKER_PLUGIN_PATH="$HOME/.local/share/labcentral/packer-plugins"
packer plugins install github.com/hashicorp/amazon 1.8.1
packer plugins install github.com/hashicorp/proxmox 1.2.3
packer plugins install github.com/hashicorp/ansible 1.1.4
sha256sum "$(command -v packer)"
labcentral environment fingerprint-plugins "$PACKER_PLUGIN_PATH"
```

Set absolute `build.packer`/`build.pluginDirectory` paths, `packerSHA256` and
`toolchainID` from those outputs, `ansibleVersion: 2.19.3`, and a build timeout
in seconds. Packer executes on the operator; Ansible and playbooks run in guests.

### AWS sandbox setup

Use a dedicated disposable account, one commercial region, and one availability
zone. Use short-lived credentials through an SSO or assume-role profile named
`labcentral-v2`; do not paste access keys into chat or source files.

```sh
aws configure sso --profile labcentral-v2
aws sso login --profile labcentral-v2
aws sts get-caller-identity --profile labcentral-v2
```

For browser-based console login with AWS CLI 2.32 or newer, use a separate login
profile and a credential-process profile shared by the SDK and Packer:

```sh
aws login --remote --profile labcentral-v2-login --region us-east-1
aws configure set region us-east-1 --profile labcentral-v2-login
aws configure set credential_process \
  'aws configure export-credentials --profile labcentral-v2-login --format process' \
  --profile labcentral-v2
aws configure set region us-east-1 --profile labcentral-v2
aws sts get-caller-identity --profile labcentral-v2
```

This is an alternative to the SSO setup above. Enter the returned authorization
code into the waiting CLI. The CLI refreshes temporary credentials for the login
session; repeat login when the session expires.
[AWS console login documentation](https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-sign-in.html).

For initial sandbox testing, the operator role needs EC2 lifecycle permissions
(the EC2-only `AmazonEC2FullAccess` policy is a practical starting point in this
isolated account). Add `iam:GetInstanceProfile`, `iam:ListAttachedRolePolicies`,
`iam:ListRolePolicies`, `iam:GetRolePolicy` and `iam:PassRole` for the dedicated
DCV role/profile; restrict PassRole to `ec2.amazonaws.com`. The guest role itself
must never have EC2 or IAM management permissions.

[tools/aws-sandbox.yaml](tools/aws-sandbox.yaml) creates the **operator's build
network** and a dedicated DCV licensing role/profile. Deploy it once with an
administrative setup identity and an explicit operator /32:

```sh
aws cloudformation deploy --profile YOUR_SETUP_PROFILE --region us-east-1 \
  --stack-name labcentral-v2-build \
  --template-file tools/aws-sandbox.yaml --capabilities CAPABILITY_IAM \
  --parameter-overrides OperatorCIDR=203.0.113.10/32 AvailabilityZone=us-east-1a
aws cloudformation describe-stacks --profile YOUR_SETUP_PROFILE --region us-east-1 \
  --stack-name labcentral-v2-build --query 'Stacks[0].Outputs'
```

Copy outputs into `build.subnetID`, `build.securityGroupID` and
`aws.desktopProfile`. Build networking permits repository access; lab networks
are separate and are created with restricted egress by the SDK adapter. Pin an
Ubuntu 24.04 amd64 AMI in the selected region. Set `aws.account`, `region`, `zone`,
`profile: labcentral-v2`, and an allowlist such as
`types: [{name: t3.medium, cpu: 2, memoryMiB: 4096}]`.

For desktop builds, set `build.dcvURL` to the official Ubuntu 24.04 DCV archive
and `build.dcvSHA256` to its verified SHA-256. The instance profile accepts exactly
one inline `s3:GetObject` allowance on `arn:aws:s3:::dcv-license.REGION/*`, with
no attached managed policies. Runtime desktop networks add an S3 gateway endpoint
restricted to that bucket. Public IPv4, EC2, images and snapshots incur charges.

For live acceptance, provide the **local environment-file path**, confirm the
sandbox account/region, and make the logged-in profile available to the process.
The build stack remains operator-owned; remove it separately after testing.

### Proxmox setup

Supply a node, storage, dedicated resource pool, exclusive VMID/VLAN ranges, and
an existing VLAN-aware isolated lab bridge. The configured IPv4 range provides a
separate /24 for each VLAN. A gateway, when supplied, uses the same host offset
in each /24. The operator must already reach guest SSH addresses and the management
API. The library does not change host bridges, routing, or cluster firewall settings.

The cluster firewall must be enabled. Verify VLAN separation, operator-only SSH,
denied general egress and management-network separation before setting
`verifiedIsolation: true`. This flag is an operator assertion, not a substitute
for live network probes. The adapter checks cluster firewall enablement and bridge
VLAN awareness before provisioning.

Use a fresh Ubuntu 24.04 amd64 cloud-init template with a `scsi0` root disk,
cloud-init drive, SSH, and a working QEMU guest agent. Allocate enough root disk
for builds in the base template; the clone builder preserves that disk. Set a
separate `build.bridge` with DHCP and approved package-repository connectivity.
LabCentral creates full clones and grows deployment disks; it never modifies the base.

Set `proxmox.tokenID` and `tokenEnv` (the name of a process environment variable
holding the secret). Use a trusted certificate or `caFile`. An explicit
`insecureSkipTLSVerify: true` is available for operator-authorized test endpoints;
it disables API server authentication and is never the default.

## Run a lifecycle

```sh
labcentral environment check --environment "$HOME/.config/labcentral/aws.yaml"
labcentral plan --environment "$HOME/.config/labcentral/aws.yaml" examples/single
labcentral deploy --environment "$HOME/.config/labcentral/aws.yaml" examples/single
labcentral list
labcentral inspect INSTANCE_ID
labcentral access INSTANCE_ID server ssh
labcentral destroy INSTANCE_ID
labcentral image list
labcentral image remove IMAGE_KEY
```

Provisioning and destructive commands ask for confirmation; `--yes` authorizes
noninteractive execution. Deploy builds missing images and previews that work.
Use `--json` for versioned results and progress records. The library never prompts,
prints, or opens a browser. Desktop access prints a one-use loopback URL and guest
credentials; keep the command running and press Ctrl-C to revoke the session.

For the graphical workstation:

```sh
labcentral deploy --environment "$HOME/.config/labcentral/aws.yaml" examples/workstation
labcentral access INSTANCE_ID workstation desktop
```

Open the printed URL once and sign in with the printed guest credentials. The
desktop runs as that temporary guest account. The relay listens only on the
operator's loopback interface. If your browser is on another computer, forward
the printed port over SSH to the operator and use the same `127.0.0.1` URL and
port; do not expose the relay on a public interface. Closing access removes the
temporary account and session. Files in that account's home are temporary.

State defaults to `~/.local/state/labcentral/v2`. Use `--state` for a separate root.
Each target allocation range belongs to one state root; multiple independent
operators are unsupported. Preserve both SQLite state and its adjacent provider
journals when backing up. Credentials are separate mode-0600 files under the
private state root. Source snapshots, plans, image provenance and resource
identities are retained for recovery.

```sh
labcentral recover INSTANCE_ID
labcentral image builds
labcentral recover build:IMAGE_KEY
labcentral image abandon IMAGE_KEY
```

Recovery reconciles the original attempt. If a Packer build produced no image,
recovery cleans its temporary resources, and explicit `image abandon` removes
that unpublished attempt before retrying. Unknown outcomes retain allocations.
Destroy preserves image artifacts; image removal refuses references from retained
instances. There is no background cleanup: resources can remain after the operator
stops. Inspect recovery errors and clean only resources whose ownership is verified.

## Library and development

[examples/consumer](examples/consumer/main.go) is a separate Go module that runs
the lifecycle through the public API. Its `replace` directive uses this checkout;
remove that directive when consuming a published module version.

```sh
make test
make race
make build
(cd examples/consumer && go mod tidy && go build .)
go doc github.com/OutboundSpade/LabCentral/v2
# After verification, package these exact binaries and the pinned native toolchain:
PACKER_BINARY="$(command -v packer)" PLUGIN_LICENSE_DIRECTORY=/path/to/plugin-licenses make package
```

Packaging also requires the original Packer `LICENSE.txt` beside its executable
(or `PACKER_LICENSE`) and the pinned plugins’ license files in
`PLUGIN_LICENSE_DIRECTORY`. The archive includes the exact binaries, native
toolchain, versions, licenses, and checksums.

The portable contract is [model.go](model.go), with strict validation in
[source.go](source.go) and an editor schema in [schemas/lab-v2.json](schemas/lab-v2.json).
The generated [Protobuf contract](api/plugin/v2/platform.proto) defines the private
process boundary. See [ARCHITECTURE.md](ARCHITECTURE.md) for boundaries and current
limits. Contribution rules live in [AGENTS.md](AGENTS.md).

Optional integration checks use separately installed pinned tools:

```sh
LABCENTRAL_TEST_PACKER="$(command -v packer)" go test ./build
LABCENTRAL_PLAYWRIGHT_MODULE=/absolute/path/to/playwright/index.mjs \
  go test -race ./access -run TestBundledBrowser -v
```

The browser check uses Playwright 1.58.2 with its installed Chromium, or an explicit
`LABCENTRAL_CHROMIUM` executable. It tests the bundled client against a test VNC
server; it does not substitute for live Proxmox/DCV acceptance.

CI runs core checks for pull requests and pushes to `main`, skips Markdown-only
changes, and cancels superseded checks. Browser integration checks are optional:
run the **v2** workflow manually with **browsers** enabled. Local UI reviews still
check desktop/mobile rendering; set `LABCENTRAL_FIREFOX` to an installed Playwright
Firefox executable to run `web/tests/browser.mjs` against Firefox as well.
