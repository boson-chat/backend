package unfurl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestIsBlockedHost(t *testing.T) {
	for _, h := range []string{
		"localhost", "LOCALHOST", "localhost.", "foo.localhost", "printer.local", "db.internal", "nas.home.arpa",
		"127.0.0.1", "127.1", "2130706433", "0x7f.1", "0.0.0.0", "0", "::1", "[::1]", "::", "10.0.0.5", "192.168.1.2",
		"172.16.0.1", "172.31.255.255", "169.254.169.254", "169.254.1.1", "100.64.0.1", "100.100.100.200",
		"fd00::1", "fd00:ec2::254", "fe80::1", "fe80::1%25eth0", "::ffff:127.0.0.1", "::ffff:10.0.0.1",
		"64:ff9b::a00:1", "2002:7f00:1::", "224.0.0.1", "255.255.255.255", "198.18.0.1", "192.0.2.1", "",
	} {
		if !IsBlockedHost(h) {
			t.Errorf("%q should be blocked", h)
		}
	}
	for _, h := range []string{"example.com", "cafe.be", "youtube.com", "8.8.8.8", "1.1.1.1", "172.15.0.1", "172.32.0.1", "2606:4700::1111", "100.63.255.255", "100.128.0.1"} {
		if IsBlockedHost(h) {
			t.Errorf("%q should be allowed", h)
		}
	}
}

func TestCheckURL(t *testing.T) {
	ok := []string{"https://example.com/a?b", "http://example.com", "https://example.com:443/", "http://example.com:80/x"}
	bad := []string{
		"ftp://example.com/", "file:///etc/passwd", "gopher://example.com", "javascript:alert(1)", "//example.com",
		"https://example.com:8080/", "http://example.com:22/", "https://user:pw@example.com/",
		"http://127.0.0.1/", "http://[::1]/", "http://169.254.169.254/latest/meta-data/", "http://localhost/",
		"http://metadata.google.internal/", "https:///nohost",
	}
	for _, s := range ok {
		u, _ := url.Parse(s)
		if err := CheckURL(u); err != nil {
			t.Errorf("%s refused: %v", s, err)
		}
	}
	for _, s := range bad {
		u, _ := url.Parse(s)
		if err := CheckURL(u); err == nil {
			t.Errorf("%s allowed", s)
		}
	}
}

// testFetcher reaches the httptest server at "public" (loopback, which the
// guard would refuse) as if it were a public site: only that address and
// port are exempt; every other address goes through the real guard.
// lookup resolves names from the given table.
type resolver struct {
	mu    sync.Mutex
	names map[string][]string // name → answers, consumed in turn (the last repeats)
	asked map[string]int
}

func (r *resolver) lookup(_ context.Context, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	answers, ok := r.names[host]
	if !ok {
		return nil, fmt.Errorf("no such host %s", host)
	}
	i := r.asked[host]
	r.asked[host]++
	if i >= len(answers) {
		i = len(answers) - 1
	}
	return []netip.Addr{netip.MustParseAddr(answers[i])}, nil
}

func testFetcher(public *httptest.Server, names map[string][]string) (*Fetcher, *resolver) {
	f := NewFetcher(FetcherOptions{})
	exempt := netip.MustParseAddrPort(strings.TrimPrefix(public.URL, "http://"))
	all := map[string][]string{"public.example": {"127.0.0.1"}}
	for k, v := range names {
		all[k] = v
	}
	r := &resolver{names: all, asked: map[string]int{}}
	f.lookup = r.lookup
	f.portOK = func(string) bool { return true }
	f.blocked = func(ap netip.AddrPort) bool { return ap != exempt && IsBlockedAddr(ap.Addr()) }
	return f, r
}

func portOf(s *httptest.Server) string { return s.URL[strings.LastIndex(s.URL, ":")+1:] }

// base is the "public" server under its name (a literal loopback URL is
// refused before any dial).
func base(s *httptest.Server) string { return "http://public.example:" + portOf(s) }

