# Project groups

Open **Teams** in the selected workspace to join or leave a group, see its labs,
and prepare a team-owned deployment. Administrators can create groups and open or
close enrollment on the same page. Teams is installed independently of Classroom;
see [feature packages](../plugins/feature-packages.md) for setup.

Project groups use the versioned `labcentral.project-group/v1` API representation.
They are project-scoped collaboration teams, separate from administrator-managed
organizational memberships. The Teams feature package uses these core APIs; the
Classroom package is not required.

- `GET /api/v1/projects/{projectId}/groups` lists groups for a caller with project
  access, including enrollment state, member count and the caller's `joined`
  relationship. Use `limit` (1–200, default 100) and the returned `nextCursor` as
  `after` to paginate.
- `POST /api/v1/projects/{projectId}/groups` creates 1–100 groups atomically. Supply
  distinct display `names`, `enrollmentOpen` and an `Idempotency-Key`. Existing
  platform/organization administrator permissions govern creation.
- `PUT /api/v1/projects/{projectId}/groups/{id}/enrollment` accepts `{"open":false}`
  or `{"open":true}`, `If-Match` and `Idempotency-Key`. Closing prevents new joins;
  existing members retain their relationship and labs.
- `POST /api/v1/projects/{projectId}/groups/{id}/join` and `/leave` act only on the
  authenticated caller. Both require `Idempotency-Key`. Repeated join/leave is
  harmless; a user cannot join a second group in the same project until leaving
  the first. A conflicting or closed enrollment returns HTTP 409.

Joining grants team ownership access inside the exact project. It grants no roles
or organization membership: a viewer remains a viewer. Legacy membership APIs
cannot enroll users in project groups. Group IDs cannot own labs in another
project, including another project in the same organization.

Use single-deployment preparation version 2 with
`owner: {"kind":"team","id":"<group-id>"}` to prepare a published template for
an enrolled member. It does not require instructor batch permissions. The core
rechecks current enrollment during preparation and atomic admission. Labs retain
the team owner and initiating audit actor; multiple labs remain subject to quotas.

Leaving removes the relationship transactionally and revokes the caller's
pending, ready and active console sessions for that team's labs in the project.
The existing closure worker and fail-closed lease expiry deliver bounded closure.
Leaving does not delete or transfer any lab, even for the last member. Existing
expiration and administrator cleanup continue. Replaying an older successful join
after departure returns its recorded result without re-enrolling; refresh the
list to get current membership.

Persistence adds migration `0020_project_groups`; existing migration checksums,
team memberships and role identifiers are unchanged. Enrollment and administrative
changes are audited. See [ADR-0029](../adr/0029-project-groups-and-shared-console-control.md).

In Teams, **Your team** shows your collaborators and active shared labs. Use
**Groups** to find and join an open group. Administrators use **Manage** to inspect
any project's group, view its members and labs, and change enrollment without
joining it. Member rosters are visible only to that group's current members and
project administrators. Leaving removes your roster and lab access immediately.
