package geta

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"runtime"
	"slices"
	"time"
	"unicode/utf8"
)

// MethodQuery is the HTTP QUERY method
// (draft-ietf-httpbis-safe-method-w-body): safe and idempotent like GET,
// with the query in its request content.
const MethodQuery = "QUERY"

// Route is what one URL answers, one field per method. A zero field means
// the URL does not answer that method. geta answers HEAD with Get, and
// OPTIONS itself, listing the methods in Allow. A Get or Delete operation
// takes no body; serve one that reads a body as Query or Post.
type Route struct {
	Get    Operation
	Post   Operation
	Put    Operation
	Patch  Operation
	Delete Operation
	// Query answers QUERY ([MethodQuery]). [New] refuses it unless the
	// document is OpenAPI 3.2 ([WithOpenAPI]([OpenAPI32])).
	Query Operation
}

// methods lists the route's operations in a fixed order.
func (r Route) methods() []methodOp {
	all := []methodOp{
		{http.MethodGet, r.Get},
		{http.MethodPost, r.Post},
		{http.MethodPut, r.Put},
		{http.MethodPatch, r.Patch},
		{http.MethodDelete, r.Delete},
		{MethodQuery, r.Query},
	}
	out := all[:0]
	for _, m := range all {
		if m.op.op != nil {
			out = append(out, m)
		}
	}
	return out
}

type methodOp struct {
	method string
	op     Operation
}

// Operation is one method of one URL: a handler, its success status, and its
// documentation. It is built only by [Op] or [OpNoBody]; the zero value
// means the method is not served.
type Operation struct{ op *operation }

type operation struct {
	status int
	doc    Doc
	in     reflect.Type // the struct type a *In points to
	out    reflect.Type // the type a *Out points to; nil for OpNoBody
	call   func(ctx context.Context, in any) (any, error)
	site   string // where Op was called, for assembly errors
}

// Doc is the documentation and the failure table of an operation.
type Doc struct {
	Summary     string
	Description string
	// OperationID overrides the identifier derived from the method and path.
	OperationID string
	Tags        []string
	// Deprecated marks the operation deprecated in the document. Alone it
	// sends no header; set Deprecation for that.
	Deprecated bool
	// Deprecation, when set, is when the operation was or will be
	// deprecated, sent on every response as a Deprecation header (RFC 9745,
	// such as @1688169599). Sunset, when set, is when it is expected to stop
	// answering, sent on every response as a Sunset header (RFC 8594). [New]
	// refuses either without Deprecated, one that is not a whole second or is
	// past the year 9999, a Sunset before the Deprecation (RFC 9745 §4), and
	// any other header of the same name declared on the operation.
	Deprecation, Sunset time.Time
	// Failures maps returned errors to statuses, checked top to bottom. An
	// error no row matches answers 500.
	Failures []Failure
	// Security lists the schemes any one of which admits a request. nil
	// follows the gate's default; an empty, non-nil slice declares the
	// operation public.
	Security []Scheme
	// Scope is middleware this operation alone runs, in run order: after its
	// URL's directory scopes, before its input is bound. Use it for a rule
	// one method has and the others lack, such as a write only an
	// administrator may make. [New] checks its order with the rest of the
	// chain, and the document lists what it answers on this operation only.
	Scope Scope
	// BeforeGate is middleware this operation alone runs just before the
	// chain's first [Secure] gate, or after the root scope without one. Use
	// it for a rule that must hold before credentials are checked, such as a
	// rate limit on a login. [New] checks its order with the rest of the
	// chain and refuses a gate in it; the document lists what it answers on
	// this operation only. A root middleware after the gate that moves the
	// request to an operation whose BeforeGate did not run is answered 500.
	BeforeGate Scope
	// Limits, when set, gives the operation its own limits: [New] calls it
	// once with the App's ([WithLimits] or [DefaultLimits]), and the request
	// and response are handled under what it returns. New refuses what it
	// refuses of WithLimits, and refuses a component two operations would
	// document under different limits.
	Limits func(Limits) Limits
	// Timeout, when positive, replaces the length of every [Timeout] in the
	// operation's chain, shorter or longer. Each deadline keeps its place in
	// the chain. [New] refuses a negative Timeout, and a positive one on an
	// operation with no Timeout in its chain.
	Timeout time.Duration
}

// Op builds an operation whose handler returns a body. status is the success
// status, and it cannot be omitted. In and Out are inferred from h, so a
// handler of the wrong shape is a compile error on the line that calls Op.
//
// In is a struct whose tagged fields are bound from the request; a handler
// that reads nothing takes an empty struct. Out is the response body, or an
// envelope when it has fields tagged header, cookie, or body. A *[Stream] or
// *[Upgrade] Out answers with an event stream or a protocol switch.
func Op[In, Out any](status int, h func(context.Context, *In) (*Out, error), doc Doc) Operation {
	return Operation{&operation{
		status: status,
		doc:    doc,
		in:     reflect.TypeFor[In](),
		out:    reflect.TypeFor[Out](),
		call: func(ctx context.Context, in any) (any, error) {
			out, err := h(ctx, in.(*In))
			if err != nil {
				return nil, err
			}
			if out == nil {
				return nil, errNilOutput
			}
			return out, nil
		},
		site: caller(),
	}}
}

// OpNoBody builds an operation whose handler returns no body, such as a 204.
func OpNoBody[In any](status int, h func(context.Context, *In) error, doc Doc) Operation {
	return Operation{&operation{
		status: status,
		doc:    doc,
		in:     reflect.TypeFor[In](),
		call: func(ctx context.Context, in any) (any, error) {
			return nil, h(ctx, in.(*In))
		},
		site: caller(),
	}}
}

var errNilOutput = fmt.Errorf("geta: handler returned a nil output and a nil error")

// snapshot returns a copy of op whose Doc holds its own slices, so what the
// application or a middleware later does to the slices it passed or read
// changes nothing [New] assembled.
func (op *operation) snapshot() *operation {
	c := *op
	d := &c.doc
	d.Tags = slices.Clone(d.Tags)
	d.Failures = slices.Clone(d.Failures)
	d.Security = slices.Clone(d.Security)
	d.Scope = slices.Clone(d.Scope)
	d.BeforeGate = slices.Clone(d.BeforeGate)
	return &c
}

// checkText returns an error naming the first documented text of d that is
// not UTF-8.
func (d Doc) checkText() error {
	for _, f := range []struct{ name, text string }{
		{"Summary", d.Summary}, {"Description", d.Description}, {"OperationID", d.OperationID},
	} {
		if !utf8.ValidString(f.text) {
			return fmt.Errorf("Doc.%s %q is not UTF-8", f.name, f.text)
		}
	}
	for i, t := range d.Tags {
		if !utf8.ValidString(t) {
			return fmt.Errorf("Doc.Tags[%d] %q is not UTF-8", i, t)
		}
	}
	return nil
}

// caller returns the file and line Op or OpNoBody was called from.
// runtime.Caller(2) cannot fail: the stack always holds three frames.
func caller() string {
	_, file, line, _ := runtime.Caller(2)
	return fmt.Sprintf("%s:%d", file, line)
}
