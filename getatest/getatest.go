// Package getatest runs a geta application over an in-memory network, for
// tests.
//
// [New] assembles the table with [geta.New], as production does, so a
// misdeclared app fails the test on the line that builds it. Every response
// is checked against the app's OpenAPI document.
package getatest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaclient"
)

// Client sends requests to one app over httptest's in-memory network.
type Client struct {
	t   testing.TB
	app *geta.App
	srv *httptest.Server
	hc  *http.Client
	hdr http.Header
	log *servings
}

// New assembles table and serves it for the rest of the test. An assembly
// error fails the test immediately.
func New(t testing.TB, table geta.Table, opts ...geta.Option) *Client {
	t.Helper()
	app, err := geta.New(table, opts...)
	if err != nil {
		t.Fatalf("geta.New: %v", err)
	}
	return Serve(t, app)
}

// Serve serves an assembled app for the rest of the test.
func Serve(t testing.TB, app *geta.App) *Client {
	t.Helper()
	log := &servings{byID: map[string]*record{}}
	srv := httptest.NewTestServer(t, log.handler(app))
	hc := srv.Client()
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{t: t, app: app, srv: srv, hc: hc, hdr: http.Header{}, log: log}
}

// servingHeader carries the request's ID to the server. The app never sees
// it.
const servingHeader = "Getatest-Request"

// serving is the method and operation the app served a request as; ok is
// false for none. A root middleware may rewrite the method or path, so
// responses are checked against this, not the request as sent.
type serving struct {
	method string
	match  geta.Match
	ok     bool
}

// servings records, for each numbered request, what the app served it as.
type servings struct {
	mu   sync.Mutex
	next uint64
	byID map[string]*record
}

// record is kept until both the client has taken it and the handler is
// done. A client may take a stream's record while the handler still runs.
type record struct {
	serving
	taken, done bool
}

// number assigns req an ID and returns it.
func (s *servings) number(req *http.Request) string {
	s.mu.Lock()
	s.next++
	id := strconv.FormatUint(s.next, 10)
	s.mu.Unlock()
	req.Header.Set(servingHeader, id)
	return id
}

// take returns the record for id and marks it taken. A missing record means
// no operation served the request.
func (s *servings) take(id string) serving {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.byID[id]
	if r == nil {
		return serving{}
	}
	r.taken = true
	if r.done {
		delete(s.byID, id)
	}
	return r.serving
}

// put records what id is served as, unless the client has taken the record.
func (s *servings) put(id string, read func() (string, geta.Match, bool)) {
	method, m, ok := read()
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.byID[id]
	if r == nil {
		r = &record{}
		s.byID[id] = r
	}
	if !r.taken {
		r.serving = serving{method, m, ok}
	}
}

// finish marks the handler done with id's record and deletes it if the
// client has taken it or is gone (ctx is done). The record exists: the
// handler's deferred put made it.
func (s *servings) finish(id string, ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.byID[id]
	r.done = true
	if r.taken || ctx.Err() != nil {
		delete(s.byID, id)
	}
}

// handler serves app, recording what each numbered request was served as
// before any of its response reaches the client.
func (s *servings) handler(app *geta.App) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(servingHeader)
		if id == "" {
			app.ServeHTTP(w, r)
			return
		}
		ctx, read := geta.Observe(r.Context())
		r = r.WithContext(ctx)
		r.Header = r.Header.Clone()
		r.Header.Del(servingHeader)
		rw := &recorder{ResponseWriter: w, record: func() { s.put(id, read) }}
		defer s.finish(id, ctx)
		defer rw.record()
		app.ServeHTTP(rw, r)
	})
}

// recorder records the serving whenever the response starts to leave: on
// WriteHeader, the first Write, Flush, or Hijack. It implements Unwrap for
// http.ResponseController.
type recorder struct {
	http.ResponseWriter
	record func()
	wrote  bool
}

func (w *recorder) WriteHeader(code int) {
	w.record()
	w.ResponseWriter.WriteHeader(code)
}

