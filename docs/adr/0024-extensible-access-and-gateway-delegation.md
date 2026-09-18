# ADR-0024: Extensible access and browser-approved gateway delegation

- **Status:** Accepted
- **Date:** 2026-09-17
- **Supersedes:** None
- **Superseded by:** ADR-0029 (only the scope explicitly identified there)

## Decision

Core owns authorization for workload and network access. Registered namespaced methods map to core-known proxied-stream or downloadable-credential classes. Adapter selection uses explicit installation bindings, target, endpoint schema and method; ambiguity fails closed. Existing ssh/vnc requests and stored sessions retain their meaning.

All methods preserve short authorization leases and closure within 30 seconds of the last successful authorization when core is unavailable. Network adapters enforce expiry at the traffic enforcement point. Eventual remote peer deletion alone cannot satisfy this contract. Core persists revocation/recovery intent; expiry is not absence evidence. Download material is delivered once through a non-cacheable authenticated path, never ordinary artifacts, URLs, durable plugin state or logs. Provider mutations remain provider-owned.

Administrator-registered gateways use browser-approved device authorization. Requests expire after five minutes, initial polling is five seconds, and grants expire within 15 minutes with no refresh token. The subject comes from existing authenticated approval. Client, project, actions and resource/template scope constrain every call together with current authority. Revocation and approval are audited. SSH identity enrollment and unattended service accounts are outside this decision.

## Compatibility and validation

Preserve prior wire fields, checksummed migrations and immutable revision identities. Negotiate optional new services independently. Validate independent installation against unchanged core/UI artifacts, current authorization, restart recovery, SQLite/PostgreSQL parity and browser isolation. Installation compatibility never establishes target qualification.
