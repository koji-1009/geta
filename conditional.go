package geta

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"
)

// Conditional binds the four precondition header fields of RFC 9110 §13.1,
// for an operation's input to embed. The handler calls [Conditional.Check]
// with the current validators of the representation, after its own checks
// (the target exists, the caller may act on it) and before it acts
// (RFC 9110 §13.2.1).
//
//	type PutIn struct {
//		geta.Conditional
//		Path
//		Body model.User `body:"json"`
//	}
//
// The document lists the four header parameters and the statuses a failed
// precondition answers: 412 on every method, and 304 on a GET. A field sent
// on several lines is read as one list (RFC 9110 §5.3).
//
// A client sets the fields it sends: Conditional{IfMatch: new(`"v1"`)}.
//
// Check reads the request's method, which geta records when it binds the
// input. A Conditional built by hand has none and is checked as for a method
// other than GET or HEAD: a matching If-None-Match is 412, never 304, and
// If-Modified-Since is ignored. Test a GET's 304 through a request, as
// getatest sends it.
type Conditional struct {
	// IfMatch is "*" or a list of entity tags, compared strongly.
	IfMatch *string `header:"If-Match"`
	// IfNoneMatch is "*" or a list of entity tags, compared weakly.
	IfNoneMatch *string `header:"If-None-Match"`
	// IfModifiedSince is an HTTP-date; only a GET or HEAD reads it.
	IfModifiedSince *string `header:"If-Modified-Since"`
	// IfUnmodifiedSince is an HTTP-date.
	IfUnmodifiedSince *string `header:"If-Unmodified-Since"`

	method string // the request's, set when geta binds the input
}

// RequireConditional is [Conditional] for an operation that refuses an
// unconditional request (RFC 6585 §3). Its Check answers 428 when the
// request carries no precondition the method evaluates: If-Match,
// If-None-Match, a valid If-Unmodified-Since, or, on a GET or HEAD, a valid
// If-Modified-Since. The document lists 428 on the operation.
type RequireConditional struct{ Conditional }

var (
	conditionalType        = reflect.TypeFor[Conditional]()
	requireConditionalType = reflect.TypeFor[RequireConditional]()
)

// conditionalDocs describes the four parameters in the document.
var conditionalDocs = map[string]string{
	"If-Match":            `"*" or a list of one or more entity tags (RFC 9110 §13.1.1), compared strongly; a failed match answers 412`,
	"If-None-Match":       `"*" or a list of one or more entity tags (RFC 9110 §13.1.2), compared weakly; a match answers 304 to a GET or HEAD and 412 otherwise`,
	"If-Modified-Since":   "An HTTP-date (RFC 9110 §13.1.3), read on a GET or HEAD without If-None-Match; an unmodified representation answers 304. A value that is not one HTTP-date is ignored",
	"If-Unmodified-Since": "An HTTP-date (RFC 9110 §13.1.4), read without If-Match; a representation modified since answers 412. A value that is not one HTTP-date is ignored",
}

// PreconditionError is returned by [Conditional.Check] when the request must
// not be performed. geta answers it with its status on an operation whose
// input embeds [Conditional]. Returned by any other operation, it is a
// logged 500 whatever the operation's failure rows.
type PreconditionError struct {
	status   int
	field    string // the header field that decided
	detail   string
	etag     string
	modified time.Time
}

func (e *PreconditionError) Error() string {
	return fmt.Sprintf("geta: %s %s (%d)", e.field, e.detail, e.status)
}

// Status returns the status geta answers: 304, 400, 412, or 428.
func (e *PreconditionError) Status() int { return e.status }

// write answers e. A 304 carries the entity tag, or else the modification
// date (RFC 9110 §15.4.5); other statuses are problems.
func (e *PreconditionError) write(w http.ResponseWriter, r *http.Request) {
	switch e.status {
	case http.StatusNotModified:
		h := w.Header()
		if e.etag != "" {
			h.Set("ETag", e.etag)
		} else if !e.modified.IsZero() {
			h.Set("Last-Modified", e.modified.UTC().Format(http.TimeFormat))
		}
		w.WriteHeader(http.StatusNotModified)
	case http.StatusBadRequest:
		writeProblem(w, r, http.StatusBadRequest, "the request does not match its contract",
			[]Violation{{In: "header", Path: e.field, Message: e.detail}})
	default:
		writeProblem(w, r, e.status, e.field+" "+e.detail, nil)
	}
}

