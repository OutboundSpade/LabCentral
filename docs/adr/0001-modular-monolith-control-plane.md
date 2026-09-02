# ADR-0001: Modular-monolith control plane

- **Status:** Accepted
- **Date:** 2026-09-02
- **Supersedes:** None
- **Superseded by:** None

## Context and constraints

LabCentral needs one easy local install and restart-safe coordination at classroom scale. Go, Huma, HTTP-as-canonical, SQLite/PostgreSQL, and out-of-process plugins are accepted constraints.

## Considered options

1. Independently deployed control-plane microservices: operationally expensive and creates premature distributed transactions.
2. One unstructured process: easy to deploy but couples domain, transport, persistence, and plugins.
3. A modular monolith with explicit ports and one worker pool: retains boundaries without network hops.

## Decision

Use option 3. `labd` hosts HTTP, application services, scheduler, durable workers, plugin supervisor, and static assets. Domain/application packages depend on interfaces for persistence, artifacts, clocks, identities, and plugin invocations. Site Agent and plugin executables remain separate processes. Deployment may run more `labd` replicas only with PostgreSQL and shared artifacts.

## Consequences

Local mode is one server plus supervised plugin children. Modules can be split later where measurement shows a real boundary. Handler/domain and adapter/domain conformance tests enforce direction.

## Security implications

One process concentrates control-plane privilege, so provider credentials are resolved just-in-time and scoped per invocation; plugins receive only target-scoped credentials. Guests never connect to `labd` directly.

## Compatibility implications

HTTP and plugin contracts, not internal package layout, are compatibility surfaces.
