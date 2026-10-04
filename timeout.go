package geta

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Timeout sets a deadline of d on the request's context. It stops nothing
// by itself: a handler that watches its context returns
// context.DeadlineExceeded, which answers 504. The deadline bounds the time
// to a response only; once an event stream starts, it is lifted.
//
// The request body gets the same length, counted from its first read. Content
// that has not arrived by then is a 408 and the connection is closed. This
// applies only to geta's own reading, and only where the connection supports
// [http.ResponseController.SetReadDeadline]. On HTTP/1.x, a body left unread
// when the request is answered gets the same length from the answer for
// net/http to discard it; past that the connection is closed.
//
// An operation's [Doc.Timeout] replaces d for that operation in every
// Timeout of its chain. [New] refuses a d that is not positive.
func Timeout(d time.Duration) Middleware {
	var bad error
	if d <= 0 {
		bad = fmt.Errorf("geta.Timeout %v is not positive", d)
	}
	m := Ordered(OrderDeadline, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, stop := withLiftableDeadline(r.Context(), d, operationTimeout(r))
			defer stop()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	m.name = "timeout"
	m.bad = bad
	m.timeout = true
	m.length = d
	m.answers = []answer{
		{status: http.StatusGatewayTimeout, reason: "The deadline passed before a response"},
		{status: http.StatusRequestTimeout, reason: "The request body did not arrive before the deadline", bodyOnly: true},
	}
	return m
}

// operationTimeout returns the Doc.Timeout of the operation r now matches,
// or 0.
func operationTimeout(r *http.Request) time.Duration {
	rc := requestFrom(r.Context())
	if rc == nil {
		return 0
	}
	rc.rematch(r)
	if rc.match == nil {
		return 0
	}
	return rc.match.Doc.Timeout
}

// deadlineCtx is a context with a deadline that can be lifted. Until lifted
// it behaves as context.WithTimeout; after, only the parent can end it.
type deadlineCtx struct {
	context.Context // a cancel context under the parent
	mu              sync.Mutex
	start           time.Time
	def             time.Duration // the Timeout's own length
	used            time.Duration // the length the deadline was counted with
	deadline        time.Time
	lifted          bool
	timer           *time.Timer
	cancel          context.CancelCauseFunc
}

type deadlineKey struct{}

// withLiftableDeadline returns parent with a deadline op from now, or def if
// op is not positive, keeping an earlier deadline of parent's.
func withLiftableDeadline(parent context.Context, def, op time.Duration) (context.Context, func()) {
	inner, cancel := context.WithCancelCause(parent)
	used := def
	if op > 0 {
		used = op
	}
	now := time.Now()
	c := &deadlineCtx{Context: inner, start: now, def: def, used: used, deadline: now.Add(used), cancel: cancel}
	if pd, ok := parent.Deadline(); ok && pd.Before(c.deadline) {
		c.deadline = pd
	}
	c.timer = time.AfterFunc(time.Until(c.deadline), func() { cancel(context.DeadlineExceeded) })
	return c, func() {
		c.timer.Stop()
		cancel(context.Canceled)
	}
}

func (c *deadlineCtx) Deadline() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lifted {
		return c.Context.Deadline()
	}
	return c.deadline, true
}

func (c *deadlineCtx) Err() error {
	err := c.Context.Err()
	if err != nil && context.Cause(c.Context) == context.DeadlineExceeded {
		return context.DeadlineExceeded
	}
	return err
}

func (c *deadlineCtx) Value(key any) any {
	if key == (deadlineKey{}) {
		return c
	}
	return c.Context.Value(key)
}

// lift removes the deadline, unless it has already passed.
func (c *deadlineCtx) lift() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer.Stop() {
		c.lifted = true
	}
}

// recount resets the deadline to d from the Timeout's start, keeping an
// earlier parent deadline. It does nothing once lifted or passed.
func (c *deadlineCtx) recount(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lifted || c.used == d || !c.timer.Stop() {
		return
	}
	c.used = d
	c.deadline = c.start.Add(d)
	if pd, ok := c.Context.Deadline(); ok && pd.Before(c.deadline) {
		c.deadline = pd
	}
	c.timer.Reset(time.Until(c.deadline))
}

// length returns the deadline's length: the Timeout's own, or the
// operation's Doc.Timeout.
func (c *deadlineCtx) length() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used
}

