package geta_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
)

func TestCORSWildcard(t *testing.T) {
	a := accepts(t, withRoot(one("/x", get(okHandler)),
		geta.CORS(geta.AllowOrigins("*"), geta.AllowMethods("GET", "PUT"), geta.AllowHeaders("x-custom"))))
	r := do(t, a, "GET", "/x", header("Origin", "https://any.example")...)
	if r.Header().Get("Access-Control-Allow-Origin") != "*" || r.Header().Get("Access-Control-Allow-Methods") != "" || r.Header().Get("Vary") != "" {
		t.Fatal(r.Header())
	}
	if r := do(t, a, "GET", "/x"); r.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal(r.Header())
	}
	pre := do(t, a, "OPTIONS", "/x", header("Access-Control-Request-Method", "PUT")...)
	if pre.Code != 204 || pre.Header().Get("Access-Control-Allow-Methods") != "GET, PUT" || pre.Header().Get("Access-Control-Allow-Headers") != "x-custom" {
		t.Fatal(pre.Code, pre.Header())
	}
}

func TestCORSSpecificOriginVaries(t *testing.T) {
	setVary := func(v string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Vary", v)
				next.ServeHTTP(w, r)
			})
		}
	}
	for _, c := range []struct {
		origin, existing string
		allowed          bool
		vary             []string
	}{
		{"https://example.com", "Origin", true, []string{"Origin"}},
		{"https://example.com", "Accept-Encoding", true, []string{"Accept-Encoding", "Origin"}},
		{"https://example.com", "origin", true, []string{"origin"}},
		{"https://evil.example", "", false, []string{"Origin"}},
		{"https://evil.example", "Accept-Encoding", false, []string{"Accept-Encoding", "Origin"}},
		{"", "", false, []string{"Origin"}},
	} {
		root := []geta.Middleware{geta.CORS(geta.AllowOrigins("https://example.com"), geta.AllowCredentials())}
		if c.existing != "" {
			root = append(root, geta.Use(setVary(c.existing)))
		}
		r := do(t, accepts(t, withRoot(one("/x", get(okHandler)), root...)), "GET", "/x", header("Origin", c.origin)...)
		acao := r.Header().Get("Access-Control-Allow-Origin")
		if (acao == c.origin && c.allowed) != c.allowed || (!c.allowed && acao != "") {
			t.Errorf("%+v: ACAO %q", c, acao)
		}
		if c.allowed && r.Header().Get("Access-Control-Allow-Credentials") != "true" {
			t.Errorf("%+v: no credentials header", c)
		}
		if got := r.Header().Values("Vary"); strings.Join(got, "|") != strings.Join(c.vary, "|") {
			t.Errorf("%+v: Vary %q", c, got)
		}
	}
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.CORS(geta.AllowOrigins("https://example.com"))))
	pre := do(t, a, "OPTIONS", "/x", "Origin", "https://evil.example", "Access-Control-Request-Method", "GET")
	if pre.Code != 204 || pre.Header().Get("Access-Control-Allow-Origin") != "" || pre.Header().Get("Vary") != "Origin" {
		t.Fatal(pre.Code, pre.Header())
	}
}

// CORS: what a page may send and read is what the operation declares.

type corsPutIn struct {
	geta.Conditional
	Trace *string `header:"X-Trace-Id"`
	Body  *text   `body:"json"`
}

type corsKeyIn struct{}

