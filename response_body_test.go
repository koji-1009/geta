package geta_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// The rules of response.md on JSON bodies, the response buffer, envelopes,
// output headers, and raw bodies that no other test asserts.

type sortedMap struct {
	M map[string]int `json:"m"`
}

// A JSON body is written with its map keys sorted, so one value is always
// the same bytes.
func TestMapKeysAreWrittenSorted(t *testing.T) {
	out := &sortedMap{M: map[string]int{}}
	var want strings.Builder
	want.WriteString(`{"m":{`)
	for i := range 30 {
		k := fmt.Sprintf("k%02d", i)
		out.M[k] = i
		if i > 0 {
			want.WriteByte(',')
		}
		fmt.Fprintf(&want, "%q:%d", k, i)
	}
	want.WriteString(`}}`)
	a := accepts(t, one("/x", get(func(context.Context, *empty) (*sortedMap, error) { return out, nil })))
	for range 20 {
		if got := do(t, a, "GET", "/x").Body.String(); got != want.String() {
			t.Fatalf("%s\nwant %s", got, want.String())
		}
	}
}

// htmlOwn writes its own JSON.
type htmlOwn struct{}

func (htmlOwn) MarshalJSON() ([]byte, error) { return []byte(`"<b>&"`), nil }

type htmlOut struct {
	S string  `json:"s"`
	O htmlOwn `json:"o"`
}

type nilShapes struct {
	M map[string]int `json:"m"`
	S []int          `json:"s"`
}

// <, >, and & are escaped in a JSON body, in a type's own JSON output too;
// a nil map is written {}, as a nil slice is [].
func TestBodiesAreHTMLSafeAndNeverNull(t *testing.T) {
	a := accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/html", Route: get(func(context.Context, *empty) (*htmlOut, error) { return &htmlOut{S: "<b>&"}, nil })},
		{Path: "/nil", Route: get(func(context.Context, *empty) (*nilShapes, error) { return &nilShapes{}, nil })},
	}})
	// Each of <, >, and & as a JSON \u escape, spelled out.
	u := func(hex string) string { return string('\\') + "u00" + hex }
	escaped := u("3c") + "b" + u("3e") + u("26")
	if got := do(t, a, "GET", "/html").Body.String(); got != `{"s":"`+escaped+`","o":"`+escaped+`"}` {
		t.Error(got)
	}
	if got := do(t, a, "GET", "/nil").Body.String(); got != `{"m":{},"s":[]}` {
		t.Error(got)
	}
}

type htmlNote struct {
	Note string `json:"note"`
}

// A problem body is HTML-safe as a success body is: a plain row's detail, a
// described row's own members beside its extension members, and what a
// middleware writes with WriteProblem.
func TestProblemBodiesAreHTMLSafe(t *testing.T) {
	u := func(hex string) string { return string('\\') + "u00" + hex }
	escaped := u("3c") + "b" + u("3e") + u("26")
	plain := accepts(t, failing(errNotFound, geta.On(errNotFound, http.StatusNotFound, "<b>&")))
	if got := do(t, plain, "GET", "/x").Body.String(); got != `{"type":"about:blank","title":"Not Found","status":404,"detail":"`+escaped+`"}` {
		t.Error(got)
	}
	described := accepts(t, failing(errNotFound, geta.OnAsProblem(http.StatusNotFound, "<b>&", func(error) htmlNote { return htmlNote{Note: "<b>&"} })))
	if got := do(t, described, "GET", "/x").Body.String(); got != `{"type":"about:blank","title":"Not Found","status":404,"detail":"`+escaped+`","note":"`+escaped+`"}` {
		t.Error(got)
	}
	mw := geta.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { geta.WriteProblem(w, http.StatusForbidden, "<b>&") })
	}).Answers(http.StatusForbidden, "refused")
	if got := do(t, accepts(t, withRoot(one("/x", get(okHandler)), mw)), "GET", "/x").Body.String(); !strings.Contains(got, `"detail":"`+escaped+`"`) {
		t.Error(got)
	}
}

// DefaultLimits holds 64 KiB of a response; a MaxResponseBuffer below zero
// streams every body, as zero does.
func TestResponseBufferBounds(t *testing.T) {
	if geta.DefaultLimits.MaxResponseBuffer != 65536 {
		t.Fatal(geta.DefaultLimits.MaxResponseBuffer)
	}
	body := &largeList{Items: []largeItem{{ID: 1, Name: "a", Score: 1}}}
	rec := do(t, largeApp(t, body, -1), "GET", "/x")
	if rec.Code != 200 || rec.Header().Get("Content-Length") != "" || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatal(rec.Code, rec.Header())
	}
	wantJSON(t, rec.Body.Bytes(), body)
}

