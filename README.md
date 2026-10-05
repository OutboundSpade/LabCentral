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
waits only for overlapping template, image, meeting or team operations. **Live activity**
shows running/queued work, waiting dependencies, progress counts and active desktops.
Desktop launches bypass this queue and can run while checks or exercises use
another machine in the same ready team lab.

Pinned templates can include private `instructor.yaml` metadata and guest Ansible
playbooks under `instructor/`. They are extracted before image builds and are visible
only to administrators. Notes, break definitions and reset instructions stay with
the revision. **Service scoreboard** runs the revision's checks once or on a saved
schedule (one minute to one hour); it shows pass, fail and unavailable results per
team. Ready teams can be checked while other teams are still preparing. Schedules start paused and stop when the meeting ends. See
[ARCHITECTURE.md](ARCHITECTURE.md#hosted-team-service) for the manifest contract.

The five prerendered routes are `/`, `/teams/`, `/lab/`, `/admin/`, and
`/admin/templates/`. The SvelteKit UI uses daisyUI and Lucide; a Go service supplies
identity, APIs, jobs and desktop authorization. Live acceptance covers real OIDC
sign-in, retained workspaces, team switching, revocation, network isolation,
Example Branch exercises, cleanup and backup restoration.

Administrators can select ready teams under **Manage meeting → Exercises** to
stop Apache, move it to port 8080, or simulate an SSH intrusion. Every break first
restores and verifies the exercise baseline. **Reset web server** restores the
Apache listener/site, homepage, and synthetic support account; it removes the
exercise key and artifact while preserving bank data, publications, desktops,
and historical logs. **Check recovery** verifies the website from the workstation
and checks for remaining intrusion artifacts. Controls, results and instructor
walkthroughs require administrator authorization. This is a targeted exercise
reset, not a full VM restore; unrelated package or SSH configuration damage can
require operator recovery. Exercises use the meeting's pinned source and do not
require new images.

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
