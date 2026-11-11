# In-lab scoreboard feed

For the initial MWCCDC practice, use the existing **MWCCDC practice scorer** in the
companion `Deployment-Kits-CCDC` repository. Its updated `ccdc.score` worker can
publish this exact feed with `--labcentral-feed /var/lib/ccdc/labcentral/scoreboard.json`.
It preserves the practice's 21 external functional checks, four signed internal
checks, cumulative points and round history. The exporter records completion time
and weights with each committed round, whitelists public fields, and retains the
previous file if publication fails. No scorer login or raw admin API is exposed.

Install the updated `ccdc` package in the scorer guest and run this file server as
the same Unix user as the scoring worker. Supply the native template's scoring
profile; `--labcentral-feed` selects a scoring-only validator without Proxmox
placement/NIC assumptions. For the signed probe receiver or organizer web service,
the scorer's `web --native-profile` mode uses that same validator. Its original
standalone mode remains unchanged when these options are omitted. See the kit's
`Proxmox/MWCCDC-INVITES-2026/docs/SETUP.md` for the service commands. Native AWS
provisioning remains in your separate template, not the Proxmox deployment runner.

This optional adapter runs **inside a hidden Linux scorer VM**. It serves a file
produced by the scoring engine on `127.0.0.1:18080/scoreboard.json`. LabCentral reads
it through verified SSH. Install and supervise it through your template's guest
provisioning; it requires Python 3 and no additional packages:

```sh
python3 serve.py --file /var/lib/scoring/labcentral.json --port 18080
```

Configure the private `instructor.yaml`:

```yaml
version: 3
visibility:
  audience: coaches
  hiddenMachines: [scorer]
liveScoring:
  machine: scorer
  port: 18080
  path: /scoreboard.json
  staleAfterSeconds: 180
```

The source must declare `scorer` with `access: [ssh]`. Existing hidden-machine rules
still apply: visible image builds select explicit files excluding `lab.yaml`, and
visible guests declare `hostnamePeers` excluding the scorer. Native appliances
receive only their selected vendor configuration and do not require image builds.

On each **completed** scoring round, export the following projection. Use the UTC
completion time, a monotonically increasing round number, cumulative earned and
possible points for each service, and its current `up`, `down`, or `unknown` state:

```json
{
  "version": 1,
  "round": 12,
  "generatedAt": "2026-11-11T18:00:00Z",
  "services": [
    {"id": "web", "name": "Web service", "state": "up", "earned": 110, "possible": 120},
    {"id": "mail", "name": "Mail service", "state": "down", "earned": 80, "possible": 120}
  ]
}
```

The timestamp above illustrates the format; the exporter must supply the actual
round completion time. Publish the file using a temporary file and atomic rename
on the same filesystem. Keep it outside the public template files and make it
readable by the adapter process. Do not reset `generatedAt` whenever it is read:
stopped scoring must become stale. The bot retains its own round history; LabCentral
persists the last good snapshot, not a second scoring database.

For a different scoring engine, supply an exporter for its database/API. Do not point
LabCentral at a bot login page or a raw admin API. This file contains **only** the
fields shown: no passwords, target IPs, probe details, HTML, cookies, or admin actions.
LabCentral derives the range/team identity from the pinned allocation; the feed
does not name a team. All ranges should use the same points rules.

Finally, select **Live Red vs. Blue** when creating a meeting from this template.
The student meeting page displays standings and the student's service breakdown;
the coach's Scoreboard tab displays all service breakdowns. Without that meeting
option, the feed is never polled and ordinary labs keep their existing scoreboards.

See [the hosted guide](../../docs/hosted.md#live-red-vs-blue-scoreboards) for limits,
failure behavior, and the AWS acceptance checks.
