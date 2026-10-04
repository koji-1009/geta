package geta_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// Response rules that no other test asserts.

type rsAbsent struct {
	P *int           `json:"p,omitzero"`
	S []int          `json:"s"`
	M map[string]int `json:"m"`
}

type rsNullables struct {
	Required geta.Nullable[int] `json:"required"`
	Optional geta.Nullable[int] `json:"optional,omitzero"`
	Set      geta.Nullable[int] `json:"set"`
	Cleared  geta.Nullable[int] `json:"cleared,omitzero"`
}

// A nil optional pointer member is omitted, a nil slice is [], a nil map {};
// a Nullable left absent is omitted when optional and null when required,
// and one set null is null.
func TestAbsentMembersAreWrittenAsTheSchemaSays(t *testing.T) {
	a := accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/absent", Route: get(func(context.Context, *empty) (*rsAbsent, error) { return &rsAbsent{}, nil })},
		{Path: "/null", Route: get(func(context.Context, *empty) (*rsNullables, error) {
			return &rsNullables{Set: geta.NotNull(3), Cleared: geta.Null[int]()}, nil
		})},
	}})
	if got := do(t, a, "GET", "/absent").Body.String(); got != `{"s":[],"m":{}}` {
		t.Error(got)
	}
	if got := do(t, a, "GET", "/null").Body.String(); got != `{"required":null,"set":3,"cleared":null}` {
		t.Error(got)
	}
}

// A JSON success body held within the buffer carries its media type, its
// Content-Length, and nosniff.
func TestABufferedJSONBodyCarriesItsHeaders(t *testing.T) {
	type env struct {
		Location string `header:"Location"`
		Body     ok     `body:"json"`
	}
	a := accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/plain", Route: get(okHandler)},
		{Path: "/envelope", Route: get(func(context.Context, *empty) (*env, error) { return &env{Location: "/x", Body: ok{true}}, nil })},
	}})
	for _, path := range []string{"/plain", "/envelope"} {
		r := do(t, a, "GET", path)
		if r.Code != 200 || r.Header().Get("Content-Type") != "application/json" || r.Body.String() != `{"ok":true}` ||
			r.Header().Get("Content-Length") != strconv.Itoa(r.Body.Len()) || r.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Error(path, r.Code, r.Header())
		}
	}
}

// Every problem body is HTML-escaped as a success body is: the problem's own
// members and the members a description adds write <, >, and & as \u00XX.
func TestProblemBodiesAreHTMLEscaped(t *testing.T) {
	type marked struct {
		Note string `json:"note"`
	}
	h := func(context.Context, *empty) (*ok, error) { return nil, &conflictError{With: "x"} }
	a := accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/row", Route: geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{Failures: []geta.Failure{
			geta.On(errNotFound, http.StatusNotFound, ""),
			geta.OnAs[*conflictError](http.StatusConflict, "a <b> & c"),
		}})}},
		{Path: "/described", Route: geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{Failures: []geta.Failure{
			geta.OnAsProblem(http.StatusConflict, "a <b> & c", func(*conflictError) marked { return marked{Note: "<i>"} }),
		}})}},
	}})
	u := func(hex string) string { return string('\\') + "u00" + hex }
	escaped := `"detail":"a ` + u("3c") + "b" + u("3e") + " " + u("26") + ` c"`
	if got := do(t, a, "GET", "/row").Body.String(); !strings.Contains(got, escaped) || strings.ContainsAny(got, "<>&") {
		t.Error(got)
	}
	if got := do(t, a, "GET", "/described").Body.String(); !strings.Contains(got, escaped) ||
		!strings.Contains(got, `"note":"`+u("3c")+"i"+u("3e")+`"`) {
		t.Error(got)
	}
}

// A body geta can write is sent as written, though the document's schema
// refuses it: a type's own JSON off its WithSchema declaration, a string
// past a declared maxLength.
func TestBodiesOffTheirSchemaAreSentAsWritten(t *testing.T) {
	a, err := geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/own", Route: get(func(context.Context, *empty) (*liarOut, error) { return &liarOut{}, nil })},
		{Path: "/long", Route: get(func(context.Context, *empty) (*conformsDeclared, error) {
			return &conformsDeclared{S: "abcdefghijkl"}, nil
		})},
	}}, geta.WithSchema[liar]("string", ""))
	if err != nil {
		t.Fatal(err)
	}
	if r := do(t, a, "GET", "/own"); r.Code != 200 || r.Body.String() != `{"v":7}` {
		t.Error(r.Code, r.Body)
	}
	if r := do(t, a, "GET", "/long"); r.Code != 200 || r.Body.String() != `{"s":"abcdefghijkl"}` {
		t.Error(r.Code, r.Body)
	}
}

// Past MaxResponseBuffer a JSON body is not held whole: whatever the buffer
// and the body's size, serving it allocates less than a megabyte.
func TestStreamedBodyMemoryDoesNotGrowWithItsSize(t *testing.T) {
	for _, limit := range []int{4 << 10, 64 << 10} {
		for _, size := range []int{2 << 20, 8 << 20} {
			a := largeApp(t, largeOf(size, false), limit)
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			serve := func() *discardWriter {
				d := &discardWriter{h: http.Header{}}
				a.ServeHTTP(d, req)
				return d
			}
			serve()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			d := serve()
			runtime.ReadMemStats(&after)
			if d.status != 200 || d.n < size || d.h.Get("Content-Length") != "" {
				t.Fatalf("limit %d, size %d: %d, %d bytes, %v", limit, size, d.status, d.n, d.h)
			}
			if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 1<<20 {
				t.Errorf("limit %d: serving %d bytes allocated %d", limit, d.n, alloc)
			}
		}
	}
}

type rsReaderOut struct {
	Body io.Reader `body:"text/plain"`
}

// serveAborting serves GET /x on w and returns what ServeHTTP panicked with.
func serveAborting(a *geta.App, w http.ResponseWriter) (p any) {
	defer func() { p = recover() }()
	a.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	return nil
}

