// Package origins decides which browser origins (web pages) may call the
// API (CORS). ALLOWED_ORIGINS is a comma-separated list of:
//
//   - origins such as https://chat.example.org;
//   - "directory": the origins in the list at ORIGINS_URL, the auth
//     service's GET /v1/web-origins (this install's web client and those
//     of the Boson servers verified in the directories it follows),
//     fetched at start and every ORIGINS_REFRESH.
//
// Entries can be mixed ("directory,https://chat.example.org"): the fixed
// ones are allowed even before the first fetch or while auth is down. An
// empty list allows no page. There is no wildcard: the API always echoes
// the one allowed origin, never "*".
//
// Origins compare normalised: scheme and host lower-case, the default port
// and a trailing slash dropped.
//
// Same package as boson-chat/files, quicvoice and quicvideo, minus their
// "*" entry (the API never allowed any page).
package origins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/boson-chat/backend/internal/logger"
)

// Directory is the list entry for the fetched list.
const Directory = "directory"

// Normalize returns origin as scheme://host[:port] (lower-case, default
// port dropped, no path or trailing slash, no trailing dot), or "" when it
// isn't an http(s) origin.
func Normalize(origin string) string {
	u, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return ""
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || strings.ContainsAny(host, " /@*") {
		return ""
	}
	if strings.Contains(host, ":") { // IPv6 literal
		host = "[" + host + "]"
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return ""
		}
		if !(scheme == "https" && n == 443) && !(scheme == "http" && n == 80) {
			host += ":" + strconv.Itoa(n)
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return ""
	}
	return scheme + "://" + host
}

// UsesDirectory reports whether a list has the "directory" entry.
func UsesDirectory(list string) bool {
	for _, e := range strings.Split(list, ",") {
		if strings.EqualFold(strings.TrimSpace(e), Directory) {
			return true
		}
	}
	return false
}

// Policy is one parsed list.
type Policy struct {
	fixed map[string]bool
	dir   *Fetcher
}

// Parse parses a list. Its "directory" entry uses dir; with a nil dir
// that entry allows nothing more than the fixed ones. Entries that aren't
// origins ("*" included) are skipped and returned in invalid, so the
// caller can warn about them: an install with a stray entry keeps starting,
// as it did before this package.
func Parse(list string, dir *Fetcher) (p *Policy, invalid []string) {
	p = &Policy{fixed: map[string]bool{}}
	for _, e := range strings.Split(list, ",") {
		e = strings.TrimSpace(e)
		switch {
		case e == "":
		case strings.EqualFold(e, Directory):
			p.dir = dir
		default:
			if o := Normalize(e); o != "" {
				p.fixed[o] = true
			} else {
				invalid = append(invalid, e)
			}
		}
	}
	return p, invalid
}

// Match reports whether a request's Origin header value may call, and
// whether it is one of the fixed entries (as opposed to only being in the
// directory's list). An empty or malformed origin never matches.
func (p *Policy) Match(origin string) (allowed, fixed bool) {
	if p == nil {
		return false, false
	}
	o := Normalize(origin)
	if o == "" {
		return false, false
	}
	if p.fixed[o] {
		return true, true
	}
	return p.dir != nil && p.dir.Allowed(o), false
}

// Allowed reports whether a request's Origin header value may call.
func (p *Policy) Allowed(origin string) bool {
	ok, _ := p.Match(origin)
	return ok
}

// Fetcher keeps the list from ORIGINS_URL ({"origins": [...]}).
type Fetcher struct {
	url      string
	interval time.Duration
	retry    time.Duration
	client   *http.Client
	set      atomic.Pointer[map[string]bool]
}

// Fetch defaults.
const (
	DefaultInterval = 5 * time.Minute
	retryInterval   = 30 * time.Second
	fetchTimeout    = 10 * time.Second
	maxBody         = 2 << 20
	maxOrigins      = 10000
)

// NewFetcher fetches url every interval (DefaultInterval when <= 0; every
// 30 s, or interval if shorter, until a fetch succeeds and after a failure).
// client nil = a client with a 10 s timeout.
func NewFetcher(url string, interval time.Duration, client *http.Client) *Fetcher {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}
	return &Fetcher{url: url, interval: interval, retry: min(retryInterval, interval), client: client}
}

// Allowed reports whether the normalised origin is in the last good list.
func (f *Fetcher) Allowed(origin string) bool {
	m := f.set.Load()
	return m != nil && (*m)[origin]
}

// Len is the size of the last good list (0 before the first fetch).
func (f *Fetcher) Len() int {
	if m := f.set.Load(); m != nil {
		return len(*m)
	}
	return 0
}

// Loaded reports whether a fetch has succeeded.
func (f *Fetcher) Loaded() bool { return f.set.Load() != nil }

// Run fetches at once and then on the interval until ctx is done. A failed
// fetch keeps the last good list.
func (f *Fetcher) Run(ctx context.Context) {
	l := logger.FromCtx(ctx)
	for {
		wait := f.interval
		n, err := f.Refresh(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			wait = f.retry
			if f.Loaded() {
				l.Warn().Err(err).Str("url", f.url).Int("origins", f.Len()).Msg("web origins: fetch failed, keeping the last list")
			} else {
				l.Warn().Err(err).Str("url", f.url).Msg("web origins: fetch failed, only the fixed origins are allowed until it works")
			}
		default:
			l.Debug().Str("url", f.url).Int("origins", n).Msg("web origins: list fetched")
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// Refresh fetches the list once; on success it replaces the current one.
func (f *Fetcher) Refresh(ctx context.Context) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
		return 0, fmt.Errorf("GET %s: HTTP %d", f.url, resp.StatusCode)
	}
	var body struct {
		Origins *[]string `json:"origins"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&body); err != nil {
		return 0, fmt.Errorf("GET %s: %w", f.url, err)
	}
	if body.Origins == nil {
		return 0, errors.New("GET " + f.url + `: no "origins" list`)
	}
	m := make(map[string]bool, len(*body.Origins))
	for _, o := range *body.Origins {
		if n := Normalize(o); n != "" && len(m) < maxOrigins {
			m[n] = true
		}
	}
	f.set.Store(&m)
	return len(m), nil
}
