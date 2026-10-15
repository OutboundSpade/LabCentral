# LabCentral

LabCentral builds and runs hands-on virtual labs from small YAML files and ordinary
Ansible playbooks. A lab describes its images, machines and networks once; the same
source deploys on AWS EC2 or Proxmox VE, with images kept native to each platform.

The project has three parts:

- **A Go library** that validates lab source, plans deployments, builds images with
  pinned Packer and Ansible, deploys and destroys labs, and recovers from interrupted
  operations without guessing.
- **A CLI** (`labcentral`) for operators who run labs on their own AWS account or
  Proxmox cluster.
- **A hosted web service** (`labcentral-server`) for teams and classes: a template
  library with guided practice, randomized troubleshooting scenarios, team meetings
  with in-browser desktops, scoreboards and an instructor terminal.

## Contents

- [Features](#features)
- [How it fits together](#how-it-fits-together)
- [Platform support](#platform-support)
- [Quick start](#quick-start)
- [Writing a lab](#writing-a-lab)
- [Configuring an environment](#configuring-an-environment)
- [Running labs with the CLI](#running-labs-with-the-cli)
- [The hosted service](#the-hosted-service)
- [Development](#development)
- [Further reading](#further-reading)

## Features

- **Portable lab source.** One `lab.yaml` per lab, strictly validated, with a
  generated JSON Schema for editors. No template language, inheritance or hidden
  provider configuration.
- **Native images.** Packer and guest-local Ansible produce AMIs or Proxmox templates.
  Images are cached by a content identity, validated on a disposable machine before
  use, and only rebuilt when their inputs change.
- **Private by default.** Lab networks deny Internet access unless an author requests
  it and the operator allows it. On AWS, lab machines have no public addresses; a
  small per-lab gateway is the only way in.
- **Desktops in the browser.** Amazon DCV on AWS and the VM console (noVNC) on
  Proxmox, through an authenticated loopback relay with one-use launch links.
- **Recoverable operations.** Every native mutation is journaled before it happens.
  An interrupted or timed-out operation is treated as an unknown outcome and
  reconciled, never assumed to have failed. Resources are only deleted after their
  ownership is verified.
- **Platform options and extensions.** Typed per-platform settings (instance type,
  volume performance, CPU model, NIC model and more) and namespaced `extensions`
  for your own metadata, passed through to Ansible untouched.
- **Teaching tools (hosted).** Stages with answer, check or instructor completion,
  author solutions, randomized scenarios built from compatible "breaks", guest checks
  that verify real repairs, team meetings, scoreboards and weekly cloud quotas.

## How it fits together

```
lab source (lab.yaml, playbooks)        operator environment (environment.yaml)
                  \                          /
                   labcentral library / CLI  ---- SQLite intent journal, private keys
                              |
                 typed gRPC over a private Unix socket
                              |
                  labcentral-provider process ---- provider recovery journals
                     |                  |
          AWS / Proxmox APIs      pinned Packer -> guest-local Ansible
                     |
              lab machines  <---- verified SSH, desktop relays

labcentral-server (optional hosted service) uses the same library, adding OIDC sign-in,
a job scheduler, a template repository importer and a SvelteKit web UI.
```

Author code (Ansible playbooks, checks, breaks) only ever runs inside guest machines.
Provider credentials stay in the operator's environment and the provider process.

## Platform support

| Capability | AWS EC2 | Proxmox VE |
| --- | --- | --- |
| Image builds (Packer + Ansible) | Yes | Yes |
| SSH access | Yes, through the lab gateway | Yes, through operator routing |
| Desktop access | Amazon DCV | VM console (noVNC) |
| Per-network Internet egress (opt-in) | Yes, NAT on the lab gateway | Yes, via the operator's VLAN gateway |
| Platform options | Instance type, gp3 volume, CPU credits, build instance type | CPU model, NIC model, memory ballooning |
| Hosted service | Yes | Library and CLI only |
| Multiple networks, NICs and routers | Up to 8 networks and 8 NICs per machine; author CIDRs | Up to 4 networks and 4 NICs per machine |
| Windows guests | Planned | Planned |

Guests are Ubuntu 24.04 amd64. The operator machine is Linux amd64. Multiple networks
and routers have been verified on AWS; Proxmox support is implemented but not yet
verified on a real cluster. See
[ARCHITECTURE.md](ARCHITECTURE.md#qualification-status) for what has been verified on
real platforms and what is only covered by local tests.

## Quick start

You need Go 1.27 (see [tools/toolchain.json](tools/toolchain.json) for every pinned
tool version) and an AWS account or Proxmox cluster you control.

```sh
git clone https://github.com/OutboundSpade/LabCentral.git
cd LabCentral
make build
export PATH="$PWD/bin:$PATH"

labcentral init my-lab          # writes a minimal lab
labcentral validate my-lab
```

Then [configure an environment](#configuring-an-environment) and deploy:

```sh
labcentral plan   my-lab --environment ~/.config/labcentral/aws.yaml
labcentral deploy my-lab --environment ~/.config/labcentral/aws.yaml
labcentral access INSTANCE_ID server ssh
labcentral destroy INSTANCE_ID
```

The CLI looks for the `labcentral-provider` executable next to itself, so keep both
binaries together. `labcentral help COMMAND` shows usage and examples for each command.

## Writing a lab

A lab is a directory containing `lab.yaml` and the files it references.

```yaml
version: 3
name: web-investigation
description: Investigate a web server from an analyst workstation.
images:
  workstation:
    base: ubuntu-24.04
    build:
      playbook: images/workstation.yml
  server:
    base: ubuntu-24.04
    build:
      playbook: images/server.yml
      files: [images/server.yml, images/site.conf]
machines:
  workstation:
    image: workstation
    cpu: 2
    memory: 4Gi
    disk: 20Gi
    access: [ssh, desktop]
  server:
    image: server
    cpu: 1
    memory: 2Gi
    disk: 20Gi
    access: [ssh]
```

More complete examples are in [examples/](examples/README.md) and
[templates/](templates/README.md). The full contract is the generated schema
[schemas/lab-v3.json](schemas/lab-v3.json).

### Images

- `base` names a base image that the operator's environment binds to a pinned AMI or
  Proxmox template.
- `build.playbook` runs inside a temporary build machine. Omit `build` to use the base
  unchanged (SSH-only machines). Desktop machines need a build so the platform's
  desktop components can be installed.
- `build.files` limits which source files are copied into the build. It also limits
  which files affect the image's cache identity, so editing unrelated files (such as a
  README) does not trigger a rebuild. When omitted, the whole source is used.
- Runtime settings (networks, NICs, platform machine options) and operator settings that
  don't affect builds (egress permission, gateway, allowlists, operator ranges) never
  cause a rebuild.
- Ansible expressions run inside the build guest; LabCentral passes inputs and
  extensions as Ansible variables.

### Machines

- `cpu`, `memory` and `disk` are minimums. On AWS the planner picks the smallest
  allowed instance type that fits.
- `access` is any of `ssh` and `desktop`.
- `hostnamePeers` controls which machines appear in each guest's `/etc/hosts`:
  `all` (the default), a list of names, or `[]` for none.

### Networks and Internet access

Without a `networks` section, a lab has one private IPv4 network named `lab` and every
machine has one NIC on it. Machines can pin a host address with
`nics: [{network: lab, address: 20}]` (offsets 4–250); the others are numbered from
11 in name order.

Networks are isolated from each other. To connect two networks, add a machine with a
NIC on each and `forwarding: true`:

```yaml
version: 3
name: routed-investigation
networks:
  clients: {egress: internet}
  servers: {}
images:
  ubuntu: {base: ubuntu-24.04}
machines:
  client:
    image: ubuntu
    cpu: 2
    memory: 2Gi
    disk: 20Gi
    nics: [{network: clients}]
    access: [ssh]
  router:
    image: ubuntu
    cpu: 2
    memory: 2Gi
    disk: 20Gi
    forwarding: true
    nics: [{network: clients, address: 10}, {network: servers, address: 10}]
    access: [ssh]
  server:
    image: ubuntu
    cpu: 2
    memory: 2Gi
    disk: 20Gi
    nics: [{network: servers}]
    access: [ssh]
```

- A machine's first NIC is its management address for SSH, desktops and checks.
- A peer's hostname resolves to its address on the first network the two machines
  share, or to its first-NIC address if they share none.
- The planner computes static routes through routers, including chains of routers.
  Two routers joining the same pair of networks, or a cycle, is rejected.
- A machine's single default route uses its first NIC on an Internet-enabled network.
- On AWS, a network can set its own `cidr` (private and non-overlapping). All networks
  must fit in one /16 with room for the gateway's /28. Proxmox assigns network ranges
  itself and rejects author CIDRs.

Internet access is off by default. A network can request it:

```yaml
networks:
  lab:
    egress: internet
```

The operator must also allow egress in the environment (`aws.allowEgress` or
`proxmox.allowEgress`). Egress is general Internet access, with private, link-local
and carrier-grade NAT ranges blocked; it is not a URL allowlist. Image builds have
their own network and are not affected by this setting.

### Inputs

Inputs are typed values passed to playbooks as Ansible variables:

```yaml
inputs:
  welcome_message: {type: string, default: Welcome!, description: Shown on the home page}
  worker_count: {type: integer, default: 2}
  debug: {type: boolean, required: true}
```

Types are `string`, `integer` and `boolean`. Inputs are recorded in plans and build
provenance, so never use them for secrets.

### Platform options

Authors can request platform-specific settings. They refine how a platform realizes a
machine but never change its topology, reachability or access. A platform ignores the
other platform's block, and the operator's environment decides what is allowed.

```yaml
images:
  server:
    base: ubuntu-24.04
    build: {playbook: images/server.yml}
    platform:
      aws: {buildInstanceType: t3.large}
machines:
  server:
    image: server
    cpu: 2
    memory: 4Gi
    disk: 32Gi
    platform:
      aws:
        instanceType: t3.large
        volume: {type: gp3, iops: 4000, throughput: 500}
        cpuCredits: standard
      proxmox: {cpuType: host, nicModel: virtio, memoryBalloon: false}
```

| Option | Allowed values |
| --- | --- |
| `machines.*.platform.aws.instanceType` | A type from `environment.aws.types` that satisfies the CPU and memory request |
| `machines.*.platform.aws.volume` | `type: gp3`; `iops` 3000–16000 (at most 500 × disk GiB); `throughput` 125–1000 MiB/s (at most IOPS ÷ 4) |
| `machines.*.platform.aws.cpuCredits` | `standard` or `unlimited`, burstable types only |
| `images.*.platform.aws.buildInstanceType` | A type from `environment.aws.types`, used for the build and its validation machine |
| `machines.*.platform.proxmox.cpuType` | A CPU model listed in `environment.proxmox.cpuTypes` (allowing `host` enables nested virtualization) |
| `machines.*.platform.proxmox.nicModel` | `virtio`, `e1000`, `e1000e`, `vmxnet3` or `rtl8139` |
| `machines.*.platform.proxmox.memoryBalloon` | `true` or `false` |

`platforms: [aws]` at the top level restricts a lab to the listed platforms.

### Extensions

`extensions` holds your own data on the lab, networks, images and machines. Keys must
be namespaced (`org.example.scoring`) and values can be any JSON, up to 64 KiB per lab.
LabCentral stores them unchanged and passes lab and image extensions to image builds
as `labcentral_extensions`. It never interprets them, and they are not a place for
secrets.

```yaml
extensions:
  org.example.course: {unit: 3, track: blue-team}
```

### Validation

`labcentral validate DIR` checks the source strictly: unknown or duplicate keys, YAML
anchors, multiple documents, unsafe paths, symlinks, hidden files and missing files
are all errors, reported with a field path such as `machines.server.nics[0].address`.
Features a platform does not support yet are rejected during `plan`, before anything
is created.

## Configuring an environment

An environment file describes where labs run. Keep environment files and credentials
outside your lab repositories.

```sh
mkdir -p ~/.config/labcentral && chmod 700 ~/.config/labcentral
labcentral environment init ~/.config/labcentral/aws.yaml aws
# or: labcentral environment init ~/.config/labcentral/proxmox.yaml proxmox
labcentral environment check --environment ~/.config/labcentral/aws.yaml
```

### Common settings

- `bases` binds base image names to pinned images: for AWS, `id` and `identity` are
  the AMI ID; for Proxmox, `id` is the template VMID and `identity` is its configuration
  digest. `user` is the image's default login user and `diskGiB` its root disk size.
- `operatorCIDRs` lists the IPv4 ranges (/24 or narrower) that may reach labs.
- `build` pins the toolchain. Install Packer 1.14.3, verify its published checksum,
  and install exactly the pinned plugins into a dedicated directory:

```sh
export PACKER_PLUGIN_PATH="$HOME/.local/share/labcentral/packer-plugins"
packer plugins install github.com/hashicorp/amazon 1.8.1
packer plugins install github.com/hashicorp/proxmox 1.2.3
packer plugins install github.com/hashicorp/ansible 1.1.4
sha256sum "$(command -v packer)"
labcentral environment fingerprint-plugins "$PACKER_PLUGIN_PATH"
```

Set `build.packer` and `build.pluginDirectory` to absolute paths, `build.packerSHA256`
and `build.toolchainID` from the commands above, `build.ansibleVersion: 2.19.3`, and a
`build.timeoutSeconds` limit.

### AWS

Use a dedicated account with short-lived credentials (AWS IAM Identity Center, an
assumed role, or IAM Roles Anywhere). Never put access keys in lab source or chat.

1. Create the operator's build network and DCV licensing role once with
   [tools/aws-sandbox.yaml](tools/aws-sandbox.yaml):

   ```sh
   aws cloudformation deploy --stack-name labcentral-build \
     --template-file tools/aws-sandbox.yaml --capabilities CAPABILITY_IAM \
     --parameter-overrides OperatorCIDR=203.0.113.10/32 AvailabilityZone=us-east-1a
   aws cloudformation describe-stacks --stack-name labcentral-build \
     --query 'Stacks[0].Outputs'
   ```

2. Copy the outputs into `build.subnetID`, `build.securityGroupID` and
   `aws.desktopProfile`.
3. Set `aws.account`, `aws.region`, `aws.zone`, `aws.profile` and an instance type
   allowlist, for example `types: [{name: t3.medium, cpu: 2, memoryMiB: 4096}]`. Add
   `maxNICs` to a type to check multi-NIC machines offline; otherwise the provider asks
   AWS for the type's network interface limit.
4. For desktops, set `build.dcvURL` and `build.dcvSHA256` to the official Amazon DCV
   server archive for Ubuntu 24.04 and its published checksum.

Each lab gets its own VPC (`10.77.0.0/16` by default). Networks get `10.77.0.0/24`,
`10.77.1.0/24` and so on, in name order, each with its own subnet, security group and
route table. Machines have no public addresses. A `t3.micro` gateway in its own /28
holds the lab's only public address; all SSH, desktop and check traffic is tunneled
through it, with host keys pinned on both hops. Configure it with `aws.gateway: {base:
ubuntu-24.04, instanceType: t3.micro}` (these are the defaults). Set `aws.allowEgress:
true` to let labs request Internet access.

The operator identity needs EC2 lifecycle permissions and `iam:PassRole` for the DCV
licensing role (restricted to `ec2.amazonaws.com`). Lab machines never receive EC2 or
IAM permissions; desktop machines get a license-only instance profile. Public IPv4
addresses, instances, images and snapshots are billed while they exist.

### Proxmox

You need a node, storage, a dedicated resource pool, exclusive VMID and VLAN ranges,
and a VLAN-aware bridge for isolated lab networks. Each VLAN gets its own /24 from the
configured IPv4 range, and the operator's routing must already reach those addresses.

- Use an Ubuntu 24.04 amd64 cloud-init template with a `scsi0` root disk, a cloud-init
  drive and a working QEMU guest agent. LabCentral makes full clones and never modifies
  the template.
- Set `build.bridge` to a network with DHCP and package-repository access.
- Set `proxmox.tokenID` and `proxmox.tokenEnv` (the name of the environment variable
  holding the API token secret). Use a trusted certificate or `caFile`;
  `insecureSkipTLSVerify` exists for test endpoints only.
- The cluster firewall must be enabled. Verify VLAN separation, operator-only SSH and
  management isolation yourself, then set `verifiedIsolation: true`. This is your
  assertion; LabCentral checks firewall and bridge settings but cannot probe your
  physical network.
- Each lab network gets its own VLAN and /24. Your router must drop traffic between lab
  VLANs, so that labs can only connect networks through their own router machines.
- `proxmox.cpuTypes` lists CPU models authors may request. `proxmox.allowEgress: true`
  plus a `proxmox.gateway` that provides NAT lets labs request Internet access.

## Running labs with the CLI

```sh
labcentral plan    DIR --environment ENV.yaml   # preview images, machines and costs
labcentral deploy  DIR --environment ENV.yaml   # builds missing images, then deploys
labcentral list
labcentral inspect INSTANCE_ID
labcentral access  INSTANCE_ID MACHINE ssh
labcentral access  INSTANCE_ID MACHINE desktop
labcentral destroy INSTANCE_ID
labcentral image list
labcentral image remove IMAGE_KEY
```

- Commands that create or delete resources ask for confirmation; `--yes` approves them
  for automation. `--json` prints versioned results and progress events.
- `access … desktop` prints a one-use URL on `127.0.0.1` and temporary guest
  credentials. Keep the command running while you use the desktop; Ctrl-C closes the
  session and removes the temporary account. To use a browser on another computer,
  forward the port over SSH rather than exposing it.
- `labcentral config init` creates `~/.config/labcentral/config.json` (mode 0600) for
  default paths and concurrency limits. Flags override it, and it can never enable
  `--yes`.

### State and recovery

State lives in `~/.local/state/labcentral/v2` (or `--state DIR`): a SQLite journal,
provider journals and per-lab SSH keys. Back them up together. One state root owns each
allocation range; do not point two operators at the same target ranges.

```sh
labcentral recover INSTANCE_ID        # reconcile an interrupted deploy or destroy
labcentral image builds               # list unfinished image builds
labcentral recover build:IMAGE_KEY
labcentral image abandon IMAGE_KEY    # remove an unpublished build attempt
```

There is no background cleanup. Resources keep running (and costing money) until you
destroy them. `destroy` keeps images; `image remove` refuses images still used by a
retained lab.

## The hosted service

`labcentral-server` turns the library into a web application for a team or class.

- **Template library.** Imports lab templates from a Git repository, pins each commit
  and builds its images once. Students browse templates by category, difficulty, tags
  and duration.
- **Image cleanup.** Admin → Templates shows image and unused-image counts.
  “Remove unused images” queues recoverable cleanup through the admin/CSRF-protected
  `POST /api/admin/images/prune` API; progress and retry appear in Live activity.
- **Guided practice.** Each attempt snapshots a template revision's stages. Stages are
  completed by an answer, by a guest check that verifies the student's actual repair,
  or by an instructor. Authors can attach solutions that students reveal deliberately.
- **Randomized scenarios.** Authors certify compatible combinations of "breaks"; each
  attempt applies a random combination and checks the matching recovery stages.
- **Personal cloud labs** with a weekly time allowance per student (three hours by
  default), or a download to run the lab on your own infrastructure with the CLI.
- **Team meetings.** Administrators prepare one lab per team, students join a team and
  open individual desktops, and instructors apply breaks, run scheduled service checks
  with a scoreboard, and open an SSH terminal to any team machine in the browser.

Sign-in uses OpenID Connect. Users in the configured member group can practice and
join teams; users in the admin group can manage templates and meetings.

### Template repository

The server imports the `templates/` directory from the `main` branch of a Git
repository you configure. Each template is a directory containing:

| Path | Purpose |
| --- | --- |
| `lab.yaml`, playbooks | Portable lab source, as above |
| `README.md` | Student handover shown in the library |
| `instructor.yaml` | Private teaching material: stages, checks, breaks, scenarios and catalog metadata |
| `instructor/` | Private guest playbooks for checks, breaks and resets |

Private material is removed before images are built and never reaches student responses
or downloads. `labcentral validate --template DIR` checks a template exactly as the
importer will; the contract is [schemas/instructor-v3.json](schemas/instructor-v3.json)
and [templates/README.md](templates/README.md) walks through a complete example.

A minimal `instructor.yaml`:

```yaml
version: 3
visibility: {audience: students}
practice: {desktopMachine: workstation}
library:
  category: Linux
  difficulty: beginner
  estimatedMinutes: 30
  tags: [services]
checks:
  - id: web
    name: Website available
    points: 50
    steps:
      - {machine: server, playbook: check-web.yml}
stages:
  - id: find-init
    title: Identify the service manager
    instructions: Run `ps -p 1 -o comm=` on the server.
    prompt: Which service manager runs as process 1?
    completion:
      answer: {accepted: [systemd], caseSensitive: false}
    solution: Process 1 is `systemd`.
  - id: verify-web
    title: Verify the website
    instructions: Make sure the website on the server is reachable.
    completion: {check: web}
```

Each import validates every template on its own. A broken template is reported to
administrators with the failing field and does not block the others.

### Running the server

The server needs, on one Linux host:

- the `labcentral-server`, `labcentral-provider` and Packer toolchain binaries;
- the built web UI (`npm --prefix web ci && npm --prefix web run build`, then point
  `webRoot` at `web/build`);
- the Amazon DCV Web Client SDK (`dcvjs-esm` directory) for browser desktops;
- an AWS environment file, as for the CLI;
- an OIDC client registered with redirect URL `https://APP_ORIGIN/auth/callback`;
- a read-only SSH deploy key for the template repository (a GitHub SSH URL such as
  `git@github.com:example/lab-templates.git`) and a `known_hosts` file;
- a TLS-terminating reverse proxy serving two origins (the application and a separate
  desktop origin) to the server's `--listen` address.

Create a configuration file readable only by the service:

```json
{
  "root": "/var/lib/labcentral/state",
  "origin": "https://labs.example.org",
  "desktopOrigin": "https://desktop.labs.example.org",
  "issuer": "https://id.example.org",
  "clientID": "labcentral",
  "clientSecret": "…",
  "memberGroup": "labcentral-users",
  "adminGroup": "labcentral-admins",
  "repository": "git@github.com:example/lab-templates.git",
  "deployKey": "/var/lib/labcentral/config/deploy_key",
  "knownHosts": "/var/lib/labcentral/config/known_hosts",
  "environment": "/var/lib/labcentral/config/aws.yaml",
  "webRoot": "/opt/labcentral/web",
  "sdkRoot": "/opt/labcentral/dcv"
}
```

`backchannelOrigin` can point token and discovery requests at a different HTTPS origin
when the public identity-provider hostname is not reachable from the server; issuer and
audience are still verified against `issuer`.

```sh
labcentral-server --config /path/to/service.json --check-config   # validate, print limits
labcentral-server --config /path/to/service.json --listen 127.0.0.1:8080
```

Back up the state root, including its WAL files and provider journals, while the
server is stopped. Run one server per state root.

### Limits

All limits are optional; omitted or zero values use the defaults.

| Setting | Default | Meaning |
| --- | ---: | --- |
| `weeklyCloudMinutes` | 180 | Each student's weekly personal cloud allowance (resets Monday, UTC) |
| `worker.maxProvisioningConcurrency` | 3 | Simultaneous team and personal lab preparations |
| `worker.maxBulkJobs` | 3 | Builds, preparations, template refreshes and image removals |
| `worker.maxGuestJobs` | 12 | Guest checks and breaks, separate from bulk work |
| `worker.maxImageBuildJobs` | 2 | Image builds within the bulk budget |
| `worker.jobTimeoutMinutes` | 120 | Maximum job duration (1–1440) |
| `engine.maxImageBuildConcurrency` | 3 | Parallel images within one build |
| `engine.maxMachineConcurrency` | 4 | Parallel machine creates within one deployment |
| `engine.maxCleanupConcurrency` | 4 | Parallel machine deletions within one cleanup |
| `aggregate.maxNativeOperations` | 8 | Machine creates and deletes across all jobs |
| `aggregate.maxSSHSessions` | 24 | Guest SSH steps, coach terminals and desktop operations across all jobs |

Worker and engine values accept 1–32, aggregate budgets 1–64. Raising them does not
raise your cloud provider's quotas.

## Development

```sh
make build                 # bin/labcentral and bin/labcentral-provider
make check                 # go vet and tests, including the consumer example
go test -race ./...
npm --prefix web ci && npm --prefix web run check && npm --prefix web run build

# Regenerate generated files after changing their sources:
go run ./tools/schema      # schemas/*.json from the Go types
go run ./tools/protocol    # provider protocol (needs the pinned protoc)
go run ./tools/webapi      # web/src/lib/types.ts from the hosted API types
```

[examples/consumer](examples/consumer/main.go) is a separate Go module that drives the
public API; its `replace` directive points at this checkout.

Optional checks use separately installed tools:

```sh
LABCENTRAL_TEST_PACKER="$(command -v packer)" go test ./build
LABCENTRAL_PLAYWRIGHT_MODULE=/path/to/playwright/index.mjs go test -race ./access ./hosted
LABCENTRAL_LOAD_TEST=1 go test ./hosted -run '^TestConcurrencyLoad$' -v   # local load model
```

These checks do not replace testing against real AWS or Proxmox accounts. Contribution
rules for humans and coding agents are in [AGENTS.md](AGENTS.md).

## Further reading

- [ARCHITECTURE.md](ARCHITECTURE.md): design, boundaries and qualification status
- [examples/README.md](examples/README.md): small labs to start from
- [templates/README.md](templates/README.md): writing hosted templates
- [schemas/](schemas/): generated JSON Schemas for `lab.yaml` and `instructor.yaml`

## License

LabCentral is licensed under the [Apache License 2.0](LICENSE).
