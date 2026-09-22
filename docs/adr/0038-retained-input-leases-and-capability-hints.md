# ADR-0038: Retain input deadlines and keep capability hints read-only

- **Status:** Accepted
- **Date:** 2026-09-21
- **Supersedes:** ADR-0037 only for unchanged driver observations; ADR-0035 only for UI capability hints
- **Superseded by:** ; ADR-0039 (credential replacement during durable renewal only)

A current driver can retain its existing input fence and deadline while more
than half the five-second input lease remains and the authorization lease also
has more than half its lifetime remaining. Each callback still rechecks current
authority in one snapshot. The returned remaining input duration never extends
either deadline. A due renewal or pending handoff uses the existing durable
transaction; expired input still requires a new explicit request. This reduces
driver write frequency without lengthening fail-closed or handoff bounds.

Project capability listings are read models. Authorize their project read, then
compute hints from the current evaluator without recording successful or missing
hypothetical actions as execution attempts. Actual execution always authorizes
and audits again. A denied project read remains audited. The topology's access
hint uses `accessSession.read`, with the same access ownership and target rules.

These changes address writer contention under classroom load. They do not relax
SQLite FULL durability or qualify shared rotating disks for arbitrary clone
loads. Operators must measure storage contention and retain provisioning limits
within the provider's bounded task polling window. Public and plugin schemas and
provider/access plugin binaries remain unchanged.
