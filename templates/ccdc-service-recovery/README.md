# Branch service recovery

You are the defender for a small branch office. Its Linux services machine hosts
the branch website and a scheduled operations heartbeat. Keep both available and
record the commands, symptoms and evidence used during recovery.

Open your workstation desktop. Connect to the services machine with
`ssh defender@services` with the synthetic training password `branch-practice`.
This guest-only account has passwordless sudo. It is never an infrastructure
credential; do not reuse the password outside this isolated training lab.

The website should respond at `http://services/`. The cron service should run,
and `/var/lib/branch-operations/heartbeat` should receive a UTC timestamp every
minute. Read the handover, inspect service state and journals, repair the problem,
and verify your changes. Preserve evidence; restarting a service is only part of
the investigation.

Baseline practice starts with healthy services. A randomized attempt prepares an
authored combination of service failures on fresh guests. Your attempt and its
progress are saved independently; starting another attempt does not erase them.
