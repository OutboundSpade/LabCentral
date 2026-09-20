# Disposable VM builders

`TASK-TEMPLATE-005` supplies independent `io.labcentral.builder.ansible` and
`io.labcentral.builder.dockerfile` packages backed by the same worker binary. The source package declares a logical builder
requirement; a project binding supplies the installation, worker identity,
limits and mirror policy. Source import and GitHub synchronization do not start
builds. Image import and approval remain separate provider/core operations.

## Authoring

Use recipe schema `labcentral.ansible.v1` or `labcentral.dockerfile.v1`; their
closed settings contracts are in [Ansible](../../schemas/ansible-builder-v1.schema.json)
and [Dockerfile](../../schemas/dockerfile-builder-v1.schema.json). Both target
Linux amd64. Paths are relative to an imported recipe's directory, or to the
bundle root for inline recipes. Only declared context files and nonsecret
inputs cross the builder boundary.

Ansible settings select a pinned HTTPS qcow2 base (`baseURL`, `baseSHA256`) or
one `baseDependency`, a playbook, disk size and existing/cloud-init guest user.
The authored URL must be query-free and present in the local mirror allowlist.
At most five HTTPS redirects are accepted; every redirect destination is still
subject to that exact host allowlist and public-address checks. `baseImageDigests` contains its lowercase SHA-256 hex value.
The base must support NoCloud, SSH and Python 3.12 or 3.13 for the toolchain's
offline wheel set. Ansible receives declared inputs as extra variables.
Authors provide playbooks, roles and files; the worker generates Packer's
configuration. The pinned offline Ansible installation avoids an unpinned
package-manager bootstrap. Output is a standalone qcow2 with build credentials,
SSH host/user keys, machine identity, cloud-init state and temporary files
removed before publication. Repositories used by authored tasks can change;
portability does not promise byte-identical package installation results.

Proxmox deployments also require `qemu-guest-agent` installed and enabled in the
built image. The example's reusable Ansible base installs it during construction,
using the builder's bounded proxy for package downloads. The pinned Debian cloud
image alone does not include this agent. Do not start the service during an image
build that lacks the deployment's guest-agent channel; enable it for the deployed
VM. A running VM without its agent does not pass Proxmox readiness.

Dockerfile settings identify the Dockerfile and optionally its final stage.
Declare every external image digest in `baseImageDigests`; image references
must include `@sha256:...`. Stages, `COPY --from` and `RUN --mount=from` are checked
with the pinned BuildKit parser. Custom Dockerfile frontends and remote `ADD`
sources are rejected; vendor those source files instead. A BuildKit source
policy independently rejects undeclared image sources, including inherited
instructions. Declared inputs become build arguments, excluding reserved
BuildKit/Buildx controls. Proxy arguments come from the worker. The internal
result is an OCI-layout tar artifact, never part of a template export.

Recipe graphs use `builds.apiVersion: labcentral.build/v2` and `dependsOn`.
There are at most eight recipes and eight dependencies per recipe, without
cycles or duplicate edges. Ansible can consume one qcow2 base; Dockerfile
recipes address OCI dependencies by recipe name in `FROM` or named contexts.
Core deduplicates shared bases using effective source/context, dependency work,
toolchain/binding identity and limits. Parents reference immutable child output
artifacts and wait durably for them. Canceling a waiter does not cancel a build
with other active waiters or explicit requests. Old v1 recipes retain their
original meaning and cannot declare dependency edges.

## Worker boundary

Install the worker on a dedicated Linux VM with KVM and a delegated systemd
user manager. The worker account belongs to the KVM group and has a private
state directory on persistent disk (tmpfs/ramfs are rejected). It has no provider/control-plane credentials. Pin the QEMU
and qemu-img executable bytes and the complete toolchain guest image in its
version-1 profile. The profile also bounds guest disk size, concurrent guests,
allowlisted DNS hosts, total egress bytes and maximum reserved storage. Storage
reservations include the full overlay and maximum output before guest launch.
`memoryOverheadBytes` reserves a bounded hypervisor allowance in addition to the
binding’s guest RAM, and `maximumMemoryBytes` caps the sum of those reservations.
The external unit limit is guest RAM plus that explicit allowance. Root-owned toolchain files should
be readable but not writable by the worker account.

