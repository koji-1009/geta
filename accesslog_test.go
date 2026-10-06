package geta_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
)

func TestAccessLogRecordsTheTemplateNeverThePath(t *testing.T) {
	log, buf := logger()
	a := accepts(t, withRoot(one("/users/{id}", get(idHandler)), geta.AccessLog(log)))
	do(t, a, "GET", "/users/42")
	do(t, a, "POST", "/users/42")
	do(t, a, "GET", "/does/not/exist")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatal(buf.String())
	}
	for i, want := range []string{`route=/users/{id} status=200`, `route=<unmatched> status=405`, `route=<unmatched> status=404`} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d: %s", i, lines[i])
		}
	}
	if strings.Contains(buf.String(), "does/not/exist") || strings.Contains(buf.String(), "/users/42") {
		t.Fatal("a raw path reached the log:", buf.String())
	}
}

// The access log pairs the method served with the route served, after a root
// middleware changed the method.
func TestAccessLogRecordsWhatWasServed(t *testing.T) {
	var logs bytes.Buffer
	app, err := geta.New(geta.Table{
		Root: geta.Scope{geta.AccessLog(slog.New(slog.NewTextHandler(&logs, nil))), methodOverride},
		Routes: []geta.Entry{
			{Path: "/items", Route: geta.Route{
				Post:   geta.Op(http.StatusOK, okHandler, geta.Doc{}),
				Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error { return nil }, geta.Doc{}),
			}},
			{Path: "/posts/{id}", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *idIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
		},
	}, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(t, app, http.MethodPost, "/items", "X-HTTP-Method-Override", "DELETE"); rec.Code != http.StatusNoContent {
		t.Fatalf("%d", rec.Code)
	}
	if line := logs.String(); !strings.Contains(line, "method=DELETE route=/items status=204") {
		t.Fatalf("log line: %s", line)
	}
	logs.Reset()
	if rec := do(t, app, http.MethodPost, "/items", "X-HTTP-Method-Override", "PATCH"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("%d", rec.Code)
	}
	if line := logs.String(); !strings.Contains(line, "method=PATCH route=<unmatched> status=405") {
		t.Fatalf("log line: %s", line)
	}
}

// A method is logged cut to 128 bytes: a request no operation serves may
// carry any token as its method.
func TestAccessLogCutsALongMethod(t *testing.T) {
	log, buf := logger()
	a := accepts(t, withRoot(one("/users/{id}", get(idHandler)), geta.AccessLog(log)))
	req := httptest.NewRequest(http.MethodGet, "/users/42", nil)
	req.Method = strings.Repeat("X", 1000)
	a.ServeHTTP(httptest.NewRecorder(), req)
	if l := buf.String(); strings.Contains(l, strings.Repeat("X", 129)) || !strings.Contains(l, "method="+strings.Repeat("X", 128)+"… route=<unmatched> status=405") {
		t.Fatalf("log: %s", l)
	}
}

// The access log records the status the client was sent: a response nothing
// was written to is net/http's 200; a panic that passes through it aborts
// the connection, and is logged aborted with the status written before it,
// 500 when none was, and goes on unchanged; a Recover inside answers 500,
// logged as any status.
func TestAccessLogRecordsTheStatusSent(t *testing.T) {
	answer := func(f func(http.ResponseWriter)) geta.Middleware {
		return geta.Use(func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { f(w) })
		})
	}
	boom := errors.New("boom")
	for _, tc := range []struct {
		name    string
		mw      []geta.Middleware
		want    string
		aborted bool
		panics  any
	}{
		{"nothing written", []geta.Middleware{answer(func(http.ResponseWriter) {})}, "status=200", false, nil},
		{"panic before a status", []geta.Middleware{answer(func(http.ResponseWriter) { panic(boom) })}, "status=500", true, boom},
		{"abort after a status", []geta.Middleware{answer(func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusAccepted)
			panic(http.ErrAbortHandler)
		})}, "status=202", true, http.ErrAbortHandler},
		{"recovered", []geta.Middleware{geta.Recover(quietLogger()), answer(func(http.ResponseWriter) { panic(boom) })}, "status=500", false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log, buf := logger()
			a := accepts(t, withRoot(one("/x", get(okHandler)), append([]geta.Middleware{geta.AccessLog(log)}, tc.mw...)...))
			var got any
			func() {
				defer func() { got = recover() }()
				a.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
			}()
			if got != tc.panics {
				t.Fatalf("panic %v, want %v", got, tc.panics)
			}
			line := buf.String()
			if !strings.Contains(line, tc.want) || strings.Contains(line, "aborted=true") != tc.aborted {
				t.Fatalf("log line: %s", line)
			}
		})
	}
}

func TestAccessLogMarksAStream(t *testing.T) {
	log, buf := logger()
	flushing := geta.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, ": hi\n\n")
			http.NewResponseController(w).Flush()
		})
	})
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.AccessLog(log), flushing))
	do(t, a, "GET", "/x")
	if !strings.Contains(buf.String(), "streaming=true") {
		t.Fatal(buf.String())
	}
	buf2 := &syncBuffer{}
	plain := accepts(t, withRoot(one("/x", get(okHandler)), geta.AccessLog(slog.New(slog.NewTextHandler(buf2, nil)))))
	do(t, plain, "GET", "/x")
	if strings.Contains(buf2.String(), "streaming") || strings.Contains(buf2.String(), "upgrade") {
		t.Fatal(buf2.String())
	}
}
