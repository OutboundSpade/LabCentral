# LabCentral v2

Portable virtual labs, built from small YAML files and ordinary Ansible playbooks.
LabCentral is an embeddable Go library with a thin CLI. AWS EC2 and Proxmox VMs
are the initial platforms. Images stay native to their platform.

**Development implementation; not a qualified release.** Local tests do not establish
live AWS/Proxmox portability or browser usability. The current acceptance gaps are
listed in [ARCHITECTURE.md](ARCHITECTURE.md#acceptance).

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
