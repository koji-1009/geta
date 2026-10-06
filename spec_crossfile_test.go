package geta_test

import (
	"context"
	"encoding"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaclient"
	"github.com/koji-1009/geta/getatest"
)

// Rules of the spec that no other test asserts as the rule states them.

// HEAD is served by the GET operation and documented by it alone: the
// document has no head operation, and App.Operations and App.Types know
// none.
func TestTheDocumentListsNoHeadOperation(t *testing.T) {
	a := accepts(t, one("/x", get(okHandler)))
	if rec := do(t, a, http.MethodHead, "/x"); rec.Code != http.StatusOK {
		t.Fatalf("HEAD /x: %d", rec.Code)
	}
	item := at(t, doc(t, a), "paths", "/x").(map[string]any)
	if _, has := item["head"]; has {
		t.Fatalf("the document lists a head operation: %v", item)
	}
	if _, has := item["get"]; !has || len(item) != 2 {
		t.Fatalf("path item %v; want get and options alone", item)
	}
	for _, op := range a.Operations() {
		if strings.HasPrefix(op, http.MethodHead+" ") {
			t.Fatalf("Operations lists %s", op)
		}
	}
	if _, _, ok := a.Types(http.MethodHead, "/x"); ok {
		t.Fatal("Types knows a HEAD operation")
	}
}

// getatest holds a body to the document when its Content-Type begins with
// application/json, parameters included, and not only when it is exactly
// that media type.
func TestGetatestChecksAJSONBodyWhoseContentTypeHasParameters(t *testing.T) {
	answer := geta.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			io.WriteString(w, `{"ok":"no"}`)
		})
	})
	rec := &recorder{TB: t}
	getatest.New(rec, withRoot(one("/x", get(okHandler)), answer)).Get("/x")
	if len(rec.errs) != 1 || !strings.Contains(rec.errs[0], "$.ok") {
		t.Fatalf("%q", rec.errs)
	}
}

// After a root rewrite, an operation's Doc.Scope reads r.Pattern and the
// path values of the operation that serves the rewritten request alone.
func TestDocScopeReadsThePathValuesOfTheRewrittenRequest(t *testing.T) {
	var mu sync.Mutex
	var seen string
	look := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen = r.Pattern + " id=" + r.PathValue("id") + " other=" + r.PathValue("other")
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	})
	other := func(context.Context, *struct {
		Other string `path:"other"`
	}) (*ok, error) {
		return &ok{true}, nil
	}
	a := accepts(t, geta.Table{Root: geta.Scope{pathMove}, Routes: []geta.Entry{
		{Path: "/alias/{other}", Route: get(other)},
		{Path: "/users/{id}", Route: geta.Route{Get: geta.Op(http.StatusOK, idHandler, geta.Doc{Scope: geta.Scope{look}})}},
	}})
	if rec := do(t, a, http.MethodGet, "/alias/q", "X-Move", "/users/7"); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if seen != "GET /users/{id} id=7 other=" {
		t.Fatalf("Doc.Scope read %q", seen)
	}
}

// A literal holding only a closing brace is refused, naming the path; the
// path / alone is the root path.
func TestALiteralWithAClosingBraceIsRefused(t *testing.T) {
	rejects(t, one("/a}b", get(okHandler)), "/a}b")
	if rec := do(t, accepts(t, one("/", get(okHandler))), http.MethodGet, "/"); rec.Code != http.StatusOK {
		t.Fatalf("GET /: %d", rec.Code)
	}
}

type crossFilter struct {
	Min *int `form:"min"`
}

