# ADR-0003: Provider Targets are global in v1

- **Status:** Accepted
- **Date:** 2026-09-02
- **Supersedes:** None
- **Superseded by:** None

## Context and constraints

A target is an administrator-configured provider environment. The model must later allow organization ownership, while initial operations should stay simple.

## Considered options

1. Global targets with scoped grants.
2. Organization-owned targets only.
3. Both ownership modes in v1.

## Decision

Use global targets owned by the platform installation. `ProviderTargetGrant` authorizes an organization/project to schedule on a target and may add quota ceilings and allowed isolation classes. Only Platform/Provider Administrators register targets or manage credentials. Preserve a nullable future `ownerOrganizationID` field but reject it in v1.

## Consequences

Shared classroom infrastructure is simple, and tenant access is explicit. Organization-owned bring-your-own-provider requires a later ADR and data migration.

## Security implications

Listing health/capabilities is grant-scoped; credential metadata is more restricted. A target is never selected solely because it exists.

## Compatibility implications

Stable target IDs and grant objects avoid changing instance bindings when ownership modes expand.
