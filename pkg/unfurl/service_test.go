package unfurl

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testService(t *testing.T, h http.HandlerFunc) (*Service, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	s := NewService(Options{CacheSize: 3})
	f, _ := testFetcher(srv, nil)
	s.fetch = f
	return s, srv
}

func TestServiceCardCachesAndCountsOnlyFetches(t *testing.T) {
	var hits atomic.Int32
	s, srv := testService(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<meta property="og:title" content="t` + r.URL.Path + `">`))
	})
	allowed := 0
	allow := func() error { allowed++; return nil }
	c, err := s.Card(context.Background(), base(srv)+"/a#frag", allow)
	if err != nil || c == nil || c.Title != "t/a" {
		t.Fatalf("card: %v %+v", err, c)
	}
	// Same URL (fragment aside): from the cache, no fetch, no rate check.
	if c2, _ := s.Card(context.Background(), base(srv)+"/a", allow); c2 == nil || c2.Title != "t/a" || hits.Load() != 1 || allowed != 1 {
		t.Fatalf("cached: hits=%d allowed=%d", hits.Load(), allowed)
	}
	// No preview is cached too (for the negative TTL).
	if c, err := s.Card(context.Background(), base(srv)+"/missing", allow); c != nil || err != nil {
		t.Fatalf("missing: %v %+v", err, c)
	}
	_, _ = s.Card(context.Background(), base(srv)+"/missing", allow)
	if hits.Load() != 2 {
		t.Fatalf("negative not cached: %d", hits.Load())
	}
	// A refusing rate limit stops the fetch.
	limited := errors.New("limited")
	if _, err := s.Card(context.Background(), base(srv)+"/b", func() error { return limited }); !errors.Is(err, limited) || hits.Load() != 2 {
		t.Fatalf("limit: %v hits=%d", err, hits.Load())
	}
	// Refused URLs never reach the limiter or the network (the default
	// guard, without the test server's exemptions).
	s = NewService(Options{})
	for _, u := range []string{"http://127.0.0.1/", "ftp://example.com/", "https://example.com:8443/", "https://" + strings.Repeat("a", 3000) + ".com/"} {
		if _, err := s.Card(context.Background(), u, func() error { t.Fatal("limiter called"); return nil }); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: %v", u, err)
		}
	}
}

func TestServiceCardSharesConcurrentFetches(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	s, srv := testService(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<title>shared</title>`))
	})
	var wg sync.WaitGroup
	results := make(chan *Card, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _ := s.Card(context.Background(), base(srv)+"/x", nil)
			results <- c
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	close(results)
	for c := range results {
		if c == nil || c.Title != "shared" {
			t.Fatalf("result %+v", c)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("%d fetches for one URL", hits.Load())
	}
}

func TestServiceSpotify(t *testing.T) {
	s := NewService(Options{})
	var asked []string
	s.spotifyGets = func(_ context.Context, embed string) ([]byte, error) {
		asked = append(asked, embed)
		if strings.HasSuffix(embed, "/track/T1") {
			return []byte(spotifyPage(map[string]any{"type": "track", "title": "Solo", "artists": []any{map[string]any{"name": "A"}}})), nil
		}
		return nil, ErrStatus{Code: 404}
	}
	i, err := s.Spotify(context.Background(), "https://open.spotify.com/intl-de/track/T1?si=abc", nil)
	if err != nil || i == nil || i.Title != "Solo" || i.URL != "https://open.spotify.com/intl-de/track/T1?si=abc" {
		t.Fatalf("track: %v %+v", err, i)
	}
	// Another form of the same link: cached, and answered with its own URL.
	i2, _ := s.Spotify(context.Background(), "https://open.spotify.com/track/T1", func() error { t.Fatal("limiter on a hit"); return nil })
	if i2 == nil || i2.URL != "https://open.spotify.com/track/T1" || len(asked) != 1 {
		t.Fatalf("cached: %+v %v", i2, asked)
	}
	if i, err := s.Spotify(context.Background(), "https://open.spotify.com/show/MISSING", nil); i != nil || err != nil {
		t.Fatalf("missing: %v %+v", err, i)
	}
	for _, u := range []string{"https://evil.example/track/T1", "https://open.spotify.com/user/x", "not a url"} {
		if _, err := s.Spotify(context.Background(), u, nil); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: %v", u, err)
		}
	}
	for _, a := range asked {
		if !strings.HasPrefix(a, "https://open.spotify.com/embed/") {
			t.Fatalf("fetched %s", a)
		}
	}
}

func TestLRU(t *testing.T) {
	now := time.Unix(0, 0)
	c := newLRU[int](2, time.Minute, 10*time.Second)
	c.now = func() time.Time { return now }
	one, two, three := 1, 2, 3
	c.put("a", &one)
	c.put("b", &two)
	c.get("a") // a is now the most recent
	c.put("c", &three)
	if _, ok := c.get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	if v, ok := c.get("a"); !ok || *v != 1 {
		t.Fatal("a evicted")
	}
	c.put("neg", nil)
	now = now.Add(11 * time.Second)
	if _, ok := c.get("neg"); ok {
		t.Fatal("negative entry outlived its TTL")
	}
	if _, ok := c.get("a"); !ok {
		t.Fatal("positive entry expired early")
	}
	now = now.Add(time.Minute)
	if _, ok := c.get("a"); ok || c.len() != 0 {
		t.Fatalf("expired entry kept (len %d)", c.len())
	}
}