// An operation whose only query parameter is a deepObject reads the query
// string: one net/url refuses is the malformed-query 400.
func TestAMalformedQueryBesideADeepObjectIs400(t *testing.T) {
	h := func(context.Context, *struct {
		F *crossFilter `query:"f"`
	}) (*ok, error) {
		return &ok{true}, nil
	}
	c := getatest.New(t, one("/x", get(h)))
	res := c.Get("/x?f[min]=%zz")
	if res.Status != http.StatusBadRequest || res.Problem().Detail != "the query string is malformed" || len(res.Problem().Errors) != 0 {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
}

type crossRawIn struct {
	Body []byte `body:"text/plain"`
}

// A raw body in a content coding is the coding's 415: its detail names the
// codings, it carries Accept-Encoding: identity and neither Accept nor
// Accept-Patch, and the handler does not run; past MaxBodyBytes too, for a
// raw and a multipart body alike.
func TestCodedRawAndMultipartContentIsTheCodings415(t *testing.T) {
	ran := false
	h := func(_ context.Context, in *crossRawIn) (*ok, error) {
		ran = true
		return &ok{true}, nil
	}
	limits := geta.DefaultLimits
	limits.MaxBodyBytes = 8
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/r", Route: geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{}), Patch: geta.Op(http.StatusOK, h, geta.Doc{})}},
		{Path: "/m", Route: geta.Route{Post: geta.Op(http.StatusOK, echoUpload, geta.Doc{})}},
	}}, geta.WithLimits(limits))
	coded := c.With("Content-Encoding", "gzip").With("Content-Type", "text/plain")
	const detail = `Content-Encoding "gzip" is not supported`
	refusedCoding(t, coded.Post("/r", "abc"), detail)
	refusedCoding(t, coded.Patch("/r", "abc"), detail)
	refusedCoding(t, coded.Post("/r", strings.Repeat("x", 100)), detail)
	refusedCoding(t, c.With("Content-Encoding", "gzip").Multipart(http.MethodPost, "/m", url.Values{"title": {"t"}},
		getatest.FilePart{Field: "avatar", Filename: "a", Content: []byte(strings.Repeat("x", 100))}), detail)
	if ran {
		t.Fatal("the handler ran on coded content")
	}
}

// ownJSONEmbed has JSON methods of its own.
type ownJSONEmbed struct{ A int }

func (ownJSONEmbed) MarshalJSON() ([]byte, error) { return []byte(`1`), nil }
func (*ownJSONEmbed) UnmarshalJSON([]byte) error  { return nil }

// ownJSONEmbed2 has JSON methods of its own too.
type ownJSONEmbed2 struct{ B int }

func (ownJSONEmbed2) MarshalJSON() ([]byte, error) { return []byte(`2`), nil }
func (*ownJSONEmbed2) UnmarshalJSON([]byte) error  { return nil }

// A lone untagged embedded type with JSON methods of its own gives them to
// the embedding struct, which Go promotes them to, so the struct is written
// by them; two such, whose methods Go promotes neither of, are refused.
func TestAnEmbeddedTypeWithJSONMethodsIsRefused(t *testing.T) {
	type lone struct {
		ownJSONEmbed
		N int `json:"n"`
	}
	if rec := do(t, accepts(t, one("/x", get(func(context.Context, *empty) (*lone, error) { return &lone{N: 5}, nil }))), http.MethodGet, "/x"); rec.Body.String() != "1" {
		t.Fatalf("lone: %s", rec.Body)
	}
	type two struct {
		ownJSONEmbed
		ownJSONEmbed2
		N int `json:"n"`
	}
	_, _ = two{}.ownJSONEmbed, two{}.ownJSONEmbed2 // read by geta.New through reflection
	rejects(t, one("/x", get(func(context.Context, *empty) (*two, error) { return nil, nil })), "has its own JSON or text methods")
}

// format= on a type that carries a format, a format type included, is
// refused.
func TestAFormatTagOnAFormatTypeIsRefused(t *testing.T) {
	h := func(context.Context, *struct {
		D geta.Date `query:"d" schema:"format=date"`
	}) (*ok, error) {
		return &ok{true}, nil
	}
	rejects(t, one("/x", get(h)), `the type already has format "date"`)
}

// ownString is a string with JSON methods of its own.
type ownString struct{ s string }

func (o ownString) MarshalJSON() ([]byte, error) { return []byte(strconv.Quote(o.s)), nil }
func (o *ownString) UnmarshalJSON(b []byte) error {
	s, err := strconv.Unquote(string(b))
	o.s = s
	return err
}

