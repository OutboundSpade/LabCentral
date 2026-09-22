# ADR-0029: Project enrollment and shared console control

- **Status:** Accepted
- **Date:** 2026-09-18
- **Supersedes:** ADR-0010 (self-service project team relationship); ADR-0024 (shared console driver leases)
- **Superseded by:** ADR-0034 and [ADR-0036](0036-instructor-managed-group-lab-deletion.md) (only the scopes explicitly identified there)

## Decision

Administrators create project collaboration groups individually or in bounded batches and control enrollment. Eligible project users join or leave themselves, with at most one active self-service group per user per project enforced transactionally. These relationships confer team ownership access only within that project; they never grant roles, organization membership or cross-project authority. Existing administrator-managed organizational teams and memberships retain their semantics.

Open groups accept new members while labs run; new members may access existing group labs. Any member may launch and perform otherwise-authorized lifecycle operations. Ownership stays with the team and audit retains the actor. Multiple labs are subject to ordinary quotas. Leaving removes new access immediately and revokes active access within existing closure bounds; even the last member leaving never deletes or transfers a lab. Closing enrollment does not remove members. Sensitive execution and admission recheck membership.

Each console participant has an independently authenticated access session viewing the same VM. Exactly one session drives each VM resource generation. Take control requests transfer without the former driver's approval; release, disconnect, departure, revocation and expiry release control. All reconnects begin without control. Presence exposes only authorized participant display information.

Core owns a durable fenced driver lease bound to instance, resource, generation and access session. A new driver activates only after the prior input lease is withdrawn or expires. The access plugin enforces input authority, including keyboard, pointer, clipboard writes and other guest-changing RFB messages; browser viewOnly is not an enforcement boundary. Upstream connections are forced shared. Unknown client message types fail closed; bounds apply to parsing and buffering. Core/plugin outages preserve authorization expiry and cannot produce two simultaneous drivers. Handoffs are audited without screen or keystroke recording.

## Compatibility and validation

Preserve old wire fields, immutable revision identities, provider state and checksummed migrations. Use additive migrations and negotiated contracts. Test authorization, crash recovery and SQLite/PostgreSQL parity at changed boundaries. See [implementation contract](../design/portable-template-collaboration.md) and the dependency-ordered tasks in `design/checklist.yaml`.
