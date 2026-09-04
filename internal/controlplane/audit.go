package controlplane

import (
	"bytes"
	"context"
	"fmt"

	"github.com/OutboundSpade/LabCentral/internal/application"
)

func (s *Service) ListAuditRecords(ctx context.Context, filter application.AuditRecordFilter) (application.AuditRecordPage, error) {
	if s == nil || s.store == nil {
		return application.AuditRecordPage{}, fmt.Errorf("audit store is unavailable")
	}
	return s.store.ListAuditRecords(ctx, filter)
}

func (s *Service) ListEventRecords(ctx context.Context, filter application.EventRecordFilter) (application.EventRecordPage, error) {
	if s == nil || s.store == nil {
		return application.EventRecordPage{}, fmt.Errorf("event store is unavailable")
	}
	return s.store.ListEventRecords(ctx, filter)
}

func (s *Service) ExportAuditRecords(ctx context.Context, request application.AuditExportRequest) (application.AuditExportResult, []byte, error) {
	if s == nil || s.store == nil {
		return application.AuditExportResult{}, nil, fmt.Errorf("audit store is unavailable")
	}
	var output bytes.Buffer
	result, err := s.store.ExportAuditRecords(ctx, request, &output)
	return result, output.Bytes(), err
}
