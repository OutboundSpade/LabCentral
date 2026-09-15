# ADR-0020: Delegated review for the classroom operations update

- **Status:** Accepted
- **Date:** 2026-09-07
- **Supersedes:** ADR-0018 (delegated task scope only)
- **Superseded by:** None

## Decision

The owner's existing explicit authorization to execute, review and sign on
their behalf continues for TASK-TESTBED-002, the requested follow-up to
TASK-TESTBED-001 on the same private PVE1 target. This permits the implementation
agent to qualify the changed provider and review this UI/operations deployment.
All ADR-0018 technical gates, exact build/configuration/inventory bindings,
protected off-runtime signing, short validity, ownership checks and production
review boundaries remain unchanged. This is owner-delegated review, never
independent human review or production approval. No runtime contract changes.

Existing user labs and enrollment links must be preserved. Qualification uses
explicit planning exclusions to keep its temporary VM and VLAN claims separate
from user allocations, with exact expected-claim assertions before mutation.
The tested configuration remains unchanged. Evidence for the earlier accepted
build remains historical and is not overwritten. The previous one-hour soak is
not asserted for this revision; this update requires focused live measurements,
console controls, bulk deletion, cleanup and retained-user-lab checks.