// An operation's Doc.Limits sets the buffer its own response is held in:
// its body streams past its MaxResponseBuffer, while another operation's,
// as large, keeps its Content-Length under the App's.
func TestOperationLimitsBufferTheResponse(t *testing.T) {
	h := textHandler("0123456789abcdef")
	tight := func(l geta.Limits) geta.Limits { l.MaxResponseBuffer = 8; return l }
	a := accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/app", Route: get(h)},
		{Path: "/op", Route: geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{Limits: tight})}},
	}})
	app, op := do(t, a, "GET", "/app"), do(t, a, "GET", "/op")
	if app.Header().Get("Content-Length") != strconv.Itoa(app.Body.Len()) || op.Header().Get("Content-Length") != "" {
		t.Fatal(app.Header(), op.Header())
	}
	if app.Body.String() != `{"text":"0123456789abcdef"}` || op.Body.String() != app.Body.String() {
		t.Fatal(app.Body, op.Body)
	}
}

// failingConn is a ResponseWriter whose connection is gone: every Write
// fails.
type failingConn struct {
	h      http.Header
	status int
}

func (f *failingConn) Header() http.Header       { return f.h }
func (f *failingConn) WriteHeader(code int)      { f.status = code }
func (f *failingConn) Write([]byte) (int, error) { return 0, errors.New("connection reset") }
func (f *failingConn) serve(a *geta.App) (p any) {
	defer func() { p = recover() }()
	a.ServeHTTP(f, httptest.NewRequest(http.MethodGet, "/x", nil))
	return nil
}

type csvOut struct {
	Body []byte `body:"text/csv"`
}

// A committed response the connection does not take is logged at Info, as
// a client gone is, and the connection is aborted: every body geta writes,
// held or streamed, success or problem, behind Compress and ETag too. A
// middleware's WriteProblem, given no request, aborts without a log line.
func TestACommittedWriteFailureAborts(t *testing.T) {
	limits := geta.DefaultLimits
	limits.MaxResponseBuffer = 64
	refuse := geta.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { geta.WriteProblem(w, http.StatusForbidden, "") })
	}).Answers(http.StatusForbidden, "refused")
	for name, c := range map[string]struct {
		tbl    geta.Table
		status int
		logged bool
	}{
		"streamed json": {one("/x", get(func(context.Context, *empty) (*largeList, error) { return largeOf(1024, false), nil })), 200, true},
		"held json":     {one("/x", get(okHandler)), 200, true},
		"raw":           {one("/x", get(func(context.Context, *empty) (*csvOut, error) { return &csvOut{Body: []byte("a,b\n")}, nil })), 200, true},
		"problem":       {failing(errNotFound, geta.On(errNotFound, http.StatusNotFound, "")), 404, true},
		"described":     {failing(errNotFound, geta.OnAsProblem(http.StatusNotFound, "", func(error) htmlNote { return htmlNote{Note: "n"} })), 404, true},
		"unmatched":     {one("/y", get(okHandler)), 404, true},
		"defect":        {failing(errors.New("no row")), 500, true},
		"etag":          {withRoot(one("/x", get(okHandler)), geta.ETag()), 200, true},
		"compress":      {withRoot(one("/x", get(okHandler)), geta.Gzip()), 200, true},
		"write problem": {withRoot(one("/x", get(okHandler)), refuse), 403, false},
	} {
		log, buf := logger()
		a, err := geta.New(c.tbl, geta.WithLimits(limits), geta.WithLogger(log))
		if err != nil {
			t.Fatal(err)
		}
		f := &failingConn{h: http.Header{}}
		if p := f.serve(a); p != http.ErrAbortHandler || f.status != c.status {
			t.Errorf("%s: %v %d", name, p, f.status)
		}
		l := buf.String()
		logged := strings.Contains(l, "level=INFO") && strings.Contains(l, "the response could not be written") && strings.Contains(l, "connection reset")
		if logged != c.logged {
			t.Errorf("%s: %s", name, l)
		}
	}
}

type scalarHeaders struct {
	B   bool    `header:"X-B"`
	U   uint8   `header:"X-U"`
	I   int     `header:"X-I"`
	F   float64 `header:"X-F"`
	Big float64 `header:"X-Big"`
	S   float32 `header:"X-S"`
}

