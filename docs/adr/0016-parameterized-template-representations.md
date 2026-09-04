# ADR-0016: Parameterized template representations

- **Status:** Accepted
- **Date:** 2026-09-04
- **Supersedes:** ADR-0012 (template representation only)
- **Superseded by:** None

## Decision

Retain ADR-0012's independent compatibility domains. A reusable revision stores its original source digest, canonical authoring representation, representation digest/version, and required renderer semantics. Publication validates syntax, authoring schema and parameter declarations without requiring participant input values. Full expression, graph, resource, capability and policy validation occurs when previewing or instantiating with typed inputs.

`template.labcentral.io/v1` identifies the canonical authoring representation; its digest is not a resolved instance digest. Existing `canonicalVersion`/`canonicalDigest` metadata names continue to describe the explicitly versioned representation. Instances retain canonical resolved bytes and their distinct digest. An additive database migration adds immutable representation JSON, preserving prior revisions and migration checksums.

Reading source/revision metadata does not invoke the current renderer. New representations are verified against their stored digest and source. Instantiation selects the revision's explicitly supported renderer semantics; unsupported versions return a compatibility error and never silently rerender using a different version. Product upgrades do not bump renderer semantics unless behavior changes. Changing semantics requires preserving the old implementation or explicit migration to a new revision.

Typed inputs are an additive HTTP request field. They participate in immutable instance identity and idempotency; different values cannot replay another request. Participant secret creation remains outside this change. Legacy revisions retain their original identity and verification rules; existing instances continue from persisted canonical bytes and provider plans.

## Validation

Publish a required-input template, instantiate it with distinct values, reject mismatched/extra fields and incompatible renderer versions, reopen stored representations across product upgrades, and verify SQLite/PostgreSQL migration and replay parity.
