# ADR-0012: Independent compatibility domains

- **Status:** Superseded
- **Date:** 2026-09-02
- **Supersedes:** None
- **Superseded by:** ADR-0016

This historical decision is superseded by ADR-0016. Consult the successor for current behavior; the original reasoning is retained below.

## Context and constraints

Monorepo components ship together initially, but third-party plugins, stored revisions, provider state, and remote agents outlive a release.

## Considered options

1. One product SemVer controls every format.
2. Independent explicit versions with product compatibility tables.
3. Date-based implicit compatibility.

## Decision

Use option 2 for HTTP API, Lab schema, bundle manifest, canonical representation, plugin wire protocol, access protocol, capability schema, provider override schema, provider state, database schema, Go SDK, and event envelope. Handshakes exchange supported ranges and feature bits; readers reject unknown major versions and preserve safe unknown additive fields. Product releases publish a tested compatibility matrix and support current plus previous protocol major during a documented deprecation window where feasible.

Template revisions store original bundle digest, renderer/canonical versions, and canonical bytes. Migrations create new representations with provenance; they never rewrite immutable revision identity. Plugin upgrade performs protocol negotiation, state readability checks, optional explicit state migration, health verification, then drain/switch; rollback is blocked after irreversible state migration.

## Consequences

Release work carries compatibility testing, but third-party and persisted-state upgrades become predictable. Breaking changes require an ADR and migration guide.

## Security implications

Unsupported/ambiguous versions fail closed. Downgrade negotiation has configured minimums to prevent downgrade attacks.

## Compatibility implications

Protobuf tags are never reused; JSON fields follow additive/minor and breaking/major rules; capability claims include schema version and expiry.