// A raw reader that fails once the response is committed is logged at Error
// and the connection aborted; a reader's response the connection does not
// take is logged at Info and aborted, as any committed response.
func TestARawReaderFailingAfterTheCommitAborts(t *testing.T) {
	limits := geta.DefaultLimits
	limits.MaxResponseBuffer = 16
	log, buf := logger()
	a, err := geta.New(one("/x", get(func(context.Context, *empty) (*rsReaderOut, error) {
		return &rsReaderOut{Body: &brokenReader{n: 100}}, nil
	})), geta.WithLimits(limits), geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if p := serveAborting(a, rec); p != http.ErrAbortHandler || rec.Code != 200 {
		t.Fatal(p, rec.Code)
	}
	if l := buf.String(); !strings.Contains(l, "level=ERROR") || !strings.Contains(l, "the disk went away") {
		t.Fatal(l)
	}

	log, buf = logger()
	a, err = geta.New(one("/x", get(func(context.Context, *empty) (*rsReaderOut, error) {
		return &rsReaderOut{Body: strings.NewReader("abc")}, nil
	})), geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	if p := (&failingConn{h: http.Header{}}).serve(a); p != http.ErrAbortHandler {
		t.Fatal(p)
	}
	if l := buf.String(); !strings.Contains(l, "level=INFO") || !strings.Contains(l, "connection reset") {
		t.Fatal(l)
	}
}

// A body geta holds whole, a JSON body within the buffer or a problem, that
// the connection does not take is logged at Info and the connection aborted,
// as any committed response.
func TestAHeldBodyWriteFailureIsLoggedAndAborts(t *testing.T) {
	for name, tbl := range map[string]geta.Table{
		"json":    one("/x", get(okHandler)),
		"problem": failing(errNotFound, geta.On(errNotFound, http.StatusNotFound, "")),
	} {
		log, buf := logger()
		a, err := geta.New(tbl, geta.WithLogger(log))
		if err != nil {
			t.Fatal(err)
		}
		f := &failingConn{h: http.Header{}}
		if p := f.serve(a); p != http.ErrAbortHandler || f.status == 0 {
			t.Errorf("%s: %v %d", name, p, f.status)
		}
		if l := buf.String(); !strings.Contains(l, "level=INFO") || !strings.Contains(l, "connection reset") {
			t.Errorf("%s: %q", name, l)
		}
	}
}

type rsNaNEnvelope struct {
	Name string    `header:"X-Name"`
	Body largeList `body:"json"`
}

// An envelope whose JSON body fails to encode leaves none of its headers on
// the 500, only the middleware's.
func TestAnEnvelopeWhoseBodyFailsLeavesNoHeaders(t *testing.T) {
	mark := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Middleware", "kept")
			next.ServeHTTP(w, r)
		})
	})
	h := func(context.Context, *empty) (*rsNaNEnvelope, error) {
		return &rsNaNEnvelope{Name: "n", Body: *largeOf(64, true)}, nil
	}
	a, err := geta.New(withRoot(one("/x", get(h)), mark), geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	r := do(t, a, "GET", "/x")
	if r.Code != 500 || r.Header().Get("X-Name") != "" || r.Header().Get("X-Middleware") != "kept" {
		t.Fatal(r.Code, r.Header())
	}
}

// What an envelope field could state falsely, or not write, is refused.
func TestEnvelopeFieldRefusals(t *testing.T) {
	type emptyName struct {
		H string `header:""`
	}
	type listHeader struct {
		H []string `header:"X-H"`
	}
	type twoCookies struct {
		A *http.Cookie `cookie:"a"`
		B *http.Cookie `cookie:"a"`
	}
	type cookieDoc struct {
		A *http.Cookie `cookie:"a" doc:"the session"`
	}
	type bodyDoc struct {
		B ok `body:"json" doc:"the item"`
	}
	type twoBodies struct {
		A ok `body:"json"`
		B ok `body:"json"`
	}
	type bareTag struct {
		B []byte `body:"xml"`
	}
	type untagged struct {
		H     string `header:"X-H"`
		Extra string
	}
	type unexported struct {
		h string `header:"X-H"`
		B ok     `body:"json"`
	}
	type docEmbedded struct {
		envMeta `doc:"metadata"`
		B       ok `body:"json"`
	}
	for want, out := range map[string]any{
		`empty or repeated header name ""`:                      emptyName{},
		`header "X-H" has unsupported type []string`:            listHeader{},
		`empty or repeated cookie name "a"`:                     twoCookies{},
		"a cookie field takes no doc tag":                       cookieDoc{},
		"an output body takes no doc tag":                       bodyDoc{},
		"a second body field":                                   twoBodies{},
		`unknown body tag "xml"`:                                bareTag{},
		"an envelope field needs a header, cookie, or body tag": untagged{},
		"is tagged but unexported":                              unexported{h: ""},
		"an embedded struct takes no doc tag":                   docEmbedded{},
	} {
		var tbl geta.Table
		switch o := out.(type) {
		case emptyName:
			tbl = one("/x", get(func(context.Context, *empty) (*emptyName, error) { return &o, nil }))
		case listHeader:
			tbl = one("/x", get(func(context.Context, *empty) (*listHeader, error) { return &o, nil }))
		case twoCookies:
			tbl = one("/x", get(func(context.Context, *empty) (*twoCookies, error) { return &o, nil }))
		case cookieDoc:
			tbl = one("/x", get(func(context.Context, *empty) (*cookieDoc, error) { return &o, nil }))
		case bodyDoc:
			tbl = one("/x", get(func(context.Context, *empty) (*bodyDoc, error) { return &o, nil }))
		case twoBodies:
			tbl = one("/x", get(func(context.Context, *empty) (*twoBodies, error) { return &o, nil }))
		case bareTag:
			tbl = one("/x", get(func(context.Context, *empty) (*bareTag, error) { return &o, nil }))
		case untagged:
			tbl = one("/x", get(func(context.Context, *empty) (*untagged, error) { return &o, nil }))
		case unexported:
			tbl = one("/x", get(func(context.Context, *empty) (*unexported, error) { return &o, nil }))
		case docEmbedded:
			tbl = one("/x", get(func(context.Context, *empty) (*docEmbedded, error) { return &o, nil }))
		}
		rejects(t, tbl, want)
	}
}

