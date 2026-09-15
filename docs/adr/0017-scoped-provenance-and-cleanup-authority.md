# ADR-0017: Scoped provenance and separate cleanup authority

- **Status:** Superseded
- **Date:** 2026-09-04
- **Supersedes:** ADR-0014
- **Superseded by:** ADR-0018

## Decision

Retain signed exact-target qualification for admission and ordinary mutation, fresh inventory/profile checks, active reviewer trust, and ownership fencing. Release signatures establish software provenance; target qualification establishes reviewed deployment eligibility. The provider source digest covers its actual repository dependency files and module locks, not unrelated product documentation/UI. Binary identity remains exact and every behavior-changing provider build still requires qualification.

A separately signed `labcentral.proxmox-cleanup/v1` grant may authorize only destroy of one already-owned VM bound to an exact installation/target/configuration/inventory, provider binary, instance, logical resource, and ownership-token digest. It expires within 24 hours, uses a currently active non-revoked reviewer key, and cannot authorize creation, access, lifecycle changes, different ownership, or a different target. A grant is independently reviewed off-host, loaded explicitly, and retained with the existing audited destroy operation/invocation. Revoked keys and ambiguous ownership still fail closed. The initial implementation accepts one grant per process deployment; network claim cleanup still requires ordinary qualification. Provider deletion must independently verify persisted state, current ownership, committed claims, journal fencing, and target absence before claim release.

Missing or expired normal qualification never implies cleanup permission. Operators can issue this narrow grant before qualification expiry or during an incident. The runtime never signs or renews its own authority. Existing deployments without cleanup grants retain ADR-0014 behavior. A new source-digest calculation requires a fresh qualification artifact; old artifacts are not silently relabeled.

## Execution topology

Core replica count and target execution concurrency are separate facts. The Proxmox target journal retains one conservative mutation owner at a time. Its shared-volume deployment requires cross-node locking and failover qualification. Independent providers own their journals and state contracts; they do not inherit a universal RWX requirement. A future single-active target executor needs its own measured failover design; it is not necessary to install external providers now.

## Validation

Reject expired/future grants, revoked signers, modified signatures, foreign resources/ownership, wrong actions/builds/configuration/inventory, and unqualified creation. Verify a permitted exact-resource cleanup still uses the existing owned reverse-destroy path and durable core audit. Keep live infrastructure qualification gaps explicitly open.
