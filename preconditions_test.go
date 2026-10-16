package geta_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// A GET or QUERY whose input embeds no Conditional is safe (RFC 9110 §9.2.1),
// so its handler runs first, and the request's preconditions are evaluated
// against what it answered: its status, and the validators its response
// carries. "A server MUST ignore all received preconditions if its response
// to the same request without those conditions ... would have been a status
// code other than a 2xx" (§13.2.1), so the handler's own answers come first.
// A write evaluates them in its handler, after its own checks and before it
// acts, so geta.New refuses one whose input embeds no Conditional.

type hookIn struct {
	ID string `path:"id" schema:"maxLength=3"`
}

type hookWriteIn struct {
	geta.Conditional
	ID string `path:"id" schema:"maxLength=3"`
}

type hookPutIn struct {
	geta.Conditional
	ID   string `path:"id" schema:"maxLength=3"`
	Body hook   `body:"json"`
}

type hook struct {
	URL string `json:"url" schema:"minLength=1"`
}

var errNoHook = errors.New("no such hook")

// hookTable serves /hooks/{id}, where only "a" exists: a GET with no
// Conditional and no validator, and a PUT with a body and a DELETE that check
// the preconditions once they have found the target; and /accepted, a GET
// answering 202. ran counts the handlers run, and acted the writes that acted.
func hookTable(ran, acted *atomic.Int32) geta.Table {
	notFound := geta.Doc{Failures: []geta.Failure{geta.On(errNoHook, http.StatusNotFound, "no such hook")}}
	find := func(id string) error {
		ran.Add(1)
		if id != "a" {
			return errNoHook
		}
		return nil
	}
	write := func(c *geta.Conditional, id string) error {
		if err := find(id); err != nil {
			return err
		}
		if err := c.Check("", time.Time{}); err != nil {
			return err
		}
		acted.Add(1)
		return nil
	}
	return geta.Table{Routes: []geta.Entry{
		{Path: "/hooks/{id}", Route: geta.Route{
			Get: geta.Op(http.StatusOK, func(_ context.Context, in *hookIn) (*ok, error) {
				if err := find(in.ID); err != nil {
					return nil, err
				}
				return &ok{true}, nil
			}, notFound),
			Put: geta.OpNoBody(http.StatusNoContent, func(_ context.Context, in *hookPutIn) error {
				return write(&in.Conditional, in.ID)
			}, notFound),
			Delete: geta.OpNoBody(http.StatusNoContent, func(_ context.Context, in *hookWriteIn) error {
				return write(&in.Conditional, in.ID)
			}, notFound),
		}},
		{Path: "/accepted", Route: geta.Route{Get: geta.Op(http.StatusAccepted, func(context.Context, *empty) (*ok, error) {
			ran.Add(1)
			return &ok{true}, nil
		}, geta.Doc{})}},
	}}
}

// hookReads is hookTable without its writes.
func hookReads(ran, acted *atomic.Int32) geta.Table {
	tbl := hookTable(ran, acted)
	tbl.Routes[0].Route.Put, tbl.Routes[0].Route.Delete = geta.Operation{}, geta.Operation{}
	return tbl
}

// RFC 9110 field by field, on a GET that declares no validator: the
// representation it answers has no entity tag and no modification date. Every
// status is one the document lists: getatest fails any other.
func TestPreconditionsOfAGetWithoutConditional(t *testing.T) {
	var ran, acted atomic.Int32
	c := getatest.New(t, hookTable(&ran, &acted))
	for _, tc := range []struct {
		name    string
		headers []string
		method  string
		path    string
		want    int
		runs    bool
	}{
		// §13.1.1: no listed tag matches a representation that has none.
		{"If-Match a tag", header("If-Match", `"x"`), "GET", "/hooks/a", 412, true},
		{"If-Match a list", header("If-Match", `"x", "y"`), "GET", "/hooks/a", 412, true},
		{"If-Match a tag on HEAD", header("If-Match", `"x"`), "HEAD", "/hooks/a", 412, true},
		{"If-Match *", header("If-Match", "*"), "GET", "/hooks/a", 200, true},
		// §13.2.1: the handler's 404 takes precedence, whatever is sent.
		{"If-Match a tag on a missing target", header("If-Match", `"x"`), "GET", "/hooks/b", 404, true},
		{"If-Match * on a missing target", header("If-Match", "*"), "GET", "/hooks/b", 404, true},
		{"If-None-Match * on a missing target", header("If-None-Match", "*"), "GET", "/hooks/b", 404, true},
		// §13.1.2: a list holds, "*" matches the representation: 304.
		{"If-None-Match a tag", header("If-None-Match", `"x"`), "GET", "/hooks/a", 200, true},
		{"If-None-Match *", header("If-None-Match", "*"), "GET", "/hooks/a", 304, true},
		{"If-None-Match * on HEAD", header("If-None-Match", "*"), "HEAD", "/hooks/a", 304, true},
		// §15.4.5: a 304 stands only for a 200; If-Match still holds a 202.
		{"If-None-Match * on a 202", header("If-None-Match", "*"), "GET", "/accepted", 202, true},
		{"If-Match a tag on a 202", header("If-Match", `"x"`), "GET", "/accepted", 412, true},
		// §13.1.3, §13.1.4: no modification date, so the dates are ignored.
		{"If-Unmodified-Since", header("If-Unmodified-Since", dateEarly), "GET", "/hooks/a", 200, true},
		{"If-Modified-Since", header("If-Modified-Since", dateLate), "GET", "/hooks/a", 200, true},
		// Read as Check reads them: a value that is no list is a 400, and
		// several lines are one list, "*" twice no "*".
		{"If-Match neither * nor a tag", header("If-Match", "x"), "GET", "/hooks/a", 400, true},
		{"If-None-Match neither * nor a tag", header("If-None-Match", "x"), "GET", "/hooks/a", 400, true},
		// What is refused before the handler is answered whatever the
		// preconditions.
		{"a parameter refused", header("If-Match", `"x"`), "GET", "/hooks/abcd", 400, false},
		// §13.2.1: OPTIONS ignores them.
		{"OPTIONS", header("If-Match", `"x"`), "OPTIONS", "/hooks/a", 204, false},
	} {
		cl := c
		for i := 0; i+1 < len(tc.headers); i += 2 {
			cl = cl.With(tc.headers[i], tc.headers[i+1])
		}
		before := ran.Load()
		res := cl.Do(tc.method, tc.path, nil)
		if res.Status != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, res.Status, res.Text(), tc.want)
		}
		if runs := ran.Load() > before; runs != tc.runs {
			t.Errorf("%s: the handler ran: %v", tc.name, runs)
		}
		if res.Status == http.StatusNotModified && (len(res.Body) != 0 || res.Header.Get("ETag") != "") {
			t.Errorf("%s: a 304 with a body or a tag: %v %q", tc.name, res.Header, res.Body)
		}
	}
}

