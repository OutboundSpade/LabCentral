# ADR-0008: Secret references and least-privilege trust boundaries

- **Status:** Accepted
- **Date:** 2026-09-02
- **Supersedes:** None
- **Superseded by:** None

## Context and constraints

LabCentral handles high-value provider/OIDC credentials while processing malicious bundles and calling potentially malicious plugins. It is not a general secrets manager.

## Considered options

1. Store encrypted values in the main database by default.
2. References to environment/file sources locally and external secret providers in production.
3. Pass operator credentials directly to plugins at registration.

## Decision

Use option 2. The core stores typed `SecretRef` metadata and resolves values just in time. Local sources are process environment or root-readable mounted files. Production adapters may use external systems. Invocation-scoped delivery is target/plugin-specific and values are cleared when practical. Provider-state, canonical specs, events, audit, and logs prohibit secret values.

## Consequences

Backup of the relational database alone does not back up external secrets. Rotation updates references/source values without rewriting templates; in-flight calls retain only their invocation snapshot.

## Security implications

Use separate provider credentials per target with the minimum API permissions, distinct OS identities for plugins, mandatory redaction at structured-log boundaries, and metadata access control. An optional built-in encrypted store needs a future ADR covering KEK custody, envelope encryption, rotation, restore, and memory limits.

## Compatibility implications

Secret source type/version is separate from `SecretRef`; unknown sources fail closed.