// bodyWindow returns the time a body has to arrive from its first read: the
// shortest length among the Timeouts on ctx.
func bodyWindow(ctx context.Context) time.Duration {
	var w time.Duration
	for _, c := range deadlines(ctx) {
		if l := c.length(); w == 0 || l < w {
			w = l
		}
	}
	return w
}

// deadlines returns every Timeout deadline on ctx, outermost first.
func deadlines(ctx context.Context) []*deadlineCtx {
	var out []*deadlineCtx
	for {
		c, ok := ctx.Value(deadlineKey{}).(*deadlineCtx)
		if !ok {
			return out
		}
		out = append([]*deadlineCtx{c}, out...)
		ctx = c.Context
	}
}

// retime recounts every Timeout deadline on ctx with operation m's length,
// for a request a root middleware moved after a root Timeout ran.
func retime(ctx context.Context, m *Match) {
	for _, c := range deadlines(ctx) {
		d := c.def
		if m != nil && m.Doc.Timeout > 0 {
			d = m.Doc.Timeout
		}
		c.recount(d)
	}
}

// liftDeadline lifts every Timeout deadline on ctx and any read deadline geta
// set on the connection.
func liftDeadline(ctx context.Context) {
	if rc := requestFrom(ctx); rc != nil {
		if rc.read != nil {
			rc.read.clear()
		}
		if rc.drain != nil {
			rc.drain.lift()
		}
	}
	for {
		c, ok := ctx.Value(deadlineKey{}).(*deadlineCtx)
		if !ok {
			return
		}
		c.lift()
		ctx = c.Context
	}
}

// errBodyLate is returned by a body read after geta's read deadline passed.
var errBodyLate = errors.New("the request body did not arrive before the deadline")

// lateBodyError is errBodyLate wrapping the connection's own error.
type lateBodyError struct{ err error }

func (e *lateBodyError) Error() string        { return errBodyLate.Error() }
func (e *lateBodyError) Unwrap() []error      { return []error{errBodyLate, e.err} }
func (e *lateBodyError) Is(target error) bool { return target == errBodyLate }

// boundedBody is the request body an operation reads. It records whether it
// was read to its end and, when rc is set, reads under a read deadline. At
// io.EOF it clears the deadline so later reads of the connection are not cut;
// a body not read to its end keeps it, bounding net/http's discard of the
// rest. A read the deadline ends returns a *lateBodyError.
type boundedBody struct {
	io.ReadCloser
	rc    *http.ResponseController // nil: no read deadline
	once  sync.Once
	ended atomic.Bool // a read returned io.EOF
}

func (b *boundedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	switch {
	case err == io.EOF:
		b.ended.Store(true)
		b.clear()
	case err != nil && b.rc != nil && errors.Is(err, os.ErrDeadlineExceeded):
		err = &lateBodyError{err: err}
	}
	return n, err
}

func (b *boundedBody) clear() {
	if b.rc == nil {
		return
	}
	b.once.Do(func() { _ = b.rc.SetReadDeadline(time.Time{}) })
}

// boundBody returns r with its body wrapped in a boundedBody, or r itself
// when it has no body. Behind a Timeout, it sets a read deadline of
// bodyWindow from now, where the connection supports one.
//
// The window starts here rather than at the operation's deadline: a 408 is
// the client's delay (RFC 9110 §15.5.9), so time the server spent before
// reading must not count against it.
func boundBody(w http.ResponseWriter, r *http.Request) *http.Request {
	if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return r
	}
	b := &boundedBody{ReadCloser: r.Body}
	if dc, ok := r.Context().Value(deadlineKey{}).(*deadlineCtx); ok {
		if _, ok := dc.Deadline(); ok {
			ctl := http.NewResponseController(w)
			if ctl.SetReadDeadline(time.Now().Add(bodyWindow(r.Context()))) == nil {
				b.rc = ctl
			}
		}
	}
	rc := requestFrom(r.Context())
	if rc != nil {
		if rc.body == r.Body {
			rc.body = b // the body the headers describe, read through b
		}
		if b.rc != nil {
			rc.read = b
		}
	}
	bound := new(http.Request)
	*bound = *r
	bound.Body = b
	return bound
}

// lateBody returns a 408 (RFC 9110 §15.5.9) if err comes from a read the
// read deadline ended.
func lateBody(err error) (*bindError, bool) {
	if !errors.Is(err, errBodyLate) {
		return nil, false
	}
	return &bindError{status: http.StatusRequestTimeout, detail: errBodyLate.Error()}, true
}

