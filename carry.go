package geta

import (
	"fmt"
	"slices"
	"strings"
)

// carriers maps a place to what a string there can carry. The schema of a
// string or text-type parameter at that place gets the pattern, enforced
// and documented, so the document admits no value that cannot arrive. An
// output header's schema gets it too, and a handler value that breaks it is
// a 500 (outPlan.setHeaders).
//
// A header value is a field-value (RFC 9110 §5.5): no control character but
// a tab, and no whitespace at either end. A cookie value is what net/http's
// Request.CookiesNamed returns: bytes %x20-7E but DQUOTE, ";", and "\".
//
// Numbers, booleans, base64, and carriedFormats need no pattern. An enum
// member the place cannot carry is refused; otherwise an enum needs none.
// An author's pattern is kept, and the place's joins it in allOf.
var carriers = map[string]impliedPattern{
	"header": {
		pattern: `^(?:[^\x00-\x20\x7F](?:[^\x00-\x08\x0A-\x1F\x7F]*[^\x00-\x20\x7F])?)?$`,
		holds:   headerCarries,
		why:     "is not a valid header field value",
	},
	"cookie": {
		pattern: `^[\x20\x21\x23-\x3A\x3C-\x5B\x5D-\x7E]*$`,
		holds:   cookieCarries,
		why:     "is not a valid cookie value",
	},
}

// carriedFormats are the formats geta checks whose every value a place
// carries. A FormatType's format is not among them, whatever its name, since
// geta cannot read the application's grammar.
var carriedFormats = map[string][]string{
	"header": {"date-time", "date", "time", "duration", "ipv4", "ipv6", "uuid"},
	"cookie": {"date-time", "date", "time", "duration", "ipv4", "ipv6", "uuid"},
}

// headerElement is what an element of a header list (a slice header) can
// carry: a non-empty header field value with no comma.
var headerElement = impliedPattern{
	pattern: `^[^\x00-\x20\x7F,](?:[^\x00-\x08\x0A-\x1F\x7F,]*[^\x00-\x20\x7F,])?$`,
	holds:   headerElementCarries,
	why:     "is not a valid header list element",
}

// carried returns use, the schema of the parameter name at loc, restricted
// to what loc carries; an enum member it cannot carry is an error. A header
// array's items are held to headerElement.
func carried(loc, name string, use *schema) (*schema, error) {
	return carriedAt(loc, fmt.Sprintf("%s parameter %q", loc, name), use)
}

// carriedAt is carried with errors naming what, for a value that is not a
// parameter.
func carriedAt(loc, what string, use *schema) (*schema, error) {
	rule, ok := carriers[loc]
	if !ok {
		return use, nil
	}
	if use.Type == "array" && use.Items != nil {
		if loc == "header" {
			rule = headerElement
		}
		items, err := carriedBy(loc, what, use.Items, rule)
		if err != nil || items == use.Items {
			return use, err
		}
		s := use.clone()
		s.Items = items
		return s, nil
	}
	return carriedBy(loc, what, use, rule)
}

// carriedBy is carriedAt with an explicit rule.
func carriedBy(loc, what string, use *schema, rule impliedPattern) (*schema, error) {
	if use.Type != "string" || use.ContentEncoding != "" ||
		use.check != nil && slices.Contains(carriedFormats[loc], use.Format) {
		return use, nil
	}
	if len(use.Enum) > 0 {
		for _, e := range use.Enum {
			if !rule.holds(e) {
				return nil, fmt.Errorf("%s: enum member %q %s", what, e, rule.why)
			}
		}
		return use, nil
	}
	s := use.clone()
	s.implied = append(slices.Clip(use.implied), rule)
	return s, nil
}

// headerCarries reports whether s matches the header pattern of carriers.
// Bytes past ASCII are obs-text.
func headerCarries(s string) bool {
	if s == "" {
		return true
	}
	if isOWS(s[0]) || isOWS(s[len(s)-1]) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if b := s[i]; b < 0x20 && b != '\t' || b == 0x7F {
			return false
		}
	}
	return true
}

func isOWS(b byte) bool { return b == ' ' || b == '\t' }

// headerElementCarries reports whether s matches headerElement's pattern.
func headerElementCarries(s string) bool {
	return s != "" && !strings.Contains(s, ",") && headerCarries(s)
}

// cookieCarries reports whether s matches the cookie pattern of carriers.
func cookieCarries(s string) bool {
	for i := 0; i < len(s); i++ {
		if b := s[i]; b < 0x20 || b > 0x7E || b == '"' || b == ';' || b == '\\' {
			return false
		}
	}
	return true
}
