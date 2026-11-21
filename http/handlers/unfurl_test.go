package handlers

import (
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/boson-chat/backend/internal/clientip"
	"github.com/boson-chat/backend/internal/ratelimit"
	"github.com/boson-chat/backend/pkg/unfurl"
)

// fakeUnfurler answers from a table; every call is a cache miss (it calls
// beforeFetch) unless the URL is in cached.
type fakeUnfurler struct {
	cards  map[string]*unfurl.Card
	cached map[string]bool
}

func (f *fakeUnfurler) Card(_ context.Context, raw string, before func() error) (*unfurl.Card, error) {
	if raw == "http://127.0.0.1/" {
		return nil, unfurl.ErrBlocked
	}
	if !f.cached[raw] {
		if err := before(); err != nil {
			return nil, err
		}
	}
	return f.cards[raw], nil
}

func (f *fakeUnfurler) Spotify(_ context.Context, raw string, before func() error) (*unfurl.SpotifyInfo, error) {
	if raw != "https://open.spotify.com/track/T1" {
		return nil, unfurl.ErrBlocked
	}
	if err := before(); err != nil {
		return nil, err
	}
	return &unfurl.SpotifyInfo{URL: raw, Type: "track", Title: "Solo"}, nil
}

func unfurlReq(h stdhttp.Handler, path, raw, remote, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path+"?url="+url.QueryEscape(raw), nil)
	req.RemoteAddr = remote
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestUnfurlHandler(t *testing.T) {
	f := &fakeUnfurler{
		cards:  map[string]*unfurl.Card{"https://example.com/a": {URL: "https://example.com/a", Title: "A"}, "https://example.com/hot": {URL: "https://example.com/hot", Title: "Hot"}},
		cached: map[string]bool{"https://example.com/hot": true},
	}
	subject := func(tok string) (string, bool) { return "usr_" + tok, tok == "good" }
	h := NewUnfurlHandler(f, ratelimit.New(3), ratelimit.New(2), clientip.New(""), subject)
	mux := stdhttp.NewServeMux()
	h.Register(mux)

	rr := unfurlReq(mux, "/v1/unfurl", "https://example.com/a", "203.0.113.1:1", "")
	require.Equal(t, 200, rr.Code)
	var c unfurl.Card
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &c))
	assert.Equal(t, "A", c.Title)
	assert.Equal(t, "private, max-age=600", rr.Header().Get("Cache-Control"))

	assert.Equal(t, 404, unfurlReq(mux, "/v1/unfurl", "https://example.com/none", "203.0.113.1:1", "").Code)
	rr = unfurlReq(mux, "/v1/unfurl", "http://127.0.0.1/", "203.0.113.1:1", "")
	assert.Equal(t, 400, rr.Code)
	assert.JSONEq(t, `{"error":"url_not_allowed"}`, rr.Body.String())
	assert.Equal(t, 400, unfurlReq(mux, "/v1/unfurl", "", "203.0.113.1:1", "").Code)

	// Third fetch from this address: the last token. Then 429.
	assert.Equal(t, 200, unfurlReq(mux, "/v1/unfurl", "https://example.com/a", "203.0.113.1:1", "").Code)
	rr = unfurlReq(mux, "/v1/unfurl", "https://example.com/a", "203.0.113.1:1", "")
	assert.Equal(t, 429, rr.Code)
	assert.NotEmpty(t, rr.Header().Get("Retry-After"))
	// Cached answers aren't limited.
	assert.Equal(t, 200, unfurlReq(mux, "/v1/unfurl", "https://example.com/hot", "203.0.113.1:1", "").Code)
	// Another address has its own budget.
	assert.Equal(t, 200, unfurlReq(mux, "/v1/unfurl", "https://example.com/a", "203.0.113.2:1", "").Code)

	// A signed-in user: their own limit (2), whatever the address, even
	// one whose anonymous budget is spent.
	assert.Equal(t, 200, unfurlReq(mux, "/v1/unfurl/spotify", "https://open.spotify.com/track/T1", "203.0.113.1:1", "good").Code)
	assert.Equal(t, 200, unfurlReq(mux, "/v1/unfurl/spotify", "https://open.spotify.com/track/T1", "198.51.100.1:1", "good").Code)
	assert.Equal(t, 429, unfurlReq(mux, "/v1/unfurl/spotify", "https://open.spotify.com/track/T1", "198.51.100.3:1", "good").Code)
	// A bad token is just anonymous (the address's budget).
	assert.Equal(t, 200, unfurlReq(mux, "/v1/unfurl/spotify", "https://open.spotify.com/track/T1", "198.51.100.3:1", "bad").Code)
	assert.Equal(t, 400, unfurlReq(mux, "/v1/unfurl/spotify", "https://evil.example/track/T1", "198.51.100.3:1", "").Code)
}