func corsApp(t *testing.T, opts ...geta.CORSOption) *geta.App {
	t.Helper()
	key := geta.APIKeyHeader("key", "X-API-Key")
	res := &gzcResource{tag: `"v"`}
	policy := geta.Policy{Default: []geta.Scheme{geta.Bearer}, Verifiers: map[string]geta.Verifier{
		geta.Bearer.Name: func(r *http.Request) (context.Context, error) {
			if r.Header.Get("Authorization") != "Bearer t" {
				return nil, geta.ErrUnauthenticated
			}
			return nil, nil
		},
		key.Name: func(r *http.Request) (context.Context, error) {
			if r.Header.Get("X-API-Key") != "k" {
				return nil, geta.ErrUnauthenticated
			}
			return nil, nil
		},
	}}
	return accepts(t, geta.Table{
		Root: geta.Scope{
			geta.CORS(append([]geta.CORSOption{geta.AllowOrigins("https://a.example")}, opts...)...),
			geta.Gzip(), geta.ETag(), geta.Secure(policy),
		},
		Routes: []geta.Entry{
			{Path: "/r", Route: geta.Route{
				Get: geta.Op(http.StatusOK, res.get, geta.Doc{}),
				Put: geta.OpNoBody(http.StatusNoContent, func(ctx context.Context, in *corsPutIn) error {
					return res.put(ctx, &condIn{in.Conditional})
				}, geta.Doc{}),
			}},
			{Path: "/key", Route: geta.Route{
				Get: geta.Op(http.StatusOK, func(context.Context, *corsKeyIn) (*ok, error) { return &ok{true}, nil },
					geta.Doc{Security: []geta.Scheme{key}}),
			}},
			{Path: "/open", Route: geta.Route{
				Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}}),
			}},
		},
	})
}

