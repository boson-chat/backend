package origins_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boson-chat/backend/internal/origins"
)

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"https://chat.example.org":       "https://chat.example.org",
		"https://Chat.Example.ORG/":      "https://chat.example.org",
		"HTTPS://chat.example.org:443":   "https://chat.example.org",
		"https://chat.example.org:8443/": "https://chat.example.org:8443",
		"http://localhost:5173":          "http://localhost:5173",
		"http://localhost:80":            "http://localhost",
		"https://chat.example.org.":      "https://chat.example.org",
		"http://[::1]:3000":              "http://[::1]:3000",
		"https://chat.example.org/app":   "https://chat.example.org",
		"https://chat.example.org:":      "",
		"https://chat.example.org:0":     "",
		"https://u@chat.example.org":     "",
		"https://chat.example.org/?a=1":  "",
		"https://*.example.org":          "",
		"ftp://chat.example.org":         "",
		"chat.example.org":               "",
		"*":                              "",
		"null":                           "",
		"":                               "",
	} {
		if got := origins.Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseFixed(t *testing.T) {
	for _, c := range []struct {
		list, origin string
		want         bool
	}{
		{"", "https://a.test", false},
		{"*", "https://a.test", false},
		{"https://a.test,*", "https://zzz.test", false},
		{"https://a.test", "https://a.test", true},
		{"https://a.test/", "https://A.test", true},
		{"https://A.test", "https://a.test:443", true},
		{"https://a.test:8443", "https://a.test", false},
		{"https://a.test:8443", "https://a.test:8443", true},
		{" https://a.test , https://b.test ", "https://b.test", true},
		{"https://a.test", "https://b.test", false},
		{"https://a.test", "", false},
		{"https://a.test", "null", false},
		{"https://a.test", "http://a.test", false},
		// directory without a fetcher: only the fixed entries.
		{"directory,https://a.test", "https://a.test", true},
		{"directory", "https://a.test", false},
	} {
		p, _ := origins.Parse(c.list, nil)
		if got := p.Allowed(c.origin); got != c.want {
			t.Errorf("Parse(%q).Allowed(%q) = %v, want %v", c.list, c.origin, got, c.want)
		}
	}
	p, bad := origins.Parse("https://a.test,not an origin,*, Directory", nil)
	if len(bad) != 2 || bad[0] != "not an origin" || bad[1] != "*" {
		t.Fatalf("invalid entries: %q", bad)
	}
	if !p.Allowed("https://a.test") || p.Allowed("not an origin") {
		t.Fatal("invalid entries must be skipped, valid ones kept")
	}
	if _, bad := origins.Parse("directory,https://a.test", nil); len(bad) != 0 {
		t.Fatalf("valid list reported %q", bad)
	}
	if !origins.UsesDirectory("https://a.test, Directory") || origins.UsesDirectory("https://a.test") || origins.UsesDirectory("") {
		t.Fatal("UsesDirectory")
	}
	var nilPolicy *origins.Policy
	if nilPolicy.Allowed("https://a.test") {
		t.Fatal("nil policy allows")
	}
}

type fakeAuth struct {
	body   atomic.Value // string
	status atomic.Int64
	hits   atomic.Int64
}

func (f *fakeAuth) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	f.hits.Add(1)
	if s := f.status.Load(); s != 0 && s != 200 {
		w.WriteHeader(int(s))
		return
	}
	_, _ = io.WriteString(w, f.body.Load().(string))
}

func TestDirectoryFetcher(t *testing.T) {
	fa := &fakeAuth{}
	fa.body.Store(`{"origins":["https://chat.home.test","https://Chat.Other.test/"],"updated_at":null}`)
	fa.status.Store(503)
	srv := httptest.NewServer(fa)
	defer srv.Close()
	f := origins.NewFetcher(srv.URL+"/v1/web-origins", time.Hour, nil)
	p, _ := origins.Parse("directory,https://chat.home.test", f)
	ctx := context.Background()

	// auth down at start: only the fixed (own) origin.
	if _, err := f.Refresh(ctx); err == nil || f.Loaded() {
		t.Fatal("503 accepted")
	}
	if !p.Allowed("https://chat.home.test") || p.Allowed("https://chat.other.test") {
		t.Fatal("before the first fetch")
	}
	// First good fetch.
	fa.status.Store(200)
	if n, err := f.Refresh(ctx); err != nil || n != 2 || f.Len() != 2 {
		t.Fatalf("%d %v", n, err)
	}
	if !p.Allowed("https://chat.other.test") || !p.Allowed("https://CHAT.other.test:443/") || p.Allowed("https://evil.test") {
		t.Fatal("after the fetch")
	}
	// A fixed entry stays fixed even when the directory lists it too; a
	// directory-only one isn't.
	if ok, fixed := p.Match("https://chat.home.test"); !ok || !fixed {
		t.Fatal("home origin should match as fixed")
	}
	if ok, fixed := p.Match("https://chat.other.test"); !ok || fixed {
		t.Fatal("directory origin should match, not as fixed")
	}
	// Failures keep the last good list: HTTP error, bad JSON, no list.
	for _, bad := range []struct {
		status int
		body   string
	}{{500, ""}, {200, "not json"}, {200, `{"other":[]}`}} {
		fa.status.Store(int64(bad.status))
		fa.body.Store(bad.body)
		if _, err := f.Refresh(ctx); err == nil {
			t.Fatalf("%v accepted", bad)
		}
		if !p.Allowed("https://chat.other.test") {
			t.Fatalf("%v dropped the list", bad)
		}
	}
	// A new list replaces it (a delisted server goes; junk entries skipped).
	fa.status.Store(200)
	fa.body.Store(`{"origins":["https://chat.new.test","javascript:x","*",""]}`)
	if n, err := f.Refresh(ctx); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if p.Allowed("https://chat.other.test") || !p.Allowed("https://chat.new.test") || !p.Allowed("https://chat.home.test") {
		t.Fatal("replaced list")
	}
}

func TestFetcherUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	f := origins.NewFetcher(url, 0, nil)
	if _, err := f.Refresh(context.Background()); err == nil || f.Loaded() || f.Len() != 0 {
		t.Fatal("unreachable auth accepted")
	}
	if _, err := origins.NewFetcher("://bad", 0, nil).Refresh(context.Background()); err == nil {
		t.Fatal("bad URL accepted")
	}
}

func TestFetcherRunRetriesAndStops(t *testing.T) {
	fa := &fakeAuth{}
	fa.body.Store(`{"origins":["https://chat.other.test"]}`)
	fa.status.Store(503)
	srv := httptest.NewServer(fa)
	defer srv.Close()
	// interval 50ms: the retry wait is min(30s, interval).
	f := origins.NewFetcher(srv.URL, 50*time.Millisecond, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for fa.hits.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f.Loaded() {
		t.Fatal("loaded from a 503")
	}
	fa.status.Store(200)
	for !f.Allowed("https://chat.other.test") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !f.Allowed("https://chat.other.test") {
		t.Fatal("never loaded after auth came back")
	}
	// Later failures keep the list while Run goes on.
	fa.status.Store(500)
	hits := fa.hits.Load()
	for fa.hits.Load() < hits+2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !f.Allowed("https://chat.other.test") {
		t.Fatal("a failed refresh dropped the list")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run didn't stop")
	}
}