// private is the "internal service" an attacker wants to reach.
func privateServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	var hits atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<meta property="og:title" content="secret">`))
	}))
	t.Cleanup(s.Close)
	return s, &hits
}

func TestFetcherRefusesPrivateTargets(t *testing.T) {
	priv, hits := privateServer(t)
	pub := httptest.NewServer(http.NotFoundHandler())
	defer pub.Close()
	f, _ := testFetcher(pub, map[string][]string{"internal.example": {"127.0.0.1"}, "meta.example": {"169.254.169.254"}, "v6.example": {"::1"}, "mapped.example": {"::ffff:127.0.0.1"}})
	for _, u := range []string{
		priv.URL,                                  // literal loopback
		"http://localhost:" + portOf(priv),        // local name
		"http://internal.example:" + portOf(priv), // public-looking name → loopback, refused when dialling
		"http://v6.example:" + portOf(priv),
		"http://mapped.example:" + portOf(priv),
		"http://meta.example/latest/meta-data/",
	} {
		_, err := f.Get(context.Background(), u, "text/html", 1024)
		if !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: got %v, want ErrBlocked", u, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("the private server was reached %d times", hits.Load())
	}
}

func TestFetcherDefaultPortsOnly(t *testing.T) {
	// Without the test seam: a non-80/443 port is refused before any dial.
	f := NewFetcher(FetcherOptions{})
	if _, err := f.Get(context.Background(), "http://example.com:8080/", "text/html", 1024); !errors.Is(err, ErrBlocked) {
		t.Fatalf("port 8080: %v", err)
	}
	// And the dial check refuses one even if a URL check were bypassed.
	if !defaultBlocked(netip.MustParseAddrPort("93.184.216.34:8080")) || defaultBlocked(netip.MustParseAddrPort("93.184.216.34:443")) {
		t.Fatal("dial port rule")
	}
}

func TestFetcherRedirects(t *testing.T) {
	priv, hits := privateServer(t)
	var pub *httptest.Server
	pub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/to-literal":
			http.Redirect(w, r, priv.URL+"/", http.StatusFound)
		case r.URL.Path == "/to-name":
			http.Redirect(w, r, "http://internal.example:"+portOf(priv)+"/", http.StatusMovedPermanently)
		case r.URL.Path == "/to-scheme":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/hop/"):
			n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/hop/"))
			if n > 0 {
				http.Redirect(w, r, fmt.Sprintf("/hop/%d", n-1), http.StatusFound)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<title>landed</title>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer pub.Close()
	f, _ := testFetcher(pub, map[string][]string{"internal.example": {"127.0.0.1"}})
	for _, p := range []string{"/to-literal", "/to-name", "/to-scheme"} {
		if _, err := f.Get(context.Background(), base(pub)+p, "text/html", 1024); err == nil {
			t.Errorf("%s: redirect followed", p)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("a redirect reached the private server")
	}
	res, err := f.Get(context.Background(), base(pub)+"/hop/5", "text/html", 1024, "text/html")
	if err != nil || string(res.Body) != "<title>landed</title>" || !strings.HasSuffix(res.FinalURL, "/hop/0") {
		t.Fatalf("5 redirects: %v %+v", err, res)
	}
	if _, err := f.Get(context.Background(), base(pub)+"/hop/6", "text/html", 1024); err == nil {
		t.Fatal("6 redirects followed")
	}
}

// DNS rebinding: the name answers with a public address first and a
// private one the next time. The guard judges the address it connects to,
// on every connection, so the second fetch is refused.
func TestFetcherDNSRebinding(t *testing.T) {
	priv, hits := privateServer(t)
	pub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<meta property="og:title" content="public">`))
	}))
	defer pub.Close()
	// rebind.example answers with the (exempt, "public") test server first
	// and a private address the next time; evil.example answers loopback.
	f, r := testFetcher(pub, map[string][]string{
		"rebind.example": {"127.0.0.1", "10.0.0.1"},
		"evil.example":   {"127.0.0.1"},
	})
	f.client.Transport.(*http.Transport).DisableKeepAlives = true // a fresh dial per fetch
	if res, err := f.Get(context.Background(), "http://rebind.example:"+portOf(pub)+"/", "text/html", 1024); err != nil || !strings.Contains(string(res.Body), "public") {
		t.Fatalf("first answer: %v", err)
	}
	if _, err := f.Get(context.Background(), "http://rebind.example:"+portOf(pub)+"/", "text/html", 1024); !errors.Is(err, ErrBlocked) {
		t.Fatalf("after rebinding: %v, want ErrBlocked", err)
	}
	if r.asked["rebind.example"] != 2 {
		t.Fatalf("the name was resolved %d times, want once per connection", r.asked["rebind.example"])
	}
	if _, err := f.Get(context.Background(), "http://evil.example:"+portOf(priv)+"/", "text/html", 1024); !errors.Is(err, ErrBlocked) {
		t.Fatalf("name to loopback: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("private server reached")
	}
}