// The pattern ceiling and the backstops a request's value is refused by: a
// form field's maxLength past the ceiling beside a pattern, whatever the
// Limits; a default longer than MaxStringLength on a string with no
// maxLength; and an enum member a WithSchema declaration a request reaches
// gives, longer than the ceiling beside a pattern, whatever the Limits.
func TestCeilingAndBackstopRefusalsOfARequestsValue(t *testing.T) {
	wide := geta.DefaultLimits
	wide.MaxStringLength = 100000
	form := func(context.Context, *struct {
		Body struct {
			S string `form:"s" schema:"pattern=^a+$,maxLength=5000"`
		} `body:"form"`
	}) (*ok, error) {
		return &ok{true}, nil
	}
	if _, err := geta.New(one("/x", geta.Route{Post: geta.Op(http.StatusOK, form, geta.Doc{})}), geta.WithLimits(wide)); err == nil {
		t.Error("a form field's maxLength past the ceiling beside a pattern was accepted")
	}
	minimum := func(context.Context, *struct {
		S string `query:"s" schema:"pattern=^a+$,minLength=5000"`
	}) (*ok, error) {
		return &ok{true}, nil
	}
	if _, err := geta.New(one("/x", get(minimum)), geta.WithLimits(wide)); err == nil {
		t.Error("a minLength past the ceiling beside a pattern was accepted")
	}
	narrow := geta.DefaultLimits
	narrow.MaxStringLength = 3
	long := func(context.Context, *struct {
		D string `query:"d" schema:"default=abcdef"`
	}) (*ok, error) {
		return &ok{true}, nil
	}
	if _, err := geta.New(one("/x", get(long)), geta.WithLimits(narrow)); err == nil {
		t.Error("a default past MaxStringLength was accepted")
	}
	bounded := func(context.Context, *struct {
		D string `query:"d" schema:"default=abcdef,maxLength=10"`
	}) (*ok, error) {
		return &ok{true}, nil
	}
	if _, err := geta.New(one("/x", get(bounded)), geta.WithLimits(narrow)); err != nil {
		t.Errorf("a default within a declared maxLength: %v", err)
	}
	body := func(context.Context, *struct {
		Body struct {
			V ownString `json:"v"`
		} `body:"json"`
	}) (*ok, error) {
		return &ok{true}, nil
	}
	tbl := one("/x", geta.Route{Post: geta.Op(http.StatusOK, body, geta.Doc{})})
	if _, err := geta.New(tbl, geta.WithLimits(wide), geta.WithSchema[ownString]("string", "pattern=^a+$,enum="+strings.Repeat("a", 4097))); err == nil {
		t.Error("an enum member past the ceiling beside a pattern was accepted")
	}
	if _, err := geta.New(tbl, geta.WithLimits(wide), geta.WithSchema[ownString]("string", "pattern=^a+$,enum="+strings.Repeat("a", 4096))); err != nil {
		t.Errorf("an enum member at the ceiling: %v", err)
	}
}

// A float form field binds a JSON number and refuses other text.
func TestAFloatFormFieldBindsANumber(t *testing.T) {
	h := func(_ context.Context, in *struct {
		Body struct {
			F float64 `form:"f"`
		} `body:"form"`
	}) (*echoed, error) {
		return &echoed{Got: strconv.FormatFloat(in.Body.F, 'g', -1, 64)}, nil
	}
	c := getatest.New(t, one("/x", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}))
	if g := got(t, c.Form(http.MethodPost, "/x", url.Values{"f": {"1.5"}})); g != "1.5" {
		t.Fatal(g)
	}
	same(t, violations(t, c.Form(http.MethodPost, "/x", url.Values{"f": {"x"}})), "body $.f: expected number, got string")
}

// Files are held in memory while their total fits MaxMultipartMemory: each
// of three three-byte files fits alone, but the third passes the total and
// goes to a temporary file.
func TestMultipartMemoryBoundsTheTotal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	limits := geta.DefaultLimits
	limits.MaxMultipartMemory = 6
	held := -1
	h := func(ctx context.Context, in *uploadIn) (*echoed, error) {
		entries, _ := os.ReadDir(dir)
		held = len(entries)
		return echoUpload(ctx, in)
	}
	c := getatest.New(t, one("/m", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}), geta.WithLimits(limits))
	got(t, c.Multipart(http.MethodPost, "/m", url.Values{"title": {"t"}},
		getatest.FilePart{Field: "avatar", Filename: "a", Content: []byte("abc")},
		getatest.FilePart{Field: "extra", Filename: "b", Content: []byte("def")},
		getatest.FilePart{Field: "extra", Filename: "c", Content: []byte("ghi")}))
	if held != 1 {
		t.Fatalf("%d files on disk; want 1", held)
	}
}

