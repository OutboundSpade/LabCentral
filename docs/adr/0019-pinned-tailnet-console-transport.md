# ADR-0019: Pinned tailnet console transport

- **Status:** Accepted
- **Date:** 2026-09-07
- **Supersedes:** None
- **Superseded by:** None

## Context

TASK-TESTBED-001 uses owner-selected Tailscale access. Live PVE1 9.2.11
returns plain RFB on its temporary VNC port; TLS-first negotiation fails.
Its HTTPS WebSocket API also requires a management credential, which must
not be passed to the access plugin. The existing TLS transport remains the
default for other deployments.

## Decision

Permit an explicit Linux access-plugin deployment profile for authenticated
Tailscale transport. The closed v1 profile pins one target ID, MagicDNS name,
tailnet IPv4 address and node public key. Before each connection, the plugin
queries the local privileged tailscaled Unix socket and checks the complete
peer binding, online state and address. It then connects to that numeric
address with the socket bound to `tailscale0`; only VNC ports 5900–5999 are
allowed. Binding or identity failure stops the connection. There is no DNS
fallback, ordinary-interface fallback, TLS downgrade, management credential
handoff or change to the provider endpoint/protocol.

WireGuard authenticates and encrypts this transport. The local root-owned
daemon, kernel interface and operator profile are trusted deployment inputs,
as are the existing service sandbox and firewall. Peer key rotation requires
an operator profile update. TLS CA validation remains mandatory in default
mode; the explicitly selected tailnet mode authenticates by node public key.

ADR-0018 qualification, ownership, authorization rechecks, one-time browser
tokens and expiry/revocation remain unchanged. The provider binary and target
configuration are unchanged; the access binary and its profile receive new
deployment verification and live console/soak evidence. No production
validation gate is completed by this testbed-specific deployment.

## Validation

Test exact peer acceptance and rejection of wrong target, name, key, address,
offline peers, unsupported ports, incomplete profiles and daemon failure.
Verify the live service can bind its socket without added capabilities, open
the student console, and close it on revocation/expiry. Retain TLS regression
tests. Record the access build/profile and bounded UID/interface/port rules.