func TestFetcherSizeAndType(t *testing.T) {
	big := strings.Repeat("x", 3<<20)
	pub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<meta property="og:title" content="top">` + big + `<meta property="og:description" content="bottom">`))
		case "/png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("\x89PNG"))
		case "/500":
			w.WriteHeader(500)
		}
	}))
	defer pub.Close()
	f, _ := testFetcher(pub, nil)
	res, err := f.Get(context.Background(), base(pub)+"/big", "text/html", MaxHTMLBytes, htmlTypes...)
	if err != nil || len(res.Body) != MaxHTMLBytes {
		t.Fatalf("cap: %v %d", err, len(res.Body))
	}
	c := ParseCard(string(res.Body), res.FinalURL)
	if c.Title != "top" || c.Description != "" {
		t.Fatalf("parsed past the cap: %+v", c)
	}
	if _, err := f.Get(context.Background(), base(pub)+"/png", "text/html", 1024, htmlTypes...); !errors.Is(err, ErrContentType) {
		t.Fatalf("content type: %v", err)
	}
	var st ErrStatus
	if _, err := f.Get(context.Background(), base(pub)+"/500", "text/html", 1024); !errors.As(err, &st) || st.Code != 500 {
		t.Fatalf("status: %v", err)
	}
}

func TestFetcherSendsBotUserAgent(t *testing.T) {
	var ua string
	pub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/html")
	}))
	defer pub.Close()
	f, _ := testFetcher(pub, nil)
	if _, err := f.Get(context.Background(), base(pub), "text/html", 1024); err != nil {
		t.Fatal(err)
	}
	if ua != UserAgent || !strings.Contains(ua, "BosonBot") {
		t.Fatalf("UA %q", ua)
	}
}

func TestTestHostMap(t *testing.T) {
	var host string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host = r.Host
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<meta property="og:title" content="stub">`))
	}))
	defer stub.Close()
	addr := strings.TrimPrefix(stub.URL, "http://")
	// A mapped https name with the target's port: plain http to the stub.
	f := NewFetcher(FetcherOptions{TestHostMap: map[string]string{"open.spotify.com": addr, "unfurl.e2e.test": "127.0.0.1"}})
	res, err := f.Get(context.Background(), "https://open.spotify.com/embed/track/x", "text/html", 1024)
	if err != nil || !strings.Contains(string(res.Body), "stub") || host != "open.spotify.com" || res.FinalURL != "https://open.spotify.com/embed/track/x" {
		t.Fatalf("mapped https: %v host=%q %+v", err, host, res)
	}
	// A mapped name without a port keeps the URL's own.
	if _, err := f.Get(context.Background(), "http://unfurl.e2e.test:"+portOf(stub)+"/", "text/html", 1024); err != nil || host != "unfurl.e2e.test:"+portOf(stub) {
		t.Fatalf("mapped keeps port: %v %q", err, host)
	}
	// Everything else is still guarded.
	if _, err := f.Get(context.Background(), stub.URL, "text/html", 1024); !errors.Is(err, ErrBlocked) {
		t.Fatalf("unmapped loopback: %v", err)
	}
}