// The total's boundary: two files of MaxMultipartMemory bytes in all stay in
// memory, and one byte more sends the second to a temporary file.
func TestMultipartMemoryBoundary(t *testing.T) {
	for second, want := range map[string]int{"def": 0, "defg": 1} {
		dir := t.TempDir()
		t.Setenv("TMPDIR", dir)
		limits := geta.DefaultLimits
		limits.MaxMultipartMemory = 6
		held := -1
		h := func(ctx context.Context, in *uploadIn) (*echoed, error) {
			entries, _ := os.ReadDir(dir)
			held = len(entries)
			return echoUpload(ctx, in)
		}
		c := getatest.New(t, one("/m", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}), geta.WithLimits(limits))
		got(t, c.Multipart(http.MethodPost, "/m", url.Values{"title": {"t"}},
			getatest.FilePart{Field: "avatar", Filename: "a", Content: []byte("abc")},
			getatest.FilePart{Field: "extra", Filename: "b", Content: []byte(second)}))
		if held != want {
			t.Errorf("%d bytes in all: %d files on disk; want %d", 3+len(second), held, want)
		}
	}
}

// What an operation's Doc.Limits returns is refused as WithLimits is: a
// MaxBodyBytes, MaxStringLength, or MaxDepth below 1; MaxMultipartMemory
// and MaxResponseBuffer may be zero.
func TestDocLimitsAreRefusedAsWithLimitsIs(t *testing.T) {
	with := func(change func(*geta.Limits)) geta.Table {
		return one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Limits: func(l geta.Limits) geta.Limits {
			change(&l)
			return l
		}})})
	}
	for name, change := range map[string]func(*geta.Limits){
		"MaxBodyBytes":    func(l *geta.Limits) { l.MaxBodyBytes = 0 },
		"MaxStringLength": func(l *geta.Limits) { l.MaxStringLength = 0 },
		"MaxDepth":        func(l *geta.Limits) { l.MaxDepth = 0 },
	} {
		if _, err := geta.New(with(change)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s 0: %v", name, err)
		}
	}
	accepts(t, with(func(l *geta.Limits) { l.MaxMultipartMemory, l.MaxResponseBuffer = 0, 0 }))
}

// Each format type's UnmarshalText refuses text off its grammar with an
// error naming the format, as its ParseX does; ParseDateTime takes a lower
// case t and z.
func TestFormatTypesUnmarshalTextNamesTheFormat(t *testing.T) {
	for format, v := range map[string]encoding.TextUnmarshaler{
		"date-time":             new(geta.DateTime),
		"date":                  new(geta.Date),
		"time":                  new(geta.TimeOfDay),
		"duration":              new(geta.Duration),
		"ipv4":                  new(geta.IPv4),
		"ipv6":                  new(geta.IPv6),
		"json-pointer":          new(geta.JSONPointer),
		"relative-json-pointer": new(geta.RelativeJSONPointer),
	} {
		if err := v.UnmarshalText([]byte("x~")); err == nil || err.Error() != `"x~" is not a valid `+format {
			t.Errorf("%s: %v", format, err)
		}
	}
	if _, err := geta.ParseDate("x~"); err == nil || err.Error() != `"x~" is not a valid date` {
		t.Errorf("ParseDate: %v", err)
	}
	if _, err := geta.ParseDateTime("2024-01-02t03:04:05z"); err != nil {
		t.Errorf("lower case t and z: %v", err)
	}
}

// failingWriter is a connection that is gone: every Write fails.
type failingWriter struct {
	h      http.Header
	status int
}

func (f *failingWriter) Header() http.Header       { return f.h }
func (f *failingWriter) WriteHeader(code int)      { f.status = code }
func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("connection reset") }

