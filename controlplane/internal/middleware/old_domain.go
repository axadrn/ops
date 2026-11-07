package middleware

import (
	"net"
	"net/http"
	"slices"
	"strings"
)

// OldDomainHeader tells the CLI the control plane's address when a request
// came on an old one; the CLI moves its config there.
const OldDomainHeader = "X-Control-Plane-URL"

// OldDomain serves the old domains of the control plane (OLD_URLS) next
// to APP_URL. It counts every request on them by kind. A browser asking for
// a page goes on to the same path on APP_URL; everything a machine calls
// (the API, agents, downloads, install scripts, webhooks) is answered where
// it is, so nothing breaks before it moves, and API answers name the new
// address in OldDomainHeader.
func OldDomain(hosts []string, appURL string, count func(host, kind string)) func(http.Handler) http.Handler {
	appURL = strings.TrimRight(appURL, "/")
	return func(next http.Handler) http.Handler {
		if len(hosts) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := requestHost(r)
			if !slices.Contains(hosts, host) {
				next.ServeHTTP(w, r)
				return
			}
			kind := oldDomainKind(r)
			count(host, kind)
			if kind == "web" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
				http.Redirect(w, r, appURL+r.URL.RequestURI(), http.StatusMovedPermanently)
				return
			}
			w.Header().Set(OldDomainHeader, appURL)
			next.ServeHTTP(w, r)
		})
	}
}

func requestHost(r *http.Request) string {
	host := r.Host
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	return strings.ToLower(host)
}

// oldDomainKind sorts a request on an old domain.
func oldDomainKind(r *http.Request) string {
	path := r.URL.Path
	switch {
	case strings.HasPrefix(path, "/agent/"):
		return "agent"
	case strings.HasPrefix(path, "/api/") || path == "/mcp":
		return "api"
	case strings.HasPrefix(path, "/download/") || path == "/install.sh" || path == "/cli.sh":
		return "download"
	case strings.HasPrefix(path, "/webhooks/"):
		return "webhook"
	case r.Method != http.MethodGet && r.Method != http.MethodHead:
		// A form posted from a page loaded before the move.
		return "form"
	}
	return "web"
}
