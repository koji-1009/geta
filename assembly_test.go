package geta_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/internal/fixture/a"
	"github.com/koji-1009/geta/internal/fixture/b"
)

// Struct tags and the URL must agree, both ways.

func TestRejectsPathTagNotInURL(t *testing.T) {
	rejects(t, one("/users", get(idHandler)), "GET /users", `binds path parameter "id"`, "not in the URL")
}

func TestRejectsURLParameterNotBound(t *testing.T) {
	rejects(t, one("/users/{id}", get(okHandler)), "GET /users/{id}", "{id}", "does not bind")
}

func TestRejectsUnboundURLParameterOnEveryOperation(t *testing.T) {
	r := geta.Route{
		Get:    geta.Op(http.StatusOK, idHandler, geta.Doc{}),
		Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error { return nil }, geta.Doc{}),
	}
	rejects(t, one("/users/{id}", r), "DELETE /users/{id}", "does not bind")
}

func TestRejectsMalformedPaths(t *testing.T) {
	for _, p := range []string{"users", "/users/", "/users//x", "/users/{1d}", "/users/x{id}", "/a/{id}/b/{id}", "/files/a%20b", "/a%2Fb"} {
		t.Run(p, func(t *testing.T) {
			rejects(t, one(p, get(okHandler)), p)
		})
	}
}

func TestRejectsDuplicatePaths(t *testing.T) {
	tbl := geta.Table{Routes: []geta.Entry{
		{Path: "/x", Route: get(okHandler)},
		{Path: "/x", Route: get(okHandler)},
	}}
	rejects(t, tbl, "/x", "two entries")
}

// Two templates that differ only in parameter names denote the same paths,
// which OpenAPI treats as one path.
func TestRejectsTemplatesDifferingOnlyInNames(t *testing.T) {
	type nameIn struct {
		Name string `path:"name"`
	}
	tbl := geta.Table{Routes: []geta.Entry{
		{Path: "/users/{id}", Route: get(idHandler)},
		{Path: "/users/{name}", Route: get(func(ctx context.Context, in *nameIn) (*ok, error) { return nil, nil })},
	}}
	rejects(t, tbl, "/users/{name}", "differ only in parameter names")
}

func TestRejectsRouteServingNothing(t *testing.T) {
	rejects(t, one("/x", geta.Route{}), "/x", "serves no method")
}

func TestRejectsUntaggedInputField(t *testing.T) {
	type in struct{ Limit int }
	rejects(t, one("/x", get(func(ctx context.Context, _ *in) (*ok, error) { return nil, nil })),
		"Limit", "has no path, query, header, cookie, or body tag")
}

func TestRejectsPointerPathParameter(t *testing.T) {
	type in struct {
		ID *int `path:"id"`
	}
	rejects(t, one("/x/{id}", get(func(ctx context.Context, _ *in) (*ok, error) { return nil, nil })),
		`path parameter "id" is a pointer`)
}

// Schema mistakes are the author's, so assembly refuses them.

