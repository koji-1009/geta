// Package getaclient calls a geta application from Go with the same types
// its handlers take and return. Nothing is generated.
//
// An input struct is sent by its geta tags (path, query, header, cookie,
// body). A struct query parameter is sent as a deepObject
// (name[member]=value). A body is sent as JSON, as
// application/x-www-form-urlencoded (body:"form"), as multipart/form-data
// (body:"multipart", with geta.File fields made by geta.NewFile), or, for a
// tag naming a media type (body:"text/csv"), as the field's bytes with that
// Content-Type.
//
// The response is read into the handler's output type. An envelope's
// media-type body is read as raw bytes, and its status field
// (status:"200|201") receives the response status.
//
//	u, err := getaclient.Call[user.GetIn, user.Tagged](ctx, c, "GET", "/users/{id}", &user.GetIn{Path: user.Path{ID: "1"}})
//
// The template is the one in the route table. A failure is an *Error
// carrying the server's problem document, whose Type identifies the
// failure row. Headers no output field holds, such as a deprecated
// operation's Deprecation and Sunset, are read with ResponseHeader:
//
//	var h http.Header
//	u, err := getaclient.Call[user.GetIn, user.Tagged](ctx, c, "GET", "/users/{id}", &in, getaclient.ResponseHeader(&h))
//	if dep, sunset := getaclient.Deprecation(h); !dep.IsZero() || !sunset.IsZero() { ... }
package getaclient

import (
	"bytes"
	"context"
	"encoding"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/koji-1009/geta"
)

// Client sends calls to one application.
type Client struct {
	// Base is the application's URL, without a trailing slash.
	Base string
	// HTTP sends the requests; nil means http.DefaultClient.
	HTTP *http.Client
	// Header is sent with every call.
	Header http.Header
	// JSON holds extra encoding/json/v2 options for bodies, such as
	// geta.Union.JSONOptions or App.JSONOptions for sealed types.
	JSON json.Options
	// Check, if set, is called before each call to confirm the application
	// has an operation for method and template taking in and returning out
	// (out is nil for CallNoBody). getatest sets it from the app.
	Check func(method, template string, in, out reflect.Type) error
}

// Error is a response that is not the call's success: a status outside 2xx
// and 3xx, a 304, a 3xx received by CallNoBody, or a 2xx or 3xx whose
// output expects JSON but whose Content-Type is not a JSON media type. The
// last case is typically a middleware's answer, such as http.Redirect; its
// Header carries what the middleware set, including Location.
type Error struct {
	Status int
	// Problem is the problem document, if the body was one.
	Problem *geta.Problem
	// Body is the raw body.
	Body []byte
	// Header is the response header, including headers a geta.OnAsProblem
	// row sets, such as Retry-After.
	Header http.Header
	// JSON holds the client's decoding options (Client.JSON), used by
	// ProblemAs.
	JSON json.Options
}

// ProblemAs decodes the problem in err into P, the result type of a
// geta.OnAsProblem row: P's members from the problem's extension members
// and, if P is an envelope, its headers from the response. It decodes with
// the client's JSON options. It reports false if err is not an *Error with
// a problem, or the problem does not decode as P.
func ProblemAs[P any](err error) (*P, bool) {
	e, ok := errors.AsType[*Error](err)
	if !ok || e.Problem == nil {
		return nil, false
	}
	p := new(P)
	v := reflect.ValueOf(p).Elem()
	// The problem's own members (type, title, ...) are beside P's.
	opts := json.JoinOptions(e.JSON, json.RejectUnknownMembers(false))
	if v.Kind() == reflect.Struct && isEnvelope(v.Type()) {
		res := &http.Response{StatusCode: e.Status, Header: e.Header}
		if err := readEnvelope(res, e.Body, v, opts); err != nil {
			return nil, false
		}
		return p, true
	}
	if json.Unmarshal(e.Body, p, opts) != nil {
		return nil, false
	}
	return p, true
}