// A write's preconditions are its handler's to evaluate, after its own
// checks: a missing target is its 404 whatever is sent (RFC 9110 §13.2.1;
// If-None-Match: * even holds there, §13.1.2), and a failed precondition
// writes nothing (§13.1.1 "MUST NOT perform the requested method").
func TestPreconditionsOfAWriteFollowItsChecks(t *testing.T) {
	var ran, acted atomic.Int32
	c := getatest.New(t, hookTable(&ran, &acted))
	valid := map[string]any{"url": "https://example.com"}
	invalid := map[string]any{"url": ""}
	for _, tc := range []struct {
		name    string
		headers []string
		method  string
		path    string
		body    any
		want    int
		acts    bool
	}{
		{"If-Match a tag on a missing target", header("If-Match", `"x"`), "DELETE", "/hooks/b", nil, 404, false},
		{"If-None-Match * on a missing target", header("If-None-Match", "*"), "DELETE", "/hooks/b", nil, 404, false},
		{"If-Match * on a missing target", header("If-Match", "*"), "DELETE", "/hooks/b", nil, 404, false},
		{"no precondition on a missing target", nil, "DELETE", "/hooks/b", nil, 404, false},
		{"If-Match a tag", header("If-Match", `"x"`), "DELETE", "/hooks/a", nil, 412, false},
		{"If-None-Match *", header("If-None-Match", "*"), "DELETE", "/hooks/a", nil, 412, false},
		{"If-Match *", header("If-Match", "*"), "DELETE", "/hooks/a", nil, 204, true},
		{"If-None-Match a tag", header("If-None-Match", `"x"`), "DELETE", "/hooks/a", nil, 204, true},
		{"If-Unmodified-Since with no date to compare", header("If-Unmodified-Since", dateEarly), "DELETE", "/hooks/a", nil, 204, true},
		{"no precondition", nil, "DELETE", "/hooks/a", nil, 204, true},
		{"If-Match a tag on PUT", header("If-Match", `"x"`), "PUT", "/hooks/a", valid, 412, false},
		{"If-Match a tag on PUT, a missing target", header("If-Match", `"x"`), "PUT", "/hooks/b", valid, 404, false},
		// A body bound before the handler is validated before its checks;
		// geta.Deferred has the handler answer first (below).
		{"an invalid body", header("If-Match", `"x"`), "PUT", "/hooks/a", invalid, 400, false},
		{"a body of another media type", []string{"If-Match", `"x"`, "Content-Type", "text/plain"}, "PUT", "/hooks/a", "url", 415, false},
	} {
		cl := c
		for i := 0; i+1 < len(tc.headers); i += 2 {
			cl = cl.With(tc.headers[i], tc.headers[i+1])
		}
		before := acted.Load()
		res := cl.Do(tc.method, tc.path, tc.body)
		if res.Status != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, res.Status, res.Text(), tc.want)
		}
		if acts := acted.Load() > before; acts != tc.acts {
			t.Errorf("%s: the write acted: %v", tc.name, acts)
		}
	}
}

// A write whose input embeds none of Conditional, RequireConditional, and
// RequireAbsent takes no precondition: a request carrying one does not match
// its contract and is a 400 before the handler runs, so nothing is written
// and a missing target is never a 412. Where the route's GET declares a
// validator, a client may send its tag back, so geta.New refuses a PUT,
// PATCH, or DELETE there that takes none.

type itemIn struct {
	ID string `path:"id"`
}

type itemWriteIn struct {
	geta.Conditional
	itemIn
}

type itemRequireIn struct {
	geta.RequireConditional
	itemIn
}

type itemAbsentIn struct {
	geta.RequireAbsent
	itemIn
}

// itemDeepIn embeds Conditional through a struct it embeds.
type itemDeepIn struct{ itemWriteIn }

type itemBody struct {
	Name string `json:"name"`
}

type itemTagged struct {
	ETag string   `header:"ETag"`
	Body itemBody `body:"json"`
}

// itemWrite builds an operation of method with input In.
func itemWrite[In any](method string) geta.Route {
	h := func(context.Context, *In) error { return nil }
	op := geta.OpNoBody(http.StatusNoContent, h, geta.Doc{})
	switch method {
	case http.MethodPost:
		return geta.Route{Post: op}
	case http.MethodPut:
		return geta.Route{Put: op}
	case http.MethodPatch:
		return geta.Route{Patch: op}
	}
	return geta.Route{Delete: op}
}

var writeMethods = []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

type itemNoteIn struct {
	ID   string   `path:"id" schema:"maxLength=3"`
	Body itemBody `body:"json"`
}

// A write without Conditional takes no precondition: each of the four
// fields is a 400 naming it, before the handler runs, beside the request's
// other violations; a request without one is served. The document states
// none of the four as a parameter, and the 400 for them; no 412.
func TestAWriteWithoutConditionalTakesNoPrecondition(t *testing.T) {
	var ran int
	h := func(context.Context, *itemNoteIn) error { ran++; return nil }
	c := getatest.New(t, one("/items/{id}", geta.Route{
		Post:   geta.OpNoBody(http.StatusNoContent, h, geta.Doc{}),
		Put:    geta.OpNoBody(http.StatusNoContent, h, geta.Doc{}),
		Patch:  geta.OpNoBody(http.StatusNoContent, h, geta.Doc{}),
		Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *itemIn) error { ran++; return nil }, geta.Doc{}),
	}))
	body := map[string]any{"name": "a"}
	for _, method := range writeMethods {
		b := any(body)
		if method == http.MethodDelete {
			b = nil
		}
		for _, h := range []struct{ name, value string }{
			{"If-Match", `"x"`}, {"If-Match", "*"}, {"If-None-Match", "*"}, {"If-None-Match", `"x"`},
			{"If-Modified-Since", dateEarly}, {"If-Unmodified-Since", dateEarly}, {"If-Match", ""},
		} {
			before := ran
			res := c.With(h.name, h.value).Do(method, "/items/a", b)
			p := res.Problem()
			if res.Status != 400 || ran != before || len(p.Errors) != 1 || p.Errors[0].In != "header" || p.Errors[0].Path != h.name ||
				p.Errors[0].Message != "is not taken: this operation takes no precondition (its input does not embed geta.Conditional)" {
				t.Errorf("%s %s: %d %+v, ran %v", method, h.name, res.Status, p, ran != before)
			}
		}
		if res := c.Do(method, "/items/a", b); res.Status != 204 {
			t.Errorf("%s without a precondition: %d %s", method, res.Status, res.Text())
		}
	}
	// Listed with the request's other violations, its body's included.
	res := c.With("If-Match", `"x"`).With("If-None-Match", "*").Put("/items/abcd", map[string]any{"name": 1})
	var places []string
	for _, v := range res.Problem().Errors {
		places = append(places, v.In+" "+v.Path)
	}
	if res.Status != 400 || strings.Join(places, ", ") != "path id, header If-Match, header If-None-Match, body $.name" {
		t.Errorf("with other violations: %d %v", res.Status, places)
	}
	m := doc(t, c.App())
	for _, method := range []string{"post", "put", "patch", "delete"} {
		op := at(t, m, "paths", "/items/{id}", method).(map[string]any)
		if strings.Contains(compact(t, op["parameters"]), `"in":"header"`) {
			t.Errorf("%s states a header parameter: %s", method, compact(t, op["parameters"]))
		}
		responses := op["responses"].(map[string]any)
		if _, has := responses["412"]; has {
			t.Errorf("%s states a 412", method)
		}
		if d := at(t, responses, "400", "description").(string); !strings.Contains(d, "The request carries If-Match, If-None-Match, If-Modified-Since, or If-Unmodified-Since, which this operation does not take (its input does not embed geta.Conditional)") {
			t.Errorf("%s 400: %q", method, d)
		}
	}
}

