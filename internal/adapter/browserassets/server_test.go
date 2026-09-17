package browserassets

import (
	"github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/manifest"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSnapshotOriginAndIsolationHeaders(t *testing.T) {
	bytes := []byte("verified")
	packages := map[string]map[string]manifest.AssetSnapshot{"panel": {"index.html": {Content: bytes, MediaType: "text/html", Digest: "identity"}}}
	handler, err := Handler("https://assets.example.test", "https://app.example.test", packages)
	if err != nil {
		t.Fatal(err)
	}
	bytes[0] = 'X'
	delete(packages, "panel")
	request := httptest.NewRequest("GET", "https://assets.example.test/panel/index.html", nil)
	request.Header.Set("Cookie", "secret=canary")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "verified" {
		t.Fatalf("snapshot changed: %s", response.Body.String())
	}
	if response.Header().Get("Set-Cookie") != "" || response.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("credential-bearing asset response")
	}
	csp := response.Header().Get("Content-Security-Policy")
	for _, directive := range []string{"sandbox allow-scripts;", "connect-src 'none'", "frame-src 'none'", "form-action 'none'", "frame-ancestors https://app.example.test"} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("missing %s", directive)
		}
	}
	if strings.Contains(csp, "allow-same-origin") {
		t.Fatal("frame regained origin privileges")
	}
	for _, target := range []string{"https://app.example.test/panel/index.html", "https://assets.example.test/panel/index.html?code=1", "https://assets.example.test/panel/../index.html", "https://assets.example.test/panel/missing"} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", target, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("accepted %s", target)
		}
	}
}
func TestRejectSharedAssetOrigin(t *testing.T) {
	if _, err := Handler("https://app.example.test:8443", "https://app.example.test", nil); err == nil {
		t.Fatal("shared origin accepted")
	}
}
