# Classroom browser load

`browser-load.cjs` exercises five already-provisioned groups with three distinct
participant browser contexts per group, using the normal access API and real
noVNC UI. It opens consoles, samples connection status, navigates lab details and
rotates input control every 90 seconds on labs marked `testOwned`. Those labs
receive only terminal/date activity and a temporary input marker. Other labs are
observed without guest input or explicit control requests; the normal UI may
automatically acquire control when a viewer is alone.

Install the web dependencies and Playwright Chromium first. Keep all setup,
authenticated storage states, endpoints, inventory and results in a protected
operator directory outside source. The harness never creates accounts or labs.
Supply these JSON files in `LC_LOAD_DIRECTORY`:

- `participants.json`: fifteen entries with `group` (1–5), `number` (1–3),
  `subjectId` and an absolute Playwright `state` file path.
- `labs.json`: five entries with `group`, `instanceId`, `phase: "ready"` and
  `testOwned` (true only for disposable labs whose desktop may receive input).

Run with `LC_LOAD_ORIGIN=https://<installation>`, `LC_LOAD_RUN=<unique-name>`
and `LC_LOAD_SECONDS=1200`. Authentication must be valid for the run; keep the
run shorter than the requested 30-minute access-session lifetime. Browser
storage files are updated with rotated cookies. Outputs include secret-free
HTTP timing/connection events, participant summaries and screenshots. Screenshots
can contain guest content and must remain protected.

Correlate these events with server logs, active session counts and disk/CPU/
memory pressure. Monitor the load generator too. Report unsuccessful launches
rather than omitting them from the denominator. The harness records each failed
attempt, permits at most two explicit retries, and refuses to start the hold
unless all fifteen consoles connect. Distinguish browser shutdown
from unexpected disconnects and deliberate navigation aborts from API failures.
Verify input markers independently in test guests. Repeat long enough to cross
session rotation; HTTP health checks alone do not establish console stability.

Afterward revoke remaining test sessions, remove synthetic enrollments/accounts
and destroy only explicitly test-owned labs through the normal API. Preserve
existing class labs and their data. This bounded test does not establish the
100-instance production capacity gate.