// itemGets are the ways a GET declares a validator: through its input, its
// output, or geta.ETag in its chain, each with what geta.New names.
var itemGets = []struct {
	name     string
	get      geta.Operation
	root     geta.Scope
	declares string
}{
	{"an ETag header field", geta.Op(http.StatusOK, func(context.Context, *itemIn) (*itemTagged, error) { return &itemTagged{ETag: `"v1"`}, nil }, geta.Doc{}),
		nil, "its output's ETag header field"},
	{"a Last-Modified header field", geta.Op(http.StatusOK, func(context.Context, *itemIn) (*itemStamped, error) { return &itemStamped{}, nil }, geta.Doc{}),
		nil, "its output's last-modified header field"},
	{"an ETag header field embedded", geta.Op(http.StatusOK, func(context.Context, *itemIn) (*itemNested, error) { return &itemNested{}, nil }, geta.Doc{}),
		nil, "its output's ETag header field"},
	{"Conditional", geta.Op(http.StatusOK, func(_ context.Context, in *itemWriteIn) (*itemBody, error) {
		return &itemBody{}, in.Check("", time.Time{})
	}, geta.Doc{}), nil, "its input embeds geta.Conditional"},
	{"geta.ETag in the root", geta.Op(http.StatusOK, func(context.Context, *itemIn) (*itemBody, error) { return &itemBody{}, nil }, geta.Doc{}),
		geta.Scope{geta.ETag()}, "geta.ETag tags it"},
	{"geta.ETag in the GET's scope", geta.Op(http.StatusOK, func(context.Context, *itemIn) (*itemBody, error) { return &itemBody{}, nil }, geta.Doc{Scope: geta.Scope{geta.ETag()}}),
		nil, "geta.ETag tags it"},
}

// itemStamped spells its header as a tag may: names are matched canonically.
type itemStamped struct {
	Modified string   `header:"last-modified"`
	Body     itemBody `body:"json"`
}

type itemHeaders struct {
	ETag string `header:"ETag"`
}

// itemNested carries its ETag in a struct it embeds untagged.
type itemNested struct {
	itemHeaders
	Body itemBody `body:"json"`
}

// Where a route's GET declares a validator, a client holds the tag it sent
// and may send it back in If-Match (RFC 9110 §13.1.1): a PUT, PATCH, or
// DELETE there that takes no precondition would refuse it, so geta.New
// refuses the write, naming it and what the GET declares.
func TestRefusesAWriteWithoutConditionalBesideAValidatedGet(t *testing.T) {
	for _, g := range itemGets {
		for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
			r := itemWrite[itemIn](method)
			r.Get = g.get
			rejects(t, geta.Table{Root: g.root, Routes: []geta.Entry{{Path: "/items/{id}", Route: r}}},
				method+" /items/{id} (",
				"the route's GET declares a validator ("+g.declares+"), so a client may send its tag in If-Match, "+
					"but the input embeds none of geta.Conditional, geta.RequireConditional, geta.RequireAbsent, so the operation takes no precondition and answers it 400; "+
					"embed geta.Conditional and call Check with the current validators")
		}
	}
}

// What starts: writes embedding Conditional, RequireConditional, or
// RequireAbsent, in themselves or a struct they embed, beside any validated
// GET; POST, whose content is processed by the resource's own semantics
// (RFC 9110 §9.3.3), and QUERY without one; and writes without one on a
// route whose GET declares no validator, or that has no GET (geta.ETag tags
// only a GET answering 200 whole).
func TestStartsWhereTheWritesEvaluateTheGetsValidator(t *testing.T) {
	for _, g := range itemGets {
		for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
			for name, r := range map[string]geta.Route{
				"Conditional":         itemWrite[itemWriteIn](method),
				"RequireConditional":  itemWrite[itemRequireIn](method),
				"RequireAbsent":       itemWrite[itemAbsentIn](method),
				"Conditional, deeper": itemWrite[itemDeepIn](method),
			} {
				r.Get = g.get
				if _, err := geta.New(geta.Table{Root: g.root, Routes: []geta.Entry{{Path: "/items/{id}", Route: r}}}); err != nil {
					t.Errorf("%s beside %s, %s: %v", method, g.name, name, err)
				}
			}
		}
		plain := geta.OpNoBody(http.StatusNoContent, func(context.Context, *itemIn) error { return nil }, geta.Doc{})
		query := geta.Op(http.StatusOK, func(context.Context, *itemIn) (*itemBody, error) { return &itemBody{}, nil }, geta.Doc{})
		tbl := geta.Table{Root: g.root, Routes: []geta.Entry{{Path: "/items/{id}", Route: geta.Route{Get: g.get, Post: plain, Query: query}}}}
		if _, err := geta.New(tbl, geta.WithOpenAPI(geta.OpenAPI32)); err != nil {
			t.Errorf("POST and QUERY beside %s: %v", g.name, err)
		}
	}
	plain := geta.Op(http.StatusOK, func(context.Context, *itemIn) (*itemBody, error) { return &itemBody{}, nil }, geta.Doc{})
	accepted := geta.Op(http.StatusAccepted, func(context.Context, *itemIn) (*itemBody, error) { return &itemBody{}, nil }, geta.Doc{})
	stream := geta.Op(http.StatusOK, func(context.Context, *itemIn) (*geta.Stream[change], error) { return &geta.Stream[change]{}, nil }, geta.Doc{})
	for _, c := range []struct {
		name string
		get  geta.Operation
		root geta.Scope
	}{
		{"a GET declaring no validator", plain, nil},
		{"no GET", geta.Operation{}, nil},
		{"geta.ETag beside a GET answering 202", accepted, geta.Scope{geta.ETag()}},
		{"geta.ETag beside a stream", stream, geta.Scope{geta.ETag()}},
	} {
		for _, method := range writeMethods {
			r := itemWrite[itemIn](method)
			r.Get = c.get
			if _, err := geta.New(geta.Table{Root: c.root, Routes: []geta.Entry{{Path: "/items/{id}", Route: r}}}); err != nil {
				t.Errorf("%s, %s: %v", c.name, method, err)
			}
		}
	}
}

