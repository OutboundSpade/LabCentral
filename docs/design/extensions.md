# Independently installed extensions

The accepted implementation sequence is TASK-EXT-001 through TASK-EXT-008.
[ADR-0023](../adr/0023-independent-extension-packages.md) defines installation and browser isolation;
[ADR-0024](../adr/0024-extensible-access-and-gateway-delegation.md) defines access and delegation;
[ADR-0025](../adr/0025-source-build-and-image-preparation.md) defines source/build orchestration.

## Compatibility boundaries

Package installation verifies identity and contracts. Provider discovery establishes target-specific capabilities. Image approval establishes approved content. Signed target qualification establishes deployment eligibility. None substitutes for another.

Existing installation v1, plugin wire v1 and immutable template representations remain readable. New categories and optional methods are independently negotiated. Source and builder configuration is versioned plugin-owned data, never new generic portable resource kinds. Production OS isolation is provisioned by the administrator.

## Acceptance

The external fixture suite must install provider, access, source, builder and browser packages after core/UI artifacts are built, then prove full lifecycle, recovery, scoped authority and cleanup without rebuilding those artifacts. Production integrations remain outside this work. The owner subsequently authorized deploying the verified release to the private testbed under TASK-EXT-009. State/checklist report implementation progress; this document describes the accepted target contract, not a claim that every extension is implemented.

## Implemented orchestration

Core runs source resolution, builds, provider imports and deployment preparation as typed durable operations. Gateway delegation and all four browser slots have API/UI entry points. The checklist records verification and release status separately from implementation; a working fixture never qualifies a production target or builder sandbox.

### Installation contracts

An [extension package](../../design/contracts/extension-package.schema.json) indexes contribution manifests by identity and digest. Each [v2 contribution](../../design/contracts/extension-manifest.schema.json) declares exactly one category, version, supported OS/architecture, negotiated protocol range and requested permissions. A package can contain several contributions; the [v2 installation](../../design/contracts/extension-installation.schema.json) configures each backend as a separate process. Browser-only contributions omit an executable. Existing v1 readers remain accepted on the new host.

Configuration schemas must be bounded, self-contained JSON Schema objects. Compilation cannot fetch referenced schemas or open files. No runtime dependency fetching or executable uploads are exposed in the product API. Protected installation paths and explicit permission grants remain prerequisites; manifests do not establish production OS isolation.

### Access and browser contracts

Custom methods contain a namespace separator, declare resource kinds, per-method endpoint schema ranges and a core-known presentation (`terminal`, `graphical`, `credential-download`). Registry selection intersects method, resource kind, exact target binding and endpoint compatibility and rejects multiple matches. Legacy `ssh` and `vnc` descriptors are synthesized. Endpoint discovery is read-only and occurs before adapter selection; core persists the pending session before issuing any access grant.

Credential delivery additionally negotiates `access-credential-delivery-v1`, `access-enforcement-inspection-v1` and `access-issuance-recovery-v1`. A durable one-time claim precedes delivery, and only the existing authenticated token exchange may activate traffic. Core verifies the active lease and enforcement deadline before returning bytes. Recovery-capable adapters must revoke idempotently by session ID even when an issuance response and its opaque state were lost; unfinished issuance is reconciled after a 30-second grace period. Download retry never reuses consumed material. The adapter must independently enforce the maximum 30-second closure bound at the traffic enforcement point. The independent access fixture places lease enforcement in the process serving its echo traffic and exercises core-outage closure; production adapters must prove equivalent enforcement in their own network dataplane.

The browser SDK has a version-1, nonce-bound MessageChannel. The host transfers the port only to the mounted opaque-origin frame. Requests have bounded concurrency/rate and explicit permissions; streams have opaque handles and byte limits, and credentials remain in the host. Reload/navigation destroys the bridge. Catalog polling removes unavailable contributions. The mounted slots are `lab-panel`, `resource-action`, `access-view` and `plugin-settings`. A descriptor may select an installed access renderer as `pluginInstance/contributionId`. The built-in noVNC presentation follows the same discovered action and session path.

The topology API reads canonical resource/NIC fields, observed phase and access descriptors. It does not expose provider state, management addresses, endpoint credentials or arbitrary observed fields. Current provider evidence must match the configured target fingerprint before actions are advertised.

### Bundles

