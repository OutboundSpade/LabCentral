# Windows IIS

One Windows Server 2022 desktop and one small Linux SSH client share a private
network. The build installs IIS and a page; platform code installs Amazon DCV
first. Use `labcentral validate . --template` to validate the teaching package.
The instructor package stops either W3SVC or the default site, checks the page
locally with PowerShell and remotely with curl, and supplies one scenario with
two variants. Baseline deployment starts healthy; scenario preparation is hosted.

Bind `windows-2022` in your private AWS environment with `os: windows`,
`user: Administrator`, `diskGiB: 30`, and identical pinned AMI `id` and `identity`.
Resolve the current official base once, then save its returned ID (never the SSM
path) in that environment:

```sh
aws ssm get-parameter --region YOUR_REGION \
  --name /aws/service/ami-windows-latest/Windows_Server-2022-English-Full-Base \
  --query Parameter.Value --output text
```

The Linux `ubuntu-24.04` base is also required for the client and gateway. Allow
`t3.medium` (2 CPUs / 4096 MiB) for Windows and its build/qualification guest;
`t3.micro` (2 CPUs / 1024 MiB) suffices for the client and gateway. Set
`build.instanceType: t3.medium`, pinned Packer/plugin settings, the operator-owned
build subnet/security group, and the license-only `aws.desktopProfile`. Give the
build network Windows Update access to install the OpenSSH Server capability.
Runtime Internet access is unnecessary for this example. Management remains
private through the gateway; the Windows firewall stays enabled.

The Windows DCV MSI URL and SHA-256 are recorded in `tools/toolchain.json` and
checked in the guest, together with its Authenticode signature. The pin is Amazon's
DCV 2025.0-20103 server MSI. Updating the pin changes Windows desktop image keys only.

Local tests establish code behavior only. Windows boot, EC2Launch reboot/sysprep,
DCV console display, licensing and real session revocation remain unqualified
until the following acceptance run is recorded outside tracked source.

## Live acceptance in an explicitly configured disposable AWS environment

Do not use an existing installation. Keep credentials, environment files,
instance/image IDs, logs and evidence outside this repository.

- Validate and plan this template; review pinned bases, type capacities, private
  NICs, image identities and the license-only role. Build both images. Confirm
  DCV installs before IIS, author code runs in the guest, sysprep shuts down, the
  captured Windows AMI boots, readiness checks pass and qualification resources
  are confirmed removed. Allow 25–50 minutes for a cold build.
- Deploy a fresh instance. Confirm one hostname reboot, pinned SSH on both hops,
  peer names in the Windows hosts file, private addresses and no Internet egress.
  Allow 8–15 minutes; retry readiness/recover the same owner after uncertainty.
- Launch the Windows desktop through the existing hosted/private gateway. Verify
  the console is owned by `lab`, authenticate through the existing DCV Web SDK,
  observe the first frame, keyboard, resize, clipboard, reconnect and revocation.
  Check license entitlement and absence of public guest addresses. Allow 10 minutes.
- Use student SSH and the coach terminal; confirm PowerShell on Windows and the
  unchanged Linux shell. Verify `http://server/index.html` from Linux. Allow 5 minutes.
- Apply/reset each break; verify both checks fail, then pass after repair. Run each
  scenario variant, complete its local and remote check stages, and confirm an
  interrupted mutation is not replayed. Exercise concurrent step locking, script
  nonzero exit, timeout/child termination, output limits and temporary-file removal.
  Allow 15–25 minutes.
- Destroy the instance; interrupt/recover one create and one cleanup under the same
  owner. Verify termination, network/gateway removal and retained cleanup intent
  until confirmation. Remove both images and verify their snapshots are gone.
  Allow 10–20 minutes. Record IDs and confirmed absence outside tracked source.

Budget roughly 75–125 minutes and **US$1–3** for a single acceptance cycle in a
low-cost US region, including Windows license-inclusive compute, temporary build
and qualification guests, Linux guests, public gateway IPv4 and gp3 storage.
This is a planning allowance, not a current price quote; verify your region's
prices before running. NAT gateways are not used. Retained AMIs/snapshots, repeat
builds, idle resources and burst CPU charges can increase cost.
