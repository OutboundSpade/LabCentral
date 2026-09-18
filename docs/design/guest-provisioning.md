# Guest provisioning

## Terminology and phases

Template authoring declares desired workloads/configuration; bundle packaging makes content immutable; image building creates reusable VM/OCI images; rendering resolves one instance; provider realization creates infrastructure; guest provisioning configures the guest during creation/boot or later. Cloud-init and Ignition are provisioning even when injected before first boot.

## v1 methods

- **cloud-init:** typed meta/user/network-data inputs for suitable Linux VMs; provider delivers via its supported datasource. Raw fragments are bundle artifacts with explicit media type and bounded interpolation.
- **Ignition:** versioned JSON/config fragment for Fedora CoreOS-like images, delivered through provider-supported mechanism.
- **OCI config:** image, entrypoint/command/args, environment literals or SecretRefs, working directory, user, health/readiness, mounts, and bundle files.
- **Files/service config:** destination, owner/mode, content ArtifactRef or secret source, and explicit boot/service enablement supported by the method.

Provisioning specs declare supported OS family, required guest capability/image contract, timing (`firstBoot`, `everyRecreate`), and sensitivity. The rendered canonical spec includes refs/digests but not secret bytes. Providers report `injected`, `booting`, `ready`, `failed`, or `unknown` with bounded safe diagnostics.

The first Proxmox QEMU mutation is intentionally smaller than the portable Cloud-Init model. It accepts only PVE-native IPv4 `ipconfigN` values derived from typed NIC addresses and logical CIDRs, sets `ciupgrade=0`, and requires an operator-validated template with an existing Cloud-Init drive and QEMU guest agent. It rejects raw user/meta/network data, `cicustom`, author files, passwords, secrets, Ignition, and bundle artifacts before mutation. Artifact transport and richer provisioning require a later versioned protocol and threat review.

Cloudbase-Init, WinRM/PowerShell, SSH execution, Ansible, and custom guest agents are future provisioner extensions. Arbitrary control-plane SSH scripting is not the default, and guests do not require persistent inbound administrative reachability.

## Container-via-VM

The reusable runtime contract is documented in `provider-contract.md`. Bootstrap is one-time, versioned, and bound to installation/instance/resource/generation. It starts an unprivileged OCI workload, pulls an exact digest (tags may resolve during plan but digest is stored), attaches provided volumes/networks, emits readiness/logs through a provider-safe channel, and destroys credentials after use. Reset defaults to VM recreate from the base image; snapshot/restore carries explicit consistency and runtime-version compatibility.

## Image building

Builds are separate durable operations executed in isolated, disposable builders with no provider/control credentials, restricted egress, resource/time/output limits, provenance, and mandatory content digest. Bundle build contexts never execute inside `labd`. External image resolution is disabled by default pending OQ-IMAGE-001.

## Approved container VM runtime

[ADR-0022](../adr/0022-proxmox-container-vm-runtime.md) adds a fixed, typed guest runtime for explicitly VM-isolated containers. It does not enable author-supplied cloud-init or arbitrary host commands. The immutable runtime image preloads approved OCI digests; bootstrap binds a guest once and starts a rootless workload through a fixed entrypoint. See [setup and supported fields](../operations/container-vm-runtime.md).

## Reusable production image recipes

ADR-0028 adds isolated Ansible image baking and Dockerfile builders; Ansible post-launch guest provisioning remains out of scope. Source portability never includes built image bytes. The accepted production build/import contract is defined in [portable templates and collaboration](portable-template-collaboration.md).