func TestRejectsSchemaMistakes(t *testing.T) {
	cases := map[string]struct {
		tbl  geta.Table
		want string
	}{}
	add := func(name string, tbl geta.Table, want string) {
		cases[name] = struct {
			tbl  geta.Table
			want string
		}{tbl, want}
	}
	type unknownKey struct {
		Q string `query:"q" schema:"minLenght=1"`
	}
	add("unknown keyword", one("/x", get(func(context.Context, *unknownKey) (*ok, error) { return nil, nil })), `unknown keyword "minLenght"`)
	type wrongType struct {
		Q int `query:"q" schema:"maxLength=3"`
	}
	add("keyword on the wrong type", one("/x", get(func(context.Context, *wrongType) (*ok, error) { return nil, nil })), "maxLength applies to string, not integer")
	type badPattern struct {
		Q string `query:"q" schema:"pattern=("`
	}
	add("uncompilable pattern", one("/x", get(func(context.Context, *badPattern) (*ok, error) { return nil, nil })), "does not compile")
	type minOverMax struct {
		Q string `query:"q" schema:"minLength=5,maxLength=2"`
	}
	add("min over max", one("/x", get(func(context.Context, *minOverMax) (*ok, error) { return nil, nil })), "minLength 5 exceeds maxLength 2")
	type badMultiple struct {
		Q float64 `query:"q" schema:"multipleOf=0"`
	}
	add("multipleOf zero", one("/x", get(func(context.Context, *badMultiple) (*ok, error) { return nil, nil })), "not greater than zero")
	type badNumber struct {
		Q int `query:"q" schema:"minimum=zero"`
	}
	add("non-numeric bound", one("/x", get(func(context.Context, *badNumber) (*ok, error) { return nil, nil })), `"zero" is not a number`)
	type enumOutsidePattern struct {
		Q string `query:"q" schema:"pattern=^[a-z]+$,enum=ok|NO"`
	}
	add("enum member outside pattern", one("/x", get(func(context.Context, *enumOutsidePattern) (*ok, error) { return nil, nil })), `enum member "NO"`)
	type omitempty struct {
		Name string `json:"name,omitempty"`
	}
	type bodyIn struct {
		Body omitempty `body:"json"`
	}
	add("json tag options", one("/x", get(func(context.Context, *bodyIn) (*ok, error) { return nil, nil })), `json tag option "omitempty"`)
	type withChan struct {
		C chan int `json:"c"`
	}
	add("type with no JSON form", one("/x", get(func(context.Context, *empty) (*withChan, error) { return nil, nil })), "no JSON form")
	type withAny struct {
		V any `json:"v"`
	}
	add("interface field", one("/x", get(func(context.Context, *empty) (*withAny, error) { return nil, nil })), "no JSON form")
	type structParam struct {
		Q ok `header:"q"`
	}
	add("struct as a header parameter", one("/x", get(func(context.Context, *structParam) (*ok, error) { return nil, nil })), `header parameter "q" has unsupported type`)
	type twoBodies struct {
		A ok `body:"json"`
		B ok `body:"json"`
	}
	add("two bodies", one("/x", get(func(context.Context, *twoBodies) (*ok, error) { return nil, nil })), "a second body field")
	type xmlBody struct {
		A ok `body:"xml"`
	}
	add("non-json body", one("/x", get(func(context.Context, *xmlBody) (*ok, error) { return nil, nil })),
		`unknown body tag "xml"`)
	type inner struct {
		A string `json:"a"`
	}
	type dupName struct {
		inner
		B string `json:"a"`
	}
	add("duplicate JSON names", one("/x", get(func(context.Context, *empty) (*dupName, error) { return nil, nil })), `two fields have the JSON name "a"`)
	type constraintOnStruct struct {
		Body ok `body:"json" schema:"minLength=1"`
	}
	add("constraint on a struct", one("/x", get(func(context.Context, *constraintOnStruct) (*ok, error) { return nil, nil })), "on a struct type")

	type ptrNoOmitzero struct {
		Nick *string `json:"nick"`
	}
	add("pointer without omitzero", one("/x", get(func(context.Context, *empty) (*ptrNoOmitzero, error) { return nil, nil })), "tag it `json:\"nick,omitzero\"`")
	type omitzeroNoPtr struct {
		Nick string `json:"nick,omitzero"`
	}
	add("omitzero on a required field", one("/x", get(func(context.Context, *empty) (*omitzeroNoPtr, error) { return nil, nil })), "omitzero on a non-pointer field")
	type ptrElems struct {
		Items []*ok `json:"items"`
	}
	add("pointer elements", one("/x", get(func(context.Context, *empty) (*ptrElems, error) { return nil, nil })), "element type *geta_test.ok is a pointer")
	for name, c := range cases {
		t.Run(name, func(t *testing.T) { rejects(t, c.tbl, c.want) })
	}
}

// Middleware order is a value; a descending chain names both ends.

var (
	early = geta.Ordered(geta.OrderObserve, noop)
	late  = geta.Ordered(geta.OrderAuthorize, noop)
)

func TestRejectsDescendingRootChain(t *testing.T) {
	tbl := one("/x", get(okHandler))
	tbl.Root = geta.Scope{late, early}
	rejects(t, tbl, "root scope", "observe(1000)", "authorize(9000)")
}

func TestRejectsDescendingAcrossTheScopeSeam(t *testing.T) {
	// Neither list is misordered alone; the violation is only in the chain a
	// route actually gets.
	tbl := one("/x", get(okHandler), geta.Scope{early})
	tbl.Root = geta.Scope{late}
	rejects(t, tbl, "GET /x", "observe", "authorize")
}

func TestAcceptsAscendingEqualAndUnordered(t *testing.T) {
	shedA := geta.Ordered(geta.OrderShed, noop)
	shedB := geta.Ordered(geta.OrderShed, noop)
	tbl := one("/x", get(okHandler), geta.Scope{geta.Use(noop), late})
	tbl.Root = geta.Scope{early, shedB, shedA, geta.Use(noop)}
	accepts(t, tbl)
}

func TestApplicationStageInterleaves(t *testing.T) {
	audit := geta.Ordered(geta.Order{Rank: geta.OrderAuthenticate.Rank + 500, Name: "audit"}, noop)
	tbl := one("/x", get(okHandler), geta.Scope{audit, late})
	tbl.Root = geta.Scope{geta.Ordered(geta.OrderAuthenticate, noop)}
	accepts(t, tbl)
	bad := one("/x", get(okHandler), geta.Scope{late, audit})
	rejects(t, bad, "audit(8500)", "authorize(9000)")
}

func TestVocabularyAscends(t *testing.T) {
	v := []geta.Order{geta.OrderObserve, geta.OrderCrossOrigin, geta.OrderRecover, geta.OrderShed,
		geta.OrderDeadline, geta.OrderNegotiate, geta.OrderValidate, geta.OrderAuthenticate, geta.OrderAuthorize}
	for i := 1; i < len(v); i++ {
		if v[i].Rank-v[i-1].Rank != geta.OrderSpacing {
			t.Errorf("%s must sit %d inside %s", v[i], geta.OrderSpacing, v[i-1])
		}
	}
}

