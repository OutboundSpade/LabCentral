# Writing independently installed extensions

An extension package indexes contribution manifests; each backend contribution has its own process identity, supported contract and credential scope. Use `pkg/plugin-sdk-go/runtime` and `api/plugin/v1`, or the framework-independent `pkg/plugin-sdk-browser/index.js`. The [installation guide](../operations/plugin-installation.md) and [contract](../design/extensions.md) define the authority boundaries.

Source plugins implement Resolve, GetInvocation, Cancel and ArtifactTransferService. Return a stable resolved source identity, provenance digest and bounded gzip-tar bundle. Keep a durable journal keyed by the supplied invocation ID; GetInvocation must distinguish absent, running, succeeded and failed outcomes. Reissue only invocation-bound transfer handles during recovery. Credentials arrive only for the current resolve call. The [source fixture](../../examples/external-source/) demonstrates changing references, malformed output and recovery after the result was committed but the response was lost.

A bundle can opt into build semantics with this metadata in `bundle.yaml`:

```yaml
builds:
  apiVersion: labcentral.build/v1
  recipes:
    - id: base
      builder: fixture.builder/image
      schema: fixture.image.v1
      architecture: amd64
      configurationJSON: '{}'
      context: [machine.txt]
      baseImageDigests: []
      inputs: []
```

Every context file must also appear in the bundle's ordinary path/size/digest manifest. A VM `imageRef` or container `image` can use `build://base`. Recipes are immutable revision metadata; publication does not run them. Build-reference resolution occurs only under the revision's explicit semantics after persisted target bindings are approved.

Builder Validate checks its versioned recipe schema and supplied limits. Build streams bounded progress and one terminal result; GetInvocation recovers it after response loss. Cancel is idempotent and must report whether execution stopped or had already completed. ArtifactTransferService.Write receives only declared immutable context files and inputs, encoded as `{"version":1,"files":[{"path":"...","data":"base64"}],"inputs":[{"name":"...","value":"..."}]}`. Validate the invocation, digest, size and scoped handle before using any input. Runtime code and dependencies are installed beforehand. The [builder fixture](../../examples/external-builder/) emits inert bytes, never Dockerfile/Ansible execution.

Providers optionally negotiate `image-import-v1` plus `artifact-transfer-v1` and register ImageImportService alongside ProviderService. Import receives a content-addressed artifact, target configuration and invocation-scoped provider credential. Persist the original invocation's result and implement binding validation. Return the exact artifact, architecture and target configuration binding. A successful import starts unapproved and does not authorize deployment. The [lifecycle fixture](../../examples/external-lifecycle-provider/) exercises import and owned apply/read/destroy. Existing providers without this optional service keep using inventory images.

Access adapters declare namespaced methods, exact endpoint schemas, workload/network kinds, presentation and maximum offline closure time. Authenticate the first stream frame through the core callback and renew its lease. Downloadable network access must enforce the lease at the traffic endpoint and implement delivery, inspection and idempotent revocation even when issuance responses are lost. Never persist downloadable credentials in opaque state or ordinary artifacts. The [access fixture](../../examples/external-access/) serves TCP/WebSocket echo traffic, closes sockets on revocation and expiry, and proves closure while core is suspended. It is not a VPN implementation.

Browser packages declare immutable assets and contributions in `lab-panel`, `resource-action`, `access-view` or `plugin-settings`. Package every dependency; frames cannot fetch APIs or third-party scripts. `connect()` establishes the host-owned MessageChannel. Supported operations are context, topology/status, approved lab navigation, access creation and session-scoped stream read/write/close. Stream events contain an opaque handle and bytes, never a token. Resource actions are constrained to their host resource; access views can consume the initial host-owned session. The [browser fixture](../../examples/external-browser/) includes a panel and echo renderer.

After building core/UI once, set `LABCENTRAL_EXTENSION_TEST_BINARY` to that exact `labd` and run:

```sh
python3 tools/extensions/source-proof.py
python3 tools/extensions/build-proof.py
node web/tests/extensions.mjs
node web/tests/gateway-external.mjs
```

To verify the release SDK as well, set `LABCENTRAL_EXTENSION_SDK_ROOT` to the extracted distribution: all fixtures then build only from its shipped public packages and examples.

Each command verifies the core's identity remains unchanged while packages are independently built/installed. Browser scenarios use Chromium, Firefox and WebKit. The [gateway client](../../examples/external-gateway/) imports only the product API and validates HTTPS, device approval, scoped reads and revocation. Use fixtures in isolated development; production plugin isolation and exact-target qualification require separate conformance evidence. VM-backed containers remain provider-owned under the [guest runtime contract](../operations/container-vm-runtime.md); there is no runtime-driver category.

## Feature pages (host contract 2)

Declare `browser.minimumHostVersion: 2` and a contribution with `slot: page`.
The shell adds a navigation entry at `/feature/?contribution=INSTANCE/ID`. The
contribution is mounted under the selected project; switching project destroys
its frame and channel. Missing packages show an unavailable state without breaking
core navigation. Existing v1 slots and bridge framing remain compatible.

`context.read` includes `projectId`, `hostVersion` and the contribution's available
method names. Typed feature calls use `host.request(method, {input: {...}})`.
Methods include `templates.list|revisions|content|export|importPreview|create|publish|publishBundle`,
`sources.bindings|resolve`, `builds.list|get|create|cancel`, `images.get|approve`,
`targets.list`, `labs.list|prepare` and `members.list` (with an authorized target).
Declare only the corresponding `templates.read|write`, `sources.read|resolve`,
`builds.read|create`, `images.manage`, `labs.read|create` or `members.read` permissions.
Writes carry a caller-generated `idempotencyKey` retained across retries. The host
chooses the path, method, active project and security headers; callers cannot supply
those values. The canonical HTTP API still authorizes each request. Feature calls
are available only to page contributions. No API tokens or access launch tokens
are returned by these operations. See `web/src/lib/extension-bridge.ts` for the
closed validated input catalog and `examples/external-browser/page.js` for an
independently packaged example.

See [portable template packaging](portable-templates.md) for the source-only package contract and CLI.

Host contract 3 adds optional typed legacy-route aliases, project-group operations,
explicit team preparation ownership, caller preparation history, schema authoring,
source downloads, and classroom batch/lifecycle methods. Context includes the
caller's subject and project display permissions. Existing contract 2 packages
retain their behavior. See [feature packages](feature-packages.md) for installation,
compatibility, and the permission boundaries.