// A 405 and a 400 geta answers itself that the connection does not take are
// logged at Info and the connection is aborted.
func TestGetasOwn405And400WriteFailuresAbort(t *testing.T) {
	q := func(context.Context, *struct {
		Q int `query:"q"`
	}) (*ok, error) {
		return &ok{true}, nil
	}
	for name, c := range map[string]struct {
		method string
		status int
	}{
		"405": {http.MethodPost, 405},
		"400": {http.MethodGet, 400},
	} {
		log, buf := logger()
		a, err := geta.New(one("/x", get(q)), geta.WithLogger(log))
		if err != nil {
			t.Fatal(err)
		}
		f := &failingWriter{h: http.Header{}}
		p := func() (p any) {
			defer func() { p = recover() }()
			a.ServeHTTP(f, httptest.NewRequest(c.method, "/x", nil))
			return nil
		}()
		l := buf.String()
		if p != http.ErrAbortHandler || f.status != c.status || !strings.Contains(l, "level=INFO") || !strings.Contains(l, "the response could not be written") {
			t.Errorf("%s: %v %d %s", name, p, f.status, l)
		}
	}
}

type cookieBadHeader struct {
	H    string       `header:"X-H"`
	C    *http.Cookie `cookie:"c"`
	Body ok           `body:"json"`
}

type cookieBadStatus struct {
	Status int          `status:"200"`
	C      *http.Cookie `cookie:"c"`
	Body   ok           `body:"json"`
}

// notJSON writes output that is not JSON.
type notJSON struct{}

func (notJSON) MarshalJSON() ([]byte, error) { return []byte("{"), nil }

type cookieBadBody struct {
	C    *http.Cookie `cookie:"c"`
	Body notJSON      `body:"json"`
}

// An envelope whose header, status field, or body fails leaves none of its
// cookies on the 500.
func TestAFailedEnvelopeLeavesNoCookie(t *testing.T) {
	ck := &http.Cookie{Name: "c", Value: "v"}
	a := accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/h", Route: get(func(context.Context, *empty) (*cookieBadHeader, error) {
			return &cookieBadHeader{H: "a\nb", C: ck}, nil
		})},
		{Path: "/s", Route: get(func(context.Context, *empty) (*cookieBadStatus, error) {
			return &cookieBadStatus{Status: 299, C: ck}, nil
		})},
		{Path: "/b", Route: get(func(context.Context, *empty) (*cookieBadBody, error) { return &cookieBadBody{C: ck}, nil })},
	}})
	for _, path := range []string{"/h", "/s", "/b"} {
		if rec := do(t, a, http.MethodGet, path); rec.Code != 500 || rec.Header().Get("Set-Cookie") != "" {
			t.Errorf("%s: %d %v", path, rec.Code, rec.Header())
		}
	}
}

// matchedAs records the method and template geta.Matched reports.
func matchedAs(mu *sync.Mutex, seen *[]string, tag string) geta.Middleware {
	return geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m, _ := geta.Matched(r.Context())
			mu.Lock()
			*seen = append(*seen, tag+"="+m.Method+" "+m.Template)
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	})
}

// After a root middleware changes the method, Matched follows from the next
// root gate on; root middleware between the change and the gate read the
// arrival match.
func TestMatchedFollowsAMethodRewriteFromTheNextGate(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	a := accepts(t, geta.Table{
		Root: geta.Scope{methodOverride, matchedAs(&mu, &seen, "before"), gate(), matchedAs(&mu, &seen, "after")},
		Routes: []geta.Entry{{Path: "/items", Route: geta.Route{
			Post:   geta.Op(http.StatusOK, okHandler, geta.Doc{}),
			Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error { return nil }, geta.Doc{}),
		}}},
	})
	rec := do(t, a, http.MethodPost, "/items", "X-HTTP-Method-Override", "DELETE", "Authorization", "Bearer ok")
	if rec.Code != http.StatusNoContent || strings.Join(seen, ",") != "before=POST /items,after=DELETE /items" {
		t.Fatalf("%d %q", rec.Code, seen)
	}
}