// What an operation's status, or a status field, could state falsely is
// refused: a 205 beside a body, a listed status that is no 2xx or 3xx, a
// status field that carries another tag, or a schema or doc tag.
func TestStatusRefusals(t *testing.T) {
	rejects(t, one("/x", geta.Route{Post: geta.Op(http.StatusResetContent, okHandler, geta.Doc{})}), "success status 205 takes no body")
	type notFound struct {
		Status int `status:"200|404"`
	}
	type alsoHeader struct {
		Status int `status:"200" header:"X-Status"`
	}
	type alsoCookie struct {
		Status int `status:"200" cookie:"s"`
	}
	type alsoBody struct {
		Status int `status:"200" body:"json"`
	}
	type documented struct {
		Status int `status:"200|201" doc:"the status"`
	}
	type constrained struct {
		Status int `status:"200|201" schema:"minimum=200"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*notFound, error) { return nil, nil })), `status tag "200|404": 404 is not a 2xx or 3xx status other than 304`)
	rejects(t, one("/x", get(func(context.Context, *empty) (*alsoHeader, error) { return nil, nil })), "a status field takes no header, cookie, or body tag")
	rejects(t, one("/x", get(func(context.Context, *empty) (*alsoCookie, error) { return nil, nil })), "a status field takes no header, cookie, or body tag")
	rejects(t, one("/x", get(func(context.Context, *empty) (*alsoBody, error) { return nil, nil })), "a status field takes no header, cookie, or body tag")
	rejects(t, one("/x", get(func(context.Context, *empty) (*documented, error) { return nil, nil })), "a status field takes no schema or doc tag")
	rejects(t, one("/x", get(func(context.Context, *empty) (*constrained, error) { return nil, nil })), "a status field takes no schema or doc tag")
}

// A redirect (301, 302, 303, 307, 308), the operation's status or one its
// status field declares, needs an output that sets Location: geta.New
// refuses geta.OpNoBody, a plain output, an envelope without a Location
// header field, and one whose Location is a number or a bool. 300 needs none.
// 305 and 306 are no success status.

type seeOtherNoLocation struct {
	Note string `header:"X-Note"`
}

type maybeMoved struct {
	Status int    `status:"200|307"`
	Note   string `header:"X-Note"`
}

type numericLocation struct {
	Location int `header:"Location"`
}

// target is a Location of a text type.
type target struct{ path string }

func (t target) MarshalText() ([]byte, error) { return []byte(t.path), nil }

func (t *target) UnmarshalText(b []byte) error {
	t.path = string(b)
	return nil
}

type textLocation struct {
	Location target `header:"Location"`
}

type lowerLocation struct {
	Status   int     `status:"201|302"`
	Location *string `header:"location"`
	Body     ok      `body:"json"`
}

type embeddedLocation struct {
	locationHeader
}

type locationHeader struct {
	Location string `header:"Location"`
}

type retired struct {
	Status int `status:"200|305"`
}

type unused struct {
	Status int `status:"200|306"`
}

func TestRedirectsCarryALocation(t *testing.T) {
	noBody := func(context.Context, *empty) error { return nil }
	rejects(t, one("/x", geta.Route{Post: geta.OpNoBody(http.StatusSeeOther, noBody, geta.Doc{})}),
		"POST /x", "success status 303 is a redirect, but geta.OpNoBody sets no Location; use geta.Op")
	rejects(t, one("/x", geta.Route{Post: geta.Op(http.StatusMovedPermanently, okHandler, geta.Doc{})}),
		"success status 301 is a redirect, but the output geta_test.ok has no Location header field")
	rejects(t, one("/x", geta.Route{Post: geta.Op(http.StatusPermanentRedirect, func(context.Context, *empty) (*seeOtherNoLocation, error) { return nil, nil }, geta.Doc{})}),
		"success status 308 is a redirect", "the output geta_test.seeOtherNoLocation has no Location header field")
	rejects(t, one("/x", get(func(context.Context, *empty) (*maybeMoved, error) { return nil, nil })),
		"the output's status field declares 307, a redirect, but the output geta_test.maybeMoved has no Location header field")
	rejects(t, one("/x", geta.Route{Post: geta.Op(http.StatusFound, func(context.Context, *empty) (*numericLocation, error) { return nil, nil }, geta.Doc{})}),
		"success status 302 is a redirect, but the output geta_test.numericLocation's Location header field Location has type int, not a string or text type")
	rejects(t, one("/x", geta.Route{Post: geta.OpNoBody(http.StatusUseProxy, noBody, geta.Doc{})}),
		"success status 305 is deprecated")
	rejects(t, one("/x", geta.Route{Post: geta.OpNoBody(306, noBody, geta.Doc{})}),
		"success status 306 is unused")
	rejects(t, one("/x", get(func(context.Context, *empty) (*retired, error) { return nil, nil })),
		`status tag "200|305": 305 is deprecated`)
	rejects(t, one("/x", get(func(context.Context, *empty) (*unused, error) { return nil, nil })),
		`status tag "200|306": 306 is unused`)
	// 300 MAY carry a Location: any output answers it.
	accepts(t, one("/x", geta.Route{Post: geta.OpNoBody(http.StatusMultipleChoices, noBody, geta.Doc{})}))
	accepts(t, one("/x", geta.Route{Post: geta.Op(http.StatusMultipleChoices, okHandler, geta.Doc{})}))
	// A Location of a text type, spelled in any case, optional, or embedded.
	accepts(t, one("/x", geta.Route{Post: geta.Op(http.StatusSeeOther, func(context.Context, *empty) (*textLocation, error) { return nil, nil }, geta.Doc{})}))
	accepts(t, one("/x", geta.Route{Post: geta.Op(http.StatusCreated, func(context.Context, *empty) (*lowerLocation, error) { return nil, nil }, geta.Doc{})}))
	accepts(t, one("/x", geta.Route{Post: geta.Op(http.StatusTemporaryRedirect, func(context.Context, *empty) (*embeddedLocation, error) { return nil, nil }, geta.Doc{})}))
}

// A redirect whose Location is nil or empty is a defect, a 500 that carries
// none of the envelope's headers; another status the field declares may
// leave it nil.
func TestARedirectWithoutALocationIsADefect(t *testing.T) {
	where := "/there"
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/empty", Route: geta.Route{Post: geta.Op(http.StatusSeeOther, func(context.Context, *empty) (*embeddedLocation, error) {
			return &embeddedLocation{}, nil
		}, geta.Doc{})}},
		{Path: "/text", Route: geta.Route{Post: geta.Op(http.StatusSeeOther, func(context.Context, *empty) (*textLocation, error) {
			return &textLocation{}, nil
		}, geta.Doc{})}},
		{Path: "/choose", Route: geta.Route{Get: geta.Op(http.StatusCreated, func(_ context.Context, in *chooseIn) (*lowerLocation, error) {
			if in.Move {
				return &lowerLocation{Status: http.StatusFound}, nil
			}
			return &lowerLocation{Body: ok{true}}, nil
		}, geta.Doc{})}},
		{Path: "/moved", Route: geta.Route{Get: geta.Op(http.StatusCreated, func(context.Context, *empty) (*lowerLocation, error) {
			return &lowerLocation{Status: http.StatusFound, Location: &where}, nil
		}, geta.Doc{})}},
	}})
	for _, path := range []string{"/empty", "/text"} {
		if res := c.Post(path, nil); res.Status != http.StatusInternalServerError || res.Header.Get("Location") != "" {
			t.Fatal(path, res.Status, res.Header)
		}
	}
	if res := c.Get("/choose?move=true"); res.Status != http.StatusInternalServerError || res.Header.Get("Location") != "" {
		t.Fatal(res.Status, res.Header)
	}
	if res := c.Get("/choose?move=false"); res.Status != http.StatusCreated || res.Header.Values("Location") != nil {
		t.Fatal(res.Status, res.Header)
	}
	if res := c.Get("/moved"); res.Status != http.StatusFound || res.Header.Get("Location") != where {
		t.Fatal(res.Status, res.Header)
	}
}

// An output's raw body tag that is no single media type, as geta writes it,
// of bytes geta does not write as JSON, is refused.
func TestRawOutputMediaTypeRefusals(t *testing.T) {
	type jsonOut struct {
		Body []byte `body:"application/json"`
	}
	type problemOut struct {
		Body []byte `body:"application/problem+json"`
	}
	type rangeOut struct {
		Body []byte `body:"text/*"`
	}
	type caseOut struct {
		Body []byte `body:"Text/CSV"`
	}
	type formOut struct {
		Body []byte `body:"application/x-www-form-urlencoded"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*jsonOut, error) { return nil, nil })), `is JSON; use body:"json"`)
	rejects(t, one("/x", get(func(context.Context, *empty) (*problemOut, error) { return nil, nil })), `is JSON; use body:"json"`)
	rejects(t, one("/x", get(func(context.Context, *empty) (*rangeOut, error) { return nil, nil })), "is a media range")
	rejects(t, one("/x", get(func(context.Context, *empty) (*caseOut, error) { return nil, nil })), `is not canonical; write "text/csv"`)
	// A form's media type is bytes like any other in an output.
	accepts(t, one("/x", get(func(context.Context, *empty) (*formOut, error) { return nil, nil })))
}

