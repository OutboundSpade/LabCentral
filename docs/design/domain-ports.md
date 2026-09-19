# Domain and application ports

ADR-0015 replaces the unused repository/unit-of-work abstraction with the actual application boundary. Types are declared in `internal/application/controlplane.go`, `ports.go` and `provider.go`; do not duplicate signatures in prose or add speculative repositories. `internal/controlplane/model.go` contains compatibility aliases to those declarations.

## Domain

Opaque IDs, digests, typed inputs, capabilities, references and the closed resource variants live in `internal/domain`. `Network`, `Volume`, `VM`, `Container`, `AccessEndpoint` and `Output` remain typed resources. Provider override bytes belong only to separately versioned envelopes. Domain and application packages have no transport, database or provider dependencies.

## Persistence

`TemplateStore`, `InstanceStore`, `AccessStore`, `TargetStore` and `ExecutionStore` group the actual methods; `Store` composes service requirements. `WorkerStore` exposes only execution requirements. Audit, identity, authorization and quota contracts remain explicit. An operation such as batch acceptance or instance enqueue owns its complete transaction, including authorization, reservations, state, audit/event/outbox and idempotency. There is no public callback giving callers arbitrary repository access inside a transaction.

`internal/adapter/localdb` supports SQLite and PostgreSQL despite its historical name. `jobsdb` owns database-time leasing and fencing. Existing protobuf provider-state envelopes remain readable in storage; this persistence codec does not expose wire types to orchestration. Artifacts remain content-addressed with verified staging/commit and coordinated backup. Migration checksums and atomicity remain unchanged. Serialization/deadlock retries repeat only bounded database transactions and recheck fences; lease-renewal conflicts in apply policy and quota retention never repeat a provider call.

Allocation claims remain bounded opaque `(target, namespace, value)` keys bound to instance/resource/plan. Unknown execution retains claims; release requires provider absence evidence and a current fence. Target sessions copy configuration, resolve authorized credentials only for an invocation, and clear credential bytes on close. Credential scopes retain the existing instance/resource form and add a mutually exclusive typed work subject for image import/retirement or deployment preparation before a lab exists. Both forms require an invocation and unexpired deadline; source/build subjects cannot use this target credential scope. Native and independently installed provider registrations bind the same negotiated image-import and artifact-transfer clients to the provider binary identity.

## Provider and access invocation

`application.Provider` provides validation, planning, invocation, lookup and discovery using application-owned structures. The gRPC adapter maps requests, drains mutation streams and requires one terminal result. A transport error is not proof that a write failed. Persisted result/state bytes retain their prior format. Optional `ProviderAccess` and `AccessPlugin` ports preserve endpoint and access-session contracts without gRPC imports in control-plane code.

`SecretResolver` and `ArtifactStore` remain separate ports. Plugins cannot access either the core database or unrestricted secret resolution. Access authorization callbacks repeat subject/session/resource checks under core authority.

## Verification

`internal/architecture/imports_test.go` enforces the inward domain/application dependency rules, public SDK isolation, and orchestration transport boundary. Storage conformance tests cover both supported databases; worker tests cover original-invocation recovery, stale fences, compensation and claims. See [architecture conformance](../../design/contracts/architecture-conformance.yaml).
