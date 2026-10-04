package geta_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/koji-1009/geta"
)

// The helpers the package's tests share: tables, requests, the document, and
// test doubles.

type empty struct{}

type ok struct {
	OK bool `json:"ok"`
}

func okHandler(ctx context.Context, _ *empty) (*ok, error) { return &ok{true}, nil }

func noop(next http.Handler) http.Handler { return next }

func one(path string, r geta.Route, scopes ...geta.Scope) geta.Table {
	return geta.Table{Routes: []geta.Entry{{Path: path, Route: r, Scopes: scopes}}}
}

func get[In, Out any](h func(context.Context, *In) (*Out, error)) geta.Route {
	return geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{})}
}

func withRoot(tbl geta.Table, root ...geta.Middleware) geta.Table {
	tbl.Root = root
	return tbl
}

// rejects asserts that New refuses t with an error containing every
// fragment.
func rejects(t *testing.T, tbl geta.Table, fragments ...string) {
	t.Helper()
	_, err := geta.New(tbl)
	if err == nil {
		t.Fatalf("geta.New accepted the table; want an error containing %q", fragments)
	}
	for _, f := range fragments {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("error lacks %q:\n%v", f, err)
		}
	}
}

func accepts(t *testing.T, tbl geta.Table) *geta.App {
	t.Helper()
	a, err := geta.New(tbl)
	if err != nil {
		t.Fatalf("geta.New: %v", err)
	}
	return a
}

type idIn struct {
	ID int64 `path:"id"`
}

func idHandler(ctx context.Context, in *idIn) (*ok, error) { return &ok{true}, nil }

type text struct {
	Text string `json:"text"`
}

func textHandler(s string) func(context.Context, *empty) (*text, error) {
	return func(context.Context, *empty) (*text, error) { return &text{s}, nil }
}

// do runs one request against an app built from tbl.
func do(t *testing.T, a *geta.App, method, path string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Add(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	return rec
}

func header(k, v string) []string { return []string{k, v} }

func itoa(n int) string { return strconv.Itoa(n) }

func doc(t *testing.T, a *geta.App) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(a.OpenAPI(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func at(t *testing.T, v any, keys ...string) any {
	t.Helper()
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%v is not an object at %q", v, k)
		}
		v, ok = m[k]
		if !ok {
			t.Fatalf("no %q in %v", k, m)
		}
	}
	return v
}

func compact(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// syncBuffer is a log destination safe to write from several goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func logger() (*slog.Logger, *syncBuffer) {
	var buf syncBuffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// recorder is a testing.TB that records failures instead of failing.
type recorder struct {
	testing.TB
	mu   sync.Mutex
	errs []string
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}
