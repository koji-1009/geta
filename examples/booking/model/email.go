package model

import (
	"errors"
	"strings"
)

// Email is a mail address as this application takes one: a local part, an
// @, and a domain with a dot, no whitespace. geta has no email type, because
// which addresses an application takes is its own (strict RFC 5321 refuses
// a..b@docomo.ne.jp, which mail carries); Email names the format with
// SchemaFormat, so the document says format email, and its UnmarshalText
// alone holds every request to it, as a parameter or a member.
type Email struct{ local, domain string }

// ParseEmail reads an address.
func ParseEmail(s string) (Email, error) {
	var e Email
	err := e.UnmarshalText([]byte(s))
	return e, err
}

// SchemaFormat names the format the document gives Email (geta.FormatType).
func (Email) SchemaFormat() string { return "email" }

// MarshalText writes the address.
func (e Email) MarshalText() ([]byte, error) { return []byte(e.String()), nil }

// UnmarshalText reads an address, refusing what this application does not
// take.
func (e *Email) UnmarshalText(b []byte) error {
	s := string(b)
	local, domain, ok := strings.Cut(s, "@")
	if !ok || local == "" || len(s) > 254 || strings.Contains(domain, "@") ||
		!strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") ||
		strings.ContainsAny(s, " \t\r\n\"<>,;") {
		return errors.New("not a mail address this application takes")
	}
	*e = Email{local, strings.ToLower(domain)}
	return nil
}

// String is the address.
func (e Email) String() string {
	if e.local == "" {
		return ""
	}
	return e.local + "@" + e.domain
}
