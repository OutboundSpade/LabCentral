package controlplane

// Compatibility aliases: application owns these persisted command and query contracts.
import "github.com/OutboundSpade/LabCentral/internal/application"

type OperationKind = application.OperationKind
type InstanceRecord = application.InstanceRecord
type LabTemplateRecord = application.LabTemplateRecord
type GovernanceRecordIDs = application.GovernanceRecordIDs
type CreateLabTemplateRequest = application.CreateLabTemplateRequest
type UpdateLabTemplateRequest = application.UpdateLabTemplateRequest
type TemplateRevisionRecord = application.TemplateRevisionRecord
type LabTemplateCursor = application.LabTemplateCursor
type LabTemplateListRequest = application.LabTemplateListRequest
type LabTemplatePage = application.LabTemplatePage
type TemplateRevisionCursor = application.TemplateRevisionCursor
type TemplateRevisionListRequest = application.TemplateRevisionListRequest
type TemplateRevisionPage = application.TemplateRevisionPage
type PublishTemplateRevisionRequest = application.PublishTemplateRevisionRequest
type TargetSnapshot = application.TargetSnapshot
type ProviderTargetStatus = application.ProviderTargetStatus
type ResourceRecord = application.ResourceRecord
type OperationRecord = application.OperationRecord
type ResourceSeed = application.ResourceSeed
type CreateInstanceRequest = application.CreateInstanceRequest
type InstanceOwner = application.InstanceOwner
type InstanceBatchItem = application.InstanceBatchItem
type InstanceBatchRecord = application.InstanceBatchRecord
type AccessSessionRecord = application.AccessSessionRecord
type BeginAccessSessionRequest = application.BeginAccessSessionRequest
type FinalizeAccessSessionRequest = application.FinalizeAccessSessionRequest
type FailAccessSessionRequest = application.FailAccessSessionRequest
type AccessExchangeRequest = application.AccessExchangeRequest
type AccessLeaseRecheckRequest = application.AccessLeaseRecheckRequest
type EndAccessLeaseRequest = application.EndAccessLeaseRequest
type RevokeAccessSessionRequest = application.RevokeAccessSessionRequest
type AccessReconciliationResult = application.AccessReconciliationResult
type CreateInstanceBatchRequest = application.CreateInstanceBatchRequest
type QueueDestroyRequest = application.QueueDestroyRequest
type PutQuotaLimitRequest = application.PutQuotaLimitRequest
type PutLifetimePolicyRequest = application.PutLifetimePolicyRequest
type RenewInstanceLifetimeRequest = application.RenewInstanceLifetimeRequest
type ExpirationCandidate = application.ExpirationCandidate
type MarkExpirationWarningRequest = application.MarkExpirationWarningRequest
type EnqueueResult = application.EnqueueResult
type WorkClaim = application.WorkClaim
type Fence = application.Fence
type InvocationRecord = application.InvocationRecord
type PrepareInvocationRequest = application.PrepareInvocationRequest
type ResourceResultUpdate = application.ResourceResultUpdate
type FinalizeRequest = application.FinalizeRequest
type Store = application.Store

const OperationApply = application.OperationApply
const OperationDestroy = application.OperationDestroy
const MaximumAccessSessionDuration = application.MaximumAccessSessionDuration
const DefaultAccessSessionDuration = application.DefaultAccessSessionDuration
const DefaultAccessLaunchTTL = application.DefaultAccessLaunchTTL
const DefaultAccessLeaseDuration = application.DefaultAccessLeaseDuration
const MaximumTemplatePageSize = application.MaximumTemplatePageSize
const MaximumInstanceBatchSize = application.MaximumInstanceBatchSize

var ErrNoWork = application.ErrNoWork
var ErrNotFound = application.ErrNotFound
var ErrAlreadyExists = application.ErrAlreadyExists
var ErrVersionConflict = application.ErrVersionConflict
var ErrAuthorityChanged = application.ErrAuthorityChanged
var ErrAccessUnavailable = application.ErrAccessUnavailable
var ErrAccessUnsupported = application.ErrAccessUnsupported
