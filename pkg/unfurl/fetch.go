package unfurl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// UserAgent: a bot name, in the form link-preview bots use (sites that
// turn away unknown clients usually let these through).
const UserAgent = "Mozilla/5.0 (compatible; BosonBot/1.0; link preview)"

// MaxRedirects is how many redirects a fetch follows.
const MaxRedirects = 5

// FetcherOptions configure a Fetcher. Zero values take the defaults.
type FetcherOptions struct {
	// Timeout of a whole fetch, redirects included (default 8 s).
	Timeout time.Duration
	// UserAgent (default UserAgent).
	UserAgent string
	// TestHostMap is for end-to-end tests only: name → "host[:port]". A URL
	// on a mapped name is fetched over plain http from that address (its
	// own port when the target has none), with the original Host header,
	// and skips the guard. Never set it in production.
	TestHostMap map[string]string
}

// Fetcher gets public web pages through the guard (package doc).
type Fetcher struct {
	client    *http.Client
	userAgent string
	mapped    map[string]string

	// Seams for tests; production uses the package's guard.
	lookup  func(ctx context.Context, host string) ([]netip.Addr, error)
	blocked func(netip.AddrPort) bool
	portOK  func(port string) bool
}

func defaultBlocked(ap netip.AddrPort) bool {
	return (ap.Port() != 80 && ap.Port() != 443) || IsBlockedAddr(ap.Addr())
}

// checkURL is CheckURL with the fetcher's port rule.
func (f *Fetcher) checkURL(u *url.URL) error {
	if u != nil && f.mapped[strings.ToLower(u.Hostname())] != "" {
		return nil
	}
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ErrBlocked
	}
	if !f.portOK(u.Port()) || IsBlockedHost(u.Hostname()) {
		return ErrBlocked
	}
	return nil
}

// dial resolves the name itself and connects only to addresses the guard
// allows; the dialer's Control hook checks the address really connected
// to as well. A name that resolves (or re-resolves) to a refused address
// is never reached.
func (f *Fetcher) dial(d *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, ErrBlocked
		}
		port, err := strconv.ParseUint(portStr, 10, 16)
		if err != nil {
			return nil, ErrBlocked
		}
		var addrs []netip.Addr
		if a, err := netip.ParseAddr(host); err == nil {
			addrs = []netip.Addr{a}
		} else if addrs, err = f.lookup(ctx, host); err != nil {
			return nil, err
		}
		dialer := *d
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || f.blocked(netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())) {
				return ErrBlocked
			}
			return nil
		}
		lastErr := error(ErrBlocked)
		for _, a := range addrs {
			ap := netip.AddrPortFrom(a.Unmap(), uint16(port))
			if f.blocked(ap) {
				continue
			}
			c, err := dialer.DialContext(ctx, network, ap.String())
			if err == nil {
				return c, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

// Response is a fetched body, cut at the caller's limit.
type Response struct {
	Body        []byte
	FinalURL    string
	ContentType string // the media type, lowercased, without parameters
}

// ErrStatus: the server answered with something other than 2xx.
type ErrStatus struct{ Code int }

func (e ErrStatus) Error() string { return fmt.Sprintf("unfurl: HTTP %d", e.Code) }

// ErrContentType: the body isn't one of the expected types.
var ErrContentType = errors.New("unfurl: unexpected content type")

// NewFetcher builds the guarded client.
func NewFetcher(o FetcherOptions) *Fetcher {
	if o.Timeout <= 0 {
		o.Timeout = 8 * time.Second
	}
	if o.UserAgent == "" {
		o.UserAgent = UserAgent
	}
	f := &Fetcher{
		userAgent: o.UserAgent,
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		blocked: defaultBlocked,
		portOK:  allowedPort,
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	guarded := &http.Transport{
		Proxy:                  nil, // never via an environment proxy
		DialContext:            f.dial(dialer),
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  5 * time.Second,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		MaxIdleConns:           64,
		MaxIdleConnsPerHost:    4,
		IdleConnTimeout:        60 * time.Second,
	}
	var rt http.RoundTripper = guarded
	mapped := map[string]string{}
	for name, target := range o.TestHostMap {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" && target != "" {
			mapped[name] = strings.TrimSpace(target)
		}
	}
	if len(mapped) > 0 {
		rt = &testMapTransport{guarded: guarded, plain: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext}, hosts: mapped}
	}
	f.mapped = mapped
	f.client = &http.Client{
		Timeout:   o.Timeout,
		Transport: rt,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > MaxRedirects {
				return errors.New("unfurl: too many redirects")
			}
			return f.checkURL(req.URL)
		},
	}
	return f
}

// Get fetches rawURL and reads at most limit bytes of its body, which must
// be one of types (media types, e.g. "text/html").
func (f *Fetcher) Get(ctx context.Context, rawURL, accept string, limit int64, types ...string) (*Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, ErrBlocked
	}
	if err := f.checkURL(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, ErrBlocked
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	res, err := f.client.Do(req)
	if err != nil {
		if errors.Is(err, ErrBlocked) {
			return nil, ErrBlocked
		}
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, ErrStatus{Code: res.StatusCode}
	}
	mt, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	mt = strings.ToLower(mt)
	if len(types) > 0 && !contains(types, mt) {
		return nil, ErrContentType
	}
	// A declared length over the cap is still read up to the cap: the
	// metadata is near the top.
	body, err := io.ReadAll(io.LimitReader(res.Body, limit))
	if err != nil {
		return nil, err
	}
	return &Response{Body: body, FinalURL: res.Request.URL.String(), ContentType: mt}, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// testMapTransport sends mapped names to their test address over plain
// http; everything else goes through the guarded transport.
type testMapTransport struct {
	guarded, plain *http.Transport
	hosts          map[string]string
}

func (t *testMapTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, ok := t.hosts[strings.ToLower(req.URL.Hostname())]
	if !ok {
		return t.guarded.RoundTrip(req)
	}
	if _, _, err := net.SplitHostPort(target); err != nil {
		port := req.URL.Port()
		if port == "" {
			port = map[string]string{"http": "80", "https": "443"}[req.URL.Scheme]
		}
		target = net.JoinHostPort(target, port)
	}
	out := req.Clone(req.Context())
	out.URL.Scheme = "http"
	out.URL.Host = target
	out.Host = req.URL.Host
	res, err := t.plain.RoundTrip(out)
	if res != nil {
		res.Request = req
	}
	return res, err
}
