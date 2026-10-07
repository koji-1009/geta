package geta

import (
	"bufio"
	"io"
	"net/http"
	"reflect"
)

// Deferred is a JSON request body that geta reads and validates when the
// handler calls [Deferred.Value], not before the handler runs. Declare it in
// place of the body's type, tagged as a body:"json" field is:
//
//	type PutIn struct {
//		geta.Conditional
//		Path
//		Body geta.Deferred[model.User] `body:"json"`
//	}
//
// It lets the handler's own checks answer before the content is processed,
// as RFC 9110 §13.2.1 orders them: the target is looked up (404), then
// [Conditional.Check] evaluates the preconditions (428, 412, 304), and only
// then does Value read the body (400, 408, 413). The document, the
// validation, and the value are those of a body of type T; T is a pointer
// for an optional body, and Value returns nil when none is sent.
//
// What the request's headers decide is still answered before the handler: a
// content coding or another media type (415), a declared length past
// MaxBodyBytes (413), and a missing required body (400). So is a request
// whose parameters are refused: its 400 lists the body's violations too.
//
// Value's error is returned by the handler as is; geta answers it as it
// answers a body bound before the handler, whatever failure rows would
// match it. A Deferred built by hand holds no body: Value returns T's zero
// value and nil.
type Deferred[T any] struct {
	value T
	read  func(dst reflect.Value) error // set when geta binds the input
	err   error
	done  bool
}

// Value reads and validates the body on its first call, and returns what the
// first call returned on every later one.
func (d *Deferred[T]) Value() (T, error) {
	if !d.done {
		d.done = true
		if d.read != nil {
			d.err = d.read(reflect.ValueOf(&d.value).Elem())
		}
	}
	return d.value, d.err
}

// deferredBody is how geta binds a Deferred of any T.
type deferredBody interface {
	valueType() reflect.Type
	setRead(func(dst reflect.Value) error)
	valueOf() reflect.Value
}

func (*Deferred[T]) valueType() reflect.Type               { return reflect.TypeFor[T]() }
func (d *Deferred[T]) setRead(f func(reflect.Value) error) { d.read = f }
func (d *Deferred[T]) valueOf() reflect.Value              { return reflect.ValueOf(&d.value).Elem() }

var deferredType = reflect.TypeFor[deferredBody]()

// deferredOf returns T when t, after one pointer, is a Deferred[T].
func deferredOf(t reflect.Type) (reflect.Type, bool) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || !reflect.PointerTo(t).Implements(deferredType) {
		return nil, false
	}
	return reflect.New(t).Interface().(deferredBody).valueType(), true
}

// bodyError is a deferred body geta refused, returned by Deferred.Value.
// dispatch answers it as it answers a body bound before the handler.
type bodyError struct{ be *bindError }

func (e *bodyError) Error() string {
	if e.be.err != nil {
		return "geta: the request body could not be taken in: " + e.be.err.Error()
	}
	return "geta: the request body was refused (" + http.StatusText(e.be.status) + ")"
}

func (e *bodyError) Unwrap() error { return e.be.err }

// admit judges a deferred body by what the request's headers and first byte
// show, as bind does before it reads the rest, and returns the body to read,
// or nil when none was sent.
func (b *bodyPlan) admit(w http.ResponseWriter, r *http.Request, limits Limits) (io.Reader, *bindError) {
	n := declaredLength(r)
	if n > 0 {
		if be := b.judge(w, r); be != nil {
			return nil, be
		}
		if n > limits.MaxBodyBytes {
			return nil, tooLong(limits.MaxBodyBytes)
		}
	}
	br := bufio.NewReader(http.MaxBytesReader(w, r.Body, limits.MaxBodyBytes))
	if _, err := br.Peek(1); err != nil {
		if err == io.EOF {
			if !b.optional {
				return nil, bodyViolation("$", "missing required request body")
			}
			return nil, nil
		}
		return nil, readFailure(err, limits)
	}
	if n <= 0 {
		if be := b.judge(w, r); be != nil {
			return nil, be
		}
	}
	return br, nil
}

// later sets d to read body, which admit returned, when the handler asks.
func (b *bodyPlan) later(d deferredBody, body io.Reader, limits Limits) {
	if body == nil {
		return // absent and optional: Value returns nil
	}
	d.setRead(func(dst reflect.Value) error {
		f := getFastDecoder(limits)
		defer f.release()
		f.opts = b.opts
		var be *bindError
		if _, err := f.body.ReadFrom(body); err != nil {
			be = readFailure(err, limits)
		} else {
			be = b.decode(f, dst, limits)
		}
		switch {
		case be == nil:
			return nil
		case be.status == http.StatusBadRequest:
			// Its violations, as bind lists them.
			be = &bindError{status: http.StatusBadRequest, detail: "the request does not match its contract", errs: be.errs, omitted: be.omitted}
		}
		return &bodyError{be}
	})
}
