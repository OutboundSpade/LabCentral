# Template rendering

## Deterministic pipeline

1. Verify bundle manifest/digest and parse limited YAML.
2. Validate `v1alpha1` author schema; apply literal defaults; validate typed inputs.
3. Compile/type-check CEL against a versioned environment (`params`, component args, current iteration key/value, pure helper subset).
4. Expand named components depth-first with recursion rejection and depth/count limits. In `v1alpha1`, component definitions contain resources but not nested component uses, so recursion is structurally impossible.
5. Expand `forEach` in canonical key order and apply `when`; generate stable qualified names.
6. Resolve typed refs and substitute only explicitly expression-enabled scalar fields.
7. Build typed portable domain objects and infer dependency edges.
8. Detect cycles and validate topology, policy, quotas, and required capabilities.
9. Normalize durations/CIDRs/IPs/enums/units, emit JCS-compatible canonical JSON, and calculate digest.
10. Persist canonical bytes, renderer/environment versions, graph, warnings, and provenance before provider validation.

Limits are server policy and included in validation output: bundle bytes/files, YAML depth/nodes/aliases, CEL AST/cost, component depth, expansion count, resource count, string length, and canonical bytes. Hitting a limit is `invalid_declaration`, never a partial render.

## Schema ownership

```mermaid
flowchart LR
  G[Go domain semantics] --> A[Authoring Go DTOs]
  A --> J[Lab JSON Schema]
  G --> C[Canonical Go types]
  H[Huma HTTP DTOs] --> O[OpenAPI 3.1]
  P[plugin.proto] --> PC[Capability schema]
  PP[Provider implementation] --> OS[Override JSON Schema]
  J -. conformance .-> C
  O -. API mapping tests .-> G
  PC -. match tests .-> G
```

No schema is generated blindly from another: semantic mapping/conformance tests prove alignment. HTTP DTOs include server resources and may differ from author YAML. Canonical types contain no author-only syntax. Plugin capability messages use typed extensible parameters. Override schemas are plugin-owned and content-digested.

## Example canonical fragment

```json
{"apiVersion":"canonical.labcentral.io/v1alpha1","resources":[{"id":"network.app","kind":"Network","spec":{"egress":{"mode":"deny"},"ipam":{"mode":"providerManaged"},"kind":"isolated"}}]}
```

Canonical resources use the stable topological apply order. Ready-node ties sort by resource class (`Network`, `Volume`, `VM`/`Container`, `AccessEndpoint`, `Output`) and then logical resource ID; destroy order is the exact reverse. Map-like lists sort by stable keys, while ordered NICs remain ordered. Numbers outside the typed domain’s permitted IEEE-754/I-JSON range are rejected before canonicalization rather than rounded. JCS preserves Unicode code points and sorts object property names by UTF-16 code units; it does not normalize ordinary string values. Bundle paths are separately NFC-normalized by the bundle contract.

Component arguments are literals or direct lexical bindings to declared root `params.<name>` values and the current iteration variables. Component child names are qualified by the use name and stable iteration key; invalid or oversized keys use a deterministic 128-bit SHA-256 suffix. A `secretRef` component argument may bind only to a declared root secret reference. Only its `{source,name,key}` identity reaches canonical bytes; secret material never enters CEL or canonical output.

The executable golden catalog is [`schemas/fixtures/canonical/vectors.yaml`](../../schemas/fixtures/canonical/vectors.yaml). It covers object order, YAML anchors/aliases, equivalent number forms/negative zero, and Unicode. [`tools/jcs.mjs`](../../tools/jcs.mjs) is a design-contract oracle used only to verify those vectors; the Go implementation must reproduce the bytes/digests independently and pass the same fixtures.