// With no root gate, a Doc.BeforeGate runs just before the first gate of
// the operation's directory scopes.
func TestBeforeGateRunsBeforeTheFirstDirectoryScopeGate(t *testing.T) {
	var tr trail
	route := geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{BeforeGate: geta.Scope{tr.mark("bg")}})}
	a := accepts(t, geta.Table{Routes: []geta.Entry{{Path: "/x", Route: route, Scopes: []geta.Scope{{tr.mark("d1"), gate(), tr.mark("d2")}}}}})
	if rec := do(t, a, http.MethodGet, "/x", "Authorization", "Bearer ok"); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if got := tr.take(); got != "d1=/x,bg=/x,d2=/x" {
		t.Fatal(got)
	}
	b := accepts(t, geta.Table{Routes: []geta.Entry{{Path: "/x", Route: route, Scopes: []geta.Scope{{gate(), tr.mark("mid"), gate()}}}}})
	if rec := do(t, b, http.MethodGet, "/x", "Authorization", "Bearer ok"); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if got := tr.take(); got != "bg=/x,mid=/x" {
		t.Fatal(got)
	}
}

type locatedOut struct {
	Location string `header:"Location"`
	Body     ok     `body:"json"`
}

// A response to an allowed origin exposes the headers the operation's
// success declares, then ExposeHeaders.
func TestCORSExposesTheDeclaredHeadersThenTheListed(t *testing.T) {
	h := func(context.Context, *empty) (*locatedOut, error) { return &locatedOut{Location: "/x/1"}, nil }
	a := accepts(t, withRoot(one("/x", get(h)), geta.CORS(geta.AllowOrigins("https://a.example"), geta.ExposeHeaders("X-Extra"))))
	rec := do(t, a, http.MethodGet, "/x", "Origin", "https://a.example")
	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "Location, X-Extra" {
		t.Fatalf("%q", got)
	}
}

type splitInner struct {
	S string `json:"s"`
}

type splitOuter struct {
	N  int        `json:"n"`
	In splitInner `json:"in"`
}

// A component a request reads and a response writes is split when only a
// component it reaches differs: its own members state nothing either side
// would differ on.
func TestAComponentSplitsWhereOnlyWhatItReachesDiffers(t *testing.T) {
	h := func(_ context.Context, in *struct {
		Body splitOuter `body:"json"`
	}) (*splitOuter, error) {
		return &in.Body, nil
	}
	m := doc(t, accepts(t, one("/x", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})})))
	if got := at(t, m, "components", "schemas", "splitOuter-Input", "properties", "in", "$ref"); got != "#/components/schemas/splitInner-Input" {
		t.Fatal(got)
	}
	if got := at(t, m, "components", "schemas", "splitOuter", "properties", "in", "$ref"); got != "#/components/schemas/splitInner" {
		t.Fatal(got)
	}
}

// A cookie parameter's schema states what a cookie carries for a string and
// an application's geta.FormatType, joining an author's pattern in allOf,
// and states nothing for a number or a format type every value of which a
// cookie carries; an output header of a geta.FormatType states what a header
// carries.
func TestWhatACookieAndAnOutputHeaderCarryIsDocumented(t *testing.T) {
	h := func(context.Context, *struct {
		C1 *int       `cookie:"c1"`
		C2 *geta.Date `cookie:"c2"`
		C3 *Email     `cookie:"c3"`
		C4 *string    `cookie:"c4" schema:"pattern=^a"`
	}) (*ok, error) {
		return &ok{true}, nil
	}
	m := doc(t, accepts(t, one("/x", get(h))))
	schemas := map[string]string{}
	for _, p := range at(t, m, "paths", "/x", "get", "parameters").([]any) {
		schemas[p.(map[string]any)["name"].(string)] = compact(t, p.(map[string]any)["schema"])
	}
	if strings.Contains(schemas["c1"], "pattern") || strings.Contains(schemas["c2"], "pattern") ||
		!strings.Contains(schemas["c3"], "pattern") || !strings.Contains(schemas["c4"], "allOf") {
		t.Fatalf("%v", schemas)
	}
	var in *signupIn
	if got := compact(t, at(t, doc(t, accepts(t, signupApp(&in))), "paths", "/signup", "post", "responses", "201", "headers", "X-Contact", "schema")); !strings.Contains(got, `"format":"email"`) || !strings.Contains(got, "pattern") {
		t.Fatal(got)
	}
}

