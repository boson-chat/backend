// Package clientip finds the address a request came from behind trusted
// reverse proxies (Traefik in the stack).
package clientip

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Resolver trusts X-Forwarded-For only from its proxies.
type Resolver struct {
	trusted []netip.Prefix
}

// defaultTrusted: with no list configured, loopback and private peers
// (a proxy on the same host or the compose network).
var defaultTrusted = []string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"}

// New parses a comma-separated CIDR (or address) list; empty means the
// defaults. Invalid entries are skipped.
func New(list string) *Resolver {
	entries := strings.Split(list, ",")
	if strings.TrimSpace(list) == "" {
		entries = defaultTrusted
	}
	r := &Resolver{}
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if p, err := netip.ParsePrefix(e); err == nil {
			r.trusted = append(r.trusted, p.Masked())
		} else if a, err := netip.ParseAddr(e); err == nil {
			r.trusted = append(r.trusted, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
		}
	}
	return r
}

func (r *Resolver) isTrusted(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range r.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// IP is the client's address: the peer, or when the peer is a trusted
// proxy, the right-most X-Forwarded-For entry that isn't one (entries to
// its left are whatever the client sent).
func (r *Resolver) IP(req *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	peer = peer.Unmap()
	if !r.isTrusted(peer) {
		return peer
	}
	var hops []string
	for _, h := range req.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(h, ",")...)
	}
	client := peer
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		client = a.Unmap()
		if !r.isTrusted(client) {
			break
		}
	}
	return client
}

// Key is a rate-limit key for an address: IPv6 by its /64 (one host's
// usual allocation), IPv4 as is.
func Key(a netip.Addr) string {
	if !a.IsValid() {
		return "unknown"
	}
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}
