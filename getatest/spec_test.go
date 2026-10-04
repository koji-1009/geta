package getatest

import (
	"bufio"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaclient"
)

// Rules of getatest that no other test asserts.

// seen is one request as the app received it.
type seen struct {
	method, contentType string
	header              http.Header
	body                []byte
}

// capture records every request the app receives on paths under /c and
// answers it 418, which it declares.
func capture() (geta.Middleware, func() []seen) {
	var mu sync.Mutex
	var all []seen
	m := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/c") {
				next.ServeHTTP(w, r)
				return
			}
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			all = append(all, seen{r.Method, r.Header.Get("Content-Type"), r.Header.Clone(), b})
			mu.Unlock()
			w.WriteHeader(http.StatusTeapot)
		})
	}).Answers(http.StatusTeapot, "Captured")
	return m, func() []seen {
		mu.Lock()
		defer mu.Unlock()
		return append([]seen(nil), all...)
	}
}

func greet(context.Context, *struct{}) (*greeting, error) { return &greeting{"hi"}, nil }

// Do sends a string or []byte as is and any other body as JSON, as
// application/json unless the client was given a Content-Type; the method
// helpers are Do with their method.
func TestDoSendsBodiesAndTheirContentType(t *testing.T) {
	m, got := capture()
	c := New(t, geta.Table{Root: geta.Scope{m}, Routes: []geta.Entry{{Path: "/g", Route: geta.Route{Get: geta.Op(http.StatusOK, greet, geta.Doc{})}}}})
	c.Do(http.MethodPut, "/c", "raw text")
	c.Do(http.MethodPut, "/c", []byte("raw bytes"))
	c.Do(http.MethodPut, "/c", map[string]int{"n": 1})
	c.With("Content-Type", "text/plain").Do(http.MethodPut, "/c", "plain")
	c.Get("/c")
	c.Delete("/c")
	c.Post("/c", "p")
	c.Put("/c", "u")
	c.Patch("/c", "a")
	c.Query("/c", "q")
	want := []seen{
		{http.MethodPut, "application/json", nil, []byte("raw text")},
		{http.MethodPut, "application/json", nil, []byte("raw bytes")},
		{http.MethodPut, "application/json", nil, []byte(`{"n":1}`)},
		{http.MethodPut, "text/plain", nil, []byte("plain")},
		{http.MethodGet, "", nil, nil},
		{http.MethodDelete, "", nil, nil},
		{http.MethodPost, "application/json", nil, []byte("p")},
		{http.MethodPut, "application/json", nil, []byte("u")},
		{http.MethodPatch, "application/json", nil, []byte("a")},
		{geta.MethodQuery, "application/json", nil, []byte("q")},
	}
	all := got()
	if len(all) != len(want) {
		t.Fatalf("%d requests", len(all))
	}
	for i, w := range want {
		if all[i].method != w.method || all[i].contentType != w.contentType || string(all[i].body) != string(w.body) {
			t.Errorf("request %d: %s %q %q, want %s %q %q", i, all[i].method, all[i].contentType, all[i].body, w.method, w.contentType, w.body)
		}
	}
}

// Content sends the bytes as the content type given, Form an urlencoded
// form, Multipart a multipart/form-data body: the values by sorted name,
// then each file with its filename and content type,
// application/octet-stream when none is given.
func TestContentFormAndMultipartSendTheirMediaTypes(t *testing.T) {
	m, got := capture()
	c := New(t, geta.Table{Root: geta.Scope{m}, Routes: []geta.Entry{{Path: "/g", Route: geta.Route{Get: geta.Op(http.StatusOK, greet, geta.Doc{})}}}})
	c.Content(http.MethodPost, "/c", "text/csv", []byte("a,b\n"))
	c.Form(http.MethodPost, "/c", url.Values{"b": {"2"}, "a": {"1 &"}})
	c.Multipart(http.MethodPost, "/c", url.Values{"z": {"last"}, "a": {"first"}},
		FilePart{Field: "f", Filename: "x.png", ContentType: "image/png", Content: []byte("PNG")},
		FilePart{Field: "g", Filename: "y.bin", Content: []byte("BIN")})
	all := got()
	if len(all) != 3 {
		t.Fatalf("%d requests", len(all))
	}
	if all[0].contentType != "text/csv" || string(all[0].body) != "a,b\n" {
		t.Errorf("Content: %q %q", all[0].contentType, all[0].body)
	}
	if all[1].contentType != "application/x-www-form-urlencoded" || string(all[1].body) != "a=1+%26&b=2" {
		t.Errorf("Form: %q %q", all[1].contentType, all[1].body)
	}
	mt, params, err := mime.ParseMediaType(all[2].contentType)
	if err != nil || mt != "multipart/form-data" {
		t.Fatalf("Multipart: %q %v", all[2].contentType, err)
	}
	r := multipart.NewReader(strings.NewReader(string(all[2].body)), params["boundary"])
	var parts []string
	for {
		p, err := r.NextPart()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(p)
		parts = append(parts, p.FormName()+"|"+p.FileName()+"|"+p.Header.Get("Content-Type")+"|"+string(b))
	}
	if got := strings.Join(parts, "\n"); got != "a|||first\nz|||last\nf|x.png|image/png|PNG\ng|y.bin|application/octet-stream|BIN" {
		t.Errorf("Multipart parts:\n%s", got)
	}
}

