package geta

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
)

// ETag tags a 200 response to GET or HEAD with a strong validator over its
// body, and answers 304 with no body when If-None-Match matches it, comparing
// weakly (RFC 9110 §13.1.2). A tag the handler set is kept. Behind
// [Compress] or [Gzip], a client holding a coded form's tag gets its 304
// too. Other methods and streams pass through untouched.
//
// If-None-Match is read as [Conditional.Check] reads it. A value that is
// neither "*" nor a list of one or more entity tags, an empty one included,
// answers 400 in place of the 200; the document lists it beside the 304.
//
// On an operation whose input does not embed [Conditional], ETag evaluates
// every precondition, in RFC 9110 §13.2.2's order, as Check would against
// the tag it sends and the Last-Modified the handler set: If-Match with that
// tag holds and any other is 412, If-None-Match matching it is 304. On
// another 2xx it evaluates them against the handler's own ETag and
// Last-Modified, answering no 304. geta does not evaluate them before the
// handler there: the tag is known only once the body is.
func ETag() Middleware {
	m := Ordered(OrderValidate, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				next.ServeHTTP(w, r)
				return
			}
			inm := r.Header.Values("If-None-Match")
			b := newBuffer(w)
			returned := false
			defer func() {
				if !returned {
					b.lost()
				}
			}()
			next.ServeHTTP(b, r)
			returned = true
			if !b.held() || b.status == 0 {
				return
			}
			h := b.header
			// An operation without Conditional has its preconditions read
			// here, against what it answered (selected); one with Conditional
			// has evaluated them, and only If-None-Match is read again.
			own := selectingAt(r)
			if b.status != http.StatusOK {
				if own {
					if pe := selected(r, b.status, h.Get("ETag"), lastModified(h)); pe != nil {
						b.lost()
						pe.write(w, r)
						return
					}
				}
				b.send(r, b.status, b.body.Bytes())
				return
			}
			tag := h.Get("ETag")
			if tag == "" {
				sum := sha256.Sum256(b.body.Bytes())
				tag = `"` + base64.RawURLEncoding.EncodeToString(sum[:18]) + `"`
				h.Set("ETag", tag)
			}
			if own {
				switch pe := selected(r, b.status, strings.TrimSpace(tag), lastModified(h)); {
				case pe == nil:
				case pe.status == http.StatusNotModified:
					for _, k := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Content-Language", "Content-Range"} {
						h.Del(k)
					}
					b.send(r, http.StatusNotModified, nil)
					return
				default:
					b.lost()
					pe.write(w, r)
					return
				}
			} else if len(inm) > 0 {
				tags, star, ok := conditionTags(*conditionField(inm))
				if !ok {
					b.lost()
					writeProblem(w, r, http.StatusBadRequest, "the request does not match its contract",
						[]Violation{{In: "header", Path: "If-None-Match", Message: malformedTags}})
					return
				}
				if star || matches(tags, strings.TrimSpace(tag), weakEqual) {
					for _, k := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Content-Language", "Content-Range"} {
						h.Del(k)
					}
					b.send(r, http.StatusNotModified, nil)
					return
				}
			}
			b.send(r, b.status, b.body.Bytes())
		})
	})
	m.name = "etag"
	m.etag = true
	m.answers = []answer{
		{status: http.StatusNotModified, reason: "The representation has not changed", getOnly: true, held: true},
		{status: http.StatusBadRequest, reason: "If-None-Match is neither \"*\" nor a list of one or more entity tags", getOnly: true, held: true},
	}
	m.exposes = []string{"ETag"}
	return m
}

// etagList parses If-None-Match values into their entity tags, "*" included.
// Tags may hold commas inside their quotes.
func etagList(values []string) []string {
	var tags []string
	for _, v := range values {
		if strings.Trim(v, " \t") == "*" {
			tags = append(tags, "*")
			continue
		}
		tags, _ = appendETags(tags, v)
	}
	return tags
}

// appendETags appends the entity tags of the list v to tags, stopping at the
// first element that is not one (its end cannot be known). ok reports
// whether all of v is an entity-tag list (RFC 9110 §8.8.3).
func appendETags(tags []string, v string) (_ []string, ok bool) {
	ows := func(i int) int {
		for i < len(v) && (v[i] == ' ' || v[i] == '\t') {
			i++
		}
		return i
	}
	i := 0
	for {
		for i = ows(i); i < len(v) && v[i] == ','; i = ows(i + 1) {
		}
		if i == len(v) {
			return tags, true
		}
		start := i
		if strings.HasPrefix(v[i:], "W/") {
			i += 2
		}
		if i == len(v) || v[i] != '"' {
			return tags, false
		}
		for i++; i < len(v) && etagc(v[i]); i++ {
		}
		if i == len(v) || v[i] != '"' {
			return tags, false
		}
		i++
		end := i
		if i = ows(i); i < len(v) && v[i] != ',' {
			return tags, false
		}
		tags = append(tags, v[start:end])
	}
}

// etagc reports whether b may appear inside an entity tag's quotes:
// %x21 / %x23-7E / obs-text.
func etagc(b byte) bool {
	return b == 0x21 || b >= 0x23 && b != 0x7F
}
