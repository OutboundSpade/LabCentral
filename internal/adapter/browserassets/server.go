// Package browserassets serves only verified browser package snapshots. It has
// no access to sessions, provider credentials, APIs, or mutable filesystem data.
package browserassets

import (
	"fmt"
	"github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/manifest"
	"net/http"
	"net/url"
	"path"
	"strings"
)

func Handler(origin, coreOrigin string, packages map[string]map[string]manifest.AssetSnapshot) (http.Handler, error) {
	asset, err := url.Parse(origin)
	if err != nil || asset.Host == "" || asset.User != nil || asset.Path != "" || asset.RawQuery != "" || asset.Fragment != "" || (asset.Scheme != "https" && asset.Scheme != "http") {
		return nil, fmt.Errorf("invalid asset origin")
	}
	core, err := url.Parse(coreOrigin)
	if err != nil || core.Host == "" || core.User != nil || core.Path != "" || core.RawQuery != "" || core.Fragment != "" || (core.Scheme != "https" && core.Scheme != "http") || asset.Hostname() == core.Hostname() {
		return nil, fmt.Errorf("browser assets require a separate cookie hostname")
	}
	// Own all bytes so later callers cannot mutate an authenticated snapshot.
	frozen := map[string]map[string]manifest.AssetSnapshot{}
	for id, files := range packages {
		frozen[id] = map[string]manifest.AssetSnapshot{}
		for name, file := range files {
			file.Content = append([]byte(nil), file.Content...)
			frozen[id][name] = file
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Access-Control-Allow-Origin", "*") // Public, immutable assets; never credentials.
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; default-src 'none'; script-src "+origin+"; style-src "+origin+" 'unsafe-inline'; img-src "+origin+" data:; font-src "+origin+"; connect-src 'none'; frame-src 'none'; worker-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors "+coreOrigin)
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		if r.Host != asset.Host || (r.Method != http.MethodGet && r.Method != http.MethodHead) || r.URL.RawQuery != "" {
			http.NotFound(w, r)
			return
		}
		pieces := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
		if len(pieces) != 2 || pieces[1] != path.Clean(pieces[1]) {
			http.NotFound(w, r)
			return
		}
		file, ok := frozen[pieces[0]][pieces[1]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", file.MediaType)
		w.Header().Set("ETag", `"`+file.Digest+`"`)
		w.Header().Set("Cache-Control", "no-cache")
		if r.Method == http.MethodGet {
			_, _ = w.Write(file.Content)
		}
	}), nil
}
