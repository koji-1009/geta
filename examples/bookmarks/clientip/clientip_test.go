package clientip

import (
	"net/http"
	"net/netip"
	"testing"
)

func TestKeyReadsThroughTrustedProxiesOnly(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	key := Key(trusted)
	for _, c := range []struct {
		name, remote string
		xff          []string
		want         string
	}{
		{"a client straight to the server", "203.0.113.7:4000", nil, "203.0.113.7"},
		{"a client straight to the server, lying in the header", "203.0.113.7:4000", []string{"198.51.100.1"}, "203.0.113.7"},
		{"through the proxy", "10.0.0.5:4000", []string{"203.0.113.7"}, "203.0.113.7"},
		{"through the proxy, the client lying before it", "10.0.0.5:4000", []string{"198.51.100.1, 203.0.113.7"}, "203.0.113.7"},
		{"through two proxies of ours", "10.0.0.5:4000", []string{"203.0.113.7, 10.0.0.9"}, "203.0.113.7"},
		{"the header on two lines", "10.0.0.5:4000", []string{"198.51.100.1", "203.0.113.7"}, "203.0.113.7"},
		{"an IPv6 client through a loopback sidecar", "[::1]:4000", []string{"2001:db8::7"}, "2001:db8::7"},
		{"an IPv4-mapped client", "10.0.0.5:4000", []string{"::ffff:203.0.113.7"}, "203.0.113.7"},
		{"the proxy sent no header", "10.0.0.5:4000", nil, "10.0.0.5"},
		{"every hop is ours", "10.0.0.5:4000", []string{"10.1.1.1"}, "10.1.1.1"},
		{"text where an address should be", "10.0.0.5:4000", []string{"unknown"}, "10.0.0.5"},
	} {
		r := &http.Request{RemoteAddr: c.remote, Header: http.Header{}}
		for _, v := range c.xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		if got, ok := key(r); !ok || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.name, got, ok, c.want)
		}
	}
	// Trusting none is keying by the peer.
	r := &http.Request{RemoteAddr: "10.0.0.5:4000", Header: http.Header{"X-Forwarded-For": {"203.0.113.7"}}}
	if got, _ := Key(nil)(r); got != "10.0.0.5" {
		t.Errorf("no proxy trusted: %q", got)
	}
}