The existing publication API continues accepting a YAML source document. The additive `bundle-revisions` endpoint accepts a base64 gzip-tar archive in `archive`. The archive contains `bundle.yaml` and exactly its declared regular files; the manifest excludes itself from its file list. Core checks normalized UTF-8 paths, case-fold collisions, file types, digests, byte/file/depth/compression limits and trailing data without extraction. The immutable artifact retains the archive; the content API returns its verified entrypoint. Existing renderer identity and single-document revision bytes remain unchanged. Revisions with `builds.apiVersion: labcentral.build/v1` select `labcentral.renderer/v1alpha1+builds/v1`. Their literal `build://recipe-id` VM/container image references must resolve to approved persisted target bindings before planning; published bundles and existing instance canonical bytes are never rewritten.

### Source and build operations

Administrator bindings name allowed projects and validated configuration. Source credentials are read from protected files only for the invocation; source results carry an immutable identity, provenance digest and scoped artifact handle. Explicit `/source-resolutions` calls publish verified bundles as the requesting author, with a database fence and a final authority check. The source owns repository transport, credentials and its durable invocation journal. There are no native polling schedules or webhook listeners.

Bundle build metadata declares immutable recipes, builder binding, versioned schema name, architecture, context paths, base-image digests and declared nonsecret inputs. Core sends only those files and inputs through `ArtifactTransferService`; handles never enter persisted work results. Builders receive recipe and binding configuration plus mandatory CPU, memory, time, input/output and egress limits. Production enforcement of those limits and hostile build isolation is the operator-provisioned builder's responsibility. The fixture emits inert image bytes and never executes author code.

`/builds`, `/image-imports` and `/deployment-preparations` enqueue durable work. `/extension-operations` supports project-scoped inspection and cancellation; retry submits a new idempotency key. Builds deduplicate within a project using recipe/context, base images, architecture, exact builder binary, configuration, limits and declared inputs. Imports deduplicate separately using artifact, architecture, exact provider binary and target configuration. One binding admits one active invocation by a database-time lease. Recovery uses the original invocation ID before redispatch; a timeout does not establish failure.

Preparation creates fixed build/import children, waits for them, verifies image approval and the provider binding, then invokes ordinary instance or atomic batch admission with current authority, quotas and a fenced transaction. Canceling a preparation does not cancel shared children. An explicit build/import cannot be canceled while it has an active preparation waiter or multiple distinct direct requests. Cancellation of an uncertain import first recovers its outcome; a completed import is retained unapproved for explicit retirement.

Imports require administrator target-management authority in this first version. `/image-bindings/{id}/policy` separately approves or permanently retires a binding. Import completion never qualifies the target or approves its image. Provider validation and the existing exact-target qualification gates still apply. Existing providers without `image-import-v1` continue using inventory images; URL imports remain disabled.

### Gateway delegation

Administrators register API clients (gateway clients in the protocol) and receive a secret once. `DELETE /admin/gateway-clients/{id}` permanently removes the client, pending device requests and grants in one audited transaction. Retries are idempotent. Historical audit/event records and access-session grant bindings remain; sessions fail their next authorization check and use the existing durable closure path and bounded enforcement deadlines. Disabling is reversible; deletion requires a new registration to reconnect. `/device/authorization` and `/device/token` use RFC 8628 form bodies and client authentication. A five-minute request starts with a five-second polling interval; premature polling increases it. The signed-in browser reviews client identity, project, actions, templates, instances and targets at `/device`. Approval cannot choose a subject or modify the stored scope. Device codes and approved grants are single-use/revocable as appropriate, and only keyed hashes are stored.

Grants last at most fifteen minutes without refresh. Gateway calls send the grant plus registered client identity and secret, and are confined to `/gateway/*` discovery, deployment, operation and access-session routes. Every call intersects approved scope with current user authority. Access sessions additionally bind to the grant and cannot outlive it. `/account` lists and revokes the user's grants; audit records carry both user and gateway identity. SSH enrollment/server and unattended service accounts remain outside this contract.

### Storage and recovery

Additive jobs migration `0003_typed_work_subjects` isolates non-instance work from existing instance workers. Core migrations `0014`–`0017` preserve old access records and add work, gateway and build records without changing previously checksummed migrations. Work input/output artifact references, published bundles, dependencies and image bindings are included in database backups; offline artifact GC retains every referenced input/output. Restore the database and artifact set together. Target images are retired explicitly and are never automatically deleted with a lab.

## Feature packages and portable source libraries

ADR-0026 and ADR-0027 extend the four original browser slots with top-level pages and typed feature operations, and extend explicit single-source resolution with collection synchronization and durable schedules. The preceding sections describe the existing v1 implementation; the accepted additions are defined in [portable templates and collaboration](portable-template-collaboration.md).