// sse answers GET /raw with a comment and one event, and, with ?hold, keeps
// the stream open until the client hangs up.
func sse() geta.Middleware {
	return geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/raw" {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, ": hi\n\nevent: e\nid: 1\ndata: a\ndata: b\n\n")
			http.NewResponseController(w).Flush()
			if r.URL.Query().Has("hold") {
				<-r.Context().Done()
			}
		})
	})
}

// An EventStream reads each event (its name, id, and data lines joined),
// counts comments, and reports false once the stream ends or the client
// hangs up; a response that is no event stream is its Response, fully
// read, and Next reports false at once.
func TestStreamReadsEventsCommentsAndRefusals(t *testing.T) {
	c := New(t, geta.Table{Root: geta.Scope{sse()}, Routes: []geta.Entry{{Path: "/g", Route: geta.Route{Get: geta.Op(http.StatusOK, greet, geta.Doc{})}}}})
	s := c.Stream("/raw")
	e, ok := s.Next()
	if !ok || e != (Event{Name: "e", ID: "1", Data: "a\nb"}) || s.Comments != 1 || s.Response.Status != http.StatusOK {
		t.Fatalf("%+v %v comments %d status %d", e, ok, s.Comments, s.Response.Status)
	}
	if _, ok := s.Next(); ok {
		t.Fatal("an event after the stream ended")
	}

	held := c.Stream("/raw?hold")
	if _, ok := held.Next(); !ok {
		t.Fatal("no event")
	}
	held.Close()
	if _, ok := held.Next(); ok {
		t.Fatal("an event after Close")
	}

	refused := c.Stream("/none")
	if refused.Response.Status != http.StatusNotFound || len(refused.Response.Body) == 0 {
		t.Fatalf("%d %q", refused.Response.Status, refused.Response.Body)
	}
	if _, ok := refused.Next(); ok {
		t.Fatal("an event from a refusal")
	}
}

// Upgrade sends Connection: Upgrade and Upgrade: protocol, or neither when
// protocol is ""; a 101 is Switched with its connection, any other answer
// is the Response, fully read.
func TestUpgradeSwitchesOrHandsBackTheRefusal(t *testing.T) {
	var mu sync.Mutex
	var headers []http.Header
	record := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			headers = append(headers, r.Header.Clone())
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	})
	echo := func(context.Context, *struct{}) (*geta.Upgrade, error) {
		return &geta.Upgrade{Protocol: "echo", Serve: func(conn net.Conn, rw *bufio.ReadWriter) {
			line, _ := rw.ReadString('\n')
			rw.WriteString(line)
			rw.Flush()
		}}, nil
	}
	c := New(t, geta.Table{Root: geta.Scope{record}, Routes: []geta.Entry{{Path: "/ws", Route: geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, echo, geta.Doc{})}}}})
	u := c.Upgrade("/ws", "echo")
	if !u.Switched || u.Response.Status != http.StatusSwitchingProtocols || u.Conn == nil {
		t.Fatalf("%v %d", u.Switched, u.Response.Status)
	}
	io.WriteString(u.Conn, "ping\n")
	if line, _ := bufio.NewReader(u.Conn).ReadString('\n'); line != "ping\n" {
		t.Fatalf("%q", line)
	}
	for _, protocol := range []string{"other", ""} {
		u := c.Upgrade("/ws", protocol)
		if u.Switched || u.Conn != nil || u.Response.Status != http.StatusUpgradeRequired || len(u.Response.Body) == 0 {
			t.Errorf("%q: %v %d %q", protocol, u.Switched, u.Response.Status, u.Response.Body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(headers) != 3 {
		t.Fatalf("%d requests", len(headers))
	}
	for i, want := range [][2]string{{"Upgrade", "echo"}, {"Upgrade", "other"}, {"", ""}} {
		if got := [2]string{headers[i].Get("Connection"), headers[i].Get("Upgrade")}; got != want {
			t.Errorf("request %d: Connection %q Upgrade %q, want %q", i, got[0], got[1], want)
		}
	}
}

// specLiar declares a string but writes a number.
type specLiar struct{}

func (specLiar) MarshalJSON() ([]byte, error) { return []byte(`7`), nil }

type specLiarOut struct {
	V   specLiar `json:"v"`
	Pad string   `json:"pad"`
}

// The document check holds a body as sent: one the server sent in a content
// coding is not checked; the same body decoded by the client's transport is.
func TestCodedResponsesAreNotHeldToTheDocument(t *testing.T) {
	app, err := geta.New(geta.Table{Root: geta.Scope{geta.Gzip()}, Routes: []geta.Entry{{Path: "/big", Route: geta.Route{
		Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*specLiarOut, error) {
			return &specLiarOut{Pad: strings.Repeat("a", 2000)}, nil
		}, geta.Doc{}),
	}}}}, geta.WithSchema[specLiar]("string", ""))
	if err != nil {
		t.Fatal(err)
	}
	f := inGoroutine(t, func(f *failures) {
		res := Serve(f, app).With("Accept-Encoding", "gzip").Get("/big")
		if res.Header.Get("Content-Encoding") != "gzip" {
			f.errs = append(f.errs, "not coded")
		}
	})
	if len(f.errs) != 0 {
		t.Fatalf("a coded body was checked: %q", f.errs)
	}
	f = inGoroutine(t, func(f *failures) {
		res := Serve(f, app).Get("/big")
		if res.Header.Get("Content-Encoding") != "" {
			f.errs = append(f.errs, "still coded")
		}
	})
	if len(f.errs) != 1 || !strings.Contains(f.errs[0], "$.v: expected string, got integer") {
		t.Fatalf("%q", f.errs)
	}
}