type rsDefectStatus struct {
	Status int `status:"200|201"`
	Body   ok  `body:"json"`
}

type rsDefectHeader struct {
	Note string `header:"X-Note"`
}

type rsDefectCookie struct {
	C *http.Cookie `cookie:"sid"`
}

// Every defect a response can have is a 500 that carries a fresh urn:uuid
// instance and nothing of the error, and is logged at Error with the same
// instance.
func TestEveryResponseDefectCarriesAnInstance(t *testing.T) {
	routes := map[string]geta.Route{
		"/unmatched": get(func(context.Context, *empty) (*ok, error) { return nil, errors.New("db down") }),
		"/nil":       get(func(context.Context, *empty) (*ok, error) { return nil, nil }),
		"/status": get(func(context.Context, *empty) (*rsDefectStatus, error) {
			return &rsDefectStatus{Status: http.StatusAccepted}, nil
		}),
		"/encode":  get(func(context.Context, *empty) (*largeList, error) { return largeOf(64, true), nil }),
		"/ownjson": get(func(context.Context, *empty) (*brokenOut, error) { return &brokenOut{}, nil }),
		"/header":  get(func(context.Context, *empty) (*rsDefectHeader, error) { return &rsDefectHeader{Note: "a\nb"}, nil }),
		"/cookie": get(func(context.Context, *empty) (*rsDefectCookie, error) {
			return &rsDefectCookie{C: &http.Cookie{Value: "a;b"}}, nil
		}),
		"/reader": get(func(context.Context, *empty) (*rsReaderOut, error) { return &rsReaderOut{}, nil }),
		"/sealed": get(func(context.Context, *empty) (*drawing, error) { return &drawing{Others: []shape{}}, nil }),
		"/stream": get(func(context.Context, *empty) (*geta.Stream[change], error) { return &geta.Stream[change]{}, nil }),
		"/upgrade": geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, func(context.Context, *empty) (*geta.Upgrade, error) {
			return &geta.Upgrade{Protocol: "echo"}, nil
		}, geta.Doc{})},
		"/precondition": get(func(context.Context, *empty) (*ok, error) {
			return nil, (&geta.Conditional{IfMatch: new(`"x"`)}).Check(`"y"`, time.Time{})
		}),
	}
	var tbl geta.Table
	for path, r := range routes {
		tbl.Routes = append(tbl.Routes, geta.Entry{Path: path, Route: r})
	}
	log, buf := logger()
	a, err := geta.New(tbl, geta.WithUnion(shapes), geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for path := range routes {
		r := do(t, a, "GET", path)
		var p geta.Problem
		if err := json.Unmarshal(r.Body.Bytes(), &p); err != nil {
			t.Fatalf("%s: %v %s", path, err, r.Body)
		}
		if r.Code != 500 || p.Status != 500 || p.Detail != "" || p.Errors != nil || !strings.HasPrefix(p.Instance, "urn:uuid:") ||
			len(p.Instance) != len("urn:uuid:")+36 || seen[p.Instance] {
			t.Errorf("%s: %d %s", path, r.Code, r.Body)
			continue
		}
		seen[p.Instance] = true
		for line := range strings.SplitSeq(buf.String(), "\n") {
			if strings.Contains(line, "instance="+p.Instance) {
				if !strings.Contains(line, "level=ERROR") {
					t.Errorf("%s: %s", path, line)
				}
				p.Instance = ""
			}
		}
		if p.Instance != "" {
			t.Errorf("%s: no log line carries the instance: %s", path, buf.String())
		}
		if strings.Contains(r.Body.String(), "db down") || strings.Contains(r.Body.String(), "a\\nb") {
			t.Errorf("%s: the error leaked: %s", path, r.Body)
		}
	}
}

