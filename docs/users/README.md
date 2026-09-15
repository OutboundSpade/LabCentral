# Use LabCentral

Your administrator supplies the LabCentral URL and your account or enrollment
link. For a local development instance, follow [local setup](local-setup.md).

## Sign in and choose a workspace

Sign in using the configured account provider or local username and password.
Use the **Workspace** selector at the top of the page to change projects. A
workspace is a project in the API; it holds templates, people, policies and labs.
Only projects you can access appear. If none appear, ask an administrator to
assign your membership and project role, then choose **Check for access**.

## Launch and use a lab

1. Open **Launch lab**, choose a template revision and environment, and fill in
   the declared inputs. Only published revisions are available.
2. Select **Launch lab**. Provisioning is asynchronous; **My labs** shows progress.
3. Open the lab details for instructions and **Open console** when ready.
4. The console toolbar controls scaling, special keys, full screen and view-only
   mode. A console session can expire before the lab does; reopen it from details.
5. End the lab when finished. Confirming deletion removes the environment and
   files inside it. The workspace's lifetime policy can also expire a lab.

**My labs** shows your environments. Use search and the ended-labs control to
find older records. Selected deletion asks you to confirm the exact labs and
reports individual failures; retry only the failed items.

## Teach a classroom

Use **Templates** to create a named template and publish an immutable YAML
revision. [Examples](../../examples/) and the [authoring contract](../design/declarative-labs.md)
explain supported resources and parameters. Publishing a new revision does not
change running labs.

In **Classroom**, select a template, environment and eligible participants, then
launch a batch. Each participant owns their own isolated instance. Follow the
batch and per-lab status, and inspect failures before retrying.

Classroom navigation is always visible. Batch launch is available only when you
have instructor authority and the workspace has environment access. If it is
unavailable, change workspace or have an administrator grant the environment
and assign an appropriate role. Navigation visibility never grants permissions.

## Administer LabCentral

| Page | What you can do |
| --- | --- |
| Workspaces | Create and rename organizations and workspaces; open a workspace's settings |
| People | Create accounts, enable/disable them and assign project roles |
| Plugins | Inspect provider environments, refresh discovery and grant/revoke workspace access |
| Settings | Set the selected workspace's lab count limit and default lifetime |
| Operations | Inspect queues, failures, resource progress and VM measurements |

For a new classroom, create its organization/workspace, assign people, grant an
environment in **Plugins**, configure limits and publish a template. A workspace
alone does not grant its members infrastructure access.

Operations charts show current queued/running/failed counts and the latest CPU
and memory samples for the displayed page of VMs. They are not historical trends.
Missing or stale measurements show **Unavailable**, not zero. Active-lab counts
include container labs; VM measurements cover VMs only. The data tables provide
exact values and remain usable if a chart cannot load.

Plugin executables are installed by the server operator using verified manifests
and a restart. The UI manages provider discovery and access; it does not upload
or execute plugins. See the [installation guide](../operations/plugin-installation.md).
