# ADR-0018: Scoped provenance and owner-delegated testbed review

- **Status:** Accepted
- **Date:** 2026-09-07
- **Supersedes:** ADR-0017
- **Superseded by:** ADR-0020 (delegated task scope only); ADR-0021 (journal-wide execution serialization only)

## Decision

Retain signed exact-target qualification for admission and ordinary mutation, fresh inventory/profile checks, active reviewer trust, and ownership fencing. Release signatures establish software provenance; target qualification establishes reviewed deployment eligibility. The provider source digest covers its actual repository dependency files and module locks, not unrelated product documentation/UI. Binary identity remains exact and every behavior-changing provider build still requires qualification.

A separately signed `labcentral.proxmox-cleanup/v1` grant may authorize only destroy of one already-owned VM bound to an exact installation/target/configuration/inventory, provider binary, instance, logical resource, and ownership-token digest. It expires within 24 hours, uses a currently active non-revoked reviewer key, and cannot authorize creation, access, lifecycle changes, different ownership, or a different target. A grant is independently reviewed off-host, loaded explicitly, and retained with the existing audited destroy operation/invocation. Revoked keys and ambiguous ownership still fail closed. The initial implementation accepts one grant per process deployment; network claim cleanup still requires ordinary qualification. Provider deletion must independently verify persisted state, current ownership, committed claims, journal fencing, and target absence before claim release.

Missing or expired normal qualification never implies cleanup permission. Operators can issue this narrow grant before qualification expiry or during an incident. The runtime never signs or renews its own authority. Existing deployments without cleanup grants retain ADR-0014 behavior. A new source-digest calculation requires a fresh qualification artifact; old artifacts are not silently relabeled.

## Execution topology

Core replica count and target execution concurrency are separate facts. The original Proxmox target journal retained one conservative mutation owner at a time. ADR-0021 supersedes that serialization with bounded independent deployments and conflicting-claim locks. Its shared-volume deployment requires cross-node locking and failover qualification. Independent providers own their journals and state contracts; they do not inherit a universal RWX requirement. A future single-active target executor needs its own measured failover design; it is not necessary to install external providers now.

## Validation

Reject expired/future grants, revoked signers, modified signatures, foreign resources/ownership, wrong actions/builds/configuration/inventory, and unqualified creation. Verify a permitted exact-resource cleanup still uses the existing owned reverse-destroy path and durable core audit. Keep live infrastructure qualification gaps explicitly open.

## Owner-delegated private testbed review

For TASK-TESTBED-001 only, the owner explicitly authorized the implementation
agent to perform the review and sign on the owner's behalf on 2026-09-07.
The same operator may therefore prepare, execute, review and sign the evidence
for `target.classroom-test` / `installation.classroom-test` on PVE1. This
replaces the separate-person requirement for this disposable private testbed;
it is not independent human review and must never be described as such.

All technical gates above remain: actual exact-build T3 cases and typed probes,
observed cleanup, target/build/inventory binding, active reviewer trust and
short-lived signatures. The delegated signer operates from the protected
operator workspace, outside core/provider/access services. A dedicated testbed
key stays in a mode-0700 operator directory, is never copied to guests or runtime
services, and authorizes only this target through its explicit trust policy.
No runtime self-signing or self-renewal is introduced. Credentials used by a
bounded qualification runner remain separate from the signing key.

The immutable source snapshot and measured dependency digest identify the
working-tree candidate when no published release exists. A reviewer records
actual case results and limitations before signing; a successful signature is
not permission to fabricate missing evidence. The required 25-lab browser
console soak, expiry and restart/cleanup checks remain acceptance criteria.

Production deployments, other targets and general cleanup-grant approval retain
the independent-review procedure. Machine schemas, protocol, runtime admission,
ownership checks and existing pilot authority are unchanged. This narrower
review separation is an owner-accepted testbed tradeoff, not production approval.
