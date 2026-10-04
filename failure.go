package geta

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"unicode/utf8"
)

// Failure is one row of an operation's failure table: when a returned error
// matches, the response carries this status and detail. Build rows with [On]
// or [OnAs]; the zero value is invalid and rejected by [New].
//
// Only rows can produce a 4xx or 5xx from a handler's error, so the failures
// in the OpenAPI document and the failures on the wire are the same set.
type Failure struct {
	status int
	detail string
	match  func(error) bool
	label  string // what the row matches, for messages
	bad    string // why the row is invalid, if it is
	typ    string // the problem type URI; "" means about:blank
	// describe, set by OnAsProblem, turns the matched error into the
	// problem's extension members and the response's headers.
	describe *describer
}

// describer holds OnAsProblem's function and the type P it returns.
type describer struct {
	t  reflect.Type
	fn func(error) reflect.Value // an addressable P
}

// OnAsProblem answers status when errors.AsType[T](err) succeeds, as [OnAs]
// does, with a problem describe builds from the error. P's JSON members are
// the problem's extension members (RFC 9457 §3.2); a string member named
// detail replaces the row's detail (a nil *string keeps it). P may instead
// be an envelope: its header fields are response headers (a Retry-After on a
// 429 or 503), and its body:"json" field holds the members.
//
//	geta.OnAsProblem(http.StatusTooManyRequests, "quota exceeded", func(e *store.QuotaError) Quota {
//		return Quota{RetryAfter: e.Wait, Body: QuotaMembers{Remaining: e.Remaining}}
//	})
//
// The document states P's members and headers with the row. [New] refuses a
// P or body that is not a struct, a member named type, title, status,
// instance, or errors, a detail that is not a string, a cookie, status, or
// raw body field, and a nil describe. err.Error() never reaches the
// response; only what describe returns does.
func OnAsProblem[T error, P any](status int, detail string, describe func(T) P) Failure {
	f := OnAs[T](status, detail)
	f.label = "errors.As " + reflect.TypeFor[T]().String() + ", described by " + reflect.TypeFor[P]().String()
	if describe == nil {
		f.bad = "geta.OnAsProblem with a nil describe function"
		return f
	}
	f.describe = &describer{t: reflect.TypeFor[P](), fn: func(err error) reflect.Value {
		e, _ := errors.AsType[T](err)
		p := describe(e)
		return reflect.ValueOf(&p).Elem()
	}}
	return f
}

// Type gives the row's problem a type, a URI reference naming the kind of
// failure (RFC 9457 §3.1.1). Rows without one send about:blank. The document
// lists each row's type beside its detail. [New] refuses a uri that is not a
// URI reference.
func (f Failure) Type(uri string) Failure {
	f.typ = uri
	if u, err := url.Parse(uri); uri == "" || err != nil || u.String() != uri {
		f.bad = fmt.Sprintf("failure row %q: problem type %q is not a URI reference", f.label, uri)
	}
	return f
}

func (f Failure) problemType() string {
	if f.typ == "" {
		return "about:blank"
	}
	return f.typ
}

// On answers status with detail when errors.Is(err, target). An empty detail
// sends only the status's standard text. err.Error() never reaches the
// response. [New] refuses a nil target, and one that is or wraps
// context.DeadlineExceeded, since geta answers that 504 before the table is
// read.
func On(target error, status int, detail string) Failure {
	f := Failure{status: status, detail: detail}
	if target == nil {
		f.bad = "geta.On with a nil target matches nothing"
		return f
	}
	f.label = fmt.Sprintf("errors.Is %v", target)
	f.match = func(err error) bool { return errors.Is(err, target) }
	if errors.Is(target, context.DeadlineExceeded) {
		f.bad = fmt.Sprintf("failure row %q matches context.DeadlineExceeded, which geta answers 504", f.label)
	}
	return f
}

// OnAs answers status with detail when errors.AsType[T](err) succeeds.
func OnAs[T error](status int, detail string) Failure {
	return Failure{
		status: status,
		detail: detail,
		label:  "errors.As " + reflect.TypeFor[T]().String(),
		match: func(err error) bool {
			_, ok := errors.AsType[T](err)
			return ok
		},
	}
}

// Status returns the row's response status.
func (f Failure) Status() int { return f.status }

func (f Failure) check() error {
	switch {
	case f.bad != "":
		return errors.New(f.bad)
	case f.match == nil:
		return errors.New("zero geta.Failure: build rows with geta.On or geta.OnAs")
	case f.status < 400 || f.status > 599:
		return fmt.Errorf("failure row %q answers %d, which is not a 4xx or 5xx status", f.label, f.status)
	case !utf8.ValidString(f.detail):
		return fmt.Errorf("failure row %q: detail %q is not UTF-8", f.label, f.detail)
	}
	return nil
}
