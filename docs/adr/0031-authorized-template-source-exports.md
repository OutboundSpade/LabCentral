# ADR-0031: Author privileges for template source export

- **Status:** Accepted
- **Date:** 2026-09-20
- **Supersedes:** ADR-0027 (source-export authorization only)
- **Superseded by:** None

## Context

Participants need published template metadata, topology and launch access. A
portable source archive additionally contains image construction scripts and
complete application configuration. Project template-read authority previously
allowed participants to download that archive directly through the API, even
when authoring pages and the upstream repository were private. This can disclose
instructor-authored setup details that are deliberately absent from student
handover material.

## Decision

Exporting any template's full source requires the existing project template
revision publication authority. Core/API authorization enforces this requirement;
the feature bridge declares `templates.write` for `templates.export` accordingly.
No role or database migration is necessary. Administrators and
authorized template authors retain source export. An instructor with only
classroom management authority needs an author role to export source. Participants retain ordinary
template reads, topology/content summaries, published-template preparation,
launch and access to their authorized running labs.

Instructor guides, answer keys and privileged verification harnesses remain
outside exported bundles and guest filesystems regardless of the export policy.
Student guest administration necessarily allows inspection of installed guest
applications and configurations; the export boundary does not conceal those
systems from their administrators.

## Compatibility and verification

Archive bytes, package schemas and HTTP request/response shapes are unchanged.
This intentionally tightens source-export authorization: a caller with only
`template.read` now receives 403 from the export endpoint. Update API/feature
permission documentation and test participant rejection plus unchanged author
round trips. Existing revisions remain immutable and launch permissions remain
unchanged. UI visibility is not an authorization boundary.
