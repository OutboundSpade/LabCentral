# Proxmox image imports

Image import operates on an internal build artifact. Portable exports contain no
image bytes, imported-image evidence, approval, target configuration or allocation
records. ADR-0028 governs this boundary.

`proxmox.target-config/v1.4` adds the optional closed `imageImport` profile:

```json
{
  "stagingStorage": "import-staging",
  "storageAlias": "primary",
  "vmidRanges": [{"start": 800000, "end": 800099}],
  "maximumArtifactBytes": 34359738368,
  "maximumDiskBytes": 137438953472
}
```

This fragment belongs in an otherwise qualified target configuration. A destination
pool and configured VM-image storage profile are required. The staging store must
report active, enabled, file-backed `import` content. Its scoped credentials need
`Datastore.AllocateTemplate`, `Datastore.Allocate` and `Datastore.Audit` on the
staging store. Proxmox requires `Datastore.Allocate` to remove uploaded import
volumes; use a dedicated staging store to narrow that permission. The destination pool adds
`VM.Clone`, `VM.Config.Disk` and `VM.Config.HWType` to its existing scoped rights.
The provider initializes its protected journal with the existing
`--initialize-journal-directory` option. Configuration never creates qualification
evidence or bypasses the signed production gate.

The optional `image-import-reservations-v1` protocol adds `ImageImportService.Plan`
and a typed reserved plan on `Import`. Core commits the opaque plan digest and its
allocation claims together in migration `0019_image_import_reservations`. Image
VMIDs share the target/namespace/value uniqueness constraint with deployments.
The allocation table uses an empty legacy `instance_id` for non-instance claims
and the owning import operation in `resource_id`; the plan table references that
operation. Recovery reuses the original reservation. Uncertain outcomes retain it.

The provider streams standalone qcow2 artifacts through an expiring invocation
grant backed by fresh qualification. It verifies transferred size/hash and rejects
external backing files, incompatible features, encryption, snapshots and excessive
virtual size. A durable receipt binds the bytes. Local retained staging is capped
at 64 GiB; an artifact is at most 32 GiB. Proxmox uploads include SHA-256 and use
only invocation-owned filenames. No source URL, shell command or arbitrary host
path is accepted. Proxmox also performs its own untrusted-image validation.

Upload, VM creation and template conversion persist intent before submission and
the task identity afterward. Recovery polls the original task; a lost upload
response requires its unique filename and checksum verification in the task log.
Unknown or ambiguous history fails closed and retains the reservation for operator
reconciliation. Imports create stopped templates with a cloud-init disk and guest
agent, without network devices or guest credentials. Evidence binds artifact,
provenance, target, ownership marker and exact VM configuration. Cleanup removes
only the invocation's staging files.

For v1.4, infrastructure qualification uses the explicitly versioned
`proxmox.mutation-inventory/v1.1` projection, retaining configured static templates
and stable infrastructure facts. Additional imported templates do not change it.
Imported images are individually validated. Older configuration versions retain
their original inventory digest behavior.

Import completion creates an **unapproved** binding with a local reference of the
form `labcentral.local/images/<binding-alias>@sha256:<artifact-digest>`. Prepared
and direct YAML/API launches require current project approval at atomic admission.
Each admitted deployment retains the exact binding. Labs with multiple images use
`proxmox.validation-plan/v1.1`; single-image plans retain v1.0. Existing VM plans
and provider state keep their prior formats.

`image-retirement-v1` adds durable explicit retirement. Core marks the binding
unapproved/retired in the transaction that queues retirement work. Deletion waits
while a retained deployment is not destroyed. The original qualified provider
then deletes the exact owned template; only confirmed absence releases its
allocation. A provider tombstone prevents old import replay from reactivating it.
Legacy providers retain policy-only retirement. Failed or uncertain imports
without completed bindings require operator reconciliation; allocations are not
silently released.

Preparation recognizes build bundles and portable packages. It renders composition
and typed inputs to select used recipe roots, builds their dependencies, and
imports only selected outputs. Durable results report `building`, `importing`, or
`awaiting-approval`; retirement reports `waiting-for-labs`. Admission rechecks
approval.

`TASK-TEMPLATE-006` implements these paths. Exact-target live qualification remains a `TASK-TEMPLATE-008` gate before using the changed artifacts on infrastructure. These implementation contracts do not claim production deployment.

The opt-in `TestCredentialedProxmoxT3ImageImport` qualification harness requires
`LABCENTRAL_PROXMOX_T3_IMAGE_EXECUTE=I_ACKNOWLEDGE_LABCENTRAL_PROXMOX_T3_DESTRUCTIVE`
and the same prefix with `TARGET_CONFIG`, `CREDENTIAL_FILE`, `CONFIRM_TARGET`,
`RUN_ID`, `VMID`, and `ARTIFACT`. Supply an initially absent, reserved test VMID,
a unique invocation ID, and a standalone qcow2 no larger than 64 MiB. Run it
only in the isolated qualification runner against the exact candidate target.
It verifies bounded transfer, an accepted create whose response is withheld,
restart recovery, binding validation, replay, owned retirement, and absence.
Failures retain the journal and any uncertain resources for ownership-checked
operator reconciliation. This supplements the infrastructure T3 matrix and
does not enable production mutation by itself.

## Participant preparation

`POST /deployment-preparations` accepts `preparation.version: 2` for a single
published revision and an optional `owner: {kind: "team", id: "…"}`. Omitting
`owner` retains the initiating user as owner. Version 2 rejects the legacy
`owners` batch field. Omitted/version-1 requests retain their author and
instructor-batch semantics.

Version 2 requires current project template-read and instance-create authority,
including the requested owner relationship and target grant. It does not grant
authoring, general build/import, or image approval permissions. Core derives
bounded build/import work from that published revision, links every dependency
to its preparation transactionally, and rechecks authority before new execution.
Shared work can continue for another currently authorized preparation. The
initiating user remains the audit actor. Participants can read their own
preparation and its linked work through `/extension-operations/{id}`; project
read access remains required.

Progress reports `building`, `packaging`, `importing`, or `awaiting-approval`. Once images are
approved, admission rechecks the preparation's exact actor, revision, project,
owner and target under its fence in the same transaction as quota/allocation
reservation. Revoked launch authority cannot admit a pending deployment.

## Packaged containers

`imageImport.containerRuntime` optionally pins `baseSHA256` and `agentSHA256`.
It requires the existing qualified container runtime/guest-agent profile.
Discovery advertises `image.import.container-vm` only alongside actual import
storage support and fresh infrastructure qualification.

The negotiated `container-image-import-v1` feature interprets import
`configuration_json` as closed `labcentral.container-image/v1` evidence. A
`proxmox.image-import-plan/v1.1` binds that evidence together with the qcow2
artifact and reserved allocation; changing the manifest, configuration ID or
runtime pins invalidates the plan. Ordinary qcow2 imports retain v1 identities.
The imported image reference uses the OCI manifest digest, while
`artifactDigest` continues to identify the containing qcow2 bytes. Per-resource
planning selects that imported VM template and v2 offline runtime configuration;
no registry fetch or arbitrary guest command is added.
