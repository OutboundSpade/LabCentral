package render

import "fmt"

type Stage string

const (
	StageParse               Stage = "parse"
	StageSchema              Stage = "schema"
	StageCELTypecheck        Stage = "cel_typecheck"
	StageCELEvaluation       Stage = "cel_evaluation"
	StageExpansion           Stage = "expansion"
	StageReferenceResolution Stage = "reference_resolution"
	StageGraph               Stage = "graph"
	StageCanonicalization    Stage = "canonicalization"
)

type Error struct {
	Stage   Stage
	Code    string
	Path    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	location := ""
	if e.Path != "" {
		location = " at " + e.Path
	}
	return fmt.Sprintf("%s/%s%s: %s", e.Stage, e.Code, location, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

func renderError(stage Stage, code, path, message string, cause error) *Error {
	return &Error{Stage: stage, Code: code, Path: path, Message: message, Cause: cause}
}
