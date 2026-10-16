# Running the hosted service

`labcentral-server` turns LabCentral into a web application for a team or class:
students practice in their own cloud workspaces, and instructors run team meetings
with in-browser desktops, breaks, checks and a scoreboard. Hosted provisioning
currently requires AWS.

- [What it does](#what-it-does)
- [The template repository](#the-template-repository)
- [Requirements](#requirements)
- [Configuration](#configuration)
- [Limits](#limits)
- [Operations](#operations)

## What it does

```mermaid
flowchart LR
    Repo[Template repository<br/>templates/*] -->|Refresh from GitHub| Import[Pinned revisions]
    Import -->|Build images| Images[Native images]
    Images --> Practice[Practice<br/>personal workspaces]
    Images --> Meeting[Meetings<br/>one lab per team]
    Student((Student)) --> Practice
    Student --> Meeting
    Admin((Admin)) --> Import
    Admin --> Meeting
```

- **Template library.** Imports templates from a Git repository, pins each commit and
  builds images once. Students browse by category, difficulty, tags and duration.
- **Guided practice.** Each attempt snapshots a revision's stages. A stage is completed
  by an answer, by a guest check that verifies the student's actual repair, or by an
  instructor. Authors can attach solutions that students reveal deliberately.
- **Randomized scenarios.** Authors certify compatible combinations of breaks; each
  attempt applies one at random and checks the matching recovery stages.
- **Personal workspaces** with a weekly cloud allowance per student (three hours by
  default), or a download to run the lab on your own infrastructure with the CLI.
- **Team meetings.** Admins start a meeting from a built template, then prepare one lab
  per team. Students join a team and open desktops (one holder per Windows machine, individual
  sessions on Linux); instructors apply breaks,
  run scheduled service checks with a scoreboard, and open an SSH terminal to any team
  machine in the browser.
- **Image cleanup.** Admin → Templates shows image counts, and "Remove unused images"
  removes images that no current template, meeting or workspace needs.

Sign-in uses OpenID Connect. Users in the configured member group can practice and join
teams; users in the admin group manage templates and meetings. Group membership is
rechecked every few minutes. If the identity provider is slow or down, signed-in users
stay signed in for up to 15 minutes; after that pages ask them to try again, and a
revoked account is signed out as soon as the provider answers.

## The template repository

The server imports the `templates/` directory from the `main` branch of a Git
repository you configure. Each template is a directory containing:

| Path | Purpose |
| --- | --- |
| `lab.yaml`, image playbooks or scripts | Portable lab source |
| `README.md` | Student handover shown in the library |
| `instructor.yaml` | Private teaching material: stages, checks, breaks, scenarios and catalog details |
| `instructor/` | Private programs for checks, breaks and resets |

Private material is removed before images are built and never reaches students.
`labcentral validate --template DIR` checks a template exactly as the importer will.
Each template is imported on its own: a broken one is reported to admins with the
failing field and does not block the others. See
[Writing lab templates](templates.md) and the
[starter template](../examples/starter-template/).

To publish a change: push to `main`, then **Admin → Templates → Refresh from GitHub**,
then **Build images** on the new revisions. Meetings and attempts keep the revision they
started with.

If an image build fails, choose **Retry build** in **Admin → Templates**. The server
reconciles the previous attempt and, after confirmed cleanup, builds the missing image;
already-built images are kept. **Build images** also handles pending failed attempts.
If cleanup cannot be confirmed, the job stays retryable: ask the operator to resolve
the native build resources, then retry. Removing all of the revision's images is not
needed for a failed build retry.

## Requirements

On one Linux host:

- the `labcentral-server` and `labcentral-provider` binaries and the Packer toolchain;
- the built web UI (`npm --prefix web ci && npm --prefix web run build`, then point
  `webRoot` at `web/build`);
- the Amazon DCV Web Client SDK (`dcvjs-esm` directory) for browser desktops;
- an AWS environment file, as for the CLI (see [Running labs with the CLI](operators.md));
- an OIDC client with redirect URL `https://APP_ORIGIN/auth/callback`;
- a read-only SSH deploy key for the template repository (a GitHub SSH URL such as
  `git@github.com:example/lab-templates.git`) and a `known_hosts` file;
- a TLS-terminating reverse proxy serving two origins, the application and a separate
  desktop origin, to the server's `--listen` address.

## Configuration

Create a configuration file readable only by the service:

```json
{
  "root": "/var/lib/labcentral/state",
  "origin": "https://labs.example.org",
  "desktopOrigin": "https://desktop.labs.example.org",
  "issuer": "https://id.example.org",
  "clientID": "labcentral",
  "clientSecret": "…",
  "memberGroup": "labcentral-users",
  "adminGroup": "labcentral-admins",
  "repository": "git@github.com:example/lab-templates.git",
  "deployKey": "/var/lib/labcentral/config/deploy_key",
  "knownHosts": "/var/lib/labcentral/config/known_hosts",
  "environment": "/var/lib/labcentral/config/aws.yaml",
  "webRoot": "/opt/labcentral/web",
  "sdkRoot": "/opt/labcentral/dcv"
}
```

`backchannelOrigin` can send token and discovery requests to a different HTTPS origin
when the public identity-provider hostname isn't reachable from the server; issuer and
audience are still verified against `issuer`.

```sh
labcentral-server --config /path/to/service.json --check-config   # validate, print limits
labcentral-server --config /path/to/service.json --listen 127.0.0.1:8080
```

## Windows templates

Templates can include Windows Server 2022 machines (AWS only). Add to the service's
environment file:

- a Windows base, such as `bases.windows-2022`, with `os: windows`,
  `user: Administrator`, `diskGiB: 30` and the same AMI ID in `id` and `identity`.
  Resolve `/aws/service/ami-windows-latest/Windows_Server-2022-English-Full-Base` once
  and bind the ID, never the parameter path;
- the DCV installer pins `build.windowsDCVURL` and `build.windowsDCVSHA256` (see
  [Windows guests](operators.md#windows-guests)); the build network needs Windows Update
  access to install OpenSSH;
- an instance type with at least 4 GiB in `aws.types`, such as
  `{name: t3.medium, cpu: 2, memoryMiB: 4096}`, selected for builds with
  `build.instanceType` or the image's `platform.aws.buildInstanceType`.

The service's AWS role must be allowed to launch that AMI and instance type. A template
the environment can't support shows the reason in **Admin → Templates** and can't be
built; Linux templates are unaffected.

**One screen per Windows machine.** A Windows machine has a single console, so in a
meeting one person uses a team's Windows desktop at a time. Teammates see who has it
and can **Take over**, which disconnects that person first; saved files stay, but open
programs close. Coaches can open or take over any team's desktop from **Admin →
Meeting → Coach machine access**. Practice attempts belong to one person, so this
doesn't apply there.

**Coach terminal.** Opens PowerShell as `Administrator`. The terminal keeps its initial
size on Windows.

**Time and cost.** Expect about an hour for a first Windows image build and several
minutes for a Windows desktop to start (the opening and preparation limits below are
longer for Windows). Windows instances cost more than Linux ones, and closing a desktop
doesn't stop the lab's machines.

## Limits

All limits are optional; omitted or zero values use the defaults.

| Setting | Default | Meaning |
| --- | ---: | --- |
| `weeklyCloudMinutes` | 180 | Each student's weekly personal cloud allowance (resets Monday, UTC) |
| `worker.maxProvisioningConcurrency` | 3 | Simultaneous team and personal lab preparations |
| `worker.maxBulkJobs` | 3 | Builds, preparations, template refreshes and image removals |
| `worker.maxGuestJobs` | 12 | Guest checks and breaks, separate from bulk work |
| `worker.maxImageBuildJobs` | 2 | Image builds within the bulk budget |
| `worker.prepareTimeoutMinutes` | 30 | Per Linux allocation preparation (1–1440 minutes) |
| `worker.windowsPrepareTimeoutMinutes` | 45 | Per Windows or mixed allocation preparation (1–1440 minutes) |
| `desktop.openSeconds` | 90 | Hosted Linux desktop opening (1–3600 seconds) |
| `desktop.windowsOpenSeconds` | 600 | Hosted Windows desktop opening (1–3600 seconds; provider ceiling 600) |
| `worker.jobTimeoutMinutes` | 120 | Maximum job duration (1–1440) |
| `engine.maxImageBuildConcurrency` | 3 | Parallel images within one build |
| `engine.maxMachineConcurrency` | 4 | Parallel machine creates within one deployment |
| `engine.maxCleanupConcurrency` | 4 | Parallel machine deletions within one cleanup |
| `aggregate.maxNativeOperations` | 8 | Machine creates and deletes across all jobs |
| `aggregate.maxSSHSessions` | 24 | Guest SSH steps, coach terminals and desktop operations across all jobs |

Worker concurrency and engine values accept 1–32 and aggregate budgets 1–64. Raising them does not
raise your cloud provider's quotas. A meeting of 6 teams × 6 students with 36
concurrent desktops has been verified with the defaults: desktops opened at once took
6–41 seconds, because desktop operations share `aggregate.maxSSHSessions`.

## Operations

- **Backups.** Back up the state root, including its WAL files and provider journals,
  while the server is stopped.
- **One server per state root.** Never run two servers against the same state.
- **Deploying updates.** Deploy when nothing is running (no open meeting, no active
  workspaces or jobs), after a backup.
- **Release checks.** Before trusting a release, sign in with a real identity, open a
  real desktop session and confirm cleanup; local tests cannot substitute for that.
