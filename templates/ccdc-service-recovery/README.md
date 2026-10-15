# Branch service recovery

You are the defender for a small branch office. Its Linux `services` machine hosts
the branch website and a scheduled operations heartbeat. Keep both running, and
record the commands, symptoms and evidence you use.

- **Website:** `http://services/` (open it in Firefox on your workstation, or use
  `curl`).
- **Heartbeat:** the cron service writes a UTC timestamp to
  `/var/lib/branch-operations/heartbeat` every minute.
- **Sign in:** from your workstation terminal, `ssh defender@services` with the
  password `branch-practice`. The account can use `sudo`.

This password exists only inside this isolated training lab. Never reuse it anywhere
else.

Inspect service state and journals, repair the problem, and verify your fix.
Preserve evidence: restarting a service is only part of the investigation.
