# Authentication, tenancy, and RBAC

## Authentication modes

- `none`: one synthetic `local-admin`, loopback bind default, persistent UI/API warning, `--unsafe-listen-non-loopback-without-auth` required otherwise. Reverse-proxy headers are ignored.
- `local`: Argon2id password hashes with per-user salt and versioned parameters; rate limiting, secure recovery/bootstrap, session rotation, optional later MFA.
- `oidc`: authorization-code + PKCE, exact issuer/audience/nonce/state checks, configured claim mapping, key rotation, and local membership/role assignments. OIDC authenticates; it does not automatically grant platform admin.

## Scope model

Organization contains Projects; Teams belong to an Organization and may be granted to Projects. Users join through Memberships. Role assignments bind subject (user/team) to scope (platform/organization/project/team). Instances have one owner (`user|team|project`) and optional explicit shares. Provider Targets are global and usable only through target grants (ADR-0003).

## Roles and principal permissions

| Role | Principal actions |
|---|---|
| Platform Administrator | all platform actions, auth mode, global policy, provider admins |
| Organization Administrator | org/projects/teams/memberships/quotas/templates and org audit; no provider credential values |
| Provider Administrator | register/configure/health/credential refs/target grants; no implicit lab console access |
| Lab Author | create/edit templates, upload bundles, publish revisions within granted project |
| Instructor / Lab Operator | validate/plan/batch-create, lifecycle/TTL/snapshot/reset/destroy shared class labs |
| Participant / Student | create within grants/quotas, view/use own or team labs, allowed lifecycle/access actions |
| Viewer / Auditor | read permitted resources/events/audit; no access session unless separately granted |

Fine-grained stable actions include `providerTarget.register|configure|health.read|grant`, `template.create|revision.publish`, `instance.create|batchCreate|read|start|stop|reset|ttl.extend|snapshot|restore|clone|destroy`, `accessSession.create|revoke`, `quota.manage`, `membership.manage`, `audit.read`.

Authorization evaluates actor, action, resource/scope ancestry, membership validity, instance ownership/shares, target grant, quota/policy constraints, and sensitive subfield. List endpoints apply the identical predicate in query filters and cannot reveal existence across tenants. Operations capture the actor and reauthorize at execution for destructive/credential-sensitive steps; loss of permission stops at a safe boundary.

## Classroom isolation

Batch creation expands one request into independent instances owned by each user/team, with shared revision but fresh logical IDs/secrets. Participants cannot enumerate or access other teams’ instances. Instructors receive explicit project-scope operator rights. Snapshots/clones preserve access policy by rule, not by copying arbitrary ACL rows; a clone receives a new owner selection authorized at creation.