// Every problem geta answers is application/problem+json with its
// Content-Length and nosniff, its type, its status's title, and its status;
// WriteProblem writes one, an empty detail leaving the member out.
func TestEveryProblemHasOneShape(t *testing.T) {
	upgrade := func(context.Context, *empty) (*geta.Upgrade, error) {
		return &geta.Upgrade{Protocol: "echo", Serve: func(net.Conn, *bufio.ReadWriter) {}}, nil
	}
	cond := func(_ context.Context, in *condIn) (*ok, error) { return nil, in.Check(`"v"`, time.Time{}) }
	deadline := func(context.Context, *empty) (*ok, error) { return nil, context.DeadlineExceeded }
	a, err := geta.New(geta.Table{Routes: append(describedTable().Routes,
		geta.Entry{Path: "/row", Route: failing(errNotFound, geta.On(errNotFound, 404, "gone")).Routes[0].Route},
		geta.Entry{Path: "/defect", Route: failing(errNotFound).Routes[0].Route},
		geta.Entry{Path: "/deadline", Route: get(deadline)},
		geta.Entry{Path: "/cond", Route: get(cond)},
		geta.Entry{Path: "/req", Route: get(func(_ context.Context, in *requiredIn) (*ok, error) { return nil, in.Check(`"v"`, time.Time{}) })},
		geta.Entry{Path: "/ws", Route: geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, upgrade, geta.Doc{})}},
	)}, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	check := func(name string, r *httptest.ResponseRecorder, status int) {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal(r.Body.Bytes(), &m); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if r.Code != status || r.Header().Get("Content-Type") != geta.ProblemContentType ||
			r.Header().Get("Content-Length") != strconv.Itoa(r.Body.Len()) || r.Header().Get("X-Content-Type-Options") != "nosniff" ||
			m["type"] == nil || m["title"] != http.StatusText(status) || m["status"] != float64(status) {
			t.Errorf("%s: %d %v %s", name, r.Code, r.Header(), r.Body)
		}
	}
	check("row", do(t, a, "GET", "/row"), 404)
	check("described", do(t, a, "GET", "/f?kind=quota"), 429)
	check("defect", do(t, a, "GET", "/defect"), 500)
	check("deadline", do(t, a, "GET", "/deadline"), 504)
	check("precondition", do(t, a, "GET", "/cond", "If-Match", `"x"`), 412)
	check("malformed precondition", do(t, a, "GET", "/cond", "If-Match", "x"), 400)
	check("precondition required", do(t, a, "GET", "/req"), 428)
	check("upgrade required", do(t, a, "GET", "/ws"), 426)

	for detail, want := range map[string]string{
		"no entry": `{"type":"about:blank","title":"Forbidden","status":403,"detail":"no entry"}`,
		"":         `{"type":"about:blank","title":"Forbidden","status":403}`,
	} {
		rec := httptest.NewRecorder()
		geta.WriteProblem(rec, http.StatusForbidden, detail)
		check("WriteProblem", rec, 403)
		if rec.Body.String() != want {
			t.Errorf("%q: %s", detail, rec.Body)
		}
	}
}

