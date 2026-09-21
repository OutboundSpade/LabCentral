# GitHub template libraries

The independently installed `lab-source-github` source plugin reads a configured
GitHub repository and index. It supports public repositories without credentials
and private repositories through an invocation-scoped token with repository
Contents read permission. It only connects to `https://api.github.com`; redirects,
Git hooks, submodules, symlinks and third-party download URLs are not followed.

Build `./cmd/lab-source-github` and install it as a v2 source manifest with plugin
ID `io.labcentral.source.github`, source kind `github`, protocol 1.0 and separate
plugin-owned state (`--state /absolute/protected/journal`). The usual executable
hash and protected installation rules apply. The manifest must include
[`github-source-binding-v1.schema.json`](../../schemas/github-source-binding-v1.schema.json)
as its `configurationSchema`; core rejects nonempty bindings without a declared
schema. Release packaging includes this schema. Its binding configuration is:

```json
{
  "owner": "example-organization",
  "repository": "lab-templates",
  "reference": "main",
  "index": "examples/template-library/index.yaml"
}
```

Bind it to explicit projects. If required, set the binding's `credentialFile` to
a protected file containing the token. Never place it in configuration, the
repository or a template archive. The declared reference must match the binding;
a browser cannot redirect the plugin to another repository or reference.

For a containerized core with an attached native source service, mount the
credential file read-only at the exact `credentialFile` path inside the core
container. Make it readable only by the core service identity; core passes it to
the source plugin for the invocation. The plugin registration token and GitHub
repository credential are separate credentials. Prefer a repository-scoped token
with Contents read permission; an operator's GitHub CLI credential may have
broader access and depends on that account's continued authorization.

The source service needs DNS through the configured resolver and outbound HTTPS
to `api.github.com`. A dormant service restricted to loopback cannot synchronize.
Keep core/provider/access network restrictions separate. If using an IP allowlist,
retrieve current `api` ranges from GitHub's `/meta` endpoint, allow TCP 443, and
refresh the operator-managed rules when those ranges change. Keep actual ranges,
credential locations and installation-specific rules in private operator records.

An index uses `labcentral.library/v1`, with an explicit `templates` array (including
an empty array when intentionally removing all entries). Each entry has a stable
`key`, display `name`, optional `description`, and source `root` relative to the
index directory. Every root contains a locked portable `bundle.yaml`. See the
[indexed example](../../examples/template-library/index.yaml) and
[source packaging instructions](portable-templates.md). The resolver validates
all files and vendored dependency locks; it never refreshes stale locks silently.
A collection is limited to 32 templates and 64 MiB total packaged source.

Create a project binding with `POST /api/v1/template-libraries` and a caller-owned
`Idempotency-Key`. The body contains `projectId`, `binding` (`INSTANCE/BINDING`),
`reference` and optional `intervalSeconds` (default 900, bounded 60–86400). Listing,
entries and history use `/template-libraries`, `/{id}/entries` and `/{id}/history`,
with an explicit `projectId` query. `POST /{id}/sync` queues Sync now;
`POST /{id}/schedule` with `{paused: true|false}` controls scheduled polling.

Each invocation persists one resolved Git commit before retrieving the collection.
A restart resumes that commit. Core verifies every source before staging any
revision and publishes the complete update in one fenced transaction. Concurrent
manual/scheduled requests share one active library operation. Equal source bytes
reuse retained revisions, even if the upstream commit changes. Removed entries
become unavailable upstream; their revisions and running labs remain. Synchronized
templates reject local updates: export/import into a newly created template for
an independent copy. Synchronization publishes source only and never starts builds.

The recorded author's current authority is checked before queuing and again at
publication, including within the database transaction. Revocation pauses the
schedule. Another administrator triggering Sync now does not lend their authority
to the recorded author. Transient invocation failures retry the same invocation
with bounded exponential backoff. Invalid source produces a failed history entry.
Credentials and transfer tokens are excluded from durable core results.

The plugin keeps immutable source in a private content-addressed cache, bounded to
1 GiB, and invocation journals bounded to 16,384 records. Cache files older than
24 hours may be removed when reserving new work. Journals retain the pinned commit;
recovery refetches that same commit if cached bytes were evicted. A full journal
stops new resolution. Operators can archive terminal invocation records after
confirming core no longer needs their recovery; never remove unfinished journals.
The plugin does not read the control-plane database or artifact directory.

The optional `source-collection-v1` capability adds `ResolveCollection` and a typed
collection result to source recovery. The GitHub package provides this collection
workflow; existing single-bundle source plugins retain `Resolve` unchanged.

## Initialize a private library

Create the repository with `gh repo create ORGANIZATION/REPOSITORY --private`,
then commit a root `index.yaml` on the configured branch:

```yaml
apiVersion: labcentral.library/v1
templates: []
```

Check the organization's base repository permission as well as team and individual
grants. Private visibility still allows organization members to read the source
when the base permission is Read; a repository role cannot lower that inherited
permission. LabCentral's author-only source export does not change GitHub access.
Keep instructor solutions outside the repository and exported bundles, and use
an organization access policy appropriate for everyone who can inspect build source.

An empty index is a valid starting library. Add locked portable template roots and
stable entries as meeting labs are authored; an existing template's portable
export can supply the source files. Configure the installed source binding before
adding the library under Template Library's **Libraries** tab. Select the source,
enter its configured branch and use the default fifteen-minute interval.

Verify a successful initial scheduled synchronization and **Sync now** in the
history, including the expected commit. Confirm that polling is enabled and that
the next poll is scheduled. An empty-index check verifies private transport and
publication setup; importing real entries still requires valid portable packages.
