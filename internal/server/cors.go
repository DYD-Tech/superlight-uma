package server

import (
	"net/http"
	"strings"
)

// originMatcher matches request origins against configured patterns:
//
//	"*"                       any origin
//	"https://stats.example"   exact origin
//	"https://*.pages.dev"     any subdomain (one or more labels) of pages.dev over https
type originMatcher struct {
	any      bool
	exact    map[string]bool
	wildcard []wildcardOrigin
}

type wildcardOrigin struct{ prefix, suffix string } // "https://", ".pages.dev"

func newOriginMatcher(patterns []string) *originMatcher {
	m := &originMatcher{exact: map[string]bool{}}
	for _, p := range patterns {
		p = strings.TrimRight(strings.TrimSpace(p), "/")
		switch {
		case p == "":
		case p == "*":
			m.any = true
		case strings.Contains(p, "://*."):
			scheme, host, _ := strings.Cut(p, "://*")
			m.wildcard = append(m.wildcard, wildcardOrigin{prefix: scheme + "://", suffix: strings.ToLower(host)})
		default:
			m.exact[strings.ToLower(p)] = true
		}
	}
	return m
}

func (m *originMatcher) enabled() bool {
	return m.any || len(m.exact) > 0 || len(m.wildcard) > 0
}

func (m *originMatcher) allowed(origin string) bool {
	if origin == "" || origin == "null" {
		return false
	}
	if m.any {
		return true
	}
	o := strings.ToLower(origin)
	if m.exact[o] {
		return true
	}
	for _, w := range m.wildcard {
		if !strings.HasPrefix(o, w.prefix) || !strings.HasSuffix(o, w.suffix) {
			continue
		}
		label := o[len(w.prefix) : len(o)-len(w.suffix)]
		if label != "" && validHostLabels(label) {
			return true
		}
	}
	return false
}

// validHostLabels accepts "a", "a.b" made of [a-z0-9-]; no ports, paths or empty labels.
func validHostLabels(s string) bool {
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// cors wraps h with CORS handling for allowed origins. Preflight requests are
// answered directly (before auth), so browsers can send Authorization headers.
// Only headers are used for auth (no cookies), so credentials are never allowed.
func (s *Server) cors(h http.Handler) http.Handler {
	m := newOriginMatcher(s.cfg.CORSOrigins)
	if !m.enabled() {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			h.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Origin")
		ok := m.allowed(origin)
		if ok {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.Header().Add("Vary", "Access-Control-Request-Method")
			w.Header().Add("Vary", "Access-Control-Request-Headers")
			if !ok {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}