// An error no row matches is logged at Error with its text, the method, and
// the route template.
func TestAnUnmatchedErrorIsLoggedWithItsOperation(t *testing.T) {
	log, buf := logger()
	h := func(context.Context, *idIn) (*ok, error) { return nil, fmt.Errorf("query: %w", errNotFound) }
	a, err := geta.New(one("/items/{id}", geta.Route{Put: geta.Op(http.StatusOK, h, geta.Doc{})}), geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	if r := do(t, a, "PUT", "/items/7"); r.Code != 500 {
		t.Fatal(r.Code)
	}
	if l := buf.String(); !strings.Contains(l, "level=ERROR") || !strings.Contains(l, "method=PUT") ||
		!strings.Contains(l, "route=/items/{id}") || !strings.Contains(l, `error="query: store: not found"`) {
		t.Fatal(l)
	}
}

// A context.Canceled with the client gone writes nothing and is logged at
// Info with the method and the route template.
func TestAClientGoneIsLoggedAtInfo(t *testing.T) {
	log, buf := logger()
	a, err := geta.New(failing(context.Canceled), geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/x", nil))
	if rec.Body.Len() != 0 || len(rec.Header()) != 0 {
		t.Fatal(rec.Header(), rec.Body)
	}
	if l := buf.String(); !strings.Contains(l, "level=INFO") || !strings.Contains(l, "method=GET") || !strings.Contains(l, "route=/x") {
		t.Fatal(l)
	}
}

type rsRefusedBody struct {
	Name string `json:"name" schema:"maxLength=4"`
}

type rsRefusedIn struct {
	ID    string        `path:"id"`
	Limit int           `query:"limit" schema:"maximum=10"`
	Body  rsRefusedBody `body:"json"`
}

// Each client error geta answers itself for a broken contract or limit (400,
// 412, 413, 415, 428) is logged at Debug on the App's logger, with the
// method, the route template, the status, a detail that quotes nothing of
// the request, and for violations their number and places; never the raw
// path, a value, or a violation's text. A 404 is not logged, and nothing is
// at Info.
func TestGetasOwnRefusalsAreLoggedAtDebug(t *testing.T) {
	table := conditionalTable()
	table.Routes = append(table.Routes, geta.Entry{Path: "/items/{id}", Route: geta.Route{
		Post: geta.OpNoBody(http.StatusNoContent, func(context.Context, *rsRefusedIn) error { return nil }, geta.Doc{}),
	}})
	limits := geta.DefaultLimits
	limits.MaxBodyBytes = 64
	var buf syncBuffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	a, err := geta.New(table, geta.WithLogger(log), geta.WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	const secret = "s3cr3t"
	// send answers the request's status and the lines it logged.
	send := func(method, target, body string, header ...string) (int, string) {
		start := len(buf.String())
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, req)
		return rec.Code, buf.String()[start:]
	}
	ct := []string{"Content-Type", "application/json"}
	for _, tc := range []struct {
		name, method, target, body string
		header                     []string
		status                     int
		want                       []string
	}{
		{"query", http.MethodPost, "/items/" + secret + "?limit=99" + secret, `{"name":"a"}`, ct, 400,
			[]string{`route=/items/{id}`, `detail="the request does not match its contract"`, "violations=1", "in=query"}},
		{"body", http.MethodPost, "/items/" + secret + "?limit=1", `{"name":"` + secret + `"}`, ct, 400,
			[]string{`route=/items/{id}`, "violations=1", "in=body"}},
		{"both", http.MethodPost, "/items/" + secret + "?limit=" + secret, `{"name":"` + secret + `"}`, ct, 400,
			[]string{"violations=2", "in=query,body"}},
		{"type", http.MethodPost, "/items/" + secret, `{}`, []string{"Content-Type", "text/" + secret}, 415,
			[]string{`detail="the Content-Type is not application/json"`}},
		{"coding", http.MethodPost, "/items/" + secret, `{}`, append([]string{"Content-Encoding", secret}, ct...), 415,
			[]string{`detail="unsupported Content-Encoding"`}},
		{"size", http.MethodPost, "/items/" + secret, `{"name":"` + strings.Repeat("a", 100) + `"}`, ct, 413,
			[]string{`detail="the request body exceeds 64 bytes"`}},
		{"required", http.MethodPut, "/req", "", nil, 428, []string{"route=/req", `detail="If-Match is required`}},
		{"failed", http.MethodPut, "/r", "", []string{"If-Match", `"` + secret + `"`}, 412, []string{"route=/r", "status=412"}},
	} {
		got, l := send(tc.method, tc.target, tc.body, tc.header...)
		if got != tc.status {
			t.Errorf("%s: status %d, want %d", tc.name, got, tc.status)
			continue
		}
		want := append([]string{"level=DEBUG", `msg="geta: request refused"`, "method=" + tc.method, "status=" + strconv.Itoa(tc.status)}, tc.want...)
		for _, w := range want {
			if !strings.Contains(l, w) {
				t.Errorf("%s: %q lacks %q", tc.name, l, w)
			}
		}
		if strings.Contains(l, secret) || strings.Count(l, "\n") != 1 {
			t.Errorf("%s: %q", tc.name, l)
		}
	}
	if got, l := send(http.MethodGet, "/nope/"+secret, ""); got != 404 || l != "" {
		t.Errorf("a 404 is logged: %d %q", got, l)
	}
	info, ibuf := logger()
	a, err = geta.New(table, geta.WithLogger(info))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/items/x?limit=99", strings.NewReader(`{}`)))
	if rec.Code != 415 || ibuf.String() != "" {
		t.Errorf("logged at Info: %d %q", rec.Code, ibuf.String())
	}
}

type rsJudgedIn struct {
	geta.Conditional
	Kind string    `query:"kind"`
	Body io.Reader `body:"application/octet-stream"`
}

// A handler's error is judged in order: a nil output, the context's errors,
// a raw reader read past the body limit, a Conditional's verdict on the
// statuses its operation lists, then the rows, so a catch-all row answers
// every other error, a verdict the operation does not list and a tag Check
// refuses are not among them: those are logged 500s, whatever the rows.
func TestAHandlerErrorIsJudgedInOrder(t *testing.T) {
	h := func(_ context.Context, in *rsJudgedIn) (*ok, error) {
		switch in.Kind {
		case "nil":
			return nil, nil
		case "deadline":
			return nil, fmt.Errorf("slow: %w", context.DeadlineExceeded)
		case "large":
			_, err := io.ReadAll(in.Body)
			return nil, err
		case "verdict":
			return nil, in.Check(`"v"`, time.Time{})
		case "required":
			return nil, new(geta.RequireConditional).Check(`"v"`, time.Time{})
		case "badtag":
			return nil, in.Check("v", time.Time{})
		case "canceled":
			return nil, context.Canceled
		}
		return nil, errNotFound
	}
	small := func(l geta.Limits) geta.Limits { l.MaxBodyBytes = 4; return l }
	log, buf := logger()
	a, err := geta.New(one("/x", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{Limits: small, Failures: []geta.Failure{
		geta.OnAs[error](http.StatusServiceUnavailable, "down"),
	}})}), geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	for kind, want := range map[string]int{
		"nil": 500, "deadline": 504, "large": 413, "verdict": 412, "required": 500, "badtag": 500, "other": 503,
	} {
		body := "ab"
		if kind == "large" {
			body = "abcdefgh"
		}
		req := httptest.NewRequest(http.MethodPost, "/x?kind="+kind, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("If-Match", `"old"`)
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%s: %d, want %d: %s", kind, rec.Code, want, rec.Body)
		}
		if kind == "required" || kind == "badtag" {
			var p geta.Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Instance == "" ||
				!strings.Contains(buf.String(), "level=ERROR") || !strings.Contains(buf.String(), p.Instance) {
				t.Errorf("%s: a logged 500 with its instance wanted: %s %s", kind, rec.Body, buf)
			}
		}
	}
	// A client gone is answered with nothing, whatever the rows.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/x?kind=canceled", strings.NewReader("ab"))
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Body.Len() != 0 || len(rec.Header()) != 0 {
		t.Fatal(rec.Header(), rec.Body)
	}
}

// Every member a problem writes itself is refused as a description's.
func TestEveryMemberTheProblemWritesIsRefused(t *testing.T) {
	type typeMember struct {
		V string `json:"type"`
	}
	type titleMember struct {
		V string `json:"title"`
	}
	type statusMember struct {
		V int `json:"status"`
	}
	type instanceMember struct {
		V string `json:"instance"`
	}
	type errorsMember struct {
		V []string `json:"errors"`
	}
	table := func(row geta.Failure) geta.Table {
		return one("/f", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Failures: []geta.Failure{row}})})
	}
	for member, row := range map[string]geta.Failure{
		"type":     geta.OnAsProblem(429, "", func(*quotaError) typeMember { return typeMember{} }),
		"title":    geta.OnAsProblem(429, "", func(*quotaError) titleMember { return titleMember{} }),
		"status":   geta.OnAsProblem(429, "", func(*quotaError) statusMember { return statusMember{} }),
		"instance": geta.OnAsProblem(429, "", func(*quotaError) instanceMember { return instanceMember{} }),
		"errors":   geta.OnAsProblem(429, "", func(*quotaError) errorsMember { return errorsMember{} }),
	} {
		rejects(t, table(row), `member "`+member+`" is reserved`)
	}
}

// An RFC 850 date's two-digit year is the latest year that ends in it and is
// not more than 50 years ahead.
func TestRFC850YearsAreNeverMoreThanFiftyYearsAhead(t *testing.T) {
	a := accepts(t, conditionalTable())
	now := time.Now().Year()
	date := func(year int) string { return fmt.Sprintf("Monday, 01-Jan-%02d 00:00:00 GMT", year%100) }
	// 51 years ahead reads as 49 years ago, before the change in 2024.
	if r := do(t, a, "PUT", "/r", "If-Unmodified-Since", date(now+51)); r.Code != 412 {
		t.Errorf("%s: %d", date(now+51), r.Code)
	}
	// 49 years ahead reads as itself, after the change.
	if r := do(t, a, "PUT", "/r", "If-Unmodified-Since", date(now+49)); r.Code != 204 {
		t.Errorf("%s: %d", date(now+49), r.Code)
	}
}

// On a GET, a valid If-Modified-Since is a precondition RequireConditional
// takes; one that is no HTTP-date is none.
func TestRequireConditionalTakesIfModifiedSinceOnAGet(t *testing.T) {
	h := func(_ context.Context, in *requiredIn) (*ok, error) {
		if err := in.Check(current, modifiedAt); err != nil {
			return nil, err
		}
		return &ok{true}, nil
	}
	a := accepts(t, one("/x", get(h)))
	if r := do(t, a, "GET", "/x", "If-Modified-Since", dateEarly); r.Code != 200 {
		t.Error(r.Code)
	}
	if r := do(t, a, "GET", "/x", "If-Modified-Since", dateLate); r.Code != 304 {
		t.Error(r.Code)
	}
	if r := do(t, a, "GET", "/x", "If-Modified-Since", "soon"); r.Code != 428 {
		t.Error(r.Code)
	}
}