Every author build boots a fresh overlay of that trusted image. QEMU parses
only this trusted base on the worker host. Author-provided qcow2/OCI inputs are
processed inside the disposable guest. Packer's inner image VM and ansible-local
provisioning run there; BuildKit also runs there. The generated Packer VM uses
TCG with its `max` CPU model, including the x86-64-v2 instructions needed by
AlmaLinux 10 x86_64_v2 bases, so the worker does not require a third level of
nested KVM. The deployed provider CPU must independently meet the base image
requirements.

A private virtio-serial channel carries bounded, digest-verified chunks and
status. No SSH credential, management network port or writable host filesystem
is exposed to the author guest. Restricted QEMU user networking permits only
the fixed per-build HTTP CONNECT proxy. The proxy permits configured public
DNS hosts on ports 80/443, checks every resolved address, pins each TCP dial to
that checked address, rejects internal addresses, and bounds connections and
bytes. Reservations are persisted before forwarding; recovery cannot replenish
an exhausted egress allowance. Changing the allowlist changes build identity.

An independent transient systemd unit enforces CPU, memory, task, file-size and
wall-time bounds with control-group termination. A worker crash does not reset
the deadline. The invocation journal records its original unit, fence, inputs,
profile and result. A verified output receipt recovers a crash between output
collection, guest cleanup and final result publication. Success and cancellation
are not reported until external guest closure is confirmed. Outputs remain
untrusted and need the separate import/approval path.

The worker journal is bounded to 16,384 invocation records, four configured
concurrent guests at most, eight additional staging invocations, nine input
artifacts per invocation and 32 GiB aggregate input/output limits per build.
Each overlay has a separate file-size limit. Capacity exhaustion stops admission; `maximumStorageBytes` bounds the complete
set of reservations and retained outputs;
operators retain enough storage for configured concurrency and retained outputs.
Do not remove an active invocation or a result still needed by core recovery.
Back up the worker journal with its output files when recovering the deployment.

## Installation

Build `cmd/lab-builder-vm` once. `tools/builders/package-relays.py BINARY NEW_OUTPUT_DIRECTORY`
creates independently installable Ansible, Dockerfile and fixed-runtime packages with pinned
executables and a closed worker-binding schema. Configure each installation's
explicit `--kind` argument and local TLS connection separately; packages contain
no endpoint or credentials. `tools/builders/fetch-toolchain.py` verifies the
reviewed [tool lock](../../tools/builders/toolchain.lock.json), including Packer,
its QEMU/Ansible plugins, BuildKit and the complete offline Ansible wheel closure.
`tools/builders/build-toolchain.sh VERIFIED_DEBIAN_BASE BUILDER_BINARY NEW_OUTPUT_QCOW2`
constructs a new guest image from the reviewed Debian base. It never overwrites
an existing output. Distribution packages are recorded inside the resulting
image, which is then pinned as a whole; rebuilding against updated distribution
packages produces a new toolchain identity.

Run the same binary in worker mode:

Use a dedicated Linux user with KVM access, a lingering systemd user manager,
and a private `/run/user/UID` runtime directory. Guest sockets use short hashed
names there, independent of the persistent state path. The worker journal and
images belong on persistent disk. Keep the toolchain image immutable while
guests use it; drain builds before changing a profile or executable identity.

```text
lab-builder-vm worker --endpoint WORKER_ADDRESS:PORT --state PRIVATE_STATE \
  --profile PRIVATE_PROFILE --tls-ca CA_PEM --tls-cert SERVER_PEM --tls-key SERVER_KEY
```