// errBadETag is returned by Check for an etag that is not one entity tag.
var errBadETag = errors.New("geta: Conditional.Check: etag is not one entity tag")

// Check evaluates the request's preconditions against the current
// representation of the target, in the order RFC 9110 §13.2.2 sets, and
// returns nil when the method may be performed. etag is the
// representation's entity tag as an ETag header carries it (`"v1"` or
// `W/"v1"`), or "" when it has none; modified is its last modification, or
// the zero Time when it has none, compared to the second.
//
//  1. If-Match: "*" matches; a list matches when one of its tags compares
//     strongly equal to etag (a weak etag never does). No match is 412.
//  2. Without If-Match, If-Unmodified-Since: a representation modified
//     after the date is 412. Without a modification date, it is ignored.
//  3. If-None-Match: "*" matches; a list matches when one of its tags
//     compares weakly equal to etag. A match is 304 on a GET or HEAD, 412
//     otherwise.
//  4. Without If-None-Match, on a GET or HEAD, If-Modified-Since: a
//     representation not modified after the date is 304. Without a
//     modification date, it is ignored.
//
// A date that is not one HTTP-date is ignored (RFC 9110 §13.1.3, §13.1.4).
// An If-Match or If-None-Match that is neither "*" nor a list of one or more
// entity tags, an empty one included, is 400. An etag that is not one entity
// tag is a handler defect: Check returns an error that is not a
// PreconditionError, answered as a logged 500 whatever the failure rows. A
// failed precondition is always refused, never treated as already applied.
func (c *Conditional) Check(etag string, modified time.Time) error {
	return c.check(true, etag, modified)
}

// CheckAbsent evaluates the request's preconditions for a target with no
// current representation, such as a PUT that would create it: If-Match
// fails (412), and If-None-Match, "*" included, holds. Dates are ignored.
func (c *Conditional) CheckAbsent() error {
	return c.check(false, "", time.Time{})
}

// Check is [Conditional.Check], but first answers 428 for a request that
// carries no precondition.
func (c *RequireConditional) Check(etag string, modified time.Time) error {
	if err := c.required(); err != nil {
		return err
	}
	return c.Conditional.Check(etag, modified)
}

// CheckAbsent is [Conditional.CheckAbsent], but first answers 428 for a
// request that carries no precondition.
func (c *RequireConditional) CheckAbsent() error {
	if err := c.required(); err != nil {
		return err
	}
	return c.Conditional.CheckAbsent()
}

// required returns a 428 if the request carries no precondition its method
// evaluates.
func (c *Conditional) required() error {
	_, unmodified := httpDate(c.IfUnmodifiedSince)
	_, modifiedSince := httpDate(c.IfModifiedSince)
	if c.IfMatch != nil || c.IfNoneMatch != nil || unmodified || modifiedSince && c.safe() {
		return nil
	}
	return &PreconditionError{status: http.StatusPreconditionRequired, field: "If-Match",
		detail: "is required"}
}

// safe reports whether the request is a GET or HEAD. An unbound Conditional
// has no method and is not.
func (c *Conditional) safe() bool {
	return c.method == http.MethodGet || c.method == http.MethodHead
}

