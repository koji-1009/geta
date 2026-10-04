// Package clientip finds the address of the client a request came from,
// behind reverse proxies the application runs, to key a rate limit by.
package clientip

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Key returns a ratelimit.PerKey key function: the client's address, read
// through the proxies in trusted.
//
// Behind a reverse proxy, r.RemoteAddr is the proxy's address, and keying by
// it puts every client in one bucket. The client's own
// address is in X-Forwarded-For, which each proxy appends the address it
// received the request from to. Only the entries your own proxies appended
// can be believed: a client may send X-Forwarded-For itself, with any
// address in it, and would pick a fresh bucket for every request. So Key
// reads the header from the right, past every address in trusted, and keys
// by the first address that is not one of your proxies: the client as your
// outermost proxy saw it. A request whose RemoteAddr is not a trusted proxy
// is keyed by RemoteAddr, whatever its X-Forwarded-For says.
//
// List in trusted only the proxies you run (a load balancer's subnet, a
// sidecar's loopback), never a range clients can send from. With trusted
// empty, Key keys by RemoteAddr's host. A request with no RemoteAddr (one
// served over a Unix socket) is exempt.
func Key(trusted []netip.Prefix) func(*http.Request) (string, bool) {
	isTrusted := func(a netip.Addr) bool {
		for _, p := range trusted {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	return func(r *http.Request) (string, bool) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		peer, err := netip.ParseAddr(host)
		if err != nil {
			return r.RemoteAddr, r.RemoteAddr != ""
		}
		peer = peer.Unmap()
		if !isTrusted(peer) {
			return peer.String(), true
		}
		// One header may be sent on several lines; together they are one
		// list, read from its last entry back.
		var hops []string
		for _, v := range r.Header.Values("X-Forwarded-For") {
			hops = append(hops, strings.Split(v, ",")...)
		}
		for i := len(hops) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				// A trusted proxy appends an address; an entry that is not
				// one was written before the request reached them. Key by
				// the nearest proxy rather than by text a client chose.
				return peer.String(), true
			}
			a = a.Unmap()
			if !isTrusted(a) {
				return a.String(), true
			}
			peer = a
		}
		// Every hop was a proxy of ours: the request began inside.
		return peer.String(), true
	}
}
