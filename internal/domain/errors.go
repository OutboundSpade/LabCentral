package domain

import (
	"fmt"
	"time"
)

type ErrorClass string

const (
	ErrorInvalidDeclaration       ErrorClass = "invalid_declaration"
	ErrorMissingCapability        ErrorClass = "missing_capability"
	ErrorProviderValidation       ErrorClass = "provider_validation"
	ErrorAuthentication           ErrorClass = "authentication"
	ErrorAuthorization            ErrorClass = "authorization"
	ErrorQuota                    ErrorClass = "quota"
	ErrorCapacity                 ErrorClass = "capacity"
	ErrorProviderUnavailable      ErrorClass = "provider_unavailable"
	ErrorPluginUnavailable        ErrorClass = "plugin_unavailable"
	ErrorTransientProvider        ErrorClass = "transient_provider"
	ErrorPermanentProvider        ErrorClass = "permanent_provider"
	ErrorTimeout                  ErrorClass = "timeout"
	ErrorCancellation             ErrorClass = "cancellation"
	ErrorPartialCleanup           ErrorClass = "partial_cleanup"
	ErrorStateVersionIncompatible ErrorClass = "state_version_incompatible"
)

// SafeDetail is explicitly safe to expose to an authorized API caller. Raw
// provider output and secret material must never be placed here.
type SafeDetail struct {
	Key   string
	Value string
}

// DomainError is the transport-neutral error contract. Causes are domain
// errors rather than arbitrary errors so Error cannot accidentally disclose a
// provider response, SQL statement, credential, or other unsafe detail.
type DomainError struct {
	Code          string
	Class         ErrorClass
	Message       string
	ResourceID    LogicalResourceID
	ReferencePath string
	FieldPath     string
	Retryable     bool
	RetryAfter    time.Duration
	SafeDetails   []SafeDetail
	CorrelationID string
	Causes        []*DomainError
}

func (e *DomainError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Code == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func invalidTransition(machine, from, to string) *DomainError {
	return &DomainError{
		Code:    "invalid_state_transition",
		Class:   ErrorInvalidDeclaration,
		Message: fmt.Sprintf("%s cannot transition from %s to %s", machine, from, to),
	}
}
