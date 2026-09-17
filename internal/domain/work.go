package domain

import "fmt"

// WorkSubject distinguishes non-instance work without manufacturing lab IDs.
// These are core-owned operation kinds, not a plugin-defined workflow language.
type WorkSubjectKind string

const (
	WorkInstance    WorkSubjectKind = "instance"
	WorkSource      WorkSubjectKind = "source"
	WorkBuild       WorkSubjectKind = "build"
	WorkImageImport WorkSubjectKind = "image-import"
	WorkPreparation WorkSubjectKind = "preparation"
)

type WorkSubject struct {
	Kind WorkSubjectKind
	ID   string
}

func (s WorkSubject) Validate() error {
	switch s.Kind {
	case WorkInstance, WorkSource, WorkBuild, WorkImageImport, WorkPreparation:
	default:
		return fmt.Errorf("unsupported work subject")
	}
	if _, err := NewOperationID(s.ID); err != nil {
		return fmt.Errorf("invalid work subject ID: %w", err)
	}
	return nil
}
