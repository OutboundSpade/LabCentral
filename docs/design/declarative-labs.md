# Declarative Labs and bundles

## Author document

The initial authoring identity is `apiVersion: labs.labcentral.io/v1alpha1`, `kind: Lab`. `metadata.name` is DNS-label-like and revision-local. The top-level `spec` contains `parameters`, `components`, `resources`, `outputs`, `providerOverrides`, `requirements`, and educational `metadata` preserved but not interpreted by infrastructure core.

The initial normative authoring contract is [`schemas/lab-v1alpha1.schema.json`](../../schemas/lab-v1alpha1.schema.json); future Go authoring types and generated/editor schemas must pass compatibility tests against it. Curated invalid and strict-parser fixtures live under [`schemas/fixtures/`](../../schemas/fixtures/). YAML parsers must reject duplicate keys, non-string map keys, unknown fields except explicit annotations, invalid UTF-8, unsafe tags, and documents beyond configured byte/node/depth/alias limits.

## Bundle format `labcentral.bundle/v1alpha1`

```text
lab.yaml                 required
bundle.yaml              required manifest
README.md                recommended
guest/                   cloud-init, Ignition, declarative files
contexts/                bounded container build contexts
docs/                    author documentation
```

`bundle.yaml` records format version, entrypoint, every regular file path/size/SHA-256/media type, total expanded bytes, and optional source provenance. Paths are UTF-8, relative, slash-separated, NFC-normalized, and may not be empty, absolute, contain `..`, collide by case-folding, or point through symlinks/hardlinks/devices. Upload streams to quarantine, enforces compressed/expanded/file-count/ratio limits, hashes while reading, validates the manifest, then atomically publishes by digest. Large VM images are forbidden; build contexts have separate policy and sandbox execution.

## Inputs, references, and composition

Parameters are typed `string`, `integer`, `number`, `boolean`, `enum`, `cidr`, `ip`, `duration`, or `secretRef`, with required/default and type-appropriate constraints. Defaults are literals, not expressions. Secret values never enter expression bindings.

References use explicit typed strings such as `networkRef: networks.red`, `imageRef: targetImage:debian-12`, `artifactRef: bundle://guest/router.yaml`, `secretRef: {source, name, key}`. Output expressions use the constrained observed-field namespace. Ambiguous/unresolved references fail before graph construction.

Components are named typed resource fragments with declared parameters. Composition is acyclic, lexical, and cannot override generated IDs. `forEach` accepts only a bounded list/map expression; map keys are sorted canonically and become stable identity keys. `when` must evaluate to Boolean. Explicit `dependsOn` may only reference a resolved resource and adds edges.

## Provider overrides

```yaml
providerOverrides:
  proxmox:
    apiVersion: provider.labcentral.io/proxmox/v1alpha1
    spec:
      storagePool: fast-zfs
```

Core validates envelope/name/version, records a portability warning, preserves canonical JSON bytes, and routes it only to the matching plugin. The plugin advertises a schema ID/digest/URL-safe inline schema and performs Stage 2 validation. Unknown or mismatched override namespaces fail. Overrides cannot weaken core security policy or portable requested isolation.

## Revision and instance identity

A revision digest covers the verified bundle manifest and file digests. Instance canonical identity covers rendered portable spec, selected policy inputs, materialized non-secret parameter values, provider override bytes, and canonical format version—but not secret values or observed/provider state. SecretRef identity is included. Clones point to source instance/snapshot provenance and receive fresh resource IDs.

## Authoring requirements

The optional requirements block adds capability and isolation constraints; provider
validation already checks resource kinds and fields. See the
[author explanation and examples](../users/templates.md). Published revisions
retain their immutable requirements. No automatic removal weakens an existing lab.