// A precondition field is bound by geta.Conditional alone: a plain header
// field of that name would read it as a string, past RFC 9110 §13.2's
// evaluation, so geta.New refuses it, in any spelling, on any method.
func TestAPreconditionFieldBoundAsAPlainHeaderIsRefused(t *testing.T) {
	type plainIn struct {
		Tag *string `header:"if-none-match"`
	}
	rejects(t, one("/x", get(func(context.Context, *plainIn) (*ok, error) { return nil, nil })),
		`plainIn.Tag: header "if-none-match" is a precondition field (RFC 9110 §13.1); embed geta.Conditional, which binds it and evaluates it by Check`)
	type dateIn struct {
		Since string `header:"If-Modified-Since"`
	}
	rejects(t, one("/x", geta.Route{Post: geta.OpNoBody(http.StatusNoContent, func(context.Context, *dateIn) error { return nil }, geta.Doc{})}),
		`header "If-Modified-Since" is a precondition field`)
}

// GET /items/{id} sends the current tag; a DELETE embedding Conditional
// checks it: the tag the GET sent deletes (204), a stale one is a 412
// deleting nothing.
func TestAWriteBesideATaggedGetChecksItsTag(t *testing.T) {
	version := 1
	tag := func() string { return fmt.Sprintf(`"v%d"`, version) }
	deleted := 0
	a := accepts(t, one("/items/{id}", geta.Route{
		Get: geta.Op(http.StatusOK, func(context.Context, *itemIn) (*itemTagged, error) {
			return &itemTagged{ETag: tag(), Body: itemBody{Name: "a"}}, nil
		}, geta.Doc{}),
		Delete: geta.OpNoBody(http.StatusNoContent, func(_ context.Context, in *itemWriteIn) error {
			if err := in.Check(tag(), time.Time{}); err != nil {
				return err
			}
			deleted++
			version++
			return nil
		}, geta.Doc{}),
	}))
	sent := do(t, a, "GET", "/items/1").Header().Get("ETag")
	if sent != `"v1"` {
		t.Fatalf("GET sent %q", sent)
	}
	if r := do(t, a, "DELETE", "/items/1", "If-Match", sent); r.Code != 204 || deleted != 1 {
		t.Errorf("the tag the GET sent: %d %s, deleted %d", r.Code, r.Body, deleted)
	}
	if r := do(t, a, "DELETE", "/items/1", "If-Match", sent); r.Code != 412 || deleted != 1 {
		t.Errorf("a stale tag: %d %s, deleted %d", r.Code, r.Body, deleted)
	}
}

// geta's own refusals on a GET: a 412 problem naming the field, and a 304
// with no body.
func TestPreconditionAnswersWithoutConditional(t *testing.T) {
	var ran, acted atomic.Int32
	a := accepts(t, hookReads(&ran, &acted))
	rec := do(t, a, "GET", "/hooks/a", "If-Match", `"x"`)
	if rec.Code != 412 || rec.Header().Get("Content-Type") != geta.ProblemContentType ||
		!strings.Contains(rec.Body.String(), `"detail":"If-Match failed: no tag matches the current representation's"`) {
		t.Errorf("412: %d %s %s", rec.Code, rec.Header(), rec.Body)
	}
	rec = do(t, a, "GET", "/hooks/a", "If-None-Match", "*")
	if rec.Code != 304 || rec.Body.Len() != 0 || rec.Header().Get("ETag") != "" || rec.Header().Get("Content-Type") != "" {
		t.Errorf("304: %d %s %s", rec.Code, rec.Header(), rec.Body)
	}
	// Several lines are one list (RFC 9110 §5.3): "*" twice is no "*".
	if rec := do(t, a, "GET", "/hooks/a", "If-Match", "*", "If-Match", "*"); rec.Code != 400 {
		t.Errorf("If-Match * on two lines: %d", rec.Code)
	}
}

// On a GET geta.ETag tags, the middleware evaluates them once the handler has
// answered: "*" is a 304 where the target exists, and the handler's 404 where
// it does not.
func TestPreconditionsBesideTheETagMiddleware(t *testing.T) {
	var ran, acted atomic.Int32
	c := getatest.New(t, withRoot(hookReads(&ran, &acted), geta.ETag()))
	if res := c.With("If-None-Match", "*").Get("/hooks/a"); res.Status != 304 || res.Header.Get("ETag") == "" {
		t.Errorf("existing: %d %s", res.Status, res.Header)
	}
	if res := c.With("If-None-Match", "*").Get("/hooks/b"); res.Status != 404 {
		t.Errorf("missing: %d", res.Status)
	}
	if res := c.With("If-Match", `"x"`).Get("/hooks/a"); res.Status != 412 {
		t.Errorf("If-Match: %d", res.Status)
	}
}

