package middleware

import (
	stdhttp "net/http"

	"github.com/boson-chat/backend/internal/origins"
)

// CORS allows the configured origins to call the API. allowedOrigins is a
// comma-separated list of exact origins (scheme://host[:port], compared
// normalised); wildcards are intentionally rejected so we never echo back
// arbitrary origins. "directory" entries allow nothing here: use
// CORSPolicy with a fetcher for those.
func CORS(allowedOrigins string) func(stdhttp.Handler) stdhttp.Handler {
	p, _ := origins.Parse(allowedOrigins, nil)
	return CORSPolicy(p)
}

// CORSPolicy answers CORS for the pages p allows (ALLOWED_ORIGINS; its
// "directory" entry follows the auth service's web origins). An allowed
// page gets its own origin echoed (never "*", Vary: Origin); other pages
// get no CORS headers, so the browser blocks the call.
//
// The API authenticates with bearer tokens only (no cookies), so
// credentials don't matter to it. Access-Control-Allow-Credentials is still
// sent for the fixed origins, as it always was, so this install's own web
// client keeps working whatever its fetch credentials mode; pages allowed
// only through the directory (other installs' web clients, calling with the
// home bearer token) don't get it.
//
// Every OPTIONS request is answered here (204) and never reaches the routes.
func CORSPolicy(p *origins.Policy) func(stdhttp.Handler) stdhttp.Handler {
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			if origin := r.Header.Get("Origin"); origin != "" {
				h := w.Header()
				h.Add("Vary", "Origin")
				if ok, fixed := p.Match(origin); ok {
					h.Set("Access-Control-Allow-Origin", origin)
					if fixed {
						h.Set("Access-Control-Allow-Credentials", "true")
					}
					h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
					h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
					h.Set("Access-Control-Max-Age", "300")
				}
			}

			if r.Method == stdhttp.MethodOptions {
				w.WriteHeader(stdhttp.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
