# ADR-0007: Typed logical resources are orchestration boundaries

- **Status:** Accepted
- **Date:** 2026-09-02
- **Supersedes:** None
- **Superseded by:** None

## Context and constraints

Providers may expand one portable declaration into many primitives. Users need portable status and safe partial recovery.

## Considered options

1. One generic resource map: weak validation and provider-defined semantics.
2. Track every provider primitive in core: leaks providers into the domain.
3. Typed logical resources with opaque, versioned provider state.

## Decision

Use option 3. First-class author resources are `Network`, `Volume`, `VM`, `Container`, `AccessEndpoint`, and `Output`. Images, artifacts, secrets, NICs, mounts, published ports, and provisioning methods are typed references/subresources because their lifecycle is owned by another resource or external inventory. Each logical resource is the normal identity, lock, status, retry, and compensation boundary. Plugins own physical decomposition and return ownership IDs plus opaque state metadata.

## Consequences

Failures clean partial primitives of the failing logical resource but retain earlier successful resources. Destroy continues in reverse graph order and reports leftovers. A later namespaced extension-resource API is separate from portable v1.

## Security implications

Deletion requires installation/target/instance/resource ownership identity match. Provider-state blobs may not contain reusable plaintext credentials and are size-limited/encrypted-at-rest where configured.

## Compatibility implications

Provider state records plugin identity/version/schema and migration need. Core schema additions follow Lab versioning.
