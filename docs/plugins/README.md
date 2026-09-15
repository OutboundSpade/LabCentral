# Build a LabCentral plugin

Infrastructure providers run as separate processes and translate existing typed
resources into infrastructure operations. Access plugins are separate processes
that broker short-lived access sessions. Neither can read the control-plane
database or make authorization decisions.

## Start with the public SDK

The [external provider example](../../examples/external-provider/README.md) is a
separate Go module that imports only the public SDK and generated protocol. It
registers two targets without adding a provider branch to `labd`.

```sh
cd examples/external-provider
go build -mod=readonly -o ../../bin/external-provider .
```

The fixture is discovery-only and advertises VM execution as unsupported. Its
local `replace` directive supports repository tests; a distributed provider must
pin a published SDK revision.

| Package or contract | Purpose |
| --- | --- |
| [Go SDK](../../pkg/plugin-sdk-go/) | Registration, runtime, launch, manifest and contract helpers |
| [Protocol v1](../../api/plugin/v1/plugin.proto) | Versioned gRPC wire messages |
| [Provider contract](../design/provider-contract.md) | Validation, planning, execution, read, destroy and recovery |
| [Plugin system](../design/plugin-system.md) | Handshake, compatibility, process lifecycle and permission boundaries |
| [Manifest schema](../../design/contracts/plugin-manifest.schema.json) | Executable identity, checksum and supported platform/protocol |
| [Installation schema](../../design/contracts/plugin-installation.schema.json) | Plugin instance and target bindings |
| [Access contract](../design/access-sessions.md) | Short-lived sessions, one-use tokens, ownership and revocation |

## Implement a real provider

1. Declare supported **existing** resource kinds and negotiate protocol features.
   Discovery must report capabilities from the actual target, with validity.
2. Implement typed validation and planning; respect core-assigned instance IDs,
   allocation claims and dependency inputs.
3. Journal mutations durably. Recover uncertain calls using their original
   invocation IDs. A timeout is not proof that an operation failed.
4. Bind apply, read and destroy to exact owned resources. An unavailable provider
   is not evidence of resource absence and cannot release allocations.
5. Resolve only supplied credential references in the permitted invocation scope.
   Keep secrets and upstream management details out of product outputs.
6. Run the [shared contract tests](../../pkg/plugin-sdk-go/contracttest/) and
   applicable [conformance cases](../../design/contracts/provider-conformance.yaml),
   including crashes, duplicate calls, partial creation and cleanup failures.

The [testing strategy](../design/testing.md) separates fake API tests from live
target qualification. Protocol compatibility does not establish target isolation.
Changes to portable resources or breaking wire contracts require an ADR.

## Package and install

Build the executable, calculate its SHA-256, and write a versioned manifest.
Use the [installation guide](../operations/plugin-installation.md) for protected
paths, target configuration, service isolation and restart requirements. Inspect
the registered targets and grant workspace access in **Plugins** after startup.

Do not infer installation support from a provider kind name. Targets are explicit
bindings, and each target discovers its own capabilities. Follow
[compatibility](../compatibility.md) when changing schemas or readable state.
