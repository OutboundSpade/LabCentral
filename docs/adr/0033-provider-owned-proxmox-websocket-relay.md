# ADR-0033: Provider-owned encrypted Proxmox console relay

- **Status:** Accepted
- **Date:** 2026-09-21
- **Supersedes:** None
- **Superseded by:** None

## Decision

Add an explicit provider-owned WSS transport for Proxmox consoles. The existing
TLS socket and ADR-0019 tailnet profiles remain supported without fallback or
changed semantics. Some supported PVE installations expose plain RFB on their
temporary TCP port but provide authenticated TLS through `vncwebsocket`.

After the existing signed qualification, current permissions, ownership and
resource-state checks, the provider requests a WebSocket console ticket and opens
the exact node/VM endpoint using its invocation-scoped API credential. It verifies
the configured HTTPS server with normal certificate and hostname validation,
refuses redirects, and never delivers that credential or upstream URL/ticket to
the access process or browser.

The additive `proxmox.vnc-relay-endpoint/v1.0` envelope binds the same resource,
target and provider-state identity to a random relay ID, one-use secret and VNC
challenge password. Core passes this envelope only in memory. The access plugin
connects through an operator-configured, protected same-host Unix directory;
both peers verify the other's configured OS UID. An unguessable one-use secret,
bounded connection window, capacity limit and fixed lifetime constrain each
relay. Neither side accepts an arbitrary path, target URL or management request.
There is no remote plaintext listener. Provider exit, expiry or either peer's
disconnect closes the relay and its upstream connection; stale sockets cannot
restore a consumed grant.

Core remains the sole authorization authority. The existing access plugin still
consumes VNC authentication, handles one-time browser launch tokens, enforces
shared-driver input and current authorization leases, and closes on revocation
or core outage. The relay makes no user/tenant authorization decisions. An access
process compromise gains only the already-issued bounded streams, not a reusable
Proxmox management credential.

## Compatibility and validation

Negotiate the new endpoint schema explicitly. Existing core, target configuration,
provider state, public HTTP API and database formats are unchanged. Enable the
transport only with matching independently deployed provider/access packages and
protected local relay configuration. A changed provider still requires fresh
exact-build qualification under ADR-0018; installing a relay does not bypass it.

Test verified WSS, exact paths/authentication, redirect and certificate rejection,
wrong/replayed/expired local grants, peer identity, bounded resources, shutdown
and disconnect cleanup. Retain direct TLS/tailnet and authorization/driver tests.
Validate the exact release against a live authorized VM and browser console.
