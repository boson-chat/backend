package handlers

import (
	"context"
	"errors"
	"math"
	stdhttp "net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/boson-chat/backend/internal/clientip"
	"github.com/boson-chat/backend/internal/ratelimit"
	"github.com/boson-chat/backend/pkg/unfurl"
)

// Unfurler is pkg/unfurl's Service.
type Unfurler interface {
	Card(ctx context.Context, raw string, beforeFetch func() error) (*unfurl.Card, error)
	Spotify(ctx context.Context, raw string, beforeFetch func() error) (*unfurl.SpotifyInfo, error)
}

// UnfurlHandler serves link previews to clients that can't fetch other
// sites themselves (the web client, under CORS), in the shapes the desktop
// shell's fetchers produce:
//
//	GET /v1/unfurl?url=…          → a site card (OpenGraph; YouTube titles)
//	GET /v1/unfurl/spotify?url=…  → a Spotify card
//
// 200 with the card; 404 no_preview (nothing to show: not found, not HTML,
// unreachable); 400 url_not_allowed (not a public http(s) URL on 80/443,
// or not a Spotify link); 429 rate_limited with Retry-After. No sign-in
// needed: anonymous callers are limited per address, callers with a valid
// access token per user. Cached answers don't count.
type UnfurlHandler struct {
	svc      Unfurler
	ipLimit  *ratelimit.Limiter
	usrLimit *ratelimit.Limiter
	clientIP func(*stdhttp.Request) netip.Addr
	// subject verifies an access token and returns its sub.
	subject func(token string) (string, bool)
}

func NewUnfurlHandler(svc Unfurler, ipLimit, userLimit *ratelimit.Limiter, ips *clientip.Resolver, subject func(string) (string, bool)) *UnfurlHandler {
	return &UnfurlHandler{svc: svc, ipLimit: ipLimit, usrLimit: userLimit, clientIP: ips.IP, subject: subject}
}

func (h *UnfurlHandler) Register(mux *stdhttp.ServeMux) {
	mux.HandleFunc("GET /v1/unfurl", h.card)
	mux.HandleFunc("GET /v1/unfurl/spotify", h.spotify)
}

type errRateLimited struct{ wait time.Duration }

func (e errRateLimited) Error() string { return "rate limited" }

// limiter spends a token from the caller's bucket: the user's when signed
// in (so people behind one address don't share a budget), else the
// address's.
func (h *UnfurlHandler) limiter(r *stdhttp.Request) func() error {
	user := ""
	if authz := r.Header.Get("Authorization"); strings.HasPrefix(authz, "Bearer ") && h.subject != nil {
		if sub, ok := h.subject(strings.TrimPrefix(authz, "Bearer ")); ok {
			user = sub
		}
	}
	ip := clientip.Key(h.clientIP(r))
	return func() error {
		if user != "" {
			if ok, wait := h.usrLimit.Allow(user); !ok {
				return errRateLimited{wait}
			}
			return nil
		}
		if ok, wait := h.ipLimit.Allow(ip); !ok {
			return errRateLimited{wait}
		}
		return nil
	}
}

func (h *UnfurlHandler) card(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	raw := r.URL.Query().Get("url")
	if raw == "" {
		writeError(w, stdhttp.StatusBadRequest, "missing_url")
		return
	}
	c, err := h.svc.Card(r.Context(), raw, h.limiter(r))
	respondUnfurl(w, c, c == nil, err)
}

func (h *UnfurlHandler) spotify(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	raw := r.URL.Query().Get("url")
	if raw == "" {
		writeError(w, stdhttp.StatusBadRequest, "missing_url")
		return
	}
	i, err := h.svc.Spotify(r.Context(), raw, h.limiter(r))
	respondUnfurl(w, i, i == nil, err)
}

func respondUnfurl(w stdhttp.ResponseWriter, body any, none bool, err error) {
	var limited errRateLimited
	switch {
	case errors.As(err, &limited):
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Max(1, math.Ceil(limited.wait.Seconds())))))
		writeError(w, stdhttp.StatusTooManyRequests, "rate_limited")
	case errors.Is(err, unfurl.ErrBlocked):
		writeError(w, stdhttp.StatusBadRequest, "url_not_allowed")
	case err != nil:
		writeInternalError(w, err)
	case none:
		w.Header().Set("Cache-Control", "private, max-age=60")
		writeError(w, stdhttp.StatusNotFound, "no_preview")
	default:
		w.Header().Set("Cache-Control", "private, max-age=600")
		writeJSON(w, stdhttp.StatusOK, body)
	}
}
