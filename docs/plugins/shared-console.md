# Shared console control

For the native deployment, retain `Restart=always` in the noVNC systemd unit.
Heartbeat loss closes existing sessions and can end the process with a clean
exit status; the service manager must restart it so core can reattach after an
outage. An explicit service stop still remains stopped. Reconnected users begin
as viewers and must request control again.

`shared-console-control-v1` is an optional negotiated access-plugin feature under
ADR-0029. `CreateAccessRequest.console_control_version = 1` selects its semantics.
Core selects it for VNC when the configured plugin advertises support. Project
group VNC access requires it. Existing non-shared access records retain their
wire/state representation and never receive an input grant.

New shared sessions begin as viewers. Core persists one fenced driver lease for
an instance, resource and exact generation. An authenticated user can request
control only for their own active access session. Take control replaces the
requested session immediately; the previous driver's input lease is not renewed.
Core activates the requester only after that prior lease expires. Release stops
renewal in the same way. Requests are idempotent and do not restore released
control on replay. A grant that expires requires a fresh Take control action.

The callback response carries a separate `ConsoleInputLease`: a strictly positive
fence and at most five seconds of `valid_for`. The plugin measures this duration
from the **monotonic start of its callback request**, not response arrival, and
checks the deadline at every input write. This bounds a delayed response. Core
renews authorization independently, while shared sessions poll every second.
Missing input grants remove permission; callback failures cannot extend an input
lease. Existing per-user authorization and bounded stream closure still apply.

Migration `0021_console_control` retains the resource's shared-control marker.
Once selected for a resource generation, legacy sessions cannot bypass it. The
first input grant also waits out any pre-existing legacy authorization lease.
All reconnections require a new individually authenticated session and begin
without control. Core restart preserves holder, fence and deadline; plugin restart
closes its upstream connections. Deployment must preserve the existing clock and
lease assumptions used by authorization and durable operations.

The noVNC adapter frames the **RFB byte stream**, independently of WebSocket frame
boundaries. It forces ClientInit shared mode. SetPixelFormat, SetEncodings,
FramebufferUpdateRequest, ContinuousUpdates and bounded Fence messages support
viewing. Keyboard, ordinary/extended pointer, ordinary/extended clipboard,
desktop resize, XVP power and QEMU extended-key messages require input authority.
Unknown messages, unknown QEMU subtypes and oversized arrays/payloads close the
stream. Browser `viewOnly` is an additional presentation setting.

The API exposes `GET /api/v1/access-sessions/{id}/console`, plus POST
`/console/take-control` and `/console/release-control` with `Idempotency-Key`.
Responses use `labcentral.console-presence/v1` and include current connected
sessions, display names, the driver and pending requester. They contain no
screen content, keystrokes or callback credentials. Membership and control
changes are audited. The console UI shows Watch/Take control/Release control
and connected members; no Classroom package is involved.