// The document states what geta reads and answers where the input embeds no
// Conditional: If-Match and If-None-Match as optional header parameters, and
// the dates only where the output carries a Last-Modified to compare them
// with (If-Modified-Since on a GET alone); 412 and 400, and 304 on a GET
// that answers 200. OPTIONS lists none of them, nor does a write, whose
// parameters are its Conditional's.
func TestPreconditionsWithoutConditionalAreDocumented(t *testing.T) {
	type datedOut struct {
		Modified string   `header:"Last-Modified"`
		Body     itemBody `body:"json"`
	}
	var ran, acted atomic.Int32
	tbl := hookTable(&ran, &acted)
	tbl.Routes = append(tbl.Routes,
		geta.Entry{Path: "/dated", Route: geta.Route{
			Get:   geta.Op(http.StatusOK, func(context.Context, *empty) (*datedOut, error) { return &datedOut{}, nil }, geta.Doc{}),
			Query: geta.Op(http.StatusOK, func(context.Context, *empty) (*datedOut, error) { return &datedOut{}, nil }, geta.Doc{}),
		}},
		geta.Entry{Path: "/q", Route: geta.Route{Query: geta.Op(http.StatusOK, okHandler, geta.Doc{})}},
	)
	a, err := geta.New(tbl, geta.WithOpenAPI(geta.OpenAPI32))
	if err != nil {
		t.Fatal(err)
	}
	m := doc(t, a)
	const against = ", against the validators the response carries (its input does not embed geta.Conditional)"
	params := func(path, method string) string {
		var names []string
		ps, _ := at(t, m, "paths", path, method).(map[string]any)["parameters"].([]any)
		for _, p := range ps {
			if p := p.(map[string]any); p["in"] == "header" {
				names = append(names, fmt.Sprint(p["name"], " required=", p["required"]))
			}
		}
		return strings.Join(names, ", ")
	}
	for _, c := range []struct {
		path, method, params string
		want                 map[string]string
		not                  []string
	}{
		{"/hooks/{id}", "get", "If-Match required=false, If-None-Match required=false", map[string]string{
			"304": "The representation has not changed: If-None-Match" + against,
			"412": "A precondition failed: If-Match" + against,
			"400": `The request does not match its contract; If-Match or If-None-Match is neither "*" nor a list of one or more entity tags`,
		}, nil},
		{"/accepted", "get", "If-Match required=false, If-None-Match required=false", map[string]string{
			"412": "A precondition failed: If-Match" + against,
		}, []string{"304"}},
		{"/dated", "get", "If-Match required=false, If-Unmodified-Since required=false, If-None-Match required=false, If-Modified-Since required=false", map[string]string{
			"304": "The representation has not changed: If-None-Match, or If-Modified-Since" + against,
			"412": "A precondition failed: If-Match, or If-Unmodified-Since" + against,
		}, nil},
		{"/dated", "query", "If-Match required=false, If-Unmodified-Since required=false, If-None-Match required=false", map[string]string{
			"412": "A precondition failed: If-Match, If-Unmodified-Since, or If-None-Match" + against,
		}, []string{"304"}},
		{"/q", "query", "If-Match required=false, If-None-Match required=false", map[string]string{
			"412": "A precondition failed: If-Match, or If-None-Match" + against,
		}, []string{"304"}},
		{"/hooks/{id}", "options", "", nil, []string{"304", "412"}},
		{"/hooks/{id}", "delete", "If-Match required=false, If-None-Match required=false, If-Modified-Since required=false, If-Unmodified-Since required=false", nil, []string{"304"}},
	} {
		if got := params(c.path, c.method); got != c.params {
			t.Errorf("%s %s: parameters %q, want %q", c.method, c.path, got, c.params)
		}
		responses := at(t, m, "paths", c.path, c.method, "responses").(map[string]any)
		for s, want := range c.want {
			if got := at(t, responses, s, "description"); got != want {
				t.Errorf("%s %s %s: %q, want %q", c.method, c.path, s, got, want)
			}
		}
		for _, s := range c.not {
			if _, has := responses[s]; has {
				t.Errorf("%s %s lists %s", c.method, c.path, s)
			}
		}
	}
	if got := at(t, m, "paths", "/hooks/{id}", "get", "parameters").([]any)[1].(map[string]any)["description"].(string); !strings.Contains(got, "Evaluated once the handler has answered, and only on a 2xx") {
		t.Errorf("If-None-Match: %q", got)
	}
	if _, has := at(t, m, "paths", "/hooks/{id}", "get", "responses", "304").(map[string]any)["headers"]; has {
		t.Error("a 304 with no validator to carry states headers")
	}
	tagged := doc(t, accepts(t, withRoot(hookReads(&ran, &acted), geta.ETag())))
	if got := at(t, tagged, "paths", "/hooks/{id}", "get", "responses", "304", "description"); got != "The representation has not changed" {
		t.Errorf("a tagged GET's 304: %q", got)
	}
}

// A GET whose input embeds no Conditional but which declares a validator
// through its output or geta.ETag: the preconditions are evaluated against
// the validators its response carries, in §13.2.2's order: If-Match with the
// tag the GET sends holds (§13.1.1), and If-None-Match matching it is a 304
// (§13.1.2). Every status is one the document lists: getatest fails any
// other.

type itemDated struct {
	Modified string   `header:"Last-Modified"`
	Body     itemBody `body:"json"`
}

type itemEither struct {
	Status int      `status:"200|202"`
	ETag   string   `header:"ETag"`
	Body   itemBody `body:"json"`
}

// selectingTable serves /tagged/{id} (an ETag header field), /computed/{id}
// (geta.ETag in the GET's scope, no header field), /dated/{id} (a
// Last-Modified header field alone), and /either/{id} (an ETag header field,
// answering 202 for id "later"), where only "a" and "later" exist. ran counts
// the handlers run.
func selectingTable(ran *atomic.Int32) geta.Table {
	notFound := geta.Doc{Failures: []geta.Failure{geta.On(errNoHook, http.StatusNotFound, "no such item")}}
	find := func(id string) error {
		ran.Add(1)
		if id != "a" && id != "later" {
			return errNoHook
		}
		return nil
	}
	computed := notFound
	computed.Scope = geta.Scope{geta.ETag()}
	return geta.Table{Routes: []geta.Entry{
		{Path: "/tagged/{id}", Route: geta.Route{Get: geta.Op(http.StatusOK, func(_ context.Context, in *itemIn) (*itemTagged, error) {
			if err := find(in.ID); err != nil {
				return nil, err
			}
			return &itemTagged{ETag: current, Body: itemBody{Name: "a"}}, nil
		}, notFound)}},
		{Path: "/computed/{id}", Route: geta.Route{Get: geta.Op(http.StatusOK, func(_ context.Context, in *itemIn) (*itemBody, error) {
			if err := find(in.ID); err != nil {
				return nil, err
			}
			return &itemBody{Name: "a"}, nil
		}, computed)}},
		{Path: "/dated/{id}", Route: geta.Route{Get: geta.Op(http.StatusOK, func(_ context.Context, in *itemIn) (*itemDated, error) {
			if err := find(in.ID); err != nil {
				return nil, err
			}
			return &itemDated{Modified: dateExact, Body: itemBody{Name: "a"}}, nil
		}, notFound)}},
		{Path: "/either/{id}", Route: geta.Route{Get: geta.Op(http.StatusOK, func(_ context.Context, in *itemIn) (*itemEither, error) {
			if err := find(in.ID); err != nil {
				return nil, err
			}
			out := &itemEither{ETag: current, Body: itemBody{Name: "a"}}
			if in.ID == "later" {
				out.Status = http.StatusAccepted
			}
			return out, nil
		}, notFound)}},
	}}
}

