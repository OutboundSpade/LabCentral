# Installing feature packages

Template Library, Teams, and Classroom are separate browser packages. Core retains
lab management, access, authorization, project administration, and the product API.
Teams and shared VM consoles work with Classroom absent.

The native distribution includes `features/standard.installation.json`, enabling
Template Library and Teams, and `features/with-classroom.installation.json`, which
also enables Classroom. These examples use `/opt/labcentral/features` as their
protected plugin root. Adjust that absolute path for your installation. When
combining with provider/access/source/builder entries, use one protected installation
file and resolve every manifest beneath its configured root. Preserve existing
provider entries and local project bindings.

Configure the separate HTTPS asset origin and loopback asset listener as described
in [plugin installation](../operations/plugin-installation.md), then restart core.
Never serve extension assets from the authenticated product origin. No core/UI
rebuild is needed to install, update, or remove these packages. Removing a package
hides its navigation entry; its data and existing labs remain available through
core APIs. A core-only installation needs no feature assets or asset listener.

For independent packaging from source, run:

```sh
node features/package.mjs /absolute/protected/features
```

The packager copies static assets and the published browser SDK, verifies them
through manifest digests at installation, and writes individual installation
examples. It does not import core/UI code or need a Go build. The third optional
argument sets the installation root recorded in those examples, independently of
the packaging destination. Release examples do not embed temporary build paths.

## Workflows

Template Library supports browsing, source export, import preview, independent
copies, YAML and schema-form authoring, GitHub library synchronization, image
builds/imports/approval, and single-owner preparation. Participants select and prepare published templates in Teams without author privileges. Template Library is shown to authors and instructors. Preparation status survives
page reload through the caller-scoped `GET /deployment-preparations?projectId=…`
API, which returns at most 200 recent requests belonging to that caller. The general
extension-operation inventory retains its author requirement. Image approval and
retirement remain subject to core image-administrator permissions.

Teams provides administrator group creation/enrollment controls, self join/leave,
team-owned preparation, and retained group labs. Leaving does not delete labs.
Classroom provides instructor roster/batch launch and confirmed bulk cleanup using
the existing APIs and roles. Basic lab detail and shared consoles remain core UI.
See [project groups](../users/project-groups.md) and
[shared console control](shared-console.md).

## Browser host contract 3

Packages declare `minimumHostVersion: 3`. Page contributions may declare the typed
`routeAlias` value `templates`, `teams`, or `classroom`; aliases are unique across an
installation. This preserves bookmarks while leaving workflow code outside core.
Contributions without aliases keep `/feature/?contribution=INSTANCE/ID`. Existing
host contracts and browser framing remain compatible.

Context includes the project, caller subject, available methods and a copy of the
project's display permissions. Display permissions are hints; every API call is
reauthorized. Group operations declare `groups.read`, `groups.enroll`, or
`groups.manage`. Typed bridge methods bind the active project and reject actor,
role, arbitrary route/header and foreign-project fields. Lab navigation, deletion,
batch reads and operation reads check the resource's project before returning it
or sending a command. Team preparation carries one explicit owner and never uses
instructor batch permissions. Inputs are checked as a closed typed union and mapped
to the existing API representation. No new secret-input channel is introduced.

`templates.download` obtains the canonical source export through core and initiates
the download in the host. Frames retain their opaque origin, network prohibition,
permission-bound channel and bounded request limits. See the closed catalog in
[`extension-bridge.ts`](../../web/src/lib/extension-bridge.ts).

The preparation API retains its original field names and stored input bytes.
Unused `TypedInput.Enum` and `TypedInput.SecretRef` alternatives accept JSON null,
matching the existing Go representation. The browser bridge emits this representation
from ordinary typed lab inputs; it cannot submit secret references.

## Browser host contract 4 and presentation assets

Host version 4 introduced presentation operations. Older contracts and installed
contributions remain supported by the current host. The optional `page.presentation` permission adds
closed `page.dirty({dirty: boolean})` and `page.navigate({destination: "labs" |
"teams"})` operations for page contributions. The host stores only an in-memory
dirty flag, protects navigation/workspace changes and browser unload, and checks
that Teams is installed before navigating. No arbitrary route or document content
is accepted. `groups.members({groupId, after?})` uses `groups.read`, binds the active
project and calls the authorized paginated group-member API. Context adds an image
administrator display hint; it grants no authority.

`pkg/plugin-sdk-browser/theme.css` is the single theme source. Run
`node tools/sync_theme.mjs` after theme edits to update the generated core input;
the core build rejects drift. Independent feature packaging compiles static
DaisyUI/Tailwind assets with the repository's pinned web development dependencies;
it neither imports core workflow code nor rebuilds core. Install those dependencies
with `cd web && bun install --frozen-lockfile` before source packaging. Packaged
features contain all CSS and require no CDN, runtime compiler or network styles.

Template content responses add read-only recipe summaries (logical builder,
architecture, dependencies and current builder availability) and a source-archive
flag. Existing source and parameter fields retain their meaning. Availability is
informational; build/preparation admission rechecks actual capabilities and policy.
Group member responses use `items`/`nextCursor` and expose only `id`/`displayName`.

## Browser host contract 5

Host 5 introduced the following optional fields; older contributions retain their
existing fields and behavior. A page can declare `icon` as `library`, `users`, `graduation-cap`,
`book-open`, `flask-conical`, `server`, `terminal`, `folder`, `settings`, or `puzzle`.
The host renders its bundled Lucide component, with a plug fallback when omitted;
URLs, arbitrary SVG and scripts are not accepted.

Optional `requiredActions` is an any-of list drawn from `template.read`,
`template.create`, `instance.batchCreate`, and `team.manage`. Discovery accepts
`projectId` and evaluates current core authority (including target grants for
batch launches). Missing project context hides restricted contributions. Host
route checks additionally use evaluated project actions. These declarations
restrict page access; they never grant API authority.

New closed bridge methods: `groups.delete({groupId,resourceVersion,idempotencyKey})`
requires `groups.manage`; `labs.batchList({})` requires `labs.read`; and
`navigation.console({instanceId,resourceId})` requires `navigation.lab`. Console
navigation checks project and topology, creates an authorized VNC access session,
and retains its one-time credential only in the host. No credentials reach frames.
Batch history is bounded to the latest 100 project batches and each returned batch
is authorized. Lab responses add optional `name`, `ownerName`, and `instructions`.

## Browser host contract 6

The current feature packages require host 6. `page.modal({open: boolean})`, gated
by `page.presentation` and the page slot, coordinates a native feature dialog
with the host backdrop and focus isolation. Closing, navigation or channel failure
restores the host's previous inert state. It grants no data or API authority; old
packages keep their previous behavior. Feature dialogs and core dialogs use the
same theme backdrop. Desktop launch dialogs place settings beside instructions
and keep actions together. Tabs and bulk controls share one opaque sticky header.

Teams administrators can select up to 100 groups to prepare labs, change enrollment
or delete groups, and select team labs for confirmed cleanup. Each command uses
the existing typed API, project checks, optimistic version and idempotency key.
Partial failures are named; retry repeats only failed commands with the original
key and payload. Successful requests leave the selection. Deployment preparation
remains one explicit team owner per command, independent of Classroom permissions.
