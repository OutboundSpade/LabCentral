package domain

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// IDs are opaque values. Their concrete representation is intentionally not
// exposed to persistence, transport, or provider concerns.
type (
	UserID            string
	OrganizationID    string
	ProjectID         string
	TeamID            string
	ProviderTargetID  string
	TemplateID        string
	RevisionID        string
	InstanceID        string
	LogicalResourceID string
	OperationID       string
	InvocationID      string
	SnapshotID        string
	AccessSessionID   string
	UploadID          string
)

type (
	Generation      uint64
	ResourceVersion uint64
	FencingToken    uint64
	EventSequence   uint64
)

type Name string
type CapabilityID string
type ImageRef string
type ArtifactRef string
type OwnershipID string
type SHA256Digest string
type ByteQuantity uint64

var (
	opaqueIDPattern          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	logicalResourceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}\.[a-z][a-z0-9-]{0,62}(\[[A-Za-z0-9._-]{1,128}\])?$`)
	namePattern              = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
)

func validateOpaqueID(kind, value string) error {
	if !opaqueIDPattern.MatchString(value) {
		return fmt.Errorf("%s must be 1-128 URL-safe opaque characters", kind)
	}
	return nil
}

func NewUserID(value string) (UserID, error) {
	return UserID(value), validateOpaqueID("user ID", value)
}

func NewOrganizationID(value string) (OrganizationID, error) {
	return OrganizationID(value), validateOpaqueID("organization ID", value)
}

func NewProjectID(value string) (ProjectID, error) {
	return ProjectID(value), validateOpaqueID("project ID", value)
}

func NewTeamID(value string) (TeamID, error) {
	return TeamID(value), validateOpaqueID("team ID", value)
}

func NewProviderTargetID(value string) (ProviderTargetID, error) {
	return ProviderTargetID(value), validateOpaqueID("provider target ID", value)
}

func NewTemplateID(value string) (TemplateID, error) {
	return TemplateID(value), validateOpaqueID("template ID", value)
}

func NewRevisionID(value string) (RevisionID, error) {
	return RevisionID(value), validateOpaqueID("revision ID", value)
}

func NewInstanceID(value string) (InstanceID, error) {
	return InstanceID(value), validateOpaqueID("instance ID", value)
}

func NewLogicalResourceID(value string) (LogicalResourceID, error) {
	return LogicalResourceID(value), validateLogicalResourceID(value)
}

func validateLogicalResourceID(value string) error {
	if !logicalResourceIDPattern.MatchString(value) {
		return fmt.Errorf("logical resource ID must be a qualified canonical resource key")
	}
	return nil
}

func NewOperationID(value string) (OperationID, error) {
	return OperationID(value), validateOpaqueID("operation ID", value)
}

func NewInvocationID(value string) (InvocationID, error) {
	return InvocationID(value), validateOpaqueID("invocation ID", value)
}

func NewSnapshotID(value string) (SnapshotID, error) {
	return SnapshotID(value), validateOpaqueID("snapshot ID", value)
}

func NewAccessSessionID(value string) (AccessSessionID, error) {
	return AccessSessionID(value), validateOpaqueID("access session ID", value)
}

func NewUploadID(value string) (UploadID, error) {
	return UploadID(value), validateOpaqueID("upload ID", value)
}

func NewName(value string) (Name, error) {
	if !namePattern.MatchString(value) {
		return "", fmt.Errorf("name must match %s", namePattern)
	}
	return Name(value), nil
}

func NewCapabilityID(value string) (CapabilityID, error) {
	if value == "" || len(value) > 256 {
		return "", fmt.Errorf("capability ID must be 1-256 characters")
	}
	return CapabilityID(value), nil
}

func NewImageRef(value string) (ImageRef, error) {
	if err := validateTypedReference("image reference", value); err != nil {
		return "", err
	}
	return ImageRef(value), nil
}

func NewArtifactRef(value string) (ArtifactRef, error) {
	if err := validateTypedReference("artifact reference", value); err != nil {
		return "", err
	}
	return ArtifactRef(value), nil
}

func validateTypedReference(kind, value string) error {
	if value == "" || len(value) > 2048 || strings.TrimSpace(value) != value || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s must be a non-empty, trimmed string of at most 2048 bytes", kind)
	}
	return nil
}

// ParseSHA256Digest accepts a bare lowercase hexadecimal digest or the
// conventional sha256: form and stores only the normalized hexadecimal value.
func ParseSHA256Digest(value string) (SHA256Digest, error) {
	normalized := strings.TrimPrefix(value, "sha256:")
	if len(normalized) != 64 || strings.ToLower(normalized) != normalized {
		return "", fmt.Errorf("SHA-256 digest must contain 64 lowercase hexadecimal characters")
	}
	if _, err := hex.DecodeString(normalized); err != nil {
		return "", fmt.Errorf("invalid SHA-256 digest: %w", err)
	}
	return SHA256Digest(normalized), nil
}

func (d SHA256Digest) Hex() string { return string(d) }

func (d SHA256Digest) String() string {
	if d == "" {
		return ""
	}
	return "sha256:" + string(d)
}

func NewByteQuantity(value uint64) (ByteQuantity, error) {
	if value == 0 {
		return 0, fmt.Errorf("byte quantity must be greater than zero")
	}
	return ByteQuantity(value), nil
}
