package hosted

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var desktopPathID = regexp.MustCompile(`^[a-f0-9]{24}$`)

func desktopPath(id string) string       { return "/sessions/" + id }
func desktopCookieName(id string) string { return "__Host-labcentral_desktop_" + id }

// Each window has its own authenticated URL namespace and host-only cookie.
// Opening another machine must not change which guest existing tabs address.
func desktopRequest(r *http.Request) (*http.Request, string, string, error) {
	if !strings.HasPrefix(r.URL.Path, "/sessions/") {
		// DCV's documented query callback identifies root-path authentication
		// and channel requests without changing the guest's DCV web-url-path.
		q := r.URL.Query()
		if values, present := q["labcentralDesktop"]; present {
			if len(values) != 1 || !desktopPathID.MatchString(values[0]) {
				return r, "", "", fmt.Errorf("invalid desktop selector")
			}
			id := values[0]
			q.Del("labcentralDesktop")
			cloned := r.Clone(r.Context())
			u := *r.URL
			u.RawQuery = q.Encode()
			cloned.URL = &u
			return cloned, id, "", nil
		}
		return r, "", "", nil
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/sessions/"), "/", 2)
	if len(parts) != 2 || !desktopPathID.MatchString(parts[0]) {
		return r, "", "", fmt.Errorf("invalid desktop path")
	}
	id := parts[0]
	q := r.URL.Query()
	if values, present := q["labcentralDesktop"]; present {
		if len(values) != 1 || values[0] != id {
			return r, "", "", fmt.Errorf("conflicting desktop selector")
		}
		q.Del("labcentralDesktop")
	}
	cloned := r.Clone(r.Context())
	u := *r.URL
	u.Path, u.RawPath = "/"+parts[1], ""
	u.RawQuery = q.Encode()
	cloned.URL = &u
	return cloned, id, desktopPath(id), nil
}

func desktopDocument(source, base string) string {
	if base == "" {
		return source
	}
	return strings.NewReplacer(`href="/`, `href="`+base+`/`, `src="/`, `src="`+base+`/`).Replace(source)
}

func desktopProgram(source, base string) string {
	if base == "" {
		return source
	}
	return strings.NewReplacer("from '/", "from '"+base+"/", "fetch('/", "fetch('"+base+"/", "import('/", "import('"+base+"/", `"/lesson`, `"`+base+`/lesson`).Replace(source)
}
