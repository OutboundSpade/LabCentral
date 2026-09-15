package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOperationsAPIEmptyPage(t *testing.T) {
	server, _ := auditTestServer(t)
	r := httptest.NewRecorder()
	server.Handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/admin/operations", nil))
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"queued":0`) || !strings.Contains(r.Body.String(), `"items":[]`) {
		t.Fatalf("empty page %d %s", r.Code, r.Body.String())
	}
}