// headerSet reads a comma-separated header list, its names lowercased.
func headerSet(v string) []string {
	var names []string
	for n := range strings.SplitSeq(v, ",") {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return names
}

func sameHeaders(t *testing.T, what, got string, want ...string) {
	t.Helper()
	for i := range want {
		want[i] = strings.ToLower(want[i])
	}
	slices.Sort(want)
	if g := headerSet(got); !slices.Equal(g, want) {
		t.Errorf("%s: %q; want %q", what, g, want)
	}
}

func preflight(t *testing.T, a *geta.App, origin, method, path string) http.Header {
	t.Helper()
	r := do(t, a, "OPTIONS", path, "Origin", origin, "Access-Control-Request-Method", method,
		"Access-Control-Request-Headers", "authorization,content-type,if-match")
	if r.Code != http.StatusNoContent {
		t.Fatalf("preflight %s %s: %d", method, path, r.Code)
	}
	return r.Header()
}

// A preflight allows the request headers of the operation it asks about.
func TestCORSPreflightAllowsTheOperationsHeaders(t *testing.T) {
	a := corsApp(t)
	h := preflight(t, a, "https://a.example", "PUT", "/r")
	if h.Get("Access-Control-Allow-Origin") != "https://a.example" || !strings.Contains(h.Get("Access-Control-Allow-Methods"), "PUT") {
		t.Fatal(h)
	}
	sameHeaders(t, "PUT /r", h.Get("Access-Control-Allow-Headers"),
		"If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "X-Trace-Id", "Authorization", "Content-Type")
	sameHeaders(t, "GET /r", preflight(t, a, "https://a.example", "GET", "/r").Get("Access-Control-Allow-Headers"),
		"If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "Authorization")
	sameHeaders(t, "HEAD /r", preflight(t, a, "https://a.example", "HEAD", "/r").Get("Access-Control-Allow-Headers"),
		"If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "Authorization")
	sameHeaders(t, "GET /key", preflight(t, a, "https://a.example", "GET", "/key").Get("Access-Control-Allow-Headers"), "X-API-Key")
	sameHeaders(t, "GET /open", preflight(t, a, "https://a.example", "GET", "/open").Get("Access-Control-Allow-Headers"))
	// No operation: nothing derived.
	sameHeaders(t, "DELETE /r", preflight(t, a, "https://a.example", "DELETE", "/r").Get("Access-Control-Allow-Headers"))
	sameHeaders(t, "GET /nope", preflight(t, a, "https://a.example", "GET", "/nope").Get("Access-Control-Allow-Headers"))
	// An origin not allowed is told nothing.
	if h := preflight(t, a, "https://b.example", "PUT", "/r"); h.Get("Access-Control-Allow-Headers") != "" || h.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal(h)
	}
	// AllowHeaders adds to what is derived, each name once.
	a = corsApp(t, geta.AllowHeaders("X-Extra", "authorization"))
	sameHeaders(t, "GET /r with AllowHeaders", preflight(t, a, "https://a.example", "GET", "/r").Get("Access-Control-Allow-Headers"),
		"If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "Authorization", "X-Extra")
	sameHeaders(t, "GET /nope with AllowHeaders", preflight(t, a, "https://a.example", "GET", "/nope").Get("Access-Control-Allow-Headers"), "X-Extra", "authorization")
}

// A response lets the page read the response headers of the operation that
// answered it.
func TestCORSExposesTheOperationsHeaders(t *testing.T) {
	a := corsApp(t, geta.ExposeHeaders("X-Request-Id"))
	r := do(t, a, "GET", "/r", "Origin", "https://a.example", "Authorization", "Bearer t")
	if r.Code != 200 || r.Header().Get("ETag") != `"v"` {
		t.Fatal(r.Code, r.Header())
	}
	sameHeaders(t, "GET /r", r.Header().Get("Access-Control-Expose-Headers"), "ETag", "WWW-Authenticate", "X-Request-Id")
	if !slices.Contains(r.Header().Values("Vary"), "Origin") {
		t.Fatal(r.Header())
	}
	// The 401 a gate answers exposes its challenge.
	r = do(t, a, "GET", "/r", "Origin", "https://a.example")
	if r.Code != 401 || r.Header().Get("WWW-Authenticate") == "" {
		t.Fatal(r.Code, r.Header())
	}
	sameHeaders(t, "GET /r 401", r.Header().Get("Access-Control-Expose-Headers"), "ETag", "WWW-Authenticate", "X-Request-Id")
	// An operation with a body exposes the Accept its 415 names.
	r = do(t, a, "PUT", "/r", "Origin", "https://a.example", "Authorization", "Bearer t")
	if !slices.Contains(headerSet(r.Header().Get("Access-Control-Expose-Headers")), "accept") {
		t.Fatal(r.Header())
	}
	r = do(t, a, "GET", "/open", "Origin", "https://a.example")
	sameHeaders(t, "GET /open", r.Header().Get("Access-Control-Expose-Headers"), "ETag", "X-Request-Id")
	r = do(t, a, "GET", "/nope", "Origin", "https://a.example")
	sameHeaders(t, "GET /nope", r.Header().Get("Access-Control-Expose-Headers"), "X-Request-Id")
	if r := do(t, a, "GET", "/r", "Origin", "https://b.example", "Authorization", "Bearer t"); r.Header().Get("Access-Control-Expose-Headers") != "" {
		t.Fatal(r.Header())
	}
}

// A page on another origin fetches, reads the tag, and writes with it.
func TestCORSConditionalWriteAcrossOrigins(t *testing.T) {
	a := corsApp(t)
	origin := "https://a.example"
	r := do(t, a, "GET", "/r", "Origin", origin, "Authorization", "Bearer t", "Accept-Encoding", "gzip")
	tag := r.Header().Get("ETag")
	if r.Code != 200 || tag != `"v-gzip"` || !slices.Contains(headerSet(r.Header().Get("Access-Control-Expose-Headers")), "etag") {
		t.Fatal(r.Code, r.Header())
	}
	h := preflight(t, a, origin, "PUT", "/r")
	for _, want := range []string{"if-match", "authorization", "content-type"} {
		if !slices.Contains(headerSet(h.Get("Access-Control-Allow-Headers")), want) {
			t.Fatalf("preflight lacks %s: %v", want, h)
		}
	}
	put := func(ifMatch string) int {
		req := do(t, a, "PUT", "/r", "Origin", origin, "Authorization", "Bearer t", "If-Match", ifMatch)
		if req.Header().Get("Access-Control-Allow-Origin") != origin {
			t.Fatal(req.Header())
		}
		return req.Code
	}
	if c := put(tag); c != 204 {
		t.Fatalf("PUT with the tag read: %d", c)
	}
	if c := put(tag); c != 412 {
		t.Fatalf("PUT with a stale tag: %d", c)
	}
}
