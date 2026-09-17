# Use LabCentral

Your administrator supplies the LabCentral URL and your account or enrollment
link. For a local development instance, follow [local setup](local-setup.md).

## Sign in and choose a workspace

Sign in using the configured account provider or local username and password.
Use the **Workspace** selector in the sidebar to change projects. A
workspace is a project in the API; it holds templates, people, policies and labs.
Only projects you can access appear. If none appear, ask an administrator to
assign your membership and project role, then choose **Check for access**.

## Launch and use a lab

1. In **My labs**, select **Launch lab**, choose a template revision and environment, and fill in
   the declared inputs. Set an optional lifetime and plugin options, or use the
   workspace and template defaults. Only published revisions are available.
2. Select **Launch lab**. Provisioning is asynchronous; **My labs** shows progress.
3. Open the lab details for instructions and **Open console** when ready.
4. Use **Machine** to switch between ready machines in the same lab. The console toolbar controls scaling, special keys, full screen and view-only
   mode. Reloading obtains a fresh authorized connection. Dropped connections
   retry automatically, with a Reconnect button after bounded retries. Revoked
   access stops retries; return to your lab or sign in again as directed.
5. End the lab when finished. Confirming deletion removes the environment and
   files inside it. The workspace's lifetime policy can also expire a lab.

**My labs** shows your environments. Use search and the ended-labs control to
find older records. Selected deletion asks you to confirm the exact labs and
reports individual failures; retry only the failed items.

## Teach a classroom

Use **Templates** to create a named template and publish an immutable revision.
Choose **Form** for fields generated from the lab schema, or **Source** for YAML/JSON. [Examples](../../examples/) and the [authoring contract](../design/declarative-labs.md)
explain supported resources and parameters. Publishing a new revision does not
change running labs.

In **Classroom**, each student appears with their labs, status and expiry. Select
students and choose **Launch labs**. The dialog lets you choose the template,
revision, environment, declared inputs, lifetime and plugin options, and adjust
the students included. Each student owns their instance. Open a lab for details
or end it from the student’s row; deletion still requires confirmation.

Classroom navigation is always visible. Batch launch is available only when you
have instructor authority and the workspace has environment access. If it is
unavailable, change workspace or have an administrator grant the environment
and assign an appropriate role. Navigation visibility never grants permissions.

## Administer LabCentral

| Page | What you can do |
| --- | --- |
| Workspaces | Create and rename organizations and workspaces; open a workspace's settings |
| People | Create accounts, enable/disable them and assign project roles |
| Integrations | Manage environments and workspace access, inspect installed plugins, and register API clients |
| Settings | Set the selected workspace's lab count limit and default lifetime |
| Operations | Inspect capacity, quotas, queues, operation details and VM measurements |

For a new classroom, create its organization/workspace, assign people, grant an
environment in **Integrations → Environments**, configure limits and publish a template. A workspace
alone does not grant its members infrastructure access.

Operations charts show current queued/running/failed counts and the latest CPU
and memory samples for the displayed page of VMs. They are not historical trends.
Missing or stale measurements show **Unavailable**, not zero. Active-lab counts
include container labs; VM measurements cover VMs only. The data tables provide
exact values and remain usable if a chart cannot load.

**Integrations → Environments → Settings & limits** sets concurrent deployments (1–16) per environment.
The saved limit applies across control-plane replicas without a restart. Running
operations keep their slots when a limit is reduced. Independent labs can run in
parallel; resources within one lab still follow their dependency order sequentially.

**Operations → Environment capacity & limits** shows Proxmox CPU, memory and
storage from its latest discovery snapshot. **Refresh capacity** obtains a new
snapshot. Its timestamp and expiry are shown separately from live VM measurements.
**Configure limits** opens the same settings dialog. The VM quota counts reserved,
committed and retained LabCentral resources across workspaces. Set it for any
registered provider; zero blocks new VMs. It does not change an upstream cloud
account quota or count unrelated VMs. Provider limits and available infrastructure
can impose a lower ceiling.

Plugin executables are installed by the server operator using verified manifests
and a restart. The UI manages provider discovery and access; it does not upload
or execute plugins. See the [installation guide](../operations/plugin-installation.md).

See [template examples and requirements](templates.md) for VM and container exercises.

To end several classroom labs, select their lab checkboxes or **Select all active labs**, then **End selected labs**. The confirmation lists each lab and owner. Individual failures can be retried without repeating accepted deletions. For VM-isolated containers, see [using the runtime console](../operations/container-vm-runtime.md).


**Integrations** separates three administrator tasks:

- **Environments:** where labs run, discovery, deployment limits and workspace access.
- **Plugins:** installed software and its role—provider, console/network access, template source, image builder or user interface. Proxmox and noVNC appear here alongside independently installed packages.
- **API clients:** external applications, such as SSH gateways, that request temporary access approved by each user. Register a recognizable name and save both the client ID and one-time secret. Registration alone grants no user access. Disabling a client blocks its requests.

Users approve application device codes at **Authorize an application** and inspect
or revoke their access under **Your settings → Connected applications**. Existing
`/plugins`, `/device` and `/account` bookmarks continue to work.

API clients can be disabled temporarily or permanently deleted from
**Integrations → API clients**. Deletion requires confirmation, invalidates the
client credentials and approved access, and retains audit history. Register a new
client to reconnect a deleted application.