func (e *Error) Error() string {
	if e.Problem != nil {
		s := fmt.Sprintf("%d %s", e.Status, e.Problem.Title)
		if e.Problem.Type != "" && e.Problem.Type != "about:blank" {
			s += " (" + e.Problem.Type + ")"
		}
		if e.Problem.Detail != "" {
			s += ": " + e.Problem.Detail
		}
		for _, v := range e.Problem.Errors {
			s += fmt.Sprintf("; %s %s: %s", v.In, v.Path, v.Message)
		}
		// As App.Conforms counts the violations it does not list.
		if e.Problem.Omitted > 0 {
			s += fmt.Sprintf("; and %d more not listed", e.Problem.Omitted)
		}
		return s
	}
	s := fmt.Sprintf("%d %s", e.Status, http.StatusText(e.Status))
	if e.Status >= 200 && e.Status <= 399 && e.Status != http.StatusNotModified {
		// A success status that is not the operation's answer.
		s += fmt.Sprintf(", not the call's output (Content-Type %q", e.Header.Get("Content-Type"))
		if loc := e.Header.Get("Location"); loc != "" {
			s += fmt.Sprintf(", Location %q", loc)
		}
		s += ")"
	}
	return s
}

// Call sends in to the operation for method and template and returns its
// output. Any 2xx or 3xx except 304 is a success. getaclient follows no
// redirect, whatever Client.HTTP's CheckRedirect says, so an operation that
// declares a 3xx returns its output, Location included.
//
// When Out reads the body as JSON (a plain output, or an envelope's
// body:"json" field), a success must have a JSON Content-Type
// (application/json or application/*+json); anything else, or none, is an
// *Error.
func Call[In, Out any](ctx context.Context, c *Client, method, template string, in *In, opts ...CallOption) (*Out, error) {
	if in == nil {
		in = new(In)
	}
	res, err := c.do(ctx, method, template, reflect.TypeFor[In](), reflect.TypeFor[Out](), in, opts)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	out := new(Out)
	if err := decodeOutput(res, reflect.ValueOf(out).Elem(), c.JSON); err != nil {
		return nil, fmt.Errorf("getaclient: %s %s: %w", method, template, err)
	}
	return out, nil
}

// CallNoBody calls an operation built with geta.OpNoBody. Only a 2xx is a
// success; a 3xx is an *Error, since no output can hold its Location.
func CallNoBody[In any](ctx context.Context, c *Client, method, template string, in *In, opts ...CallOption) error {
	if in == nil {
		in = new(In)
	}
	res, err := c.do(ctx, method, template, reflect.TypeFor[In](), nil, in, opts)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, res.Body)
	return res.Body.Close()
}

// CallOption changes how one call is sent.
type CallOption func(*callOptions)

type callOptions struct {
	absent    []any
	headers   []*http.Header
	nilHeader bool
}

// ResponseHeader stores the response header in *h, on success or failure.
// Use it for headers no output field holds, such as those a middleware sets
// or the Deprecation and Sunset headers [Deprecation] reads. *h is unchanged
// if no response arrives. A nil h makes the call fail before sending.
func ResponseHeader(h *http.Header) CallOption {
	return func(o *callOptions) {
		if h == nil {
			o.nilHeader = true
			return
		}
		o.headers = append(o.headers, h)
	}
}

// Deprecation reads the Deprecation header (RFC 9745, a structured-field
// Date such as "@1700000000") and the Sunset header (RFC 8594, an
// HTTP-date) from a response header, as geta writes them from
// Doc.Deprecation and Doc.Sunset. Each result is in UTC, or the zero time
// if the header is absent, repeated, or malformed.
func Deprecation(h http.Header) (deprecation, sunset time.Time) {
	if vs := h.Values("Deprecation"); len(vs) == 1 {
		deprecation = sfDate(vs[0])
	}
	if vs := h.Values("Sunset"); len(vs) == 1 {
		if t, err := http.ParseTime(vs[0]); err == nil {
			sunset = t.UTC()
		}
	}
	return deprecation, sunset
}