type rsRepresentation struct {
	Language string `header:"Content-Language"`
	Encoding string `header:"Content-Encoding"`
	Range    string `header:"Content-Range"`
	Cache    string `header:"Cache-Control"`
	Kept     string `header:"X-Kept"`
	Body     ok     `body:"json"`
}

// ETag's 304 drops the headers that describe the body it does not send and
// keeps every other header of the response it replaces.
func TestETagNotModifiedKeepsTheOtherHeaders(t *testing.T) {
	h := func(context.Context, *empty) (*rsRepresentation, error) {
		return &rsRepresentation{Language: "en", Encoding: "identity", Range: "bytes 0-1/2", Cache: "max-age=60", Kept: "1"}, nil
	}
	a := accepts(t, withRoot(one("/x", get(h)), geta.ETag()))
	r := do(t, a, "GET", "/x", "If-None-Match", "*")
	if r.Code != 304 || r.Body.Len() != 0 || r.Header().Get("ETag") == "" || r.Header().Get("Cache-Control") != "max-age=60" || r.Header().Get("X-Kept") != "1" {
		t.Fatal(r.Code, r.Header())
	}
	for _, k := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Content-Language", "Content-Range"} {
		if r.Header().Get(k) != "" {
			t.Errorf("%s: %q", k, r.Header().Get(k))
		}
	}
}

// ETag tags a HEAD as it tags its GET, from the body the GET would send.
func TestETagTagsAHeadAsItsGet(t *testing.T) {
	a := accepts(t, withRoot(one("/x", get(textHandler("hi"))), geta.ETag()))
	g, h := do(t, a, "GET", "/x"), do(t, a, "HEAD", "/x")
	if g.Header().Get("ETag") == "" || h.Code != 200 || h.Header().Get("ETag") != g.Header().Get("ETag") {
		t.Fatal(g.Header(), h.Code, h.Header())
	}
	if r := do(t, a, "HEAD", "/x", "If-None-Match", g.Header().Get("ETag")); r.Code != 304 {
		t.Fatal(r.Code)
	}
}

// ETag reads If-None-Match as Conditional.Check does: its lines are one
// list, and a malformed value or an empty list is a 400 that carries none of
// the headers the handler set, whatever tags come before the bad element.
func TestETagReadsIfNoneMatchAsCheckDoes(t *testing.T) {
	type tagged struct {
		ETag string `header:"ETag"`
		Body text   `body:"json"`
	}
	h := func(context.Context, *empty) (*tagged, error) { return &tagged{ETag: `"a"`, Body: text{"hi"}}, nil }
	a := accepts(t, withRoot(one("/x", get(h)), geta.ETag()))
	for _, inm := range []string{`"a", junk`, `junk, "a"`, ``, ` , `} {
		if r := do(t, a, "GET", "/x", "If-None-Match", inm); r.Code != 400 || r.Header().Get("ETag") != "" {
			t.Errorf("%q: %d %v", inm, r.Code, r.Header())
		}
	}
	if r := do(t, a, "GET", "/x", "If-None-Match", `"b", "a"`); r.Code != 304 {
		t.Error(r.Code)
	}
	// Two lines are one list: `*` beside a tag is no list.
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Add("If-None-Match", `"a"`)
	req.Header.Add("If-None-Match", `*`)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Error(rec.Code)
	}
}

// Compress codes a body of at least GzipThreshold bytes of every text-like
// media type, and none shorter.
func TestCompressCodesTextLikeBodiesFromTheThreshold(t *testing.T) {
	serve := func(ct string, n int) *httptest.ResponseRecorder {
		set := geta.Use(func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", ct)
				io.WriteString(w, strings.Repeat("a", n))
			})
		})
		return do(t, accepts(t, withRoot(one("/x", get(okHandler)), geta.Gzip(), set)), "GET", "/x", "Accept-Encoding", "gzip")
	}
	for _, ct := range []string{"text/html; charset=utf-8", "application/xml", "application/javascript", "application/x-ndjson",
		"application/atom+xml", "application/geo+json", "application/json"} {
		if r := serve(ct, geta.GzipThreshold); r.Header().Get("Content-Encoding") != "gzip" {
			t.Errorf("%s: %v", ct, r.Header())
		}
		if r := serve(ct, geta.GzipThreshold-1); r.Header().Get("Content-Encoding") != "" {
			t.Errorf("%s below the threshold: %v", ct, r.Header())
		}
	}
	for _, ct := range []string{"application/octet-stream", "image/png", "application/pdf"} {
		if r := serve(ct, 4*geta.GzipThreshold); r.Header().Get("Content-Encoding") != "" {
			t.Errorf("%s: %v", ct, r.Header())
		}
	}
}

// rsCounted counts its encoders, their writes, and their closes.
type rsCounted struct {
	opened, writes, closed int
}

type rsCountedWriter struct {
	w io.Writer
	c *rsCounted
}

func (z *rsCountedWriter) Write(p []byte) (int, error) { z.c.writes++; return z.w.Write(p) }
func (z *rsCountedWriter) Close() error                { z.c.closed++; return nil }

// Compress asks the coding for one encoder per response it codes, writes it
// the whole body once, and closes it once; a response it does not code asks
// for none.
func TestCompressCallsTheEncoderOncePerCodedResponse(t *testing.T) {
	c := &rsCounted{}
	coding := geta.Coding{Name: "zz", NewWriter: func(w io.Writer) (io.WriteCloser, error) {
		c.opened++
		return &rsCountedWriter{w: w, c: c}, nil
	}}
	a := accepts(t, withRoot(geta.Table{Routes: []geta.Entry{
		{Path: "/big", Route: get(textHandler(big))},
		{Path: "/small", Route: get(textHandler("small"))},
	}}, geta.Compress(coding)))
	for range 3 {
		if r := do(t, a, "GET", "/big", "Accept-Encoding", "zz"); r.Header().Get("Content-Encoding") != "zz" {
			t.Fatal(r.Header())
		}
	}
	do(t, a, "GET", "/big")
	do(t, a, "GET", "/small", "Accept-Encoding", "zz")
	if c.opened != 3 || c.writes != 3 || c.closed != 3 {
		t.Fatalf("%+v", *c)
	}
}