type specLiarEvent struct {
	V specLiar `json:"v"`
}

// An event stream's events are held to the event type's schema as Stream's
// Next reads them; a stream read whole by Get is not checked.
func TestEventsAreCheckedThroughStream(t *testing.T) {
	app, err := geta.New(geta.Table{Routes: []geta.Entry{{Path: "/e", Route: geta.Route{
		Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*geta.Stream[specLiarEvent], error) {
			return &geta.Stream[specLiarEvent]{Events: func(yield func(specLiarEvent) bool) { yield(specLiarEvent{}) }}, nil
		}, geta.Doc{}),
	}}}}, geta.WithSchema[specLiar]("string", ""))
	if err != nil {
		t.Fatal(err)
	}
	f := inGoroutine(t, func(f *failures) {
		if res := Serve(f, app).Get("/e"); res.Status != http.StatusOK || !strings.Contains(res.Text(), `data: {"v":7}`) {
			f.errs = append(f.errs, "no stream")
		}
	})
	if len(f.errs) != 0 {
		t.Fatalf("Get checked the events: %q", f.errs)
	}
	f = inGoroutine(t, func(f *failures) {
		if _, ok := Serve(f, app).Stream("/e").Next(); !ok {
			f.errs = append(f.errs, "no event")
		}
	})
	if len(f.errs) != 1 || !strings.Contains(f.errs[0], "$.v: expected string, got integer") {
		t.Fatalf("%q", f.errs)
	}
}

// Typed's client sends the getatest client's headers, and each response to
// it is held to the document as any other.
func TestTypedSendsTheClientsHeadersAndIsChecked(t *testing.T) {
	var mu sync.Mutex
	var traces []string
	teapot := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			traces = append(traces, r.Header.Get("X-Trace"))
			mu.Unlock()
			if r.Header.Get("X-Teapot") != "" {
				w.WriteHeader(http.StatusTeapot)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	app, err := geta.New(geta.Table{Root: geta.Scope{teapot}, Routes: []geta.Entry{{Path: "/g", Route: geta.Route{Get: geta.Op(http.StatusOK, greet, geta.Doc{})}}}})
	if err != nil {
		t.Fatal(err)
	}
	f := inGoroutine(t, func(f *failures) {
		c := Serve(f, app).With("X-Trace", "t")
		if out, err := getaclient.Call[struct{}, greeting](context.Background(), c.Typed(), http.MethodGet, "/g", nil); err != nil || out.Text != "hi" {
			f.errs = append(f.errs, "the call failed")
			return
		}
		getaclient.Call[struct{}, greeting](context.Background(), c.With("X-Teapot", "1").Typed(), http.MethodGet, "/g", nil)
	})
	if len(f.errs) != 1 || !strings.Contains(f.errs[0], "status 418 is not documented") {
		t.Fatalf("%q", f.errs)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(traces, ",") != "t,t" {
		t.Fatalf("X-Trace sent: %q", traces)
	}
}