// An output header is written as text: a bool true or false, an integer and
// an unsigned integer in decimal, a float in the shortest form that reads
// back as it (with an exponent past 1e21 and below 1e-6). An envelope with
// no body field writes its headers and status alone.
func TestScalarHeadersAndABodilessEnvelope(t *testing.T) {
	h := func(context.Context, *empty) (*scalarHeaders, error) {
		return &scalarHeaders{B: true, U: 7, I: -3, F: 1.5, Big: 1e21, S: 1e-7}, nil
	}
	res := getatest.New(t, one("/x", get(h))).Get("/x")
	for name, want := range map[string]string{"X-B": "true", "X-U": "7", "X-I": "-3", "X-F": "1.5", "X-Big": "1e+21", "X-S": "1e-7"} {
		if got := res.Header.Get(name); got != want {
			t.Errorf("%s: %q; want %q", name, got, want)
		}
	}
	if res.Status != 200 || len(res.Body) != 0 || res.Header.Get("Content-Type") != "" {
		t.Fatal(res.Status, res.Header, res.Text())
	}
}

type chosenStatus struct {
	Status   int    `status:"200|201"`
	Location string `header:"Location"`
	Body     ok     `body:"json"`
}

type resetBeside struct {
	Status int `status:"200|205"`
	Body   ok  `body:"json"`
}

// An undeclared status is judged before any envelope header is set: the 500
// carries none. A 205 beside a body is refused, as a 204 is.
func TestStatusFieldMistakesLeaveNoHeaders(t *testing.T) {
	h := func(context.Context, *empty) (*chosenStatus, error) {
		return &chosenStatus{Status: http.StatusAccepted, Location: "/y"}, nil
	}
	res := getatest.New(t, one("/x", get(h))).Get("/x")
	if res.Status != 500 || res.Header.Get("Location") != "" {
		t.Fatal(res.Status, res.Header)
	}
	reset := func(context.Context, *empty) (*resetBeside, error) { return nil, nil }
	rejects(t, one("/x", get(reset)), "success status 205 takes no body")
}

type readerOut struct {
	Name string    `header:"X-Name"`
	Body io.Reader `body:"text/plain"`
}

type bytesOut struct {
	Name string `header:"X-Name"`
	Body []byte `body:"text/plain"`
}

// A []byte raw body carries its Content-Length whatever its size; a nil
// io.Reader is a 500 with none of the envelope's headers, and so is an
// envelope header that fails beside a raw body of any kind.
func TestRawBodyEdges(t *testing.T) {
	limits := geta.DefaultLimits
	limits.MaxResponseBuffer = 4
	var nilReader, badBytes, badShort, badLong bool
	a, err := geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/csv", Route: get(func(context.Context, *empty) (*csvOut, error) { return &csvOut{Body: []byte("0123456789")}, nil })},
		{Path: "/nil", Route: get(func(context.Context, *empty) (*readerOut, error) {
			nilReader = true
			return &readerOut{Name: "n"}, nil
		})},
		{Path: "/bytes", Route: get(func(context.Context, *empty) (*bytesOut, error) {
			badBytes = true
			return &bytesOut{Name: "a\r\nb", Body: []byte("x")}, nil
		})},
		{Path: "/short", Route: get(func(context.Context, *empty) (*readerOut, error) {
			badShort = true
			return &readerOut{Name: " a", Body: strings.NewReader("ab")}, nil
		})},
		{Path: "/long", Route: get(func(context.Context, *empty) (*readerOut, error) {
			badLong = true
			return &readerOut{Name: "a\x00", Body: strings.NewReader("abcdefghij")}, nil
		})},
	}}, geta.WithLimits(limits), geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	c := getatest.Serve(t, a)
	if res := c.Get("/csv"); res.Status != 200 || res.Header.Get("Content-Length") != "10" || res.Text() != "0123456789" {
		t.Fatal(res.Status, res.Header, res.Text())
	}
	for _, path := range []string{"/nil", "/bytes", "/short", "/long"} {
		res := c.Get(path)
		if res.Status != 500 || res.Header.Get("X-Name") != "" || res.Header.Get("Content-Type") != geta.ProblemContentType {
			t.Errorf("%s: %d %v", path, res.Status, res.Header)
		}
	}
	if !nilReader || !badBytes || !badShort || !badLong {
		t.Fatal("a handler did not run")
	}
}

type sealedEnvelope struct {
	Name string  `header:"X-Name"`
	Body drawing `body:"json"`
}

// An envelope's JSON body is held to its sealed types as a whole output is:
// a required one left nil is a 500, with none of the envelope's headers.
func TestAnEnvelopeBodyHoldsItsSealedTypes(t *testing.T) {
	h := func(context.Context, *empty) (*sealedEnvelope, error) { return &sealedEnvelope{Name: "n"}, nil }
	res := getatest.New(t, one("/x", get(h)), geta.WithUnion(shapes), geta.WithLogger(quietLogger())).Get("/x")
	if res.Status != 500 || res.Header.Get("X-Name") != "" || strings.Contains(res.Text(), "main") {
		t.Fatal(res.Status, res.Header, res.Text())
	}
}