// sfDate parses a structured-field Date (RFC 9651 §3.3.7), or returns the
// zero time.
func sfDate(s string) time.Time {
	digits, ok := strings.CutPrefix(s, "@")
	if !ok {
		return time.Time{}
	}
	digits = strings.TrimPrefix(digits, "-")
	if digits == "" || len(digits) > 15 || strings.Trim(digits, "0123456789") != "" {
		return time.Time{}
	}
	n, _ := strconv.ParseInt(s[1:], 10, 64) // at most 15 digits: always an int64
	return time.Unix(n, 0).UTC()
}

// Absent omits the pointed-to fields from the request, so the application
// binds their defaults. Without it, a field is sent as it is, zero value
// included.
//
// Each pointer must address a field of the call's input that declares a
// default (schema:"default=20"): a query, header, or cookie parameter, a
// deepObject or form body field, or a JSON body member, at any depth
// through structs, pointers, and slice elements (&in.Body.Lines[1].Qty).
// Anything else makes the call fail before sending.
func Absent(fields ...any) CallOption {
	return func(o *callOptions) { o.absent = append(o.absent, fields...) }
}

func (c *Client) do(ctx context.Context, method, template string, inT, outT reflect.Type, in any, opts []CallOption) (*http.Response, error) {
	if c.Check != nil {
		if err := c.Check(method, template, inT, outT); err != nil {
			return nil, err
		}
	}
	var o callOptions
	for _, opt := range opts {
		opt(&o)
	}
	abs, err := newAbsences(o.absent)
	if err != nil {
		return nil, fmt.Errorf("getaclient: %s %s: %w", method, template, err)
	}
	if o.nilHeader {
		return nil, fmt.Errorf("getaclient: %s %s: nil ResponseHeader", method, template)
	}
	req, err := c.request(ctx, method, template, reflect.ValueOf(in).Elem(), abs)
	if err != nil {
		return nil, fmt.Errorf("getaclient: %s %s: %w", method, template, err)
	}
	// Headers the call sets win over the client's defaults.
	for k, vs := range c.Header {
		k = http.CanonicalHeaderKey(k)
		if _, set := req.Header[k]; set {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	// A declared 3xx is the operation's answer, so redirects are not
	// followed.
	once := *hc
	once.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := once.Do(req)
	if err != nil {
		return nil, err
	}
	for _, h := range o.headers {
		*h = res.Header
	}
	if !success(res, outT) {
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		e := &Error{Status: res.StatusCode, Body: body, Header: res.Header, JSON: c.JSON}
		if res.Header.Get("Content-Type") == geta.ProblemContentType {
			var p geta.Problem
			if json.Unmarshal(body, &p) == nil {
				e.Problem = &p
			}
		}
		return nil, e
	}
	return res, nil
}

// success reports whether res is the operation's answer for outT (nil for
// CallNoBody), as described on Call and CallNoBody.
func success(res *http.Response, outT reflect.Type) bool {
	s := res.StatusCode
	switch {
	case s >= 200 && s <= 299:
	case s >= 300 && s <= 399 && s != http.StatusNotModified && outT != nil:
	default:
		return false
	}
	return !readsJSON(outT) || isJSONMediaType(res.Header.Get("Content-Type"))
}

// readsJSON reports whether an output of type t reads the body as JSON: a
// plain output, or an envelope with a body:"json" field.
func readsJSON(t reflect.Type) bool {
	if t == nil {
		return false
	}
	if t.Kind() != reflect.Struct || !isEnvelope(t) {
		return true
	}
	return envelopeReadsJSON(t)
}

func envelopeReadsJSON(t reflect.Type) bool {
	for i := range t.NumField() {
		f := t.Field(i)
		if geta.EnvelopeEmbedded(geta.VetFieldOf(f)) {
			if envelopeReadsJSON(f.Type) {
				return true
			}
			continue
		}
		if mt, ok := f.Tag.Lookup("body"); ok && mt == "json" && f.IsExported() {
			return true
		}
	}
	return false
}

// isJSONMediaType reports whether ct is application/json or
// application/*+json (RFC 6839), ignoring parameters.
func isJSONMediaType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return mt == "application/json" || strings.HasPrefix(mt, "application/") && strings.HasSuffix(mt, "+json")
}

// request builds the request from the input's tagged fields, omitting those
// in abs.
func (c *Client) request(ctx context.Context, method, template string, in reflect.Value, abs absences) (*http.Request, error) {
	values := map[string]string{}
	query := url.Values{}
	header := http.Header{}
	var cookies []*http.Cookie
	var body io.Reader
	err := walk(in, func(loc, name string, f reflect.StructField, v reflect.Value) error {
		if left, err := abs.leave(f, v, fmt.Sprintf("%s %q", loc, name)); left || err != nil {
			return err
		}
		if loc == "body" {
			if (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && v.IsNil() {
				return nil
			}
			var b []byte
			var ct string
			var err error
			switch {
			case strings.Contains(name, "/"):
				// Raw bytes: a []byte, or an io.Reader read while sending.
				header.Set("Content-Type", name)
				if r, ok := v.Interface().(io.Reader); ok {
					body = r
					return nil
				}
				if v.Kind() == reflect.Pointer {
					v = v.Elem()
				}
				body = bytes.NewReader(v.Bytes())
				return nil
			case name == "form":
				b, ct, err = formBody(v, abs)
			case name == "multipart":
				b, ct, err = multipartBody(v, abs)
			default:
				ct = "application/json"
				b, err = jsonBody(v, c.JSON, abs)
			}
			if err != nil {
				return fmt.Errorf("body: %w", err)
			}
			body = bytes.NewReader(b)
			header.Set("Content-Type", ct)
			return nil
		}
		if loc == "query" && isDeepObject(v.Type()) {
			// A deepObject: name[member]=value per member.
			if v.Kind() == reflect.Pointer && v.IsNil() {
				return nil
			}
			return formFields(v, func(member string, mf reflect.StructField, fv reflect.Value) error {
				if left, err := abs.leave(mf, fv, fmt.Sprintf("query %q member %q", name, member)); left || err != nil {
					return err
				}
				ts, err := texts(fv)
				if err != nil {
					return fmt.Errorf("query parameter %q member %q: %w", name, member, err)
				}
				for _, t := range ts {
					query.Add(name+"["+member+"]", t)
				}
				return nil
			})
		}
		texts, err := texts(v)
		if err != nil {
			return fmt.Errorf("%s parameter %q: %w", loc, name, err)
		}
		switch loc {
		case "path":
			if !strings.Contains(template, "{"+name+"}") {
				return fmt.Errorf("template %q has no {%s}", template, name)
			}
			if len(texts) > 0 {
				values[name] = texts[0]
			}
		case "query":
			for _, t := range texts {
				query.Add(name, t)
			}
		case "header":
			list := isList(v.Type())
			for _, t := range texts {
				if why, ok := unheld(t); ok {
					return fmt.Errorf("header parameter %q: invalid value %q: %s", name, t, why)
				}
				// The server splits a list header at commas and drops empty
				// elements (RFC 9110 §5.6.1).
				if list && (t == "" || strings.Contains(t, ",")) {
					return fmt.Errorf("header parameter %q: list element %q is empty or holds a comma", name, t)
				}
				header.Add(name, t)
			}
		case "cookie":
			if len(texts) > 0 {
				if b, ok := uncarried(texts[0]); ok {
					return fmt.Errorf("cookie parameter %q: invalid value %q: holds %q", name, texts[0], b)
				}
				cookies = append(cookies, &http.Cookie{Name: name, Value: texts[0]})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := abs.unused(); err != nil {
		return nil, err
	}
	path, err := buildPath(template, values)
	if err != nil {
		return nil, err
	}
	u := strings.TrimSuffix(c.Base, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	for k, vs := range header {
		req.Header[k] = append(req.Header[k], vs...)
	}
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	return req, nil
}

var (
	fileType   = reflect.TypeFor[geta.File]()
	readerType = reflect.TypeFor[io.Reader]()
)

// isDeepObject reports whether a query parameter of type t is a deepObject:
// a struct, after one pointer, that is not a text type or geta.File.
func isDeepObject(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind() == reflect.Struct && !isText(t) && t != fileType
}

// formFields visits v's fields tagged form, descending into untagged
// embedded structs.
func formFields(v reflect.Value, visit func(name string, f reflect.StructField, v reflect.Value) error) error {
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		name, ok := f.Tag.Lookup("form")
		if !ok {
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				if err := formFields(v.Field(i), visit); err != nil {
					return err
				}
			}
			continue
		}
		if err := visit(name, f, v.Field(i)); err != nil {
			return err
		}
	}
	return nil
}

// formBody encodes a body:"form" struct as
// application/x-www-form-urlencoded, omitting fields in abs.
func formBody(v reflect.Value, abs absences) ([]byte, string, error) {
	values := url.Values{}
	err := formFields(v, func(name string, f reflect.StructField, fv reflect.Value) error {
		if left, err := abs.leave(f, fv, fmt.Sprintf("form field %q", name)); left || err != nil {
			return err
		}
		ts, err := texts(fv)
		if err != nil {
			return fmt.Errorf("form field %q: %w", name, err)
		}
		for _, t := range ts {
			values.Add(name, t)
		}
		return nil
	})
	return []byte(values.Encode()), "application/x-www-form-urlencoded", err
}

// multipartBody encodes a body:"multipart" struct as multipart/form-data
// (RFC 7578), one part per value or file, omitting fields in abs. A file
// without a content type is sent as application/octet-stream.
func multipartBody(v reflect.Value, abs absences) ([]byte, string, error) {
	// Writes to a bytes.Buffer never fail, so the writer's errors are nil.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	err := formFields(v, func(name string, f reflect.StructField, fv reflect.Value) error {
		if left, err := abs.leave(f, fv, fmt.Sprintf("form field %q", name)); left || err != nil {
			return err
		}
		if files, ok := fileValues(fv); ok {
			for _, f := range files {
				if err := writeFile(mw, name, f); err != nil {
					return fmt.Errorf("form field %q: %w", name, err)
				}
			}
			return nil
		}
		ts, err := texts(fv)
		if err != nil {
			return fmt.Errorf("form field %q: %w", name, err)
		}
		for _, t := range ts {
			mw.WriteField(name, t)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	mw.Close()
	return buf.Bytes(), mw.FormDataContentType(), nil
}

// fileValues returns v's files if v is a geta.File, a slice of them, or a
// pointer to either. A nil pointer yields no files.
func fileValues(v reflect.Value) ([]geta.File, bool) {
	t := v.Type()
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t != fileType && (t.Kind() != reflect.Slice || t.Elem() != fileType) {
		return nil, false
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, true
		}
		v = v.Elem()
	}
	if t == fileType {
		return []geta.File{v.Interface().(geta.File)}, true
	}
	out := make([]geta.File, v.Len())
	for i := range v.Len() {
		out[i] = v.Index(i).Interface().(geta.File)
	}
	return out, true
}

// writeFile writes f to mw as the part name. mw writes to a bytes.Buffer.
func writeFile(mw *multipart.Writer, name string, f geta.File) error {
	// The type and parameter names are valid tokens, so this cannot fail.
	disposition := mime.FormatMediaType("form-data", map[string]string{"name": name, "filename": f.Filename()})
	ct := f.ContentType()
	if ct == "" {
		ct = "application/octet-stream"
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", disposition)
	h.Set("Content-Type", ct)
	w, _ := mw.CreatePart(h)
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(w, rc)
	return err
}

// uncarried returns the first byte of s that net/http drops from a cookie
// value (RFC 6265 cookie-octet, except space and comma, which it quotes).
func uncarried(s string) (byte, bool) {
	for i := 0; i < len(s); i++ {
		if b := s[i]; b < 0x20 || b >= 0x7f || b == '"' || b == ';' || b == '\\' {
			return b, true
		}
	}
	return 0, false
}

// unheld describes why s cannot be sent intact as a header value: leading
// or trailing whitespace, which the server strips, or a control character
// other than tab, which net/http refuses.
func unheld(s string) (string, bool) {
	if s != "" && (s[0] == ' ' || s[0] == '\t' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		return "begins or ends with whitespace", true
	}
	for i := 0; i < len(s); i++ {
		if b := s[i]; b < 0x20 && b != '\t' || b == 0x7f {
			return fmt.Sprintf("holds %q", b), true
		}
	}
	return "", false
}

// buildPath fills the template's parameters from values and escapes each
// segment, so '?', '#', "." and ".." arrive as literal segment text.
func buildPath(template string, values map[string]string) (string, error) {
	if template == "/" {
		return template, nil
	}
	var b strings.Builder
	for seg := range strings.SplitSeq(strings.TrimPrefix(template, "/"), "/") {
		b.WriteByte('/')
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			name := seg[1 : len(seg)-1]
			v, ok := values[name]
			if !ok {
				return "", fmt.Errorf("input binds no value for %s", seg)
			}
			if v == "" {
				// geta matches no empty segment.
				return "", fmt.Errorf("path parameter %q is empty", name)
			}
			b.WriteString(escapeSegment(v))
			continue
		}
		b.WriteString(url.PathEscape(seg))
	}
	return b.String(), nil
}

// escapeSegment escapes one path segment, including "." and "..", which
// url.PathEscape leaves as dot segments.
func escapeSegment(s string) string {
	switch s {
	case ".":
		return "%2E"
	case "..":
		return "%2E%2E"
	}
	return url.PathEscape(s)
}

var locations = []string{"path", "query", "header", "cookie", "body"}

// walk visits the input's tagged fields, descending into untagged embedded
// structs.
func walk(v reflect.Value, visit func(loc, name string, f reflect.StructField, v reflect.Value) error) error {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		loc, name := "", ""
		for _, l := range locations {
			if n, ok := f.Tag.Lookup(l); ok {
				loc, name = l, n
			}
		}
		if loc == "" {
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				if err := walk(v.Field(i), visit); err != nil {
					return err
				}
			}
			continue
		}
		if err := visit(loc, name, f, v.Field(i)); err != nil {
			return err
		}
	}
	return nil
}

// isList reports whether a parameter of type t carries many values: a slice,
// or a pointer to one, that is not a text type.
func isList(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind() == reflect.Slice && !isText(t)
}

// texts renders a parameter: nothing for a nil pointer, one text for a
// scalar, one per element for a slice.
func texts(v reflect.Value) ([]string, error) {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, nil
		}
		v = v.Elem()
	}
	if v.Kind() == reflect.Slice && !isText(v.Type()) {
		out := make([]string, v.Len())
		for i := range v.Len() {
			s, err := text(v.Index(i))
			if err != nil {
				return nil, err
			}
			out[i] = s
		}
		return out, nil
	}
	s, err := text(v)
	if err != nil {
		return nil, err
	}
	return []string{s}, nil
}

var (
	textMarshaler   = reflect.TypeFor[encoding.TextMarshaler]()
	textAppender    = reflect.TypeFor[encoding.TextAppender]()
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// isText reports whether t or *t has AppendText or MarshalText.
func isText(t reflect.Type) bool {
	return reflect.PointerTo(t).Implements(textAppender) || reflect.PointerTo(t).Implements(textMarshaler)
}

func text(v reflect.Value) (string, error) {
	if isText(v.Type()) {
		p := reflect.New(v.Type())
		p.Elem().Set(v)
		if a, ok := p.Interface().(encoding.TextAppender); ok {
			b, err := a.AppendText(nil)
			return string(b), err
		}
		b, err := p.Interface().(encoding.TextMarshaler).MarshalText()
		return string(b), err
	}
	switch v.Kind() {
	case reflect.String:
		return v.String(), nil
	case reflect.Bool:
		return strconv.FormatBool(v.Bool()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10), nil
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, v.Type().Bits()), nil
	}
	return "", fmt.Errorf("type %s is not a parameter type", v.Type())
}

// decodeOutput reads a success response into out: the body for a plain
// output, or headers, cookies, and body for an envelope.
func decodeOutput(res *http.Response, out reflect.Value, opts json.Options) error {
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if out.Kind() != reflect.Struct || !isEnvelope(out.Type()) {
		return json.Unmarshal(body, out.Addr().Interface(), opts)
	}
	return readEnvelope(res, body, out, opts)
}

// readEnvelope reads an envelope's status, headers, cookies, and body into
// out, descending into embedded structs (geta.EnvelopeEmbedded).
func readEnvelope(res *http.Response, body []byte, out reflect.Value, opts json.Options) error {
	t := out.Type()
	for i := range t.NumField() {
		f, fv := t.Field(i), out.Field(i)
		if geta.EnvelopeEmbedded(geta.VetFieldOf(f)) {
			if err := readEnvelope(res, body, fv, opts); err != nil {
				return err
			}
			continue
		}
		if !f.IsExported() {
			continue
		}
		if _, ok := f.Tag.Lookup("status"); ok {
			fv.SetInt(int64(res.StatusCode))
			continue
		}
		if name, ok := f.Tag.Lookup("header"); ok {
			if vs := res.Header.Values(name); len(vs) > 0 {
				if err := setText(fv, vs[0]); err != nil {
					return fmt.Errorf("header %s: %w", name, err)
				}
			}
		}
		if name, ok := f.Tag.Lookup("cookie"); ok {
			for _, ck := range res.Cookies() {
				if ck.Name == name {
					fv.Set(reflect.ValueOf(ck))
				}
			}
		}
		if mt, ok := f.Tag.Lookup("body"); ok && mt != "json" {
			// A media-type body: the raw bytes.
			if f.Type == readerType {
				fv.Set(reflect.ValueOf(bytes.NewReader(body)))
			} else {
				fv.SetBytes(body)
			}
			continue
		}
		if _, ok := f.Tag.Lookup("body"); ok && len(body) > 0 {
			if err := json.Unmarshal(body, fv.Addr().Interface(), opts); err != nil {
				return fmt.Errorf("body: %w", err)
			}
		}
	}
	return nil
}

// isEnvelope reports whether t or a struct it embeds has a header, cookie,
// body, or status tag, which makes it an envelope rather than a JSON body.
func isEnvelope(t reflect.Type) bool {
	for i := range t.NumField() {
		f := t.Field(i)
		for _, l := range []string{"header", "cookie", "body", "status"} {
			if _, ok := f.Tag.Lookup(l); ok {
				return true
			}
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct && isEnvelope(f.Type) {
			return true
		}
	}
	return false
}

// setText stores a header's text in v, allocating a pointer field.
func setText(v reflect.Value, s string) error {
	if v.Kind() == reflect.Pointer {
		v.Set(reflect.New(v.Type().Elem()))
		v = v.Elem()
	}
	if reflect.PointerTo(v.Type()).Implements(textUnmarshaler) {
		return v.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(s))
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
		return nil
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		v.SetBool(b)
		return err
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, v.Type().Bits())
		v.SetInt(n)
		return err
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(s, 10, v.Type().Bits())
		v.SetUint(n)
		return err
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(s, v.Type().Bits())
		v.SetFloat(f)
		return err
	}
	return errors.New("type " + v.Type().String() + " is not a header type")
}