func TestRejectsZeroMiddleware(t *testing.T) {
	tbl := one("/x", get(okHandler), geta.Scope{{}})
	rejects(t, tbl, "zero geta.Middleware")
}

// Failure rows are checked once, at assembly.

func TestRejectsInvalidFailureRows(t *testing.T) {
	errX := errors.New("x")
	for name, f := range map[string]geta.Failure{
		"zero row":       {},
		"nil target":     geta.On(nil, 404, ""),
		"success status": geta.On(errX, 200, ""),
		"3xx status":     geta.On(errX, 302, ""),
		"out of range":   geta.On(errX, 600, ""),
	} {
		t.Run(name, func(t *testing.T) {
			r := geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Failures: []geta.Failure{f}})}
			rejects(t, one("/x", r), "GET /x", "failure row 0")
		})
	}
}

// Status and body must agree.

func TestRejects204WithBody(t *testing.T) {
	r := geta.Route{Delete: geta.Op(http.StatusNoContent, okHandler, geta.Doc{})}
	rejects(t, one("/x", r), "DELETE /x", "204 takes no body")
}

func TestAccepts204WithoutBody(t *testing.T) {
	r := geta.Route{Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error { return nil }, geta.Doc{})}
	accepts(t, one("/x", r))
	type headersOnly struct {
		Loc string `header:"Location"`
	}
	r = geta.Route{Post: geta.Op(http.StatusNoContent, func(context.Context, *empty) (*headersOnly, error) { return nil, nil }, geta.Doc{})}
	accepts(t, one("/x", r))
}

func TestRejectsNonSuccessStatus(t *testing.T) {
	for _, s := range []int{0, 100, 304, 404, 500} {
		r := geta.Route{Get: geta.Op(s, okHandler, geta.Doc{})}
		rejects(t, one("/x", r), "is not a 2xx or 3xx status")
	}
}

// Identifiers the document keys on must be unique.

func TestRejectsDuplicateOperationID(t *testing.T) {
	tbl := geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{OperationID: "fetch"})}},
		{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{OperationID: "fetch"})}},
	}}
	rejects(t, tbl, `operationId "fetch"`, "GET /a")
}

func TestRejectsDuplicateSchemaName(t *testing.T) {
	tbl := geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: get(func(context.Context, *empty) (*a.User, error) { return nil, nil })},
		{Path: "/b", Route: get(func(context.Context, *empty) (*b.User, error) { return nil, nil })},
	}}
	rejects(t, tbl, `schema name "User"`, "internal/fixture/a.User", "internal/fixture/b.User")
}

// Security: what a route requires must be enforceable.

var admit geta.Verifier = func(r *http.Request) (context.Context, error) { return nil, nil }

func TestRejectsSchemeWithoutVerifier(t *testing.T) {
	r := geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{geta.Bearer}})}
	tbl := one("/x", r)
	tbl.Root = geta.Scope{geta.Secure(geta.Policy{})}
	rejects(t, tbl, "GET /x", `requires scheme "bearer"`, "no verifier")
}

func TestRejectsDefaultSchemeWithoutVerifier(t *testing.T) {
	tbl := one("/x", get(okHandler))
	tbl.Root = geta.Scope{geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer}})}
	rejects(t, tbl, "GET /x", `requires scheme "bearer"`)
}

func TestRejectsProtectedRouteWithoutGate(t *testing.T) {
	r := geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{geta.Bearer}})}
	rejects(t, one("/x", r), "GET /x", "requires bearer", "no geta.Secure gate")
}

func TestPublicRouteNeedsNoGate(t *testing.T) {
	r := geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}})}
	accepts(t, one("/x", r))
	accepts(t, one("/x", get(okHandler)))
}

func TestGateInADirectoryScopeCovers(t *testing.T) {
	gate := geta.Secure(geta.Policy{Verifiers: map[string]geta.Verifier{"bearer": admit}})
	r := geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{geta.Bearer}})}
	accepts(t, one("/x", r, geta.Scope{gate}))
}

func TestRejectsConflictingSchemeDefinitions(t *testing.T) {
	other := geta.Scheme{Name: "bearer", Type: "http", Scheme: "bearer", BearerFormat: "JWT"}
	gate := geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer}, Verifiers: map[string]geta.Verifier{"bearer": admit}})
	tbl := geta.Table{Root: geta.Scope{gate}, Routes: []geta.Entry{
		{Path: "/a", Route: get(okHandler)},
		{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{other}})}},
	}}
	rejects(t, tbl, `security scheme "bearer" has two definitions`)
}

func TestRejectsDerivedOperationIDCollision(t *testing.T) {
	tbl := geta.Table{Routes: []geta.Entry{
		{Path: "/user-list", Route: get(okHandler)},
		{Path: "/user/list", Route: get(okHandler)},
	}}
	rejects(t, tbl, `operationId "getUserList"`)
}
