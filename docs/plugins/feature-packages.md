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
builds/imports/approval, and single-owner preparation. Participants can browse and
prepare published templates without author privileges. Preparation status survives
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