func (c *Conditional) check(exists bool, etag string, modified time.Time) error {
	if etag != "" && !validETag(etag) {
		return fmt.Errorf("%w: %q", errBadETag, etag)
	}
	if !modified.IsZero() {
		modified = modified.UTC().Truncate(time.Second)
	}
	refuse := func(status int, field, detail string) error {
		return &PreconditionError{status: status, field: field, detail: detail, etag: etag, modified: modified}
	}
	const malformed = malformedTags
	// 1, and 2 only without If-Match.
	if c.IfMatch != nil {
		tags, star, ok := conditionTags(*c.IfMatch)
		switch {
		case !ok:
			return refuse(http.StatusBadRequest, "If-Match", malformed)
		case !exists:
			return refuse(http.StatusPreconditionFailed, "If-Match", "failed: the target has no current representation")
		case star:
		case !matches(tags, etag, strongEqual):
			return refuse(http.StatusPreconditionFailed, "If-Match", "failed: no tag matches the current representation's")
		}
	} else if t, ok := httpDate(c.IfUnmodifiedSince); ok && exists && !modified.IsZero() && modified.After(t) {
		return refuse(http.StatusPreconditionFailed, "If-Unmodified-Since", "failed: the representation was modified after it")
	}
	// 3, and 4 only without If-None-Match.
	if c.IfNoneMatch != nil {
		tags, star, ok := conditionTags(*c.IfNoneMatch)
		switch {
		case !ok:
			return refuse(http.StatusBadRequest, "If-None-Match", malformed)
		case exists && (star || matches(tags, etag, weakEqual)):
			if c.safe() {
				return refuse(http.StatusNotModified, "If-None-Match", "matches the current representation")
			}
			return refuse(http.StatusPreconditionFailed, "If-None-Match", "failed: it matches the current representation")
		}
	} else if t, ok := httpDate(c.IfModifiedSince); ok && c.safe() && exists && !modified.IsZero() && !modified.After(t) {
		return refuse(http.StatusNotModified, "If-Modified-Since", "the representation was not modified after it")
	}
	return nil
}

// unvalidated evaluates the preconditions of a request to an operation whose
// input does not embed [Conditional], and returns the refusal, or nil to go
// on. Such an operation declares no validator, so geta evaluates them itself,
// before the request content is processed (RFC 9110 §13.2.1), against a
// current representation with no entity tag and no modification date:
//
//   - If-Match other than "*" fails, 412: no listed tag matches a
//     representation that has none (§13.1.1). "*" holds; a handler that
//     finds no target answers its own 404, which takes precedence.
//   - If-None-Match "*" fails: 304 on a GET or HEAD, 412 otherwise
//     (§13.1.2). Any other value holds. A GET or HEAD is answered 304 only
//     where a 200 would be (§15.4.5): on a GET whose successes hold none, it
//     is not evaluated, nor on one geta.ETag tags, which reads it once the
//     handler has answered (notModified).
//   - If-Modified-Since and If-Unmodified-Since are ignored: there is no
//     modification date (§13.1.3, §13.1.4).
//
// A value that is not a list of entity tags is evaluated as the RFC says of
// any other value: If-Match fails and If-None-Match holds.
func (c *compiledOp) unvalidated(r *http.Request) *PreconditionError {
	safe := r.Method == http.MethodGet || r.Method == http.MethodHead
	if v := r.Header.Values("If-Match"); len(v) > 0 && !starred(v) {
		return &PreconditionError{status: http.StatusPreconditionFailed, field: "If-Match",
			detail: "failed: the operation declares no entity tag"}
	}
	if v := r.Header.Values("If-None-Match"); len(v) > 0 && starred(v) {
		switch {
		case !safe:
			return &PreconditionError{status: http.StatusPreconditionFailed, field: "If-None-Match",
				detail: "failed: it matches the current representation"}
		case c.notModified():
			return &PreconditionError{status: http.StatusNotModified, field: "If-None-Match", detail: "matches the current representation"}
		}
	}
	return nil
}

// notModified reports whether geta answers 304 itself to If-None-Match: * on
// an operation whose input does not embed Conditional: a GET that answers
// 200 and that geta.ETag does not tag.
func (c *compiledOp) notModified() bool {
	return c.method == http.MethodGet && !c.options && slices.Contains(c.successes(), http.StatusOK) && !c.etagged()
}

// starred reports whether a precondition field's lines are "*" alone.
func starred(lines []string) bool {
	_, star, _ := conditionTags(*conditionField(lines))
	return star
}

