package middleware

import (
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/boson-chat/backend/internal/origins"
)

func newCORSHandler(t *testing.T, origins string) stdhttp.Handler {
	t.Helper()
	next := stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		w.WriteHeader(stdhttp.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return CORS(origins)(next)
}

func TestCORS_AllowedOriginEchoed(t *testing.T) {
	h := newCORSHandler(t, "http://localhost:5173,http://localhost:6173")

	req := httptest.NewRequest(stdhttp.MethodGet, "/foo", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, stdhttp.StatusOK, rr.Code)
	assert.Equal(t, "http://localhost:5173", rr.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", rr.Header().Get("Access-Control-Allow-Credentials"))
	assert.Equal(t, "Origin", rr.Header().Get("Vary"))
}

func TestCORS_DisallowedOriginNoEcho(t *testing.T) {
	h := newCORSHandler(t, "http://localhost:5173")

	req := httptest.NewRequest(stdhttp.MethodGet, "/foo", nil)
	req.Header.Set("Origin", "http://evil.example")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, stdhttp.StatusOK, rr.Code, "downstream still runs")
	assert.Empty(t, rr.Header().Get("Access-Control-Allow-Origin"), "must not echo back arbitrary origins")
}

func TestCORS_PreflightShortCircuits(t *testing.T) {
	called := false
	next := stdhttp.HandlerFunc(func(_ stdhttp.ResponseWriter, _ *stdhttp.Request) {
		called = true
	})
	h := CORS("http://localhost:5173")(next)

	req := httptest.NewRequest(stdhttp.MethodOptions, "/foo", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, stdhttp.StatusNoContent, rr.Code)
	assert.False(t, called, "OPTIONS must not reach downstream handlers")
	assert.Equal(t, "http://localhost:5173", rr.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Methods"), "POST")
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Headers"), "Authorization")
}

func TestCORS_NoOriginHeaderPassthrough(t *testing.T) {
	h := newCORSHandler(t, "http://localhost:5173")

	req := httptest.NewRequest(stdhttp.MethodGet, "/foo", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, stdhttp.StatusOK, rr.Code)
	assert.Empty(t, rr.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORS_EmptyAllowedOrigins(t *testing.T) {
	h := newCORSHandler(t, "")

	req := httptest.NewRequest(stdhttp.MethodGet, "/foo", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, stdhttp.StatusOK, rr.Code)
	assert.Empty(t, rr.Header().Get("Access-Control-Allow-Origin"))
}

// directoryHandler is the API's CORS in front of a stub route, with
// ALLOWED_ORIGINS = "directory,<fixed>" and the directory's list already
// fetched from a stand-in auth service.
func directoryHandler(t *testing.T, fixed string, listed ...string) (stdhttp.Handler, *bool) {
	t.Helper()
	body, _ := json.Marshal(map[string][]string{"origins": listed})
	auth := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(auth.Close)
	f := origins.NewFetcher(auth.URL+"/v1/web-origins", time.Hour, nil)
	_, err := f.Refresh(context.Background())
	require.NoError(t, err)
	p, invalid := origins.Parse("directory,"+fixed, f)
	require.Empty(t, invalid)
	called := new(bool)
	next := stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		*called = true
		_, _ = w.Write([]byte("ok"))
	})
	return CORSPolicy(p)(next), called
}

func TestCORSPolicy_DirectoryOriginPreflight(t *testing.T) {
	h, called := directoryHandler(t, "https://chat.boson.chat", "https://chat.boson.ircforever.org")

	req := httptest.NewRequest(stdhttp.MethodOptions, "/v1/me", nil)
	req.Header.Set("Origin", "https://chat.boson.ircforever.org")
	req.Header.Set("Access-Control-Request-Method", "PATCH")
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, stdhttp.StatusNoContent, rr.Code)
	assert.False(t, *called)
	assert.Equal(t, "https://chat.boson.ircforever.org", rr.Header().Get("Access-Control-Allow-Origin"), "echoed, never *")
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Methods"), "PATCH")
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Headers"), "Authorization")
	assert.Empty(t, rr.Header().Get("Access-Control-Allow-Credentials"), "bearer only: no credentials for directory origins")
	assert.Equal(t, "Origin", rr.Header().Get("Vary"))
}

func TestCORSPolicy_DirectoryOriginRequest(t *testing.T) {
	h, called := directoryHandler(t, "https://chat.boson.chat", "https://Chat.InternetNeko.dev/")

	req := httptest.NewRequest(stdhttp.MethodGet, "/v1/me", nil)
	req.Header.Set("Origin", "https://chat.internetneko.dev")
	req.Header.Set("Authorization", "Bearer home-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, stdhttp.StatusOK, rr.Code)
	assert.True(t, *called)
	assert.Equal(t, "https://chat.internetneko.dev", rr.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, rr.Header().Get("Access-Control-Allow-Credentials"))
	assert.Equal(t, "Origin", rr.Header().Get("Vary"))
}

func TestCORSPolicy_FixedOriginKeepsCredentials(t *testing.T) {
	// The home web client is fixed and listed by the directory as well.
	h, _ := directoryHandler(t, "https://chat.boson.chat", "https://chat.boson.chat", "https://chat.boson.ircforever.org")

	for _, m := range []string{stdhttp.MethodGet, stdhttp.MethodOptions} {
		req := httptest.NewRequest(m, "/v1/me", nil)
		req.Header.Set("Origin", "https://chat.boson.chat")
		req.Header.Set("Access-Control-Request-Method", "GET")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		assert.Equal(t, "https://chat.boson.chat", rr.Header().Get("Access-Control-Allow-Origin"), m)
		assert.Equal(t, "true", rr.Header().Get("Access-Control-Allow-Credentials"), m)
	}
}

func TestCORSPolicy_ForeignOrigin(t *testing.T) {
	h, called := directoryHandler(t, "https://chat.boson.chat", "https://chat.boson.ircforever.org")

	pre := httptest.NewRequest(stdhttp.MethodOptions, "/v1/me", nil)
	pre.Header.Set("Origin", "https://evil.example")
	pre.Header.Set("Access-Control-Request-Method", "GET")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, pre)
	assert.Equal(t, stdhttp.StatusNoContent, rr.Code)
	assert.False(t, *called)
	for _, k := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers"} {
		assert.Empty(t, rr.Header().Get(k), "preflight "+k)
	}
	assert.Equal(t, "Origin", rr.Header().Get("Vary"))

	req := httptest.NewRequest(stdhttp.MethodGet, "/v1/me", nil)
	req.Header.Set("Origin", "https://evil.example")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.True(t, *called, "downstream still runs; the browser blocks the read")
	assert.Empty(t, rr.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, rr.Header().Get("Access-Control-Allow-Credentials"))
}

