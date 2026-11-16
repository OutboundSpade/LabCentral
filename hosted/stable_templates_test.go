package hosted

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
)

func TestTemplateLatestAndRevisionVisibility(t *testing.T) {
	s, v, b := stageFixture(t)
	ctx := context.Background()
	var original Template
	if err := s.Store.Get(ctx, "template", "pinned", &original); err != nil {
		t.Fatal(err)
	}
	var source any
	if err := s.Store.Get(ctx, "source", "pinned", &source); err != nil {
		t.Fatal(err)
	}
	old, newer := time.Now().Add(-time.Hour), time.Now()
	for i, id := range []string{"older", "head"} {
		template := original
		template.ID = id
		template.CommittedAt = &old
		bundle := b
		if i == 1 {
			template.CommittedAt = &newer
			bundle.Visibility.Audience = "coaches"
		}
		view := instructorView(bundle)
		template.Instructor = &view
		for kind, value := range map[string]any{"template": template, "source": source, "template-instructor": bundle} {
			if err := s.Store.Put(ctx, kind, id, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.Store.Put(ctx, "template-head", original.Name, "head"); err != nil {
		t.Fatal(err)
	}
	// Prime the shared projection as a coach before reading it as a student.
	v.User.Admin = true
	_ = s.Store.Put(ctx, "session", v.ID, v)
	admin := decodeLearning[TemplateRevisionList](t, request(s, "GET", "/api/template-revisions/"+original.Name, "", "", ""), 200)
	if admin.LatestID != "head" || len(admin.Revisions) != 3 || !admin.Revisions[0].Latest {
		t.Fatal(admin)
	}
	v.User.Admin = false
	_ = s.Store.Put(ctx, "session", v.ID, v)
	student := decodeLearning[TemplateRevisionList](t, request(s, "GET", "/api/template-revisions/"+original.Name, "", "", ""), 200)
	if student.LatestID == "head" || len(student.Revisions) != 2 {
		t.Fatal(student)
	}
	catalog := decodeLearning[TemplateCatalog](t, request(s, "GET", "/api/templates?latest=1", "", "", ""), 200)
	if len(catalog.Templates) != 1 || catalog.Templates[0].ID != student.LatestID {
		t.Fatal(catalog)
	}
	all := decodeLearning[TemplateCatalog](t, request(s, "GET", "/api/templates", "", "", ""), 200)
	if len(all.Templates) != 2 {
		t.Fatal(all)
	}
	if request(s, "GET", "/api/template-revisions/missing", "", "", "").Code != 404 {
		t.Fatal("missing name exposed")
	}
	// Changing disclosure invalidates a warm shared object immediately.
	for _, id := range []string{"pinned", "older"} {
		b.Visibility.Audience = "coaches"
		template := original
		template.ID = id
		view := instructorView(b)
		template.Instructor = &view
		_ = s.Store.Put(ctx, "template-instructor", id, b)
		_ = s.Store.Put(ctx, "template", id, template)
	}
	if request(s, "GET", "/api/template-revisions/"+original.Name, "", "", "").Code != 404 {
		t.Fatal("cached private revisions disclosed")
	}
	v.User.Admin = true
	_ = s.Store.Put(ctx, "session", v.ID, v)
	_ = s.Store.Put(ctx, "template-retired-name", original.Name, true)
	if len(decodeLearning[TemplateCatalog](t, request(s, "GET", "/api/templates?latest=1", "", "", ""), 200).Templates) != 0 {
		t.Fatal("retired name listed")
	}
	// Retirement removes discovery, while historical links have the exact-ID rules.
	if request(s, "GET", "/api/templates/head", "", "", "").Code != 200 || request(s, "GET", "/api/template-revisions/"+original.Name, "", "", "").Code != 200 {
		t.Fatal("retained history unavailable")
	}
	r := httptest.NewRequest("GET", s.Config.Origin+"/api/template-revisions/"+original.Name, nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("anonymous revision discovery")
	}
	_ = s.Store.Put(ctx, "site-settings", "practice", practicePolicy{Enabled: false})
	v.User.Admin = false
	_ = s.Store.Put(ctx, "session", v.ID, v)
	if request(s, "GET", "/api/template-revisions/"+original.Name, "", "", "").Code != 403 {
		t.Fatal("practice policy bypassed")
	}
}

func TestStaticTemplateFallbackAndCompressedAssets(t *testing.T) {
	s, _, _ := serviceFor(t)
	files := map[string]string{"200.html": "<html>fallback</html>", "index.html": "home", "labs/existing/index.html": "existing", "_app/immutable/start.js": "javascript", "_app/immutable/start.js.br": "brotli", "_app/immutable/start.js.gz": "gzip"}
	for name, body := range files {
		p := filepath.Join(s.Config.WebRoot, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, method := range []string{"GET", "HEAD"} {
		for _, path := range []string{"/labs/name/", "/labs/name/revision/", "/admin/templates/name/", "/admin/templates/name/revision/", "/labs/name"} {
			w := request(s, method, path, "", "", "")
			if w.Code != 200 || w.Header().Get("Cache-Control") != "no-cache" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
				t.Fatal(method, path, w.Code, w.Header())
			}
			if method == "HEAD" && w.Body.Len() != 0 {
				t.Fatal("HEAD body")
			}
		}
	}
	for _, path := range []string{"/labs/", "/labs/name/revision/extra/", "/admin/templates/name/file.js", "/api/missing/", "/auth/missing/", "/_app/missing.js", "/labs/assets/", "/other/", "/debug/pprof/"} {
		if w := request(s, "GET", path, "", "", ""); w.Code != 404 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	if w := request(s, "POST", "/labs/name/", "", "", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := request(s, "GET", "/labs/existing/", "", "", ""); w.Code != 200 || w.Body.String() != "existing" {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, test := range []struct{ accept, encoding, body string }{{"br, gzip", "br", "brotli"}, {"br;q=0, gzip", "gzip", "gzip"}, {"gzip;q=1, br;q=0.5", "gzip", "gzip"}, {"br;q=0, gzip;q=0", "", "javascript"}, {"*;q=0.5", "br", "brotli"}, {"gzip;q=bogus", "", "javascript"}} {
		r := httptest.NewRequest("GET", s.Config.Origin+"/_app/immutable/start.js", nil)
		r.Header.Set("Accept-Encoding", test.accept)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 || w.Header().Get("Content-Encoding") != test.encoding || w.Body.String() != test.body || !strings.Contains(w.Header().Get("Content-Type"), "javascript") || w.Header().Get("Vary") != "Accept-Encoding" || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
			t.Fatal(test, w.Code, w.Header(), w.Body.String())
		}
	}
	if err := os.Remove(filepath.Join(s.Config.WebRoot, "200.html")); err != nil {
		t.Fatal(err)
	}
	if request(s, "GET", "/labs/name/", "", "", "").Code != 404 {
		t.Fatal("missing fallback")
	}
}

func TestImageSummaryCountsPartialHeadsAndSharedImages(t *testing.T) {
	s, e := templateFixture(t)
	ctx := context.Background()
	// The old revision has cached images but is superseded; these are unused.
	e.images = append(e.images, lcImage("new-web"), lcImage("shared"))
	imageRevision(t, s, "second", "shared", "not-built")
	imageRevision(t, s, "third", "shared")
	for _, id := range []string{"second", "third"} {
		_ = s.Store.Put(ctx, "template-head", id, id)
	}
	summary, _, err := s.imageInventory(ctx)
	if err != nil || summary.Images != 4 || summary.UnusedImages != 2 || summary.Templates != 3 || summary.PartialTemplates != 2 {
		t.Fatal(summary, err)
	}
	// Finished build records must not preserve a superseded image forever.
	e.builds = append(e.builds, buildImage("web"))
	summary, _, err = s.imageInventory(ctx)
	if err != nil || summary.UnusedImages != 2 {
		t.Fatal(summary, err)
	}
}

func lcImage(key string) lc.ImageRecord { return lc.ImageRecord{Key: key, Name: key} }
func buildImage(key string) lc.Command  { return lc.Command{Image: lc.PlannedImage{CacheKey: key}} }
