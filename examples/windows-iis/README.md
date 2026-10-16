# Windows IIS

A small mixed lab on AWS: a Windows Server 2022 desktop running IIS, and a Linux
client on the same private network.

- **Build:** `images/iis.ps1` installs IIS and a test page (the platform installs Amazon
  DCV first); `images/client.yml` prepares the Linux client.
- **Breaks:** stop the W3SVC service, or stop the default web site.
- **Checks:** the page is served locally (a PowerShell check on the server) and from
  the client (an Ansible check using `curl`).
- **Practice:** a healthy-page baseline stage and four recovery stages. A scenario
  has two variants, each applying one break and checking it from Windows and Linux.

## Run it

Your AWS environment needs a Windows base and the DCV installer pin; see
[Windows guests](../../docs/operators.md#windows-guests). This example expects:

- a `windows-2022` base (`os: windows`) and an `ubuntu-24.04` base for the client and
  the lab gateway;
- `t3.medium` (2 CPUs, 4 GiB) allowed for Windows and its builds, and `t3.micro` for
  the client and gateway;
- `build.instanceType: t3.medium` and the usual build network, with Windows Update
  access for installing OpenSSH.

```sh
labcentral validate --template examples/windows-iis
labcentral plan   examples/windows-iis --environment ~/.config/labcentral/aws.yaml
labcentral deploy examples/windows-iis --environment ~/.config/labcentral/aws.yaml
labcentral access INSTANCE_ID server desktop
```

A cold build of both images takes about 20 minutes for Linux and an hour for Windows;
a deployment takes about 7 minutes. One full build, deploy and teardown cycle costs a
few US dollars. Remove the images afterwards with `labcentral image remove` if you won't
reuse them.

This example has been run end to end on AWS (builds, desktop, SSH, every break and
scenario variant, recovery and cleanup); see
[Qualification status](../../ARCHITECTURE.md#qualification-status).