func TestCORSPolicy_DirectoryNotFetchedYet(t *testing.T) {
	// auth down at start: only the fixed origins.
	auth := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		w.WriteHeader(stdhttp.StatusServiceUnavailable)
	}))
	defer auth.Close()
	f := origins.NewFetcher(auth.URL, time.Hour, nil)
	_, err := f.Refresh(context.Background())
	require.Error(t, err)
	p, _ := origins.Parse("directory,https://chat.boson.chat", f)
	h := CORSPolicy(p)(stdhttp.HandlerFunc(func(stdhttp.ResponseWriter, *stdhttp.Request) {}))

	for origin, want := range map[string]string{
		"https://chat.boson.chat":           "https://chat.boson.chat",
		"https://chat.boson.ircforever.org": "",
	} {
		req := httptest.NewRequest(stdhttp.MethodGet, "/v1/me", nil)
		req.Header.Set("Origin", origin)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		assert.Equal(t, want, rr.Header().Get("Access-Control-Allow-Origin"), origin)
	}
}

func TestCORS_NormalisedFixedOrigin(t *testing.T) {
	h := newCORSHandler(t, "https://Chat.Example.org/")
	req := httptest.NewRequest(stdhttp.MethodGet, "/foo", nil)
	req.Header.Set("Origin", "https://chat.example.org")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, "https://chat.example.org", rr.Header().Get("Access-Control-Allow-Origin"), "the request's own origin is echoed")
}

func TestCORS_WildcardNeverAllowsAll(t *testing.T) {
	h := newCORSHandler(t, "*")
	req := httptest.NewRequest(stdhttp.MethodGet, "/foo", nil)
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Empty(t, rr.Header().Get("Access-Control-Allow-Origin"))
}
