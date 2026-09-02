# ADR-0005: Typed YAML, bounded CEL, and canonical JSON

- **Status:** Accepted
- **Date:** 2026-09-02
- **Supersedes:** None
- **Superseded by:** None

## Context and constraints

Authors need parameters, conditions, repetition, and reusable components without executable template code. Revisions and plans need stable identity.

## Considered options

1. Go templates/JavaScript: expressive but unsafe and nondeterministic.
2. CUE as the required authoring language: strong but raises the beginner barrier.
3. Typed YAML structure with explicit `when`/`forEach` and a restricted CEL environment.

## Decision

Use option 3. Parse YAML into a node tree with duplicate-key rejection and limits; validate the authoring schema; type-check CEL against declared inputs/loop variables only; expand named components, loops, and conditions in documented order; resolve references; validate typed domain values; emit canonical JSON using RFC 8785/JCS-compatible ordering/number/string rules; hash `canonicalFormatVersion || 0x00 || canonicalBytes` with SHA-256.

Expressions have no I/O, clock, randomness, reflection, user functions, or secret values. Evaluation uses AST-depth, collection-size, expansion-count, and cost limits. Output resource IDs derive from declared names plus stable iteration keys, never list positions unless explicitly selected.

## Consequences

YAML syntax remains approachable while canonical output is unambiguous. CUE may later compile into the same authoring schema, never replace the canonical model.

## Security implications

Limits cover YAML alias bombs, expression resource exhaustion, expansion bombs, and secret interpolation. Bundle paths are normalized before rendering.

## Compatibility implications

Authoring schema, renderer semantics, CEL environment, and canonical format have independent versions. Stored canonical bytes are never silently re-rendered under new semantics.
