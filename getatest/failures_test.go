package getatest

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaclient"
)

// inGoroutine runs fn with a failures TB, on a goroutine a Fatal may end.
func inGoroutine(t *testing.T, fn func(f *failures)) *failures {
	f := &failures{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(f)
	}()
	<-done
	return f
}

// Typed checks a response's documented headers as Send does, a response
// with no JSON body included.
func TestTypedChecksTheHeadersOfEveryResponse(t *testing.T) {
	aged := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Age", "-1")
			next.ServeHTTP(w, r)
		})
	}).Answers(http.StatusNoContent, "Gone").Header(http.StatusNoContent, "Age", "Seconds", geta.HeaderOf[int]("minimum=0"))
	app, err := geta.New(geta.Table{Root: geta.Scope{aged}, Routes: []geta.Entry{{Path: "/x", Route: geta.Route{
		Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *struct{}) error { return nil }, geta.Doc{}),
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	sent := inGoroutine(t, func(f *failures) { Serve(f, app).Delete("/x") })
	typed := inGoroutine(t, func(f *failures) {
		getaclient.CallNoBody[struct{}](context.Background(), Serve(f, app).Typed(), http.MethodDelete, "/x", nil)
	})
	if len(sent.errs) != 1 || len(typed.errs) != 1 || !strings.Contains(typed.errs[0], "Age") {
		t.Fatalf("Send: %q; Typed: %q", sent.errs, typed.errs)
	}
}

// New fails the test at once on an assembly error, with geta.New's text.
func TestNewFailsOnAnAssemblyError(t *testing.T) {
	type in struct {
		ID string `path:"id"`
	}
	bad := geta.Table{Routes: []geta.Entry{{Path: "/x", Route: geta.Route{
		Get: geta.Op(http.StatusOK, func(context.Context, *in) (*greeting, error) { return nil, nil }, geta.Doc{}),
	}}}}
	reached := false
	f := inGoroutine(t, func(f *failures) {
		New(f, bad)
		reached = true
	})
	if !f.fatal || reached || len(f.errs) != 1 || !strings.HasPrefix(f.errs[0], "geta.New: ") || !strings.Contains(f.errs[0], `"id"`) {
		t.Fatalf("%v %q", reached, f.errs)
	}
}

// A response's readers fail the test on what they cannot read: JSON a
// member the type lacks or no JSON, Problem another Content-Type or a body
// that is no problem, ProblemAs a problem that does not decode as P.
func TestResponseReadersFailTheTest(t *testing.T) {
	problem := http.Header{"Content-Type": {geta.ProblemContentType}}
	for name, read := range map[string]func(r *Response){
		"unknown member": func(r *Response) { r.Body = []byte(`{"text":"a","x":1}`); _ = r.JSON[greeting]() },
		"not JSON":       func(r *Response) { r.Body = []byte(`nope`); _ = r.JSON[greeting]() },
		"no problem type": func(r *Response) {
			r.Body = []byte(`{}`)
			r.Header = http.Header{"Content-Type": {"application/json"}}
			r.Problem()
		},
		"no problem body": func(r *Response) { r.Body = []byte(`[`); r.Header = problem; r.Problem() },
		"not P": func(r *Response) {
			r.Body, r.Header = []byte(`{"type":"about:blank","title":"Conflict","status":409,"text":7}`), problem
			_ = r.ProblemAs[greeting]()
		},
	} {
		f := inGoroutine(t, func(f *failures) {
			read(&Response{t: f, Status: 409, Header: http.Header{}})
			f.errs = append(f.errs, "returned")
		})
		if !f.fatal || len(f.errs) != 1 || !strings.HasPrefix(f.errs[0], "getatest: ") {
			t.Errorf("%s: %q", name, f.errs)
		}
	}
}

// A request sent through HTTP() skips the client's headers and the
// document's checks: an undocumented status passes.
func TestHTTPSkipsTheChecks(t *testing.T) {
	answer := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Teapot") != "" {
				w.WriteHeader(http.StatusTeapot)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	app, err := geta.New(geta.Table{Root: geta.Scope{answer}, Routes: []geta.Entry{{Path: "/g", Route: geta.Route{
		Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*greeting, error) { return &greeting{"hi"}, nil }, geta.Doc{}),
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	f := inGoroutine(t, func(f *failures) {
		c := Serve(f, app).With("X-Teapot", "1")
		// Through HTTP(): neither the client's header nor the check.
		res, err := c.HTTP().Get(c.URL() + "/g")
		if err != nil || res.StatusCode != http.StatusOK {
			f.errs = append(f.errs, "HTTP() sent the client's header")
		}
		res.Body.Close()
		req, _ := http.NewRequest(http.MethodGet, c.URL()+"/g", nil)
		req.Header.Set("X-Teapot", "1")
		if res, err := c.HTTP().Do(req); err != nil || res.StatusCode != http.StatusTeapot {
			f.errs = append(f.errs, "no 418")
		}
		if len(f.errs) != 0 {
			return
		}
		// Through Get: the 418 is no status the document lists.
		c.Get("/g")
	})
	if len(f.errs) != 1 || !strings.Contains(f.errs[0], "status 418 is not documented") {
		t.Fatalf("%q", f.errs)
	}
}

// A request sent over a connection from DialContext skips the
// client's headers and the document's checks, as DialContext says: the
// client's X-Teapot is not sent, and an undocumented 418 fails nothing.
func TestDialContextSkipsTheChecks(t *testing.T) {
	answer := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Teapot") != "" {
				w.WriteHeader(http.StatusTeapot)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	app, err := geta.New(geta.Table{Root: geta.Scope{answer}, Routes: []geta.Entry{{Path: "/g", Route: geta.Route{
		Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*greeting, error) { return &greeting{"hi"}, nil }, geta.Doc{}),
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	f := inGoroutine(t, func(f *failures) {
		c := Serve(f, app).With("X-Teapot", "1")
		send := func(teapot bool) int {
			conn, err := c.DialContext(context.Background(), "tcp", "example.com:80")
			if err != nil {
				f.errs = append(f.errs, err.Error())
				return 0
			}
			defer conn.Close()
			req, _ := http.NewRequest(http.MethodGet, "http://example.com/g", nil)
			req.Close = true
			if teapot {
				req.Header.Set("X-Teapot", "1")
			}
			if err := req.Write(conn); err != nil {
				f.errs = append(f.errs, err.Error())
				return 0
			}
			res, err := http.ReadResponse(bufio.NewReader(conn), req)
			if err != nil {
				f.errs = append(f.errs, err.Error())
				return 0
			}
			res.Body.Close()
			return res.StatusCode
		}
		if got := send(false); got != http.StatusOK {
			f.errs = append(f.errs, "DialContext sent the client's header")
		}
		if got := send(true); got != http.StatusTeapot {
			f.errs = append(f.errs, "no 418")
		}
		if len(f.errs) != 0 {
			return
		}
		// Through Get: the 418 is no status the document lists.
		c.Get("/g")
	})
	if len(f.errs) != 1 || !strings.Contains(f.errs[0], "status 418 is not documented") {
		t.Fatalf("%q", f.errs)
	}
}
