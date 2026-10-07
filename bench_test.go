package geta_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

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

// The same two operations in net/http alone, with the same JSON package:
// plain trusts the request, and checked does by hand what benchApp's types
// and tags ask of geta (the path value's length, the media type, the body
// limit, unknown, missing, and out-of-range members, a problem for each
// refusal, and nosniff), as an application without geta would.

func benchNetHTTP(b *testing.B, checked bool) http.Handler {
	b.Helper()
	var mu sync.RWMutex
	users := map[string]benchUser{"seed": {ID: "seed", Name: "Ada", Age: 36, Tags: []string{"x", "y"}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if checked && utf8.RuneCountInString(id) > 64 {
			benchProblem(w, http.StatusBadRequest, "id is longer than 64 bytes")
			return
		}
		mu.RLock()
		u, ok := users[id]
		mu.RUnlock()
		if !ok {
			benchProblem(w, http.StatusNotFound, "not found")
			return
		}
		out, err := json.Marshal(u)
		if err != nil {
			benchProblem(w, http.StatusInternalServerError, "")
			return
		}
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("Content-Length", strconv.Itoa(len(out)))
		if checked {
			h.Set("X-Content-Type-Options", "nosniff")
		}
		w.WriteHeader(http.StatusOK)
		w.Write(out)
	})
	mux.HandleFunc("POST /users", func(w http.ResponseWriter, r *http.Request) {
		var u benchUser
		if checked {
			if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" && !(strings.HasPrefix(mt, "application/") && strings.HasSuffix(mt, "+json")) {
				w.Header().Set("Accept", "application/json")
				benchProblem(w, http.StatusUnsupportedMediaType, "Content-Type is not application/json")
				return
			}
			var in struct {
				ID   *string   `json:"id"`
				Name *string   `json:"name"`
				Age  *int      `json:"age"`
				Tags *[]string `json:"tags"`
			}
			if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 1<<20), &in, json.RejectUnknownMembers(true)); err != nil {
				benchProblem(w, http.StatusBadRequest, "the body is not a user")
				return
			}
			// Every violation is listed, as geta lists them.
			var bad []string
			if in.ID == nil || utf8.RuneCountInString(*in.ID) < 1 || utf8.RuneCountInString(*in.ID) > 64 {
				bad = append(bad, "id")
			}
			if in.Name == nil || utf8.RuneCountInString(*in.Name) < 1 || utf8.RuneCountInString(*in.Name) > 100 {
				bad = append(bad, "name")
			}
			if in.Age == nil || *in.Age < 0 || *in.Age > 150 {
				bad = append(bad, "age")
			}
			if in.Tags == nil || len(*in.Tags) > 16 {
				bad = append(bad, "tags")
			}
			if len(bad) > 0 {
				benchProblem(w, http.StatusBadRequest, strings.Join(bad, ", ")+" out of bounds")
				return
			}
			u = benchUser{ID: *in.ID, Name: *in.Name, Age: *in.Age, Tags: *in.Tags}
		} else if err := json.UnmarshalRead(r.Body, &u); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		users[u.ID] = u
		mu.Unlock()
		w.Header().Set("Location", "/users/"+u.ID)
		w.WriteHeader(http.StatusCreated)
	})
	return mux
}

// benchProblem writes an RFC 9457 problem, as the checked handlers would.
func benchProblem(w http.ResponseWriter, status int, detail string) {
	out, _ := json.Marshal(struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Status int    `json:"status"`
		Detail string `json:"detail,omitzero"`
	}{"about:blank", http.StatusText(status), status, detail})
	h := w.Header()
	h.Set("Content-Type", geta.ProblemContentType)
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	w.Write(out)
}

func BenchmarkGetNetHTTP(b *testing.B) {
	for _, checked := range []bool{false, true} {
		b.Run(map[bool]string{false: "plain", true: "checked"}[checked], func(b *testing.B) {
			h := benchNetHTTP(b, checked)
			b.ReportAllocs()
			for b.Loop() {
				req := httptest.NewRequest(http.MethodGet, "/users/seed", nil)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					b.Fatal(rec.Code, rec.Body)
				}
			}
		})
	}
}