// closeUnread prepares a refusal geta writes in place of the handler: if r's
// boundedBody was not read to its end, it sets "Connection: close" and a read
// deadline of now. Otherwise net/http would read up to 256 KiB of the unwanted
// rest before responding, and a client that stalls would get no answer.
//
// It applies to HTTP/1.x and in-process requests only. On HTTP/2 the unread
// stream is reset alone (RFC 9113 §8.1), and Connection: close would shut
// down the whole connection.
func closeUnread(w http.ResponseWriter, r *http.Request) {
	b, ok := r.Body.(*boundedBody)
	if !ok || b.ended.Load() || r.ProtoMajor >= 2 {
		return
	}
	if rc := requestFrom(r.Context()); rc != nil && rc.drain != nil {
		rc.drain.settle()
	}
	w.Header().Set("Connection", "close")
	_ = http.NewResponseController(w).SetReadDeadline(time.Now())
}

// drain wraps the ResponseWriter of an HTTP/1.x request with a body, in an
// App with a Timeout. On the first final status or write, or when the App
// returns without either, it sets the connection's read deadline to the
// request's window (arm). That bounds net/http's discarding of an unread
// body, which otherwise has no deadline: a stalled client could hold the
// connection forever. net/http resets the read deadline for each request.
//
// arm sets nothing when the body was read to its end (net/http's background
// read must not be cut), while a read is under way, when boundBody already
// set a deadline, after closeUnread (settle), or after a stream or switched
// connection lifted the deadlines (lift).
type drain struct {
	http.ResponseWriter
	rc   *requestContext
	body drainBody
	mu   sync.Mutex
	// reading: a read is under way; ended: a read returned io.EOF;
	// settled: arm has run or must not; set: arm's deadline stands.
	reading, ended, settled, set bool
}

// drainBody wraps the request body so its drain knows whether it was read
// to its end.
type drainBody struct {
	io.ReadCloser
	d *drain
}

func (b *drainBody) Read(p []byte) (int, error) {
	d := b.d
	d.mu.Lock()
	d.reading = true
	d.mu.Unlock()
	n, err := b.ReadCloser.Read(p)
	d.mu.Lock()
	d.reading = false
	if err == io.EOF {
		d.ended = true
	}
	d.mu.Unlock()
	return n, err
}

// drainFor returns a drain-wrapped writer and request when the App has a
// Timeout and r is an HTTP/1.x request with a body; otherwise w, r, and nil.
// HTTP/2 resets an unread stream alone (RFC 9113 §8.1).
func (a *App) drainFor(w http.ResponseWriter, r *http.Request, rc *requestContext) (http.ResponseWriter, *http.Request, *drain) {
	if !a.drains || r.ProtoMajor >= 2 || r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return w, r, nil
	}
	d := &drain{ResponseWriter: w, rc: rc}
	d.body = drainBody{ReadCloser: r.Body, d: d}
	r.Body = &d.body
	rc.body, rc.drain = r.Body, d
	return d, r, d
}

// arm sets the connection's read deadline to the request's window from now,
// at most once.
func (d *drain) arm() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.settled {
		return
	}
	d.settled = true
	if d.reading || d.ended || d.rc.read != nil {
		return
	}
	w := d.rc.window()
	if w <= 0 {
		return
	}
	if http.NewResponseController(d.ResponseWriter).SetReadDeadline(time.Now().Add(w)) == nil {
		d.set = true
	}
}

// settle keeps arm from setting a read deadline.
func (d *drain) settle() {
	d.mu.Lock()
	d.settled = true
	d.mu.Unlock()
}

// lift keeps arm from setting a read deadline and clears one it set.
func (d *drain) lift() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.settled = true
	if d.set {
		d.set = false
		_ = http.NewResponseController(d.ResponseWriter).SetReadDeadline(time.Time{})
	}
}

func (d *drain) WriteHeader(code int) {
	if code >= 200 {
		d.arm()
	}
	d.ResponseWriter.WriteHeader(code)
}

func (d *drain) Write(b []byte) (int, error) {
	d.arm()
	return d.ResponseWriter.Write(b)
}

func (d *drain) Flush() {
	d.arm()
	http.NewResponseController(d.ResponseWriter).Flush()
}

func (d *drain) FlushError() error {
	d.arm()
	return http.NewResponseController(d.ResponseWriter).Flush()
}

// Hijack lifts the drain's read deadline and hijacks the connection.
func (d *drain) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	d.lift()
	return http.NewResponseController(d.ResponseWriter).Hijack()
}

func (d *drain) Unwrap() http.ResponseWriter { return d.ResponseWriter }
