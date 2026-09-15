package application

import (
	"context"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"time"
)

type OperationQuery struct {
	ProjectID domain.ProjectID
	Filter    string
	AfterTime time.Time
	AfterID   domain.OperationID
	Limit     uint32
}
type OperationPage struct {
	Items                   []OperationRecord
	Queued, Running, Failed uint64
	HasMore                 bool
}
type OperationsReader interface {
	ReadOperations(context.Context, OperationQuery) (OperationPage, error)
}
