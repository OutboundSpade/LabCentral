# Open questions

Temporary defaults let design and implementation proceed. Changing one after an accepted contract ships may require an ADR.

## OQ-GUEST-001 — Windows guest timing

Windows affects images, licensing, sysprep, Cloudbase-Init, RDP, password rotation, and snapshot testing. Options: (A) first production release parity; (B) preserve types but deliver after Proxmox/Linux path; (C) omit indefinitely. **Recommendation/default: B.** v1 schemas allow `osFamily: windows`, but Phase 2 acceptance is Linux-only and must return a precise provider/policy error for unavailable provisioning.

## OQ-IMAGE-001 — trusted external image sources

Automatic import creates SSRF, provenance, licensing, and supply-chain exposure. Options: (A) arbitrary URL; (B) operator allowlist plus digest/signature policy; (C) target inventory only. **Recommendation: B; temporary default: C.** No remote import is enabled until an allowlist, digest requirement, size limits, and isolated fetcher are implemented.

## OQ-SECRET-001 — participant-supplied guest secrets

This affects UI handling, audit redaction, retention, ownership, and cloning. Options: (A) plaintext instance input; (B) create scoped secret then pass `SecretRef`; (C) admin-only secrets. **Recommendation: B; temporary default: C.** Schemas accept only refs, and participant secret creation remains disabled.

## OQ-ACCESS-001 — first browser console path (resolved)

Options considered were (A) a control-plane WebSocket proxy, (B) a separate access plugin/proxy service, and (C) provider-native URLs only. **Resolution: B.** `TASK-PROD-003` fixes the separately supervised access process as the production boundary, beginning with Proxmox noVNC and browser SSH only where an image trusts the configured SSH user CA. The HTTP API creates and authorizes Access Sessions; the plugin exchanges one-time tokens with core and periodically rechecks a short authorization lease. Provider-native management URLs, tickets, and credentials are never returned to the browser. The stable ID is retained for traceability and is no longer an open design choice.

## OQ-QUOTA-001 — enforceable cloud-spend limits

Providers expose delayed/inexact cost data. Options: (A) hard currency limits; (B) estimated budget guardrails; (C) resource quotas only. **Recommendation: B eventually; temporary default: C.** Cost hints inform scheduling but are not advertised as hard enforcement.

The provider-extension scope, substantial simplification, typed template semantics and cleanup-authority review are resolved by owner approval and ADR-0015 through ADR-0017. They are not additional open questions. The four existing open questions and production qualification gates remain unchanged.

The owner selected Tailscale and approved the native state/UID migration on
2026-09-05. Remaining site inputs (backup destination, off-host recovery-key
custody, HTTPS enablement and release approval controls) are tracked in the
[deployment record](../operations/pve1-pilot.md) under `TASK-PROD-005`.
That deployment preference does not waive the existing scale or failover gates.

On 2026-09-07 the owner approved a separate private classroom testbed on PVE1,
OIDC provider as the example OIDC provider, and ephemeral labs with no scheduled
backup or off-host recovery infrastructure. These choices are accepted in
[classroom-ui.md](classroom-ui.md); they are not new open questions. Tailnet enrollment and instructor passkey enrollment are complete. Student
passkey enrollment and acceptance results are tracked in
[the testbed runbook](../operations/classroom-testbed.md). Production recovery
requirements above are separate from this disposable testbed milestone.

The classroom image and isolated vmbr4 bootstrap checks are now measured. The
owner delegated review and signing to the implementation agent under ADR-0018.
Actual T3 execution, reviewed signing and runtime activation are complete.
ADR-0019 records the verified peer-pinned Tailscale console transport. The
25-seat one-hour console soak, access expiry/revocation, control-VM restart,
student workflow and independent cleanup have passed. TASK-TESTBED-001 is
complete. No reviewer identity, deployment permission or VM-login input is
pending; students enroll passkeys using their individual private links.

On 2026-09-15 the owner chose catalogue-only container examples on PVE1.
ADR-0021 records bounded independent deployments; the testbed limit is two.
Within-lab parallel branches remain an implementation extension requiring
multi-invocation recovery. No additional deployment or container-provisioning
approval is pending. Production validation gates are unchanged.