func TestAGetChecksTheValidatorsItAnswers(t *testing.T) {
	var ran atomic.Int32
	c := getatest.New(t, selectingTable(&ran))
	computed := c.Get("/computed/a").Header.Get("ETag")
	if computed == "" {
		t.Fatal("geta.ETag sent no tag")
	}
	for _, tc := range []struct {
		name    string
		method  string
		path    string
		headers []string
		want    int
		etag    string // the ETag the response carries, "" for none
	}{
		// §13.1.1: the tag the GET sends matches, any other does not.
		{"If-Match the tag", "GET", "/tagged/a", header("If-Match", current), 200, current},
		{"If-Match a list holding it", "GET", "/tagged/a", header("If-Match", `"x", `+current), 200, current},
		{"If-Match another tag", "GET", "/tagged/a", header("If-Match", `"x"`), 412, ""},
		{"If-Match its weak form", "GET", "/tagged/a", header("If-Match", "W/"+current), 412, ""},
		{"If-Match *", "GET", "/tagged/a", header("If-Match", "*"), 200, current},
		{"If-Match neither * nor a tag", "GET", "/tagged/a", header("If-Match", "x"), 400, ""},
		// §13.1.2: a 304 carrying the tag, for GET and HEAD alike.
		{"If-None-Match the tag", "GET", "/tagged/a", header("If-None-Match", current), 304, current},
		{"If-None-Match its weak form", "GET", "/tagged/a", header("If-None-Match", "W/"+current), 304, current},
		{"If-None-Match *", "GET", "/tagged/a", header("If-None-Match", "*"), 304, current},
		{"If-None-Match another tag", "GET", "/tagged/a", header("If-None-Match", `"x"`), 200, current},
		{"If-None-Match on HEAD", "HEAD", "/tagged/a", header("If-None-Match", current), 304, current},
		{"If-Match another tag on HEAD", "HEAD", "/tagged/a", header("If-Match", `"x"`), 412, ""},
		// §13.2.1: the handler's 404 takes precedence.
		{"If-Match on a missing target", "GET", "/tagged/b", header("If-Match", current), 404, ""},
		{"If-None-Match * on a missing target", "GET", "/tagged/b", header("If-None-Match", "*"), 404, ""},
		// geta.ETag's tag, which only the body decides.
		{"If-Match geta.ETag's tag", "GET", "/computed/a", header("If-Match", computed), 200, computed},
		{"If-Match another tag than geta.ETag's", "GET", "/computed/a", header("If-Match", `"x"`), 412, ""},
		{"If-Match neither * nor a tag, geta.ETag", "GET", "/computed/a", header("If-Match", "x"), 400, ""},
		{"If-None-Match geta.ETag's tag", "GET", "/computed/a", header("If-None-Match", computed), 304, computed},
		{"If-Match on a missing target, geta.ETag", "GET", "/computed/b", header("If-Match", computed), 404, ""},
		// §13.1.3, §13.1.4: the date the GET sends, with no entity tag.
		{"If-Modified-Since the date", "GET", "/dated/a", header("If-Modified-Since", dateExact), 304, ""},
		{"If-Modified-Since earlier", "GET", "/dated/a", header("If-Modified-Since", dateEarly), 200, ""},
		{"If-Unmodified-Since earlier", "GET", "/dated/a", header("If-Unmodified-Since", dateEarly), 412, ""},
		{"If-Unmodified-Since the date", "GET", "/dated/a", header("If-Unmodified-Since", dateExact), 200, ""},
		{"If-Match a tag, with no entity tag", "GET", "/dated/a", header("If-Match", current), 412, ""},
		// §15.4.5: a 304 stands only for a 200.
		{"If-None-Match the tag on a 202", "GET", "/either/later", header("If-None-Match", current), 202, current},
		{"If-Match another tag on a 202", "GET", "/either/later", header("If-Match", `"x"`), 412, ""},
		{"If-None-Match the tag on its 200", "GET", "/either/a", header("If-None-Match", current), 304, current},
	} {
		cl := c
		for i := 0; i+1 < len(tc.headers); i += 2 {
			cl = cl.With(tc.headers[i], tc.headers[i+1])
		}
		before := ran.Load()
		res := cl.Do(tc.method, tc.path, nil)
		if res.Status != tc.want || res.Header.Get("ETag") != tc.etag {
			t.Errorf("%s: %d ETag %q %s, want %d ETag %q", tc.name, res.Status, res.Header.Get("ETag"), res.Text(), tc.want, tc.etag)
		}
		if res.Status == http.StatusNotModified && len(res.Body) != 0 {
			t.Errorf("%s: a 304 with a body", tc.name)
		}
		if ran.Load() == before {
			t.Errorf("%s: the handler did not run", tc.name)
		}
	}
	if res := c.With("If-Modified-Since", dateExact).Get("/dated/a"); res.Header.Get("Last-Modified") != dateExact {
		t.Errorf("a 304 without the date: %v", res.Header)
	}
}

// The document states what a GET that checks its validators answers: 304
// where it answers 200, 412, and 400, and the validators on its 304.
func TestAGetCheckingItsValidatorsIsDocumented(t *testing.T) {
	var ran atomic.Int32
	m := doc(t, accepts(t, selectingTable(&ran)))
	const against = ", against the validators the response carries (its input does not embed geta.Conditional)"
	for path, want := range map[string]map[string]string{
		"/tagged/{id}": {
			"304": "The representation has not changed: If-None-Match" + against,
			"412": "A precondition failed: If-Match" + against,
		},
		"/computed/{id}": {
			"304": "The representation has not changed",
			"412": "A precondition failed: If-Match" + against,
		},
		"/dated/{id}": {
			"304": "The representation has not changed: If-None-Match, or If-Modified-Since" + against,
			"412": "A precondition failed: If-Match, or If-Unmodified-Since" + against,
		},
	} {
		for s, d := range want {
			if got := at(t, m, "paths", path, "get", "responses", s, "description"); got != d {
				t.Errorf("%s %s: %q, want %q", path, s, got, d)
			}
		}
		if got := at(t, m, "paths", path, "get", "responses", "400", "description").(string); !strings.Contains(got, `If-Match`) {
			t.Errorf("%s 400: %q", path, got)
		}
	}
	// The 304 states the validator the 200 carries, typed as the 200 states it.
	for path, name := range map[string]string{"/tagged/{id}": "ETag", "/dated/{id}": "Last-Modified"} {
		headers := at(t, m, "paths", path, "get", "responses", "304", "headers").(map[string]any)
		if len(headers) != 1 || compact(t, at(t, headers, name, "schema")) != compact(t, at(t, m, "paths", path, "get", "responses", "200", "headers", name, "schema")) {
			t.Errorf("%s 304 headers: %s", path, compact(t, headers))
		}
	}
	if got := at(t, m, "paths", "/dated/{id}", "get", "responses", "304", "headers", "Last-Modified", "description"); got != "The modification date the 200 would carry, when it carries no entity tag" {
		t.Errorf("/dated 304 Last-Modified: %q", got)
	}
}

// A 304 carries the header fields of the 200 it stands for among
// Content-Location, Date, ETag, Vary, Cache-Control, and Expires (RFC 9110
// §15.4.5 "MUST"), and the document states them on the 304, typed as on the
// 200: where geta evaluates the preconditions against the output, where
// geta.ETag does, where Check does and the handler returns the output with
// its error, and on a stream.

type cachedOut struct {
	ETag         string   `header:"ETag"`
	CacheControl string   `header:"Cache-Control"`
	Vary         string   `header:"Vary"`
	Expires      *string  `header:"Expires"`
	Location     string   `header:"Content-Location"`
	Other        string   `header:"X-Other"`
	Body         itemBody `body:"json"`
	Modified     *string  `header:"Last-Modified"`
}

func cached() *cachedOut {
	return &cachedOut{ETag: current, CacheControl: "max-age=60", Vary: "Accept-Language", Expires: new("Thu, 01 Jan 2099 00:00:00 GMT"),
		Location: "/items/a", Other: "x", Body: itemBody{Name: "a"}}
}

