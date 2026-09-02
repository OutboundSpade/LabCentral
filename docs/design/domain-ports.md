# Domain and application ports

This is the implementation boundary for `TASK-DOMAIN-001`. The accepted interfaces and state contracts are implemented under `internal/domain` and `internal/application`; `internal/architecture/imports_test.go` enforces their dependency direction recursively. Implementations may group methods to avoid tiny packages, but may not leak Huma, SQL, generated plugin, or provider SDK types into domain code.

## Value types

Use distinct opaque string types for `UserID`, `OrganizationID`, `ProjectID`, `TeamID`, `ProviderTargetID`, `TemplateID`, `RevisionID`, `InstanceID`, `LogicalResourceID`, `OperationID`, `InvocationID`, `SnapshotID`, and `AccessSessionID`. `Generation`, `ResourceVersion`, `FencingToken`, and aggregate event sequence are unsigned monotonic values. Constructors validate names, CIDRs, IPs, durations, byte quantities, digests, capability IDs, and typed references once.

The canonical domain is a closed sum:

```go
type LogicalResource interface {
    ID() LogicalResourceID
    Kind() ResourceKind
    Dependencies() []LogicalResourceID
    RequiredCapabilities() CapabilityExpression
    logicalResourceSeal()
}

type ResourceSpec struct {
    Network        *NetworkSpec
    Volume         *VolumeSpec
    VM             *VMSpec
    Container      *ContainerSpec
    AccessEndpoint *AccessEndpointSpec
    Output          *OutputSpec
}
```

Exactly one variant is present. This is not a generic map; provider override bytes exist only in a separately versioned envelope.

## Pure domain services

```go
type LabRenderer interface {
    Render(ctx context.Context, revision RevisionContent, inputs TypedInputs, policy RenderPolicy) (CanonicalLab, RenderReport, error)
}

type GraphBuilder interface {
    Build(CanonicalLab) (ResourceGraph, error)
}

type CapabilityMatcher interface {
    Match(ResourceGraph, Policy, CapabilitySnapshot) MatchReport
}

type Scheduler interface {
    Select(SchedulingRequest, []EligibleTarget) (SchedulingDecision, error)
}

type Authorizer interface {
    Check(AuthorizationRequest) AuthorizationDecision
}

type StateReducer interface {
    Reduce(AggregateState, DomainCommand, Observations) (AggregateState, []DomainEvent, error)
}
```

Pure services receive all time/policy/capability inputs explicitly and perform no I/O. Canonical bytes/digest are created by one renderer/canonicalizer version, not recomputed in HTTP handlers or plugins.

## Persistence ports

```go
type UnitOfWork interface {
    Do(ctx context.Context, fn func(context.Context, Repositories) error) error
}

type Repositories interface {
    Identity() IdentityRepository
    Tenancy() TenancyRepository
    Providers() ProviderRepository
    Templates() TemplateRepository
    Instances() InstanceRepository
    Operations() OperationRepository
    Governance() GovernanceRepository
    Events() EventRepository
}
```

Repositories expose aggregate operations with expected `ResourceVersion`, not table CRUD. `OperationRepository` claims jobs, renews leases, checkpoints steps, and commits only with matching fencing token. `GovernanceRepository` reserves/reconciles quota within the caller’s unit of work. `EventRepository` appends aggregate-sequenced event, audit intent, and outbox atomically with state. Adapters translate duplicate/version/serialization conflicts into domain errors.

## External side-effect ports

```go
type ArtifactStore interface {
    Stage(context.Context, StageRequest) (UploadID, error)
    Write(context.Context, UploadID, io.Reader) (MeasuredArtifact, error)
    Commit(context.Context, UploadID, VerifiedManifest) (ArtifactRef, error)
    Open(context.Context, AuthorizedArtifactRef) (io.ReadCloser, error)
}

type PluginInvoker interface {
    DiscoverTarget(context.Context, TargetInvocation) (CapabilitySnapshot, error)
    ValidateLab(context.Context, ValidateInvocation) (ProviderValidation, error)
    PlanResource(context.Context, PlanInvocation) (ProviderPlan, error)
    Invoke(context.Context, MutationInvocation) (ProgressStream, error)
    GetInvocation(context.Context, InvocationQuery) (InvocationObservation, error)
    CancelInvocation(context.Context, InvocationCancel) (CancelOutcome, error)
}

type SecretResolver interface {
    Resolve(context.Context, AuthorizedSecretRef, InvocationScope) (EphemeralSecret, error)
}

type Clock interface { Now() time.Time }
type IDGenerator interface { New() string }
```

`PluginInvoker` is an application port; generated gRPC messages stay inside `internal/pluginhost` adapters. `EphemeralSecret` is non-serializable by convention/API design, redacts formatting, and cannot enter Domain Events. Fake clock/ID/plugin/artifact/secret adapters make normal tests deterministic.

## Application command boundary

Commands include `PublishRevision`, `ValidateLab`, `PlanLab`, `CreateInstance`, `ApplyInstance`, lifecycle commands, `RefreshInstance`, snapshot/restore/clone, `DestroyInstance`, `RetryOperation`, `CancelOperation`, and access-session commands. Each command handler follows:

1. Authenticate subject and authorize action/object/scope.
2. Validate request version/idempotency and typed input.
3. In one transaction, load aggregates, evaluate invariant, reserve quota if needed, persist desired state/Operation/job/audit/outbox, and return IDs.
4. Workers perform provider side effects outside the transaction through durable invocation records.
5. In a new fenced transaction, validate generation/lease, persist observation/result, reconcile quota, append events, and unlock/checkpoint.

HTTP handlers map DTO → command and result/error → DTO only. Provider plugins import neither these interfaces nor domain internals; the public SDK wraps the wire protocol.

## Compile-time dependency checks

The first Go module must add import-boundary tests or a static dependency rule proving:

- `internal/domain` imports standard library plus explicitly approved pure libraries only.
- `api/http` may import application-facing DTOs but domain imports no `api/http` or Huma package.
- persistence adapters implement ports without exporting SQL models.
- `providers/*` import generated plugin API/SDK and provider libraries, never `internal/*`.
- `pkg/plugin-sdk-go` imports generated public protocol only.
