package localdb

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"testing"
	"time"
)

func TestSQLiteOperationsPage(t *testing.T) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:operations-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	runOperationsPage(t, s)
}
func TestPostgreSQLOperationsPage(t *testing.T) {
	s := newPostgreSQLQuotaTestStore(t, "operation_pages")
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	runOperationsPage(t, s)
}
func runOperationsPage(t *testing.T, s *Store) {
	ctx := context.Background()
	setupQuotaTestTenancy(t, s)
	for _, item := range []struct{ id, project string }{{"inst.ops-a", "project.beta"}, {"inst.ops-b", "project.beta"}, {"inst.ops-hidden", "project.gamma"}} {
		if _, err := s.CreateInstance(ctx, quotaTestCreateRequest(item.id, domain.ProjectID(item.project), 3600)); err != nil {
			t.Fatal(err)
		}
	}
	q := application.OperationQuery{ProjectID: "project.beta", Filter: "active", Limit: 1}
	first, err := s.ReadOperations(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || !first.HasMore || first.Queued != 2 || first.Running != 0 || first.Failed != 0 {
		t.Fatalf("first: %+v", first)
	}
	q.AfterTime = first.Items[0].CreatedAt
	q.AfterID = first.Items[0].ID
	last, err := s.ReadOperations(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Items) != 1 || last.HasMore || last.Items[0].ID == q.AfterID || last.Items[0].InstanceID == "inst.ops-hidden" {
		t.Fatalf("last: %+v", last)
	}
	q.Filter = "failed"
	q.AfterTime = first.Items[0].CreatedAt
	failed, err := s.ReadOperations(ctx, q)
	if err != nil || len(failed.Items) != 0 {
		t.Fatalf("failure filter: %+v %v", failed, err)
	}
	q.Limit = 201
	if _, err := s.ReadOperations(ctx, q); err == nil {
		t.Fatal("unbounded query")
	}
}