func TestANotModifiedCarriesWhatThe200Would(t *testing.T) {
	type cacheOnly struct {
		CacheControl string   `header:"Cache-Control"`
		Body         itemBody `body:"json"`
	}
	var returned bool // whether /checked returns the output with its 304
	tbl := geta.Table{Routes: []geta.Entry{
		{Path: "/selected", Route: get(func(context.Context, *empty) (*cachedOut, error) { return cached(), nil })},
		{Path: "/checked", Route: get(func(_ context.Context, in *condIn) (*cachedOut, error) {
			if err := in.Check(current, time.Time{}); err != nil {
				if returned {
					return cached(), err
				}
				return nil, err
			}
			return cached(), nil
		})},
		{Path: "/cache-only", Route: get(func(_ context.Context, in *condIn) (*cacheOnly, error) {
			if err := in.Check(current, time.Time{}); err != nil {
				return &cacheOnly{CacheControl: "no-store"}, err
			}
			return &cacheOnly{CacheControl: "no-store"}, nil
		})},
		{Path: "/tagged", Route: get(func(_ context.Context, in *condIn) (*itemTagged, error) {
			if err := in.Check(current, time.Time{}); err != nil {
				return nil, err // nothing but the tag to carry, which Check has
			}
			return &itemTagged{ETag: current}, nil
		})},
		{Path: "/stream", Route: get(func(context.Context, *empty) (*geta.Stream[change], error) {
			return &geta.Stream[change]{Events: func(func(change) bool) {}}, nil
		})},
		{Path: "/etag", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *empty) (*cacheOnly, error) {
			return &cacheOnly{CacheControl: "max-age=5"}, nil
		}, geta.Doc{Scope: geta.Scope{geta.ETag()}})}},
	}}
	c := getatest.New(t, tbl)
	carries := func(name string, res *getatest.Response, want map[string]string) {
		t.Helper()
		if res.Status != http.StatusNotModified || len(res.Body) != 0 {
			t.Errorf("%s: %d %s", name, res.Status, res.Text())
			return
		}
		for k, v := range want {
			if got := res.Header.Get(k); got != v {
				t.Errorf("%s: %s %q, want %q", name, k, got, v)
			}
		}
	}
	all := map[string]string{"ETag": current, "Cache-Control": "max-age=60", "Vary": "Accept-Language",
		"Expires": "Thu, 01 Jan 2099 00:00:00 GMT", "Content-Location": "/items/a", "X-Other": "", "Content-Type": "", "Last-Modified": ""}
	carries("geta's evaluation", c.With("If-None-Match", current).Get("/selected"), all)
	returned = true
	carries("Check, the output returned", c.With("If-None-Match", current).Get("/checked"), all)
	carries("Check, a field beside no tag", c.With("If-None-Match", current).Get("/cache-only"), map[string]string{"Cache-Control": "no-store", "ETag": current})
	carries("Check, the tag alone", c.With("If-None-Match", current).Get("/tagged"), map[string]string{"ETag": current})
	carries("a stream", c.With("If-None-Match", "*").Get("/stream"), map[string]string{"Cache-Control": "no-cache", "Content-Type": ""})
	carries("geta.ETag", c.With("If-None-Match", "*").Get("/etag"), map[string]string{"Cache-Control": "max-age=5"})
	// Where the output declares such a field, a 304 without the output
	// would drop it: a defect.
	returned = false
	if res := c.With("If-None-Match", current).Get("/checked"); res.Status != 500 {
		t.Errorf("a 304 without the output: %d %s", res.Status, res.Text())
	}
	// The output is read only for a 304.
	returned = true
	if res := c.With("If-Match", `"x"`).Get("/checked"); res.Status != 412 || res.Header.Get("Cache-Control") != "" {
		t.Errorf("a 412: %d %v", res.Status, res.Header)
	}
	m := doc(t, c.App())
	for _, path := range []string{"/selected", "/checked"} {
		headers := at(t, m, "paths", path, "get", "responses", "304", "headers").(map[string]any)
		for _, name := range []string{"ETag", "Cache-Control", "Vary", "Expires", "Content-Location", "Last-Modified"} {
			if _, has := headers[name]; !has {
				t.Errorf("%s 304 does not state %s: %v", path, name, headers)
			}
		}
		if _, has := headers["X-Other"]; has {
			t.Errorf("%s 304 states X-Other", path)
		}
		if got := at(t, headers, "Cache-Control", "description"); got != "The Cache-Control the 200 would carry (RFC 9110 section 15.4.5)" {
			t.Errorf("%s 304 Cache-Control: %q", path, got)
		}
		if a, b := compact(t, at(t, headers, "Cache-Control", "schema")), compact(t, at(t, m, "paths", path, "get", "responses", "200", "headers", "Cache-Control", "schema")); a != b {
			t.Errorf("%s 304 Cache-Control schema %s, the 200's %s", path, a, b)
		}
	}
	if got := compact(t, at(t, m, "paths", "/stream", "get", "responses", "304", "headers")); got != `{"Cache-Control":{"description":"As the stream sends it (RFC 9110 section 15.4.5)","required":false,"schema":{"const":"no-cache","type":"string"}}}` {
		t.Errorf("/stream 304: %s", got)
	}
}

// A body declared geta.Deferred is read when the handler asks, so the
// handler's own checks answer first, in RFC 9110 §13.2.1's order: the target
// (404), then the preconditions (428, 412), then the content (400).

type deferredPutIn struct {
	geta.RequireConditional
	ID   string              `path:"id" schema:"maxLength=3"`
	Body geta.Deferred[hook] `body:"json"`
}

type plainPutIn struct {
	geta.RequireConditional
	ID   string `path:"id"`
	Body hook   `body:"json"`
}

// deferredTable serves PUT /d/{id} with a Deferred body and PUT /p/{id} with
// a plain one, both where only "a" exists, tagged "v2". got receives the
// body the handler read.
func deferredTable(got *hook, limits func(geta.Limits) geta.Limits) geta.Table {
	notFound := []geta.Failure{geta.On(errNoHook, http.StatusNotFound, "no such hook")}
	return geta.Table{Routes: []geta.Entry{
		{Path: "/d/{id}", Route: geta.Route{Put: geta.OpNoBody(http.StatusNoContent, func(_ context.Context, in *deferredPutIn) error {
			if in.ID != "a" {
				return errNoHook
			}
			if err := in.Check(current, time.Time{}); err != nil {
				return err
			}
			h, err := in.Body.Value()
			if err != nil {
				return fmt.Errorf("reading the hook: %w", err) // answered as is, wrapped or not
			}
			*got = h
			return nil
		}, geta.Doc{Failures: notFound, Limits: limits})}},
		{Path: "/p/{id}", Route: geta.Route{Put: geta.OpNoBody(http.StatusNoContent, func(_ context.Context, in *plainPutIn) error {
			if in.ID != "a" {
				return errNoHook
			}
			return in.Check(current, time.Time{})
		}, geta.Doc{Failures: notFound})}},
	}}
}