// components.securitySchemes writes a scheme's bearerFormat,
// openIdConnectUrl, and description where they are set.
func TestSecuritySchemesWriteEveryMemberSet(t *testing.T) {
	jwt := geta.Scheme{Name: "jwt", Type: "http", Scheme: "bearer", BearerFormat: "JWT", Description: "a signed token"}
	oidc := geta.Scheme{Name: "oidc", Type: "openIdConnect", OpenIDConnectURL: "https://issuer.example/.well-known/openid-configuration"}
	pass := func(*http.Request) (context.Context, error) { return nil, nil }
	gate := geta.Secure(geta.Policy{Default: []geta.Scheme{jwt, oidc}, Verifiers: map[string]geta.Verifier{"jwt": pass, "oidc": pass}})
	m := doc(t, accepts(t, withRoot(one("/x", get(okHandler)), gate)))
	if got := compact(t, at(t, m, "components", "securitySchemes", "jwt")); got != `{"bearerFormat":"JWT","description":"a signed token","scheme":"bearer","type":"http"}` {
		t.Error(got)
	}
	if got := compact(t, at(t, m, "components", "securitySchemes", "oidc")); got != `{"openIdConnectUrl":"https://issuer.example/.well-known/openid-configuration","type":"openIdConnect"}` {
		t.Error(got)
	}
}

type absentNested struct {
	Inner dfltLine  `json:"inner"`
	Ptr   *dfltLine `json:"ptr,omitzero"`
}

// getaclient.Absent leaves out a JSON member inside a nested struct and
// behind a pointer, so geta binds its default.
func TestAbsentReachesNestedAndPointedMembers(t *testing.T) {
	h := func(_ context.Context, in *struct {
		Body absentNested `body:"json"`
	}) (*absentNested, error) {
		return &in.Body, nil
	}
	c := getatest.New(t, one("/x", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}))
	in := struct {
		Body absentNested `body:"json"`
	}{Body: absentNested{Inner: dfltLine{Note: "n"}, Ptr: &dfltLine{Note: "p"}}}
	out, err := getaclient.Call[struct {
		Body absentNested `body:"json"`
	}, absentNested](t.Context(), c.Typed(), http.MethodPost, "/x", &in, getaclient.Absent(&in.Body.Inner.Qty, &in.Body.Ptr.Qty))
	if err != nil || out.Inner.Qty != 1 || out.Ptr == nil || out.Ptr.Qty != 1 {
		t.Fatal(out, err)
	}
}

// A problem with no detail reads as its status and title alone.
func TestClientErrorTextWithoutADetail(t *testing.T) {
	gone := errors.New("gone")
	c := getatest.New(t, one("/g", geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *empty) (*ok, error) { return nil, gone },
		geta.Doc{Failures: []geta.Failure{geta.On(gone, http.StatusGone, "")}})}))
	_, err := getaclient.Call[empty, ok](t.Context(), c.Typed(), http.MethodGet, "/g", nil)
	if err == nil || err.Error() != "410 Gone" {
		t.Fatalf("%v", err)
	}
}

// A Typed call whose types are not the operation's fails the test before
// anything is sent.
func TestTypedRefusesBeforeSending(t *testing.T) {
	var mu sync.Mutex
	sent := 0
	count := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			sent++
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	})
	type other struct {
		X int `json:"x"`
	}
	rec := &recorder{TB: t}
	c := getatest.New(rec, withRoot(one("/x", get(okHandler)), count)).Typed()
	if _, err := getaclient.Call[empty, other](t.Context(), c, http.MethodGet, "/x", nil); err == nil {
		t.Fatal("a wrong output was called")
	}
	if _, err := getaclient.Call[other, ok](t.Context(), c, http.MethodGet, "/x", nil); err == nil {
		t.Fatal("a wrong input was called")
	}
	if sent != 0 || len(rec.errs) != 2 || !slices.ContainsFunc(rec.errs, func(e string) bool { return strings.Contains(e, "getatest:") }) {
		t.Fatalf("%d sent, %q", sent, rec.errs)
	}
}