func (w *recorder) Write(b []byte) (int, error) {
	if !w.wrote {
		w.wrote = true
		w.record()
	}
	return w.ResponseWriter.Write(b)
}

func (w *recorder) Flush() { w.FlushError() }

func (w *recorder) FlushError() error {
	w.record()
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.record()
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *recorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// App is the assembled app.
func (c *Client) App() *geta.App { return c.app }

// URL is the base URL requests go to.
func (c *Client) URL() string { return c.srv.URL }

// HTTP is the underlying client, for requests the helpers do not cover.
func (c *Client) HTTP() *http.Client { return c.hc }

// DialContext opens a connection to the app over the in-memory network,
// ignoring network and addr. [Client.URL] names a real host
// (http://example.com), so give DialContext to any third-party client that
// dials on its own, such as gorilla/websocket's Dialer.NetDialContext.
// Requests sent this way bypass the client's headers and document checks.
func (c *Client) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return c.srv.Client().Transport.(*http.Transport).DialContext(ctx, network, addr)
}

// With returns a client that sends the header key: value on every request,
// replacing any value c sends for key.
func (c *Client) With(key, value string) *Client {
	d := *c
	d.hdr = c.hdr.Clone()
	d.hdr.Set(key, value)
	return &d
}

// Bearer returns a client that sends "Authorization: Bearer token",
// replacing any Authorization header c sends.
func (c *Client) Bearer(token string) *Client { return c.With("Authorization", "Bearer "+token) }

// Get sends GET path.
func (c *Client) Get(path string) *Response { return c.Do(http.MethodGet, path, nil) }

// Delete sends DELETE path.
func (c *Client) Delete(path string) *Response { return c.Do(http.MethodDelete, path, nil) }

// Post sends POST path with body; see [Client.Do].
func (c *Client) Post(path string, body any) *Response { return c.Do(http.MethodPost, path, body) }

// Put sends PUT path with body; see [Client.Do].
func (c *Client) Put(path string, body any) *Response { return c.Do(http.MethodPut, path, body) }

// Patch sends PATCH path with body; see [Client.Do].
func (c *Client) Patch(path string, body any) *Response { return c.Do(http.MethodPatch, path, body) }

// Query sends QUERY path with body; see [Client.Do]. The app serves QUERY
// only with an OpenAPI 3.2 document (geta.Route.Query).
func (c *Client) Query(path string, body any) *Response { return c.Do(geta.MethodQuery, path, body) }

