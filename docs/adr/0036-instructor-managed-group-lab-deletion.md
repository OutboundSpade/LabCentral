# ADR-0036: Instructor-managed group lab deletion

- **Status:** Accepted
- **Date:** 2026-09-21
- **Supersedes:** ADR-0029 (participant destruction of team-owned labs only)
- **Superseded by:** None

## Decision

A participant assignment never authorizes `instance.destroy` for a team-owned
lab. Group or organizational team membership grants shared access, not deletion.
A separate instructor or platform-administrator assignment may still authorize
that action under the existing scope and target-grant rules. Participant-owned
personal lab deletion, group enrollment, consoles and other permissions retain
their existing behavior. Automatic expiration and operator cleanup remain intact.

Core authorization enforces this for direct API and feature-bridge requests.
The destroy transaction rechecks elevated authority for team-owned labs so role
changes cannot fall back to participant ownership.
The UI hides the corresponding group deletion controls from students. No schema,
provider protocol or persistence migration changes are required. This intentionally
removes an earlier participant permission; existing labs keep their ownership.

## Validation

Check denial for group members, retained personal deletion, elevated assignments
including users with both roles, and student/instructor UI behavior. Reuse the
SQLite/PostgreSQL enrollment fixture for the membership-backed denial.
