package geta_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/koji-1009/geta"
)

// The request path, in process: GET /users/{id} answers a map lookup as JSON,
// POST /users binds and validates a JSON body and answers with a header.

type benchUser struct {
	ID   string   `json:"id" schema:"minLength=1,maxLength=64"`
	Name string   `json:"name" schema:"minLength=1,maxLength=100"`
	Age  int      `json:"age" schema:"minimum=0,maximum=150"`
	Tags []string `json:"tags" schema:"maxItems=16"`
}

type benchGetIn struct {
	ID string `path:"id" schema:"maxLength=64"`
}

type benchPostIn struct {
	Body benchUser `body:"json"`
}

type benchCreated struct {
	Location string `header:"Location"`
}

var errBenchMissing = errors.New("not found")

func benchApp(b *testing.B) http.Handler {
	b.Helper()
	var mu sync.RWMutex
	users := map[string]benchUser{"seed": {ID: "seed", Name: "Ada", Age: 36, Tags: []string{"x", "y"}}}
	get := func(ctx context.Context, in *benchGetIn) (*benchUser, error) {
		mu.RLock()
		defer mu.RUnlock()
		u, ok := users[in.ID]
		if !ok {
			return nil, errBenchMissing
		}
		return &u, nil
	}
	post := func(ctx context.Context, in *benchPostIn) (*benchCreated, error) {
		mu.Lock()
		users[in.Body.ID] = in.Body
		mu.Unlock()
		return &benchCreated{Location: "/users/" + in.Body.ID}, nil
	}
	a, err := geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/users/{id}", Route: geta.Route{Get: geta.Op(http.StatusOK, get,
			geta.Doc{Failures: []geta.Failure{geta.On(errBenchMissing, http.StatusNotFound, "not found")}})}},
		{Path: "/users", Route: geta.Route{Post: geta.Op(http.StatusCreated, post, geta.Doc{})}},
	}})
	if err != nil {
		b.Fatal(err)
	}
	return a
}

func BenchmarkGet(b *testing.B) {
	h := benchApp(b)
	b.ReportAllocs()
	for b.Loop() {
		req := httptest.NewRequest(http.MethodGet, "/users/seed", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatal(rec.Code, rec.Body)
		}
	}
}

func BenchmarkPost(b *testing.B) {
	h := benchApp(b)
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		i++
		body := `{"id":"b` + strconv.Itoa(i) + `","name":"Ada","age":36,"tags":["x","y"]}`
		req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			b.Fatal(rec.Code, rec.Body)
		}
	}
}

// A body with sealed types: a required one, one in a slice, and an optional
// one nested in a variant.

type benchShape interface{ isBenchShape() }

type benchCircle struct {
	Kind   string  `json:"kind"`
	Radius float64 `json:"radius" schema:"minimum=0"`
}

type benchSquare struct {
	Kind  string     `json:"kind"`
	Side  int        `json:"side" schema:"minimum=0"`
	Inner benchShape `json:"inner,omitzero"`
}

func (benchCircle) isBenchShape() {}
func (benchSquare) isBenchShape() {}

var benchShapes = geta.Sealed[benchShape]("kind", geta.Case[benchCircle]("circle"), geta.Case[benchSquare]("square"))

type benchDrawing struct {
	ID     string       `json:"id" schema:"minLength=1,maxLength=64"`
	Main   benchShape   `json:"main"`
	Others []benchShape `json:"others" schema:"maxItems=16"`
}

type benchDrawingIn struct {
	Body benchDrawing `body:"json"`
}

func benchSealedApp(b *testing.B) http.Handler {
	b.Helper()
	post := func(ctx context.Context, in *benchDrawingIn) (*benchCreated, error) {
		return &benchCreated{Location: "/drawings/" + in.Body.ID}, nil
	}
	a, err := geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/drawings", Route: geta.Route{Post: geta.Op(http.StatusCreated, post, geta.Doc{})}},
	}}, geta.WithUnion(benchShapes))
	if err != nil {
		b.Fatal(err)
	}
	return a
}

// BenchmarkPostSealed measures a valid body with four sealed values, the
// discriminator first in each, as geta writes it.
func BenchmarkPostSealed(b *testing.B) {
	h := benchSealedApp(b)
	b.ReportAllocs()
	const body = `{"id":"d1","main":{"kind":"square","side":2,"inner":{"kind":"circle","radius":1.5}},` +
		`"others":[{"kind":"circle","radius":3},{"kind":"square","side":4}]}`
	for b.Loop() {
		req := httptest.NewRequest(http.MethodPost, "/drawings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			b.Fatal(rec.Code, rec.Body)
		}
	}
}

// BenchmarkPostSealedLate is BenchmarkPostSealed with each discriminator
// written last, so that each sealed value is read ahead.
func BenchmarkPostSealedLate(b *testing.B) {
	h := benchSealedApp(b)
	b.ReportAllocs()
	const body = `{"id":"d1","main":{"side":2,"inner":{"radius":1.5,"kind":"circle"},"kind":"square"},` +
		`"others":[{"radius":3,"kind":"circle"},{"side":4,"kind":"square"}]}`
	for b.Loop() {
		req := httptest.NewRequest(http.MethodPost, "/drawings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			b.Fatal(rec.Code, rec.Body)
		}
	}
}

// BenchmarkLargeGet answers a 4 MiB body under the default limits, to a
// writer that keeps none of it: B/op is what the response itself holds.
func BenchmarkLargeGet(b *testing.B) {
	large := largeOf(4<<20, false)
	a, err := geta.New(one("/x", get(func(context.Context, *empty) (*largeList, error) { return large, nil })))
	if err != nil {
		b.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	d := &discardWriter{h: http.Header{}}
	b.ReportAllocs()
	for b.Loop() {
		clear(d.h)
		d.status, d.n = 0, 0
		a.ServeHTTP(d, req)
		if d.status != http.StatusOK || d.n < 4<<20 {
			b.Fatal(d.status, d.n)
		}
	}
	b.SetBytes(int64(d.n))
}

// BenchmarkGzipGet answers a body Gzip codes, to a writer that keeps none of
// it.
func BenchmarkGzipGet(b *testing.B) {
	a, err := geta.New(withRoot(one("/x", get(textHandler(big))), geta.Gzip()))
	if err != nil {
		b.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	d := &discardWriter{h: http.Header{}}
	b.ReportAllocs()
	for b.Loop() {
		clear(d.h)
		d.status, d.n = 0, 0
		a.ServeHTTP(d, req)
		if d.status != http.StatusOK || d.h.Get("Content-Encoding") != "gzip" {
			b.Fatal(d.status, d.h)
		}
	}
}

// BenchmarkRejectedPost measures a body that fails its contract in three ways.
func BenchmarkRejectedPost(b *testing.B) {
	h := benchApp(b)
	b.ReportAllocs()
	const body = `{"id":"","name":"Ada","age":200,"extra":1}`
	for b.Loop() {
		req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			b.Fatal(rec.Code, rec.Body)
		}
	}
}
