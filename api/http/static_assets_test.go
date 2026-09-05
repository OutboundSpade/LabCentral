package httpapi

import (
	"mime"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	webassets "github.com/OutboundSpade/LabCentral/web"
)

func TestEmbeddedUIBootAssetsAreServed(t *testing.T) {
	handler := staticHandler(webassets.FS())
	index := httptest.NewRecorder()
	handler.ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/", nil))
	if index.Code != http.StatusOK {
		t.Fatalf("UI index status = %d", index.Code)
	}
	assets := regexp.MustCompile(`(?:href|src)="\.?(/_app/[^\"]+)"`).FindAllStringSubmatch(index.Body.String(), -1)
	if len(assets) == 0 {
		t.Fatal("UI index contains no boot assets; cannot verify browser startup")
	}
	for _, asset := range assets {
		t.Run(asset[1], func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, asset[1], nil))
			if response.Code != http.StatusOK || response.Body.Len() == 0 {
				t.Fatalf("embedded boot asset status = %d, bytes = %d", response.Code, response.Body.Len())
			}
			contentType, _, err := mime.ParseMediaType(response.Header().Get("Content-Type"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(asset[1], ".js") && contentType != "text/javascript" && contentType != "application/javascript" {
				t.Fatalf("JavaScript boot asset served as %q", contentType)
			}
			if strings.HasSuffix(asset[1], ".css") && contentType != "text/css" {
				t.Fatalf("stylesheet served as %q", contentType)
			}
		})
	}
}