// Do sends a request. A string or []byte body is sent as is; any other
// non-nil body is encoded as JSON. The Content-Type is application/json
// unless the client sets one.
func (c *Client) Do(method, path string, body any) *Response {
	c.t.Helper()
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
	case []byte:
		r = bytes.NewReader(b)
	default:
		data, err := json.Marshal(b)
		if err != nil {
			c.t.Fatalf("getatest: encoding body: %v", err)
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(c.t.Context(), method, c.srv.URL+path, r)
	if err != nil {
		c.t.Fatalf("getatest: %v", err)
	}
	if r != nil && c.hdr.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.Send(req)
}

// Content sends method path with body as contentType, as for a raw body
// (body:"text/csv").
func (c *Client) Content(method, path, contentType string, body []byte) *Response {
	c.t.Helper()
	req, err := http.NewRequestWithContext(c.t.Context(), method, c.srv.URL+path, bytes.NewReader(body))
	if err != nil {
		c.t.Fatalf("getatest: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	return c.Send(req)
}

// Form sends method path with values as an
// application/x-www-form-urlencoded body.
func (c *Client) Form(method, path string, values url.Values) *Response {
	c.t.Helper()
	req, err := http.NewRequestWithContext(c.t.Context(), method, c.srv.URL+path, strings.NewReader(values.Encode()))
	if err != nil {
		c.t.Fatalf("getatest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.Send(req)
}

// FilePart is a file part for [Client.Multipart]. An empty ContentType is
// sent as application/octet-stream.
type FilePart struct {
	Field, Filename, ContentType string
	Content                      []byte
}

// Multipart sends method path with a multipart/form-data body (RFC 7578):
// one part per value, sorted by name, then one per file, in order.
func (c *Client) Multipart(method, path string, values url.Values, files ...FilePart) *Response {
	c.t.Helper()
	// Writes to a bytes.Buffer never fail, so the writer's errors are nil.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, name := range slices.Sorted(maps.Keys(values)) {
		for _, v := range values[name] {
			mw.WriteField(name, v)
		}
	}
	for _, f := range files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": f.Field, "filename": f.Filename}))
		ct := f.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		h.Set("Content-Type", ct)
		w, _ := mw.CreatePart(h)
		w.Write(f.Content)
	}
	mw.Close()
	req, err := http.NewRequestWithContext(c.t.Context(), method, c.srv.URL+path, &buf)
	if err != nil {
		c.t.Fatalf("getatest: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return c.Send(req)
}

// Send sends req with the client's headers added. A header req sets itself
// wins over the client's.
//
// Connections honor read deadlines as a real server's do, so a request
// whose body stalls (such as an io.Pipe the test writes part of) gets the
// 408 a geta.Timeout answers.
//
// Send fails the test if the status is not in the OpenAPI document for the
// served operation, or if a header or JSON body does not conform to it.
func (c *Client) Send(req *http.Request) *Response {
	c.t.Helper()
	c.addHeaders(req)
	id := c.log.number(req)
	res, err := c.hc.Do(req)
	sv := c.log.take(id) // on a failure too, so the record goes
	if err != nil {
		c.t.Fatalf("getatest: %s %s: %v", req.Method, req.URL.Path, err)
	}
	c.documented(req, sv, res.StatusCode)
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		c.t.Fatalf("getatest: %s %s: reading response: %v", req.Method, req.URL.Path, err)
	}
	c.conforms(req, sv, res, data)
	return &Response{t: c.t, Status: res.StatusCode, Header: res.Header, Body: data, opts: c.app.JSONOptions()}
}

// addHeaders adds the client's headers that req does not set itself.
func (c *Client) addHeaders(req *http.Request) {
	if req.Header == nil {
		req.Header = http.Header{}
	}
	for k, vs := range c.hdr {
		if _, own := req.Header[k]; own {
			continue
		}
		req.Header[k] = slices.Clone(vs)
	}
}

// conforms fails the test when a documented header, or a JSON body that is
// not content-encoded, does not match the schema for the served operation.
func (c *Client) conforms(req *http.Request, sv serving, res *http.Response, body []byte) {
	c.t.Helper()
	if !sv.ok {
		return
	}
	if !checked(res) {
		body = nil // the headers alone
	}
	if err := c.app.Conforms(sv.match, res.StatusCode, res.Header, body); err != nil {
		c.t.Errorf("getatest: %s %s%s: %v", req.Method, req.URL.Path, servedAs(req, sv), err)
	}
}

// checked reports whether res has a body App.Conforms checks as a whole: a
// JSON success or problem, not content-encoded.
func checked(res *http.Response) bool {
	ct := res.Header.Get("Content-Type")
	return (strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, geta.ProblemContentType)) && res.Header.Get("Content-Encoding") == ""
}

// documented fails the test when the served operation answered a status its
// OpenAPI document does not list. A middleware that answers on its own must
// declare it with geta.Middleware.Answers. A request no operation served
// (a 404, a 405) is not checked.
func (c *Client) documented(req *http.Request, sv serving, status int) {
	c.t.Helper()
	if !sv.ok {
		return
	}
	if ok, matched := c.app.Documented(sv.match, status); matched && !ok {
		c.t.Errorf("getatest: %s %s%s: status %d is not documented for %s %s; declare middleware statuses with Middleware.Answers",
			req.Method, req.URL.Path, servedAs(req, sv), status, sv.match.Method, sv.match.Template)
	}
}

// servedAs names the served method if a root middleware changed it.
func servedAs(req *http.Request, sv serving) string {
	if sv.method == "" || sv.method == req.Method {
		return ""
	}
	return " (served as " + sv.method + ")"
}

// Typed returns a getaclient.Client for this app. Before each call, the
// method and template must name an operation whose input and output types
// match the call's, or the test fails. Responses are checked against the
// document as for [Client.Send].
func (c *Client) Typed() *getaclient.Client {
	hc := *c.hc
	hc.Transport = documenting{c: c, next: c.hc.Transport}
	return &getaclient.Client{
		Base:   c.srv.URL,
		HTTP:   &hc,
		Header: c.hdr.Clone(),
		JSON:   c.app.JSONOptions(),
		Check: func(method, template string, in, out reflect.Type) error {
			c.t.Helper()
			wantIn, wantOut, ok := c.app.Types(method, template)
			var err error
			switch {
			case !ok:
				err = fmt.Errorf("getatest: no operation answers %s %s", method, template)
			case wantIn != in:
				err = fmt.Errorf("getatest: %s %s takes %s, not %s", method, template, wantIn, in)
			case wantOut != out:
				err = fmt.Errorf("getatest: %s %s returns %s, not %s", method, template, typeName(wantOut), typeName(out))
			}
			if err != nil {
				c.t.Errorf("%v", err)
			}
			return err
		},
	}
}

func typeName(t reflect.Type) string {
	if t == nil {
		return "no body (OpNoBody)"
	}
	return t.String()
}

// documenting checks each response against the document. next is the
// in-memory server's transport, never nil.
type documenting struct {
	c    *Client
	next http.RoundTripper
}

func (d documenting) RoundTrip(req *http.Request) (*http.Response, error) {
	// A RoundTripper must not modify its request.
	req = req.Clone(req.Context())
	id := d.c.log.number(req)
	res, err := d.next.RoundTrip(req)
	sv := d.c.log.take(id) // on a failure too, so the record goes
	if err != nil {
		return res, err
	}
	d.c.documented(req, sv, res.StatusCode)
	if checked(res) {
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			return nil, err
		}
		d.c.conforms(req, sv, res, body)
		res.Body = io.NopCloser(bytes.NewReader(body))
	}
	return res, nil
}

// Response is a received response, fully read.
type Response struct {
	t      testing.TB
	Status int
	Header http.Header
	Body   []byte
	opts   json.Options // the app's sealed types
}

// JSON decodes the body as T. A body that is not JSON, or has a member T
// does not declare, fails the test.
func (r *Response) JSON[T any]() T {
	r.t.Helper()
	var v T
	if err := json.Unmarshal(r.Body, &v, json.RejectUnknownMembers(true), r.opts); err != nil {
		r.t.Fatalf("getatest: decoding %s as %T: %v\nbody: %s", r.Header.Get("Content-Type"), v, err, r.Body)
	}
	return v
}

// Problem decodes the body as a problem, ignoring extension members; read
// those with [Response.ProblemAs]. A body of another Content-Type fails the
// test.
func (r *Response) Problem() geta.Problem {
	r.t.Helper()
	if ct := r.Header.Get("Content-Type"); ct != geta.ProblemContentType {
		r.t.Fatalf("getatest: Content-Type %q is not %s\nbody: %s", ct, geta.ProblemContentType, r.Body)
	}
	var p geta.Problem
	if err := json.Unmarshal(r.Body, &p); err != nil {
		r.t.Fatalf("getatest: decoding problem: %v\nbody: %s", err, r.Body)
	}
	return p
}

// ProblemAs decodes the problem into P, the result type of a
// geta.OnAsProblem row, as getaclient.ProblemAs does, with the app's JSON
// options. A response that does not decode as P fails the test.
func (r *Response) ProblemAs[P any]() P {
	r.t.Helper()
	p := r.Problem()
	got, ok := getaclient.ProblemAs[P](&getaclient.Error{Status: r.Status, Problem: &p, Body: r.Body, Header: r.Header, JSON: r.opts})
	if !ok {
		var zero P
		r.t.Fatalf("getatest: problem does not decode as %T\nbody: %s", zero, r.Body)
		return zero
	}
	return *got
}

// Text is the body as a string.
func (r *Response) Text() string { return string(r.Body) }
