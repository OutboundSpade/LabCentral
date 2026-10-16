# Running labs with the CLI

This guide is for operators who run labs on their own AWS account or Proxmox cluster
with the `labcentral` command-line tool. To write labs, see
[Writing lab templates](templates.md); to run the web service for a class, see
[Running the hosted service](hosted.md).

- [Install](#install)
- [Configure an environment](#configure-an-environment)
- [AWS](#aws)
- [Proxmox](#proxmox)
- [Run labs](#run-labs)
- [State and recovery](#state-and-recovery)

```mermaid
flowchart LR
    Lab[Lab directory] --> Plan[labcentral plan]
    Env[Environment file] --> Plan
    Plan --> Deploy[labcentral deploy<br/>builds missing images,<br/>then creates machines]
    Deploy --> Access[labcentral access<br/>SSH or desktop]
    Access --> Destroy[labcentral destroy]
    Destroy -.->|images are kept| Images[labcentral image remove]
```

## Install

You need Go 1.27 (every pinned tool version is in
[tools/toolchain.json](../tools/toolchain.json)) and an AWS account or Proxmox cluster
you control. The operator machine is Linux amd64.

```sh
git clone https://github.com/OutboundSpade/LabCentral.git
cd LabCentral
make build
export PATH="$PWD/bin:$PATH"
```

`make build` produces `labcentral` and `labcentral-provider`. The CLI looks for the
provider next to itself, so keep both binaries together. `labcentral help COMMAND`
shows usage and examples for every command.

## Configure an environment

An environment file describes where labs run: the target, base images, network
policy and the pinned build toolchain. Keep environment files and credentials outside
your lab repositories.

```sh
mkdir -p ~/.config/labcentral && chmod 700 ~/.config/labcentral
labcentral environment init ~/.config/labcentral/aws.yaml aws
# For Proxmox, use the guided setup below.
labcentral environment check --environment ~/.config/labcentral/aws.yaml
```

### Base images

`bases` binds the base names that labs use (such as `ubuntu-24.04`) to pinned images:

| Field | AWS | Proxmox |
| --- | --- | --- |
| `id`, `identity` | The AMI ID (both fields) | Template VMID, and its configuration digest |
| `user` | The image's default login user | The image's default login user |
| `diskGiB` | Root disk size | Root disk size |
| `os` | `linux` (default) or `windows` | `linux` |

Guests are Ubuntu 24.04 amd64, plus Windows Server 2022 on AWS.

### Build toolchain

`build` pins the toolchain. Install Packer 1.14.3, verify its published checksum, and
install exactly the pinned plugins into a dedicated directory:

```sh
export PACKER_PLUGIN_PATH="$HOME/.local/share/labcentral/packer-plugins"
packer plugins install github.com/hashicorp/amazon 1.8.1
packer plugins install github.com/hashicorp/proxmox 1.2.3
packer plugins install github.com/hashicorp/ansible 1.1.4
sha256sum "$(command -v packer)"
labcentral environment fingerprint-plugins "$PACKER_PLUGIN_PATH"
```

Then set:

- `build.packer` and `build.pluginDirectory` to absolute paths;
- `build.packerSHA256` and `build.toolchainID` from the commands above;
- `build.ansibleVersion: 2.19.3` and a `build.timeoutSeconds` limit.

`operatorCIDRs` lists the IPv4 ranges (/24 or narrower) allowed to reach lab gateways.

## AWS

Use a dedicated account with short-lived credentials (IAM Identity Center, an assumed
role, or IAM Roles Anywhere). Never put access keys in lab source.

1. Create the build network and the DCV licensing role once with
   [tools/aws-sandbox.yaml](../tools/aws-sandbox.yaml):

   ```sh
   aws cloudformation deploy --stack-name labcentral-build \
     --template-file tools/aws-sandbox.yaml --capabilities CAPABILITY_IAM \
     --parameter-overrides OperatorCIDR=203.0.113.10/32 AvailabilityZone=us-east-1a
   aws cloudformation describe-stacks --stack-name labcentral-build \
     --query 'Stacks[0].Outputs'
   ```

2. Copy the outputs into `build.subnetID`, `build.securityGroupID` and
   `aws.desktopProfile`.
3. Set `aws.account`, `aws.region`, `aws.zone`, `aws.profile` and an instance-type
   allowlist, for example `types: [{name: t3.medium, cpu: 2, memoryMiB: 4096}]`. Add
   `maxNICs` to a type to check multi-NIC machines offline; otherwise the provider asks
   AWS for the type's limit.
4. For Linux desktops, set `build.dcvURL` and `build.dcvSHA256` to the official Amazon
   DCV server archive for Ubuntu 24.04 and its published checksum.

**How labs are laid out.** Each lab gets its own VPC (`10.77.0.0/16` by default) with
one subnet, security group and route table per network. Machines have no public
addresses: a small gateway holds the lab's only public address, and all SSH, desktop
and check traffic is tunneled through it with host keys pinned on both hops. Configure
it with `aws.gateway: {base: ubuntu-24.04, instanceType: t3.micro}` (the defaults).
Set `aws.allowEgress: true` to let labs request Internet access.

**Permissions and costs.** The operator identity needs EC2 lifecycle permissions and
`iam:PassRole` for the DCV licensing role (restricted to `ec2.amazonaws.com`). Lab
machines get no AWS permissions; desktop machines get a license-only instance profile.
Instances, public IPv4 addresses, images and snapshots are billed while they exist.

### Windows guests

1. Resolve the current Windows Server 2022 AMI once and pin it:

   ```sh
   aws ssm get-parameter --name \
     /aws/service/ami-windows-latest/Windows_Server-2022-English-Full-Base \
     --query Parameter.Value --output text
   ```

2. Add a base with that ID as both `id` and `identity`, `os: windows`,
   `user: Administrator` and `diskGiB: 30`. Never store the parameter path itself.
3. Allow an instance type with at least 4 GiB of memory, and select it for builds with
   `build.instanceType` or the image's `platform.aws.buildInstanceType`.
4. For Windows desktops, pin the DCV server installer:

   ```yaml
   build:
     windowsDCVURL: https://d1uj6qtbmh3dt5.cloudfront.net/2025.0/Servers/nice-dcv-server-x64-Release-2025.0-20103.msi
     windowsDCVSHA256: 593e2b78cd41b64fac255f303e48b1e7f73129969279ba2eae067e519f962067
   ```

The build network needs Windows Update access so the built-in OpenSSH Server can be
installed. See [examples/windows-iis](../examples/windows-iis/README.md) for a small
mixed Windows and Linux lab.

## Proxmox

Run the CLI on a Linux amd64 operator machine that can reach both the Proxmox API
and DHCP addresses on the management/build bridge. API-only access through a VPN is
insufficient for SSH and image builds. Use a local operator VM when needed.

1. Install the CLI, Packer and pinned plugins as above, plus `xorriso` (for example,
   `sudo apt-get install xorriso`). Use Proxmox VE 9 with directory storage and a
   trusted API certificate, or provide `--ca-file`.
2. Supply an administrator API token **once**, through an environment variable. Keep
   it outside shell history and logs:

   ```sh
   read -rsp 'Admin token secret: ' LABCENTRAL_PROXMOX_ADMIN_TOKEN; echo
   export LABCENTRAL_PROXMOX_ADMIN_TOKEN
   labcentral environment setup proxmox ~/.config/labcentral/proxmox.yaml \
     --url https://pve.example.invalid:8006 --admin-token-id 'admin@pve!setup'
   ```

   Review the exact plan and confirm, or use `--yes` for automation. Setup detects the
   node, directory storage and management bridge, reserves free VMID/VLAN/CIDR ranges,
   creates a dedicated pool, user, scoped runtime role, read-only inventory role and token,
   and builds a pinned Ubuntu 24.04 base with a verified QEMU guest agent. It boots a
   temporary NoCloud seed to install the agent **inside the guest**, checks it after
   a restart, sanitizes it and converts it to a template. No host shell or snippets are needed.
3. Export the runtime token from the generated 0600 file and check the environment:

   ```sh
   export LABCENTRAL_PROXMOX_TOKEN="$(cat ~/.config/labcentral/proxmox.yaml.token)"
   unset LABCENTRAL_PROXMOX_ADMIN_TOKEN
   labcentral environment check --environment ~/.config/labcentral/proxmox.yaml
   ```

Setup writes the environment and private ownership/recovery receipts beside it. Keep
these together. Rerunning the same command verifies ownership and reuses completed
steps. If Packer/plugins were installed afterwards, rerun setup to pin their checksums.
`--node`, `--storage`, `--management-bridge`, `--lab-bridge`, `--pool`, `--vmid-start`,
`--vlan-start`, `--cidr` and `--operator-cidr` override detection. Setup refuses existing
unowned resources, overlapping visible allocations and unrelated pending network
changes. Lab VLANs use tags 2–4094; VLAN 1 stays reserved for the native bridge.
Mutations are scoped to its pool, storage, node and bridges. A separate read-only
`VM.Audit` grant on `/vms` lets recovery distinguish absence from lost pool access;
Proxmox removes VM-specific permissions when deleting a guest. Setup applies network
configuration only after verifying that the diff contains its own internal bridge
change. Storage content changes are included in the plan.
`--insecure-skip-tls-verify` is for explicitly configured test endpoints only.

```mermaid
flowchart LR
    Operator[Operator on management network] -->|pinned SSH| Gateway
    API[Proxmox API] -->|noVNC| Desktop
    Management[Management bridge<br/>DHCP and Internet] --- Gateway[Per-lab gateway<br/>1 CPU, 768 MiB]
    subgraph Internal[Internal VLAN-aware lab bridge — no physical ports or host IP]
      A[Lab A: VLAN 100<br/>guests and desktop] --- Router[Author router]
      Router --- B[Lab A: VLAN 101<br/>guests]
      C[Lab B: VLAN 102<br/>guests]
      A --- Desktop[Guest desktop]
    end
    Gateway ---|.1 on each lab VLAN| A
    Gateway --- B
    Management --- G2[Lab B gateway]
    G2 --- C
    Gateway -->|NAT only when enabled<br/>private destinations blocked| Internet
```

The internal `proxmox.labBridge` has no physical ports, host address or uplink;
LabCentral verifies that structural boundary. Each lab has its own gateway, outside the
author's inventory, with DHCP on `proxmox.managementBridge` and `.1` on each lab VLAN.
All guest SSH uses the gateway with both host keys pinned. Gateway nftables drops
forwarding by default and blocks RFC 1918, link-local, CGNAT, the management subnet,
operator addresses and gateway destinations.
No operator routing to VLANs, external NAT gateway or cluster firewall change is needed.
Guest NIC firewall rules remain defense in depth if the cluster firewall is enabled.
Desktops use the Proxmox API console.

Set `proxmox.allowEgress: true` to allow author-requested Internet egress per network.
Gateway rules bind source ranges to their VLAN NICs. Only opted-in networks get a
guest default route and NAT; external DNS uses public
resolvers. `proxmox.cpuTypes` allowlists CPU models. `proxmox.gatewayBase` defaults to
`ubuntu-24.04`. For static management addresses instead of DHCP, set
`proxmox.managementAddresses: {cidr: 192.0.2.0/24, gateway: 192.0.2.1, dns: [1.1.1.1]}`
and reserve that pool exclusively for LabCentral (the gateway must be below offset 4).
`build.bridge` still needs DHCP, Internet and direct operator SSH reachability.

To start clean, destroy labs and remove built images, then supply the admin token again:

```sh
labcentral environment teardown proxmox ~/.config/labcentral/proxmox.yaml \
  --admin-token-id 'admin@pve!setup'
```

Teardown checks ownership and refuses guests still using the pool or bridge. It removes
only setup's base, seed/import files, bridge, ACLs, user/token, roles and pool, and restores
storage content settings only if they have not changed elsewhere. Private receipts stay
local for recovery. An interrupted mutation remains unknown until its exact owned
resource is found or its deletion is confirmed; retain the receipt and rerun.

See [qualification status](../ARCHITECTURE.md#qualification-status) for live evidence
and remaining acceptance work.

## Run labs

```sh
labcentral plan    DIR --environment ENV.yaml   # preview images, machines and costs
labcentral deploy  DIR --environment ENV.yaml   # build missing images, then deploy
labcentral list
labcentral inspect INSTANCE_ID
labcentral access  INSTANCE_ID MACHINE ssh
labcentral access  INSTANCE_ID MACHINE desktop
labcentral destroy INSTANCE_ID
labcentral image list
labcentral image remove IMAGE_KEY
```

- Commands that create or delete resources ask for confirmation; `--yes` approves them
  for automation, and `--json` prints versioned results and progress events.
- `access … desktop` prints a one-use URL on `127.0.0.1` and temporary guest
  credentials. Keep the command running while you use the desktop; Ctrl-C closes the
  session and removes the temporary account. To use a browser on another computer,
  forward the port over SSH rather than exposing it.
- `labcentral config init` creates `~/.config/labcentral/config.json` (mode 0600) for
  default paths and concurrency limits. Flags override it, and it can never enable
  `--yes`.

## State and recovery

State lives in `~/.local/state/labcentral/v2` (or `--state DIR`): a SQLite journal,
provider journals and per-lab SSH keys. Back them up together. One state root owns each
allocation range; do not point two operators at the same target ranges.

```sh
labcentral recover INSTANCE_ID        # finish an interrupted deploy or destroy
labcentral image builds               # list unfinished image builds
labcentral recover build:IMAGE_KEY
labcentral image abandon IMAGE_KEY    # remove an unpublished build attempt
```

There is no background cleanup: resources keep running, and costing money, until you
destroy them. `destroy` keeps images; `image remove` refuses images that a retained lab
still uses.
