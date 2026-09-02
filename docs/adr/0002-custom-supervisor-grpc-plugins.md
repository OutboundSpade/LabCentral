# ADR-0002: Thin custom supervisor with Protobuf/gRPC plugins

- **Status:** Accepted
- **Date:** 2026-09-02
- **Supersedes:** None
- **Superseded by:** None

## Context and constraints

Plugins must be separate, language-neutral processes with local IPC, remote mTLS readiness, version/capability negotiation, health, progress, cancellation, idempotency, upgrade handling, and third-party distribution.

## Considered options

1. HashiCorp `go-plugin`: mature subprocess launch, handshake, gRPC, health, TLS, and reattach; its stdout handshake and Go-oriented SDK do not solve durable operation/state migration or the later outbound Site Agent.
2. Raw gRPC plus a thin LabCentral supervisor: more lifecycle code, but one protocol works for local and remote transports.
3. Kubernetes-style external controllers: too operationally heavy for laptop mode.

## Decision

Use option 2, borrowing tested concepts from `go-plugin` but not its wire handshake. The host creates a private endpoint and one-time launch token, passes only endpoint/token/plugin-instance ID through inherited descriptors or environment, then the plugin calls `Handshake` before its deadline. Use Unix sockets on Unix; Windows named pipes are the preferred local alternative, with loopback TCP plus ephemeral mTLS as the fallback. Remote calls use TLS 1.3 mTLS. gRPC health, deadlines, cancellation, keepalive, structured progress streams, and protocol range negotiation are mandatory.

The supervisor verifies configured executable digests, captures bounded stdout/stderr as structured plugin logs, uses exponential restart with a circuit breaker, and drains before upgrade. Mutations carry core-generated idempotency keys; durable invocation records outlive processes. `api/plugin/v1/plugin.proto` is illustrative until compatibility fixtures are accepted.

## Consequences

LabCentral owns roughly a small supervisor and transport adapter but avoids binding remote architecture to a framework handshake. An SDK is convenience, never the protocol definition.

## Security implications

Local filesystem/pipe ACLs, one-time launch credentials, peer identity, process identity, executable allowlists/checksums, redaction, and per-target secret injection are required. Out-of-process is fault isolation, not a sandbox; third-party plugins should run under separate OS/container identities with minimal network/filesystem access.

## Compatibility implications

Protocol major ranges are negotiated. Removed Protobuf field numbers/names are reserved. Provider state compatibility is negotiated separately from RPC compatibility.
