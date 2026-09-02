# Open questions

Temporary defaults let design and implementation proceed. Changing one after an accepted contract ships may require an ADR.

## OQ-GUEST-001 — Windows guest timing

Windows affects images, licensing, sysprep, Cloudbase-Init, RDP, password rotation, and snapshot testing. Options: (A) first production release parity; (B) preserve types but deliver after Proxmox/Linux path; (C) omit indefinitely. **Recommendation/default: B.** v1 schemas allow `osFamily: windows`, but Phase 2 acceptance is Linux-only and must return a precise provider/policy error for unavailable provisioning.

## OQ-IMAGE-001 — trusted external image sources

Automatic import creates SSRF, provenance, licensing, and supply-chain exposure. Options: (A) arbitrary URL; (B) operator allowlist plus digest/signature policy; (C) target inventory only. **Recommendation: B; temporary default: C.** No remote import is enabled until an allowlist, digest requirement, size limits, and isolated fetcher are implemented.

## OQ-SECRET-001 — participant-supplied guest secrets

This affects UI handling, audit redaction, retention, ownership, and cloning. Options: (A) plaintext instance input; (B) create scoped secret then pass `SecretRef`; (C) admin-only secrets. **Recommendation: B; temporary default: C.** Schemas accept only refs, and participant secret creation remains disabled.

## OQ-ACCESS-001 — first browser console path

Options: (A) control-plane WebSocket proxy; (B) separate access plugin/proxy service; (C) provider-native URL only. **Recommendation/default: B**, beginning with SSH/browser terminal and Proxmox noVNC adapters. The HTTP API still creates Access Sessions; provider-native URLs are allowed only when short-lived and audience-scoped.

## OQ-QUOTA-001 — enforceable cloud-spend limits

Providers expose delayed/inexact cost data. Options: (A) hard currency limits; (B) estimated budget guardrails; (C) resource quotas only. **Recommendation: B eventually; temporary default: C.** Cost hints inform scheduling but are not advertised as hard enforcement.