func BenchmarkPostNetHTTP(b *testing.B) {
	for _, checked := range []bool{false, true} {
		b.Run(map[bool]string{false: "plain", true: "checked"}[checked], func(b *testing.B) {
			h := benchNetHTTP(b, checked)
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
		})
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

func benchSealedApp(b testing.TB) http.Handler {
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

// A nested body: an order of 20 line items, each with a product and its
// options (about 2.9 KB, 147 leaves).

type benchCustomer struct {
	Name  string `json:"name" schema:"minLength=1,maxLength=100"`
	Email string `json:"email" schema:"minLength=3,maxLength=254"`
}

type benchAddress struct {
	Line1      string `json:"line1" schema:"minLength=1,maxLength=200"`
	City       string `json:"city" schema:"minLength=1,maxLength=100"`
	PostalCode string `json:"postalCode" schema:"minLength=1,maxLength=16"`
	Country    string `json:"country" schema:"minLength=2,maxLength=2"`
}

type benchProduct struct {
	SKU     string   `json:"sku" schema:"minLength=1,maxLength=32"`
	Name    string   `json:"name" schema:"minLength=1,maxLength=100"`
	Price   float64  `json:"price" schema:"minimum=0,maximum=1000000"`
	Options []string `json:"options" schema:"maxItems=8,items.minLength=1,items.maxLength=32"`
}

type benchLineItem struct {
	Qty     int          `json:"qty" schema:"minimum=1,maximum=1000"`
	Product benchProduct `json:"product"`
}

type benchOrder struct {
	ID       string          `json:"id" schema:"minLength=1,maxLength=64"`
	Customer benchCustomer   `json:"customer"`
	Address  benchAddress    `json:"address"`
	Items    []benchLineItem `json:"items" schema:"minItems=1,maxItems=50"`
}

type benchOrderIn struct {
	Body benchOrder `body:"json"`
}

// A sealed body: a circle, a rectangle, or a polygon of points.

type benchFigure interface{ isBenchFigure() }

type benchDisc struct {
	Kind   string  `json:"kind"`
	Radius float64 `json:"radius" schema:"exclusiveMinimum=0,maximum=1000000"`
}

type benchRect struct {
	Kind   string  `json:"kind"`
	Width  float64 `json:"width" schema:"exclusiveMinimum=0,maximum=1000000"`
	Height float64 `json:"height" schema:"exclusiveMinimum=0,maximum=1000000"`
}

type benchPoint struct {
	X float64 `json:"x" schema:"minimum=-1000000,maximum=1000000"`
	Y float64 `json:"y" schema:"minimum=-1000000,maximum=1000000"`
}

type benchPolygon struct {
	Kind   string       `json:"kind"`
	Points []benchPoint `json:"points" schema:"minItems=3,maxItems=32"`
}

func (benchDisc) isBenchFigure()    {}
func (benchRect) isBenchFigure()    {}
func (benchPolygon) isBenchFigure() {}

var benchFigures = geta.Sealed[benchFigure]("kind",
	geta.Case[benchDisc]("circle"), geta.Case[benchRect]("rect"), geta.Case[benchPolygon]("polygon"))

type benchFigureIn struct {
	Body benchFigure `body:"json"`
}

func benchBodiesApp(b *testing.B) http.Handler {
	b.Helper()
	orders := func(ctx context.Context, in *benchOrderIn) (*benchCreated, error) {
		return &benchCreated{Location: "/orders/" + in.Body.ID}, nil
	}
	figures := func(ctx context.Context, in *benchFigureIn) (*benchCreated, error) {
		if _, ok := in.Body.(benchPolygon); !ok {
			return nil, errBenchMissing
		}
		return &benchCreated{Location: "/shapes/polygon"}, nil
	}
	a, err := geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/orders", Route: geta.Route{Post: geta.Op(http.StatusCreated, orders, geta.Doc{})}},
		{Path: "/shapes", Route: geta.Route{Post: geta.Op(http.StatusCreated, figures, geta.Doc{})}},
	}}, geta.WithUnion(benchFigures))
	if err != nil {
		b.Fatal(err)
	}
	return a
}

// benchOrderJSON is an order of n line items.
func benchOrderJSON(id string, n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"id":%q,"customer":{"name":"Ada Lovelace","email":"ada@example.com"},`+
		`"address":{"line1":"12 St James's Square","city":"London","postalCode":"SW1Y 4JH","country":"GB"},"items":[`, id)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"qty":%d,"product":{"sku":"SKU-%06d","name":"Stainless steel bottle 750ml","price":%d.99,"options":["blue","engraved","gift-wrap"]}}`,
			i%5+1, 1000+i, 10+i)
	}
	b.WriteString(`]}`)
	return b.String()
}

// benchPolygonJSON is a polygon of n points with its discriminator first or
// last.
func benchPolygonJSON(n int, kindFirst bool) string {
	var pts strings.Builder
	for i := range n {
		if i > 0 {
			pts.WriteByte(',')
		}
		fmt.Fprintf(&pts, `{"x":%d.5,"y":-%d.25}`, i*10, i*7)
	}
	if kindFirst {
		return `{"kind":"polygon","points":[` + pts.String() + `]}`
	}
	return `{"points":[` + pts.String() + `],"kind":"polygon"}`
}

// benchBodies posts bodies in turn to path.
func benchBodies(b *testing.B, h http.Handler, path string, bodies []string) {
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(bodies[i%len(bodies)]))
		req.Header.Set("Content-Type", "application/json")
		i++
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			b.Fatal(rec.Code, rec.Body)
		}
	}
}

// BenchmarkOrders measures a valid 20-item order (about 2.9 KB), with ids
// that differ between requests.
func BenchmarkOrders(b *testing.B) {
	h := benchBodiesApp(b)
	bodies := make([]string, 1024)
	for i := range bodies {
		bodies[i] = benchOrderJSON("o"+strconv.Itoa(i), 20)
	}
	benchBodies(b, h, "/orders", bodies)
}

// BenchmarkShapes measures a sealed body, a polygon of 8 points (about 200
// bytes), with its discriminator first and last.
func BenchmarkShapes(b *testing.B) {
	h := benchBodiesApp(b)
	b.Run("first", func(b *testing.B) { benchBodies(b, h, "/shapes", []string{benchPolygonJSON(8, true)}) })
	b.Run("last", func(b *testing.B) { benchBodies(b, h, "/shapes", []string{benchPolygonJSON(8, false)}) })
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
