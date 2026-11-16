package hosted

import (
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
)

// Static fallback is deliberately limited to the two template explorer routes.
func templateFallbackPath(p string) bool {
	if strings.ContainsAny(p, "\\\x00") || path.Clean(p) != strings.TrimSuffix(p, "/") {
		return false
	}
	tail := ""
	for _, prefix := range []string{"/admin/templates/", "/labs/"} {
		if strings.HasPrefix(p, prefix) {
			tail = strings.TrimSuffix(strings.TrimPrefix(p, prefix), "/")
			break
		}
	}
	parts := strings.Split(tail, "/")
	if len(parts) < 1 || len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || strings.Contains(part, ".") || strings.HasPrefix(part, "_") || part == "assets" {
			return false
		}
	}
	return true
}

func encodingQuality(header, encoding string) float64 {
	wildcard := -1.0
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		q := 1.0
		for _, field := range fields[1:] {
			key, val, ok := strings.Cut(strings.TrimSpace(field), "=")
			if ok && key == "q" {
				n, err := strconv.ParseFloat(val, 64)
				if err != nil || n < 0 || n > 1 {
					q = 0
				} else {
					q = n
				}
			}
		}
		if strings.EqualFold(fields[0], encoding) {
			return q
		}
		if fields[0] == "*" {
			wildcard = q
		}
	}
	if wildcard >= 0 {
		return wildcard
	}
	return 0
}

func (s *Service) staticHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		http.NotFound(w, r)
		return
	}
	fs := http.Dir(s.Config.WebRoot)
	name := path.Clean("/" + r.URL.Path)
	file, err := fs.Open(name)
	fallback := false
	if os.IsNotExist(err) && templateFallbackPath(r.URL.Path) {
		name = "/200.html"
		file, err = fs.Open(name)
		fallback = true
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	info, err := file.Stat()
	file.Close()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if info.IsDir() {
		name = strings.TrimSuffix(name, "/") + "/index.html"
		f, e := fs.Open(name)
		if e != nil {
			http.NotFound(w, r)
			return
		}
		info, e = f.Stat()
		f.Close()
		if e != nil {
			http.NotFound(w, r)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-cache")
	if !fallback && strings.HasPrefix(name, "/_app/immutable/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	w.Header().Add("Vary", "Accept-Encoding")
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	br, gz := encodingQuality(r.Header.Get("Accept-Encoding"), "br"), encodingQuality(r.Header.Get("Accept-Encoding"), "gzip")
	choices := []struct {
		suffix, encoding string
		quality          float64
	}{{".br", "br", br}, {".gz", "gzip", gz}}
	if gz > br {
		choices[0], choices[1] = choices[1], choices[0]
	}
	for _, choice := range choices {
		if choice.quality <= 0 {
			continue
		}
		compressed, e := fs.Open(name + choice.suffix)
		if e != nil {
			continue
		}
		stat, e := compressed.Stat()
		if e != nil || stat.IsDir() {
			compressed.Close()
			continue
		}
		defer compressed.Close()
		w.Header().Set("Content-Encoding", choice.encoding)
		http.ServeContent(w, r, info.Name(), stat.ModTime(), compressed)
		return
	}
	file, err = fs.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}
