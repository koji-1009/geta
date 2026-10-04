package geta

import (
	"fmt"
	"net/netip"
	"strings"
)

// snumQuad reads RFC 2673's dotted quad, Snum 3("." Snum), where Snum is
// 1*3DIGIT up to 255 and may have leading zeros.
func snumQuad(s string) ([4]byte, bool) {
	var a [4]byte
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return a, false
	}
	for i, p := range parts {
		n, ok := digits(p)
		if !ok || len(p) > 3 || n > 255 {
			return a, false
		}
		a[i] = byte(n)
	}
	return a, true
}

// ipv6Groups splits an IPv6 text at its "::" into the groups before and
// after it; without one, every group is in tail. A second "::" leaves an
// empty group, which hexGroup refuses.
func ipv6Groups(s string) (head, tail []string, compressed bool) {
	split := func(p string) []string {
		if p == "" {
			return nil
		}
		return strings.Split(p, ":")
	}
	left, right, compressed := strings.Cut(s, "::")
	if !compressed {
		return nil, split(s), false
	}
	return split(left), split(right), true
}

// hexGroup reads 1*4HEXDIG.
func hexGroup(s string) bool {
	if s == "" || len(s) > 4 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isHex(s[i]) {
			return false
		}
	}
	return true
}

// IPv4 is an IPv4 address in format ipv4, RFC 2673's dotted-quad (§3.2),
// such as 192.0.2.1. Leading zeros are accepted and dropped:
// 192.000.002.001 is written as 192.0.2.1. The zero value is 0.0.0.0.
type IPv4 struct {
	a [4]byte
}

// ParseIPv4 reads an RFC 2673 dotted-quad.
func ParseIPv4(s string) (IPv4, error) {
	a, ok := snumQuad(s)
	if !ok {
		return IPv4{}, formatError("ipv4", []byte(s))
	}
	return IPv4{a}, nil
}

// IPv4Of returns a as an IPv4. It returns an error if a is not an IPv4
// address, including an IPv4-mapped IPv6 address.
func IPv4Of(a netip.Addr) (IPv4, error) {
	if !a.Is4() {
		return IPv4{}, fmt.Errorf("geta.IPv4Of: %s is not an IPv4 address", a)
	}
	return IPv4{a.As4()}, nil
}

// Addr returns the address.
func (ip IPv4) Addr() netip.Addr { return netip.AddrFrom4(ip.a) }

// String returns the address in dotted decimal, without leading zeros.
func (ip IPv4) String() string { return ip.Addr().String() }

// AppendText appends the address in dotted decimal, without leading zeros.
func (ip IPv4) AppendText(b []byte) ([]byte, error) { return ip.Addr().AppendText(b) }

// MarshalText writes the address in dotted decimal, without leading zeros.
func (ip IPv4) MarshalText() ([]byte, error) { return ip.AppendText(nil) }

// UnmarshalText reads an RFC 2673 dotted-quad.
func (ip *IPv4) UnmarshalText(b []byte) error {
	v, err := ParseIPv4(string(b))
	if err != nil {
		return err
	}
	*ip = v
	return nil
}

// IPv6 is an IPv6 address in format ipv6, in a text form of RFC 4291 §2.2
// such as 2001:db8::1 or ::ffff:192.0.2.1. A zone (fe80::1%eth0) is
// refused. It is written as RFC 5952 recommends. The zero value is ::.
type IPv6 struct {
	a [16]byte
}

// ParseIPv6 reads an RFC 4291 IPv6 address.
func ParseIPv6(s string) (IPv6, error) {
	a, ok := parseIPv6(s)
	if !ok {
		return IPv6{}, formatError("ipv6", []byte(s))
	}
	return IPv6{a}, nil
}

// IPv6Of returns a as an IPv6. It returns an error if a is not an IPv6
// address or has a zone. An IPv4-mapped IPv6 address is accepted.
func IPv6Of(a netip.Addr) (IPv6, error) {
	if !a.Is6() || a.Zone() != "" {
		return IPv6{}, fmt.Errorf("geta.IPv6Of: %s is not an IPv6 address without a zone", a)
	}
	return IPv6{a.As16()}, nil
}

// Addr returns the address.
func (ip IPv6) Addr() netip.Addr { return netip.AddrFrom16(ip.a) }

// String returns the address as RFC 5952 recommends.
func (ip IPv6) String() string { return ip.Addr().String() }

// AppendText appends the address as RFC 5952 recommends.
func (ip IPv6) AppendText(b []byte) ([]byte, error) { return ip.Addr().AppendText(b) }

// MarshalText writes the address as RFC 5952 recommends.
func (ip IPv6) MarshalText() ([]byte, error) { return ip.AppendText(nil) }

// UnmarshalText reads an RFC 4291 IPv6 address.
func (ip *IPv6) UnmarshalText(b []byte) error {
	v, err := ParseIPv6(string(b))
	if err != nil {
		return err
	}
	*ip = v
	return nil
}

// parseIPv6 reads RFC 3986's IPv6address: eight groups of 1*4HEXDIG, the
// last two of which may be an IPv4address, with one "::" standing for one or
// more zero groups.
func parseIPv6(s string) ([16]byte, bool) {
	var a [16]byte
	head, tail, compressed := ipv6Groups(s)
	// The groups as 16-bit values; an IPv4address, last, is two of them.
	words := func(gs []string, last bool) ([]uint16, bool) {
		var w []uint16
		for i, g := range gs {
			if last && i == len(gs)-1 && strings.Contains(g, ".") {
				q, ok := decOctets(g)
				if !ok {
					return nil, false
				}
				w = append(w, uint16(q[0])<<8|uint16(q[1]), uint16(q[2])<<8|uint16(q[3]))
				continue
			}
			if !hexGroup(g) {
				return nil, false
			}
			var n uint16
			for j := 0; j < len(g); j++ {
				n = n<<4 | uint16(hexVal(g[j]))
			}
			w = append(w, n)
		}
		return w, true
	}
	hw, ok1 := words(head, false)
	tw, ok2 := words(tail, true)
	if !ok1 || !ok2 {
		return a, false
	}
	n := len(hw) + len(tw)
	if compressed && n > 7 || !compressed && n != 8 {
		return a, false
	}
	for i, w := range hw {
		a[2*i], a[2*i+1] = byte(w>>8), byte(w)
	}
	for i, w := range tw {
		j := 8 - len(tw) + i
		a[2*j], a[2*j+1] = byte(w>>8), byte(w)
	}
	return a, true
}

// decOctets reads RFC 3986's IPv4address: four dec-octets, 0 to 255 with no
// leading zero.
func decOctets(s string) ([4]byte, bool) {
	a, ok := snumQuad(s)
	if !ok {
		return a, false
	}
	for p := range strings.SplitSeq(s, ".") {
		if len(p) > 1 && p[0] == '0' {
			return a, false
		}
	}
	return a, true
}

func hexVal(c byte) byte {
	switch {
	case isDigit(c):
		return c - '0'
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}