// A body that already carries a Content-Encoding is left as it is, even for
// a request that refuses identity.
func TestCompressLeavesAnEncodedBodyAsItIs(t *testing.T) {
	enc := geta.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "br")
			io.WriteString(w, big)
		})
	})
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.Gzip(), enc))
	for _, ae := range []string{"gzip", "identity;q=0, gzip"} {
		if r := do(t, a, "GET", "/x", "Accept-Encoding", ae); r.Header().Get("Content-Encoding") != "br" || r.Body.String() != big {
			t.Errorf("%q: %v", ae, r.Header())
		}
	}
}

// An encoder whose Write fails leaves the body uncoded with the identity
// form's tag.
func TestCompressEncoderWriteFailureKeepsTheIdentityTag(t *testing.T) {
	broken := geta.Coding{Name: "zz", NewWriter: func(io.Writer) (io.WriteCloser, error) { return writeFails{}, nil }}
	a := compApp(t, &gzcResource{tag: `"v"`}, broken)
	if r := do(t, a, "GET", "/r", "Accept-Encoding", "zz"); r.Code != 200 || r.Header().Get("Content-Encoding") != "" || r.Header().Get("ETag") != `"v"` {
		t.Fatal(r.Code, r.Header())
	}
}

type rsNamed struct {
	N string `json:"n"`
}

func (e rsNamed) EventName() string { return e.N }

type rsIdentified struct {
	I string `json:"i"`
}

func (e rsIdentified) EventID() string { return e.I }

// An event name or id holding CR, LF, or NUL is never sent: the stream ends
// after the events before it.
func TestEventNamesAndIDsHoldNoLineBreakOrNUL(t *testing.T) {
	for _, bad := range []string{"a\rb", "a\nb", "a\x00b"} {
		names := func(context.Context, *empty) (*geta.Stream[rsNamed], error) {
			return &geta.Stream[rsNamed]{Events: func(yield func(rsNamed) bool) {
				_ = yield(rsNamed{"ok"}) && yield(rsNamed{bad}) && yield(rsNamed{"late"})
			}}, nil
		}
		ids := func(context.Context, *empty) (*geta.Stream[rsIdentified], error) {
			return &geta.Stream[rsIdentified]{Events: func(yield func(rsIdentified) bool) {
				_ = yield(rsIdentified{"1"}) && yield(rsIdentified{bad}) && yield(rsIdentified{"3"})
			}}, nil
		}
		a, err := geta.New(geta.Table{Routes: []geta.Entry{
			{Path: "/names", Route: get(names)},
			{Path: "/ids", Route: get(ids)},
		}}, geta.WithLogger(quietLogger()))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/names", "/ids"} {
			body := do(t, a, "GET", path).Body.String()
			if strings.Count(body, "data: ") != 1 || strings.Contains(body, bad) {
				t.Errorf("%q %s: %q", bad, path, body)
			}
		}
	}
}

// Compress uses the codings it is given alone: the application's gzip codes
// a request for gzip and one for x-gzip.
func TestCompressUsesTheApplicationsGzip(t *testing.T) {
	own := &fakeCoding{name: "gzip"}
	a := accepts(t, withRoot(one("/x", get(textHandler(big))), geta.Compress(own.coding())))
	for _, ae := range []string{"gzip", "x-gzip"} {
		if r := do(t, a, "GET", "/x", "Accept-Encoding", ae); r.Header().Get("Content-Encoding") != "gzip" || !strings.HasPrefix(r.Body.String(), "gzip:") {
			t.Errorf("%q: %v", ae, r.Header())
		}
	}
	if own.opened.Load() != 2 {
		t.Fatal(own.opened.Load())
	}
}

// rsNoFlush is a ResponseWriter that cannot flush.
type rsNoFlush struct {
	h      http.Header
	status int
	body   bytes.Buffer
}

func (w *rsNoFlush) Header() http.Header         { return w.h }
func (w *rsNoFlush) WriteHeader(code int)        { w.status = code }
func (w *rsNoFlush) Write(b []byte) (int, error) { return w.body.Write(b) }

// A stream on a writer that cannot flush answers its headers and no event,
// and the defect is logged; the source does not run.
func TestAStreamOnAWriterThatCannotFlush(t *testing.T) {
	ran := false
	h := func(context.Context, *empty) (*geta.Stream[change], error) {
		return &geta.Stream[change]{Events: func(yield func(change) bool) { ran = true; yield(change{"a", "1"}) }}, nil
	}
	log, buf := logger()
	a, err := geta.New(one("/e", get(h)), geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	w := &rsNoFlush{h: http.Header{}}
	a.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/e", nil))
	if w.status != 200 || w.h.Get("Content-Type") != "text/event-stream; charset=utf-8" || w.body.Len() != 0 || ran {
		t.Fatal(w.status, w.h, w.body.String(), ran)
	}
	if l := buf.String(); !strings.Contains(l, "level=ERROR") || !strings.Contains(l, "cannot stream") {
		t.Fatal(l)
	}
}

// A request asks to switch when Connection lists upgrade and Upgrade lists
// the protocol, each among other tokens, in any case, a protocol's version
// ignored. Any other request is a 426 naming the protocol in Upgrade and
// Connection. A misbuilt Upgrade is a 500 whatever the request asks.
func TestUpgradeRequestsAndThe426(t *testing.T) {
	misbuilt := func(context.Context, *empty) (*geta.Upgrade, error) { return &geta.Upgrade{Protocol: "echo"}, nil }
	a, err := geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/ws", Route: geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, echoUpgrade, geta.Doc{})}},
		{Path: "/bad", Route: geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, misbuilt, geta.Doc{})}},
	}}, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range [][]string{
		{"Upgrade", "echo"},
		{"Connection", "Upgrade"},
		{"Connection", "keep-alive", "Upgrade", "echoes"},
	} {
		r := do(t, a, "GET", "/ws", h...)
		if r.Code != 426 || r.Header().Get("Upgrade") != "echo" || r.Header().Get("Connection") != "Upgrade" {
			t.Errorf("%q: %d %v", h, r.Code, r.Header())
		}
	}
	if r := do(t, a, "GET", "/bad"); r.Code != 500 {
		t.Errorf("misbuilt, plain request: %d", r.Code)
	}

	srv := httptest.NewServer(a)
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: x\r\nConnection: keep-alive, UPGRADE\r\nUpgrade: other, Echo/2\r\n\r\n")
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 101 || res.Header.Get("Upgrade") != "echo" || res.Header.Get("Connection") != "Upgrade" || res.Header.Get("X-Echo") != "1" {
		t.Fatal(res.StatusCode, res.Header)
	}
	io.WriteString(conn, "hi\n")
	if line, _ := br.ReadString('\n'); line != "echo: hi\n" {
		t.Fatalf("%q", line)
	}
}
