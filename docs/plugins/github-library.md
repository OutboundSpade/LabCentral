# GitHub template libraries

The independently installed `lab-source-github` source plugin reads a configured
GitHub repository and index. It supports public repositories without credentials
and private repositories through an invocation-scoped token with repository
Contents read permission. It only connects to `https://api.github.com`; redirects,
Git hooks, submodules, symlinks and third-party download URLs are not followed.

Build `./cmd/lab-source-github` and install it as a v2 source manifest with plugin
ID `io.labcentral.source.github`, source kind `github`, protocol 1.0 and separate
plugin-owned state (`--state /absolute/protected/journal`). The usual executable
hash and protected installation rules apply. Its binding configuration is:

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