func TestDeferredBodyIsReadAfterTheHandlersChecks(t *testing.T) {
	var got hook
	c := getatest.New(t, deferredTable(&got, nil))
	valid := map[string]any{"url": "https://example.com"}
	invalid := map[string]any{"url": "", "x": 1}
	for _, tc := range []struct {
		name, ifMatch, path string
		body                any
		want                int
	}{
		{"a missing target", current, "/d/b", invalid, 404},
		{"a stale precondition", `"v1"`, "/d/a", invalid, 412},
		{"no precondition", "", "/d/a", invalid, 428},
		{"an invalid body", current, "/d/a", invalid, 400},
		{"a valid body", current, "/d/a", valid, 204},
		// A plain body is bound before the handler runs.
		{"a plain body, missing target", current, "/p/b", invalid, 400},
		{"a plain body, stale precondition", `"v1"`, "/p/a", invalid, 400},
	} {
		cl := c
		if tc.ifMatch != "" {
			cl = c.With("If-Match", tc.ifMatch)
		}
		res := cl.Put(tc.path, tc.body)
		if res.Status != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, res.Status, res.Text(), tc.want)
		}
	}
	if got.URL != "https://example.com" {
		t.Errorf("the handler read %+v", got)
	}
	// Its 400 lists every violation of the body, as one bound before.
	p := c.With("If-Match", current).Put("/d/a", invalid).Problem()
	if p.Detail != "the request does not match its contract" || len(p.Errors) != 2 || p.Errors[0].In != "body" {
		t.Errorf("400: %+v", p)
	}
}

// What the headers and the first byte show is still answered before the
// handler: a content coding or another media type (415), a declared length
// past MaxBodyBytes (413), a missing body (400). So is a request whose
// parameters are refused, its 400 listing the body's violations too.
func TestDeferredBodyIsJudgedBeforeTheHandler(t *testing.T) {
	var got hook
	small := func(l geta.Limits) geta.Limits { l.MaxBodyBytes = 16; return l }
	c := getatest.New(t, deferredTable(&got, small)).With("If-Match", `"v1"`)
	if res := c.Content(http.MethodPut, "/d/a", "text/plain", []byte("x")); res.Status != 415 {
		t.Errorf("another media type: %d", res.Status)
	}
	if res := c.With("Content-Encoding", "gzip").Put("/d/a", map[string]any{"url": "u"}); res.Status != 415 {
		t.Errorf("a content coding: %d", res.Status)
	}
	if res := c.Put("/d/a", map[string]any{"url": strings.Repeat("u", 32)}); res.Status != 413 {
		t.Errorf("past MaxBodyBytes: %d", res.Status)
	}
	if res := c.Put("/d/a", nil); res.Status != 400 {
		t.Errorf("no body: %d", res.Status)
	}
	res := c.Put("/d/abcd", map[string]any{"url": ""})
	if p := res.Problem(); res.Status != 400 || len(p.Errors) != 2 || p.Errors[0].In != "path" || p.Errors[1].In != "body" {
		t.Errorf("a refused parameter: %d %+v", res.Status, p)
	}
}

type optionalDeferredIn struct {
	geta.Conditional
	Body geta.Deferred[*hook] `body:"json"`
}

// An optional body is a Deferred of a pointer: nil when none is sent. Value
// reads once and answers the same after; a Deferred built by hand holds no
// body.
func TestDeferredValue(t *testing.T) {
	var calls []string
	c := getatest.New(t, one("/o", geta.Route{Post: geta.OpNoBody(http.StatusNoContent, func(_ context.Context, in *optionalDeferredIn) error {
		if err := in.Check("", time.Time{}); err != nil {
			return err
		}
		h, err := in.Body.Value()
		again, err2 := in.Body.Value()
		if h != again || err != err2 {
			return errors.New("a second Value differs")
		}
		if err != nil {
			return err
		}
		if h == nil {
			calls = append(calls, "none")
		} else {
			calls = append(calls, h.URL)
		}
		return nil
	}, geta.Doc{})}))
	if res := c.Post("/o", nil); res.Status != 204 {
		t.Errorf("no body: %d %s", res.Status, res.Text())
	}
	if res := c.Post("/o", map[string]any{"url": "u"}); res.Status != 204 {
		t.Errorf("a body: %d %s", res.Status, res.Text())
	}
	if res := c.Post("/o", "{"); res.Status != 400 {
		t.Errorf("malformed: %d", res.Status)
	}
	if strings.Join(calls, ",") != "none,u" {
		t.Errorf("read %v", calls)
	}
	var d geta.Deferred[hook]
	if v, err := d.Value(); v != (hook{}) || err != nil {
		t.Errorf("by hand: %+v %v", v, err)
	}
}

// The document cannot tell a Deferred body from a plain one.
func TestDeferredBodyIsDocumentedAsItsType(t *testing.T) {
	type plainIn struct {
		geta.Conditional
		Body hook `body:"json" doc:"the hook"`
	}
	type laterIn struct {
		geta.Conditional
		Body geta.Deferred[hook] `body:"json" doc:"the hook"`
	}
	h := func(context.Context, *plainIn) error { return nil }
	l := func(context.Context, *laterIn) error { return nil }
	plain := doc(t, accepts(t, one("/x", geta.Route{Put: geta.OpNoBody(http.StatusNoContent, h, geta.Doc{OperationID: "x"})})))
	later := doc(t, accepts(t, one("/x", geta.Route{Put: geta.OpNoBody(http.StatusNoContent, l, geta.Doc{OperationID: "x"})})))
	if a, b := compact(t, plain), compact(t, later); a != b {
		t.Errorf("plain:\n%s\nDeferred:\n%s", a, b)
	}
}

type deferredQueryIn struct {
	Q geta.Deferred[string] `query:"q"`
}

type deferredFormIn struct {
	Body geta.Deferred[hook] `body:"form"`
}

type deferredPointerIn struct {
	Body *geta.Deferred[hook] `body:"json"`
}

type deferredEmbeddedIn struct {
	geta.Deferred[hook]
}

// A Deferred is a body:"json" field and nothing else.
func TestDeferredOnlyAsAJSONBody(t *testing.T) {
	post := func(r geta.Route) geta.Table { return one("/x", r) }
	rejects(t, post(geta.Route{Post: geta.OpNoBody(http.StatusNoContent, func(context.Context, *deferredQueryIn) error { return nil }, geta.Doc{})}),
		`Q: a geta.Deferred is a body:"json" field`)
	rejects(t, post(geta.Route{Post: geta.OpNoBody(http.StatusNoContent, func(context.Context, *deferredFormIn) error { return nil }, geta.Doc{})}),
		`Body: a geta.Deferred is a body:"json" field`)
	rejects(t, post(geta.Route{Post: geta.OpNoBody(http.StatusNoContent, func(context.Context, *deferredPointerIn) error { return nil }, geta.Doc{})}),
		`Body: the body is a pointer to a geta.Deferred`)
	rejects(t, post(geta.Route{Post: geta.OpNoBody(http.StatusNoContent, func(context.Context, *deferredEmbeddedIn) error { return nil }, geta.Doc{})}),
		`Deferred: a geta.Deferred is a body:"json" field`)
}
