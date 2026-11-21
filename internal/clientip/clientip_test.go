package clientip

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestIP(t *testing.T) {
	r := New("10.203.53.0/24")
	cases := []struct {
		remote, xff, want string
	}{
		{"203.0.113.9:1234", "", "203.0.113.9"},
		{"203.0.113.9:1234", "1.2.3.4", "203.0.113.9"},          // untrusted peer: XFF ignored
		{"10.203.53.2:1234", "1.2.3.4", "1.2.3.4"},              // via Traefik
		{"10.203.53.2:1234", "6.6.6.6, 1.2.3.4", "1.2.3.4"},     // client-sent entries on the left are ignored
		{"10.203.53.2:1234", "1.2.3.4, 10.203.53.3", "1.2.3.4"}, // a second trusted hop
		{"10.203.53.2:1234", "garbage, 1.2.3.4", "1.2.3.4"},
		{"10.203.53.2:1234", "", "10.203.53.2"},
		{"[::ffff:10.203.53.2]:1234", "2001:db8::1", "2001:db8::1"},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = c.remote
		if c.xff != "" {
			req.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := r.IP(req).String(); got != c.want {
			t.Errorf("%s / %q: got %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestDefaultsAndKey(t *testing.T) {
	r := New("")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "172.18.0.1:5000"
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	if got := r.IP(req).String(); got != "8.8.8.8" {
		t.Fatalf("default trust: %s", got)
	}
	if k := Key(netip.MustParseAddr("2001:db8:1:2:3:4:5:6")); k != "2001:db8:1:2::/64" {
		t.Fatalf("v6 key %s", k)
	}
	if k := Key(netip.MustParseAddr("1.2.3.4")); k != "1.2.3.4" {
		t.Fatalf("v4 key %s", k)
	}
	if Key(netip.Addr{}) != "unknown" {
		t.Fatal("invalid key")
	}
	if New("bogus, 192.0.2.1").isTrusted(netip.MustParseAddr("192.0.2.1")) != true {
		t.Fatal("single address entry")
	}
}
