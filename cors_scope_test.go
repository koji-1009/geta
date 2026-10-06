package geta_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/koji-1009/geta"
)

// CORS answers preflights, and OPTIONS runs the root scope alone: below the
// root a CORS would never see one, so geta.New refuses it there.
func TestCORSBelowTheRootIsRefused(t *testing.T) {
	cors := geta.CORS(geta.AllowOrigins("*"))
	const why = "works only in the root scope: it answers preflights"
	rejects(t, one("/x", get(okHandler), geta.Scope{cors}), "/x: scope 0: middleware 0 (cors) "+why)
	rejects(t, one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Scope: geta.Scope{cors}})}),
		"GET /x: operation scope: middleware 0 (cors) "+why)
	rejects(t, one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{BeforeGate: geta.Scope{cors}})}),
		"GET /x: Doc.BeforeGate: middleware 0 (cors) "+why)
	// In the root scope it answers the preflight itself.
	a := accepts(t, withRoot(one("/x", geta.Route{Put: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error { return nil }, geta.Doc{})}), cors))
	r := do(t, a, "OPTIONS", "/x", "Origin", "https://a.example", "Access-Control-Request-Method", "PUT")
	if r.Code != 204 || r.Header().Get("Access-Control-Allow-Methods") != "PUT" || r.Header().Get("Allow") != "" {
		t.Fatal(r.Code, r.Header())
	}
}

// A QUERY preflight behind a bearer gate needs no credentials, and allows
// Authorization and Content-Type; the QUERY itself, refused, exposes its
// challenge and what its 415 names.
func TestCORSQueryPreflightBehindTheGate(t *testing.T) {
	a, err := geta.New(withRoot(one("/search", geta.Route{Query: geta.Op(http.StatusOK, querySearchHandler, geta.Doc{})}),
		geta.CORS(geta.AllowOrigins("https://a.example"), geta.PreflightMaxAge(90*time.Second)), gate()), geta.WithOpenAPI(geta.OpenAPI32))
	if err != nil {
		t.Fatal(err)
	}
	r := do(t, a, "OPTIONS", "/search", "Origin", "https://a.example", "Access-Control-Request-Method", "QUERY",
		"Access-Control-Request-Headers", "authorization,content-type")
	if r.Code != 204 || r.Header().Get("Access-Control-Allow-Methods") != "QUERY" || r.Header().Get("Access-Control-Max-Age") != "90" {
		t.Fatal(r.Code, r.Header())
	}
	sameHeaders(t, "preflight", r.Header().Get("Access-Control-Allow-Headers"), "Authorization", "Content-Type")
	r = do(t, a, "QUERY", "/search", "Origin", "https://a.example")
	if r.Code != 401 {
		t.Fatal(r.Code)
	}
	sameHeaders(t, "QUERY 401", r.Header().Get("Access-Control-Expose-Headers"), "Accept", "Accept-Query", "WWW-Authenticate")
}

// Access-Control-Max-Age carries whole seconds: a PreflightMaxAge that is not a whole
// positive number of them (a fraction cut, a sub-second age sent as 0, which
// caches nothing) is refused.
func TestCORSMaxAgeIsWholeSeconds(t *testing.T) {
	for _, d := range []time.Duration{500 * time.Millisecond, 90*time.Second + 500*time.Millisecond, 0, -time.Second} {
		rejects(t, withRoot(one("/x", get(okHandler)), geta.CORS(geta.AllowOrigins("*"), geta.PreflightMaxAge(d))),
			"geta.CORS: PreflightMaxAge "+d.String()+" is not a positive whole number of seconds")
	}
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.CORS(geta.AllowOrigins("*"), geta.PreflightMaxAge(time.Second))))
	if r := do(t, a, "OPTIONS", "/x", "Origin", "https://a.example", "Access-Control-Request-Method", "GET"); r.Header().Get("Access-Control-Max-Age") != "1" {
		t.Fatal(r.Code, r.Header())
	}
}

// An allowed origin is written as a browser sends it; one no Origin header
// could match is refused: a path, a trailing slash, upper case, user info,
// no scheme.
func TestCORSOriginsAreSerializedOrigins(t *testing.T) {
	for _, o := range []string{"https://app.example/", "https://app.example/x", "https://App.example", "https://u@app.example", "app.example", ""} {
		rejects(t, withRoot(one("/x", get(okHandler)), geta.CORS(geta.AllowOrigins(o))), fmt.Sprintf("AllowOrigins %q is not an origin", o))
	}
	accepts(t, withRoot(one("/x", get(okHandler)), geta.CORS(geta.AllowOrigins("https://app.example", "http://localhost:5173", "*"))))
}

// A flush before anything is written sends the header complete: the page
// may read what the operation declares, and the response varies on Origin.
func TestCORSCompletesAFlushedResponse(t *testing.T) {
	flush := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Vary", "Accept")
			http.NewResponseController(w).Flush()
			next.ServeHTTP(w, r)
		})
	})
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.CORS(geta.AllowOrigins("https://a.example"), geta.ExposeHeaders("X-Total")), flush))
	// The header as it went out with the flush, not as it was left.
	sent := do(t, a, "GET", "/x", "Origin", "https://a.example").Result().Header
	if sent.Get("Access-Control-Expose-Headers") != "X-Total" || !strings.Contains(strings.Join(sent.Values("Vary"), ","), "Origin") {
		t.Fatal(sent)
	}
}

// A rate limiter in the root scope runs inside CORS, so a preflight spends
// no token: after any number of preflights the request is admitted.
func TestCORSPreflightSpendsNoToken(t *testing.T) {
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.CORS(geta.AllowOrigins("*")), limit(1)))
	for range 3 {
		if r := do(t, a, "OPTIONS", "/x", "Access-Control-Request-Method", "GET"); r.Code != 204 {
			t.Fatal(r.Code)
		}
	}
	if r := do(t, a, "GET", "/x"); r.Code != 200 {
		t.Fatal(r.Code, r.Header())
	}
	if r := do(t, a, "GET", "/x"); r.Code != 429 {
		t.Fatal(r.Code)
	}
}

// CORS sits outside Recover, so the 500 a panic becomes still carries the
// headers that let the page read it.
func TestCORSHeadersOnARecovered500(t *testing.T) {
	boom := func(context.Context, *empty) (*ok, error) { panic("boom") }
	a := accepts(t, withRoot(one("/x", get(boom)), geta.CORS(geta.AllowOrigins("https://a.example")), geta.Recover(quietLogger())))
	r := do(t, a, "GET", "/x", "Origin", "https://a.example")
	if r.Code != 500 || r.Header().Get("Access-Control-Allow-Origin") != "https://a.example" || r.Header().Get("Vary") != "Origin" {
		t.Fatal(r.Code, r.Header())
	}
	if strings.Contains(r.Body.String(), "boom") {
		t.Fatal(r.Body.String())
	}
}
