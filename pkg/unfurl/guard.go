// Package unfurl builds link previews (OpenGraph site cards, YouTube titles
// and Spotify cards) from public web pages, for clients that can't fetch
// arbitrary sites themselves (the web client, under CORS).
//
// It is a port of the boson desktop shell's fetchers (boson-chat/boson
// desktop/unfurl.go, spotify.go, webfetch.go) and returns the same shapes,
// with a stricter guard because here the fetch runs on a server, next to
// other services:
//
//   - http and https only, on ports 80 and 443, no credentials in the URL;
//   - every address actually dialled is checked after DNS resolution, in the
//     dialer's Control hook, so a public name that resolves (or re-resolves,
//     DNS rebinding) to a private, loopback, link-local, CGNAT, metadata or
//     otherwise special address is refused;
//   - each redirect hop is re-checked, at most five of them;
//   - bodies are read up to a size cap, with timeouts, and only from the
//     expected content types.
package unfurl

import (
	"errors"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// ErrBlocked: the URL or an address it resolved to isn't a public web
// destination.
var ErrBlocked = errors.New("unfurl: destination not allowed")

// blockedPrefixes are refused besides what netip's predicates cover
// (loopback, private, link-local, multicast, unspecified).
var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",          // "this network"
		"100.64.0.0/10",      // CGNAT (also Alibaba's 100.100.100.200 metadata)
		"192.0.0.0/24",       // IETF protocol assignments
		"192.0.2.0/24",       // TEST-NET-1
		"198.18.0.0/15",      // benchmarking
		"198.51.100.0/24",    // TEST-NET-2
		"203.0.113.0/24",     // TEST-NET-3
		"240.0.0.0/4",        // reserved, and 255.255.255.255
		"64:ff9b::/96",       // NAT64: reaches IPv4 behind the gateway
		"64:ff9b:1::/48",     // local-use NAT64
		"100::/64",           // discard-only
		"2001::/23",          // IETF protocol assignments (Teredo, ORCHID, …)
		"2001:db8::/32",      // documentation
		"2002::/16",          // 6to4: embeds an IPv4 address
		"fc00::/7",           // unique local (AWS fd00:ec2::254 metadata)
		"fec0::/10",          // site-local (deprecated)
		"::ffff:0:0:0/96",    // IPv4-translated
		"169.254.169.254/32", // cloud metadata (link-local, listed for clarity)
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// IsBlockedAddr reports whether a is anything but a public unicast address.
// IPv4-mapped IPv6 addresses are judged as the IPv4 address they carry.
func IsBlockedAddr(a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	if !a.IsValid() {
		return true
	}
	if a.IsLoopback() || a.IsPrivate() || a.IsUnspecified() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() || a.IsMulticast() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// IsBlockedHost refuses names that are local by definition and literal
// addresses IsBlockedAddr refuses. Other names are judged when dialled.
func IsBlockedHost(hostname string) bool {
	h := strings.TrimSuffix(strings.Trim(strings.ToLower(hostname), "[]"), ".")
	if h == "" || h == "localhost" {
		return true
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".home.arpa"} {
		if strings.HasSuffix(h, suffix) {
			return true
		}
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return IsBlockedAddr(a)
	}
	// Shorthand IPv4 forms some resolvers accept ("127.1", "2130706433",
	// "0x7f.1"): not a hostname anyone links to.
	if looksNumeric(h) {
		return true
	}
	return false
}

// looksNumeric: the last label is a number (no TLD is), as in the
// shorthand IPv4 forms "127.1", "2130706433" or "0x7f.1".
func looksNumeric(h string) bool {
	last := h[strings.LastIndex(h, ".")+1:]
	if strings.HasPrefix(last, "0x") {
		return true
	}
	_, err := strconv.ParseUint(last, 10, 64)
	return err == nil
}

// allowedPort: the URL's port, explicit or implied by its scheme.
func allowedPort(p string) bool { return p == "" || p == "80" || p == "443" }

// CheckURL is the guard for a URL before it's requested (the first one
// and every redirect target).
func CheckURL(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ErrBlocked
	}
	if !allowedPort(u.Port()) || IsBlockedHost(u.Hostname()) {
		return ErrBlocked
	}
	return nil
}