Use a dedicated CA and distinct client/server certificates with the appropriate
TLS extended key usages. TLS 1.3 and mutual certificate validation are required;
there is no insecure TCP mode. Run the local installed plugin without `worker`,
using the same TLS flags plus `--tls-server-name EXPECTED_WORKER_NAME` and
`--kind ansible`, `--kind dockerfile`, or `--kind container-runtime`. Each installed relay accepts only its
declared recipe schema; each package can be installed independently. It exposes
only the existing typed builder/artifact/runtime services over the normal local
plugin socket and forwards them to that worker. The core host gives this relay
no provider credential envelope.

The project binding configuration is a typed object containing `profileSHA256`
and `workerSHA256` (lowercase hex). `lab-builder-vm inspect-profile --profile PRIVATE_PROFILE` verifies the pinned
files and prints that binding object for the current binary. The worker verifies
both identities before accepting a build; the full binding participates in core's build identity. Independently
installing or moving a logical binding never changes source-package bytes.
Keep endpoint names, profile paths, certificates, installed hashes and rollback
records in protected operator records outside tracked source.

The opt-in `TestVMWorkerProcessLossRecovery` test uses
`LABCENTRAL_VM_TEST_PROFILE`, `LABCENTRAL_VM_TEST_BINARY` and
`LABCENTRAL_VM_TEST_SCRATCH` (a persistent scratch directory). It starts an
authenticated worker, kills that worker during a real guest build, recovers the
same supervised VM and invocation under a new fence, verifies the transferred
OCI output, and confirms guest closure. Its private evidence directory is
reported by the test. Ordinary tests do not launch VMs.

Ansible uses a non-root `guestUser` with temporary sudo access. Sanitization explicitly enables custom deletion, then checks that cloud-init state, the temporary Ansible installation and generated SSH identity are absent before conversion. A failed cleanup check prevents publication.

## Fixed container runtime packaging

The optional `oci-layout-v1` builder feature lets core verify a complete OCI
archive after transfer: bounded metadata, SHA-256 blobs, one Linux amd64
manifest, its configuration and layer references. This does not extract layers
on the control-plane host. The archive digest, manifest digest and configuration
image ID remain separate identities.

For a version-2 preparation, core derives an internal
`labcentral.container-runtime.v1` recipe using the project's unique
`container-runtime` binding. Portable import previews report `core:container-runtime` when that local binding is missing. Authors cannot request this recipe through the
general build endpoint. Its [closed settings](../../schemas/container-runtime-builder-v1.schema.json)
bind the verified OCI identity and operator-owned runtime policy. The binding's
`containerRuntime` object contains `baseSHA256`, `agentSHA256` and `diskGiB`
(8–64); it is local configuration, excluded from portable sources. The recipe
has one `oci` dependency and a fixed empty source context. Shared runtime builds
are deduplicated by image, runtime policy, toolchain, binding and limits.

Build the fixed base with:

```text
go build -o labcentral-container-vm ./cmd/lab-container-vm
tools/builders/build-container-runtime.sh VERIFIED_DEBIAN_BASE RUNTIME_AGENT NEW_RUNTIME_QCOW2
tools/builders/build-toolchain.sh VERIFIED_DEBIAN_BASE BUILDER_BINARY NEW_TOOLCHAIN_QCOW2 RUNTIME_QCOW2
```

The runtime base contains the fixed agent and rootless Podman account (UID 1001),
without an image or deployment identity. Pin its disk and agent SHA-256 values
in both the builder binding and target image-import policy; pin the complete
containing toolchain image in the worker profile. The packager verifies that
base, then boots an inner disposable guest, checks the fixed agent, loads the
OCI archive as `labworkload`, verifies the exact local image ID, removes temporary
authentication files and applies the existing offline identity cleanup.

The result is qcow2 plus typed `labcentral.container-image/v1` evidence. Core
imports it under negotiated `container-image-import-v1`; approval remains
separate. Runtime configuration `labcentral.container-vm/v2` preserves the
logical manifest reference while launching the exact local configuration image
ID with `--pull=never`. Existing v1 runtime configurations and static image lists
retain their original behavior. Each container still gets its own VM, with the
same no-mount, no-secret, no-published-port restrictions.