// conditionField joins a precondition field's lines into one list
// (RFC 9110 §5.3), dropping empty lines, or returns nil when there are none.
//
// The value is not a string parameter: it is not checked for UTF-8, since an
// entity tag may hold obs-text (§8.8.3), nor bound by MaxStringLength, which
// [ETag] could not apply alike.
func conditionField(lines []string) *string {
	if len(lines) == 0 {
		return nil
	}
	v := lines[0]
	if len(lines) > 1 {
		v = strings.Join(slices.DeleteFunc(slices.Clone(lines), func(s string) bool { return s == "" }), ", ")
	}
	return &v
}

// malformedTags says why an If-Match or If-None-Match is a 400.
const malformedTags = `is neither "*" nor a list of one or more entity tags`

// conditionTags parses an If-Match or If-None-Match value: "*" surrounded
// only by OWS, or a list of one or more entity tags. ok is false for anything
// else, including a list holding "*" (RFC 9110 §13.1.1) and a list with no
// tag, which names no condition.
func conditionTags(v string) (tags []string, star, ok bool) {
	if strings.Trim(v, " \t") == "*" {
		return nil, true, true
	}
	tags, ok = appendETags(nil, v)
	return tags, false, ok && len(tags) > 0
}

// validETag reports whether s is one entity tag, with nothing around it.
func validETag(s string) bool {
	tags, ok := appendETags(nil, s)
	return ok && len(tags) == 1 && tags[0] == s
}

func matches(tags []string, etag string, equal func(a, b string) bool) bool {
	if etag == "" {
		return false
	}
	for _, t := range tags {
		if equal(t, etag) {
			return true
		}
	}
	return false
}

// strongEqual is the strong comparison of RFC 9110 §8.8.3.2.
func strongEqual(a, b string) bool {
	return a == b && !strings.HasPrefix(a, "W/")
}

// weakEqual is the weak comparison of RFC 9110 §8.8.3.2.
func weakEqual(a, b string) bool {
	return strings.TrimPrefix(a, "W/") == strings.TrimPrefix(b, "W/")
}

// httpDateForms are the forms of an HTTP-date (RFC 9110 §5.6.7), each with
// the separator after its day name: IMF-fixdate, RFC 850, and asctime.
var httpDateForms = []struct {
	layout string
	sep    string
	long   bool // the day name in full (RFC 850)
}{
	{http.TimeFormat, ",", false},
	{"Monday, 02-Jan-06 15:04:05 GMT", ",", true},
	{time.ANSIC, " ", false},
}

// httpDate parses an HTTP-date (RFC 9110 §5.6.7) in any of its forms,
// exactly as the grammar writes it; time.Parse alone is more lenient. A leap
// second, 23:59:60, reads as 23:59:59. Anything else, a list of dates
// included, is no date.
func httpDate(v *string) (time.Time, bool) {
	if v == nil {
		return time.Time{}, false
	}
	s := *v
	// Every form has a space before and after its time of day.
	s = strings.Replace(s, " 23:59:60 ", " 23:59:59 ", 1)
	for _, form := range httpDateForms {
		t, err := time.Parse(form.layout, s)
		if err != nil {
			continue
		}
		// Round-trip the date; the day name is only checked to be one.
		name, rest, _ := strings.Cut(s, form.sep)
		_, written, _ := strings.Cut(t.Format(form.layout), form.sep)
		if rest != written || !dayName(name, form.long) {
			return time.Time{}, false
		}
		if form.long {
			// A two-digit year is at most 50 years ahead (RFC 9110 §5.6.7).
			now := time.Now().Year()
			year := now - now%100 + t.Year()%100
			if year > now+50 {
				year -= 100
			}
			t = t.AddDate(year-t.Year(), 0, 0)
		}
		return t.UTC(), true
	}
	return time.Time{}, false
}

// dayName reports whether s is a day name as an HTTP-date writes it: in full
// if long, else its first three letters, case-sensitive.
func dayName(s string, long bool) bool {
	for d := time.Sunday; d <= time.Saturday; d++ {
		if name := d.String(); long && s == name || !long && s == name[:3] {
			return true
		}
	}
	return false
}
