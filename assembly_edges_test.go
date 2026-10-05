package geta_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
)

// The assembly refusals of types and declarations a table reaches only
// through a nesting: an embedded struct, a map's value, a Nullable's value, a
// sealed type's variant, a stream's event, an envelope's body, header, or
// embedded struct.

type aeBad struct{ C chan int }

type aeEmbedsBad struct {
	aeBad
	X int `json:"x"`
}

type aeMapBad struct {
	M map[string]chan int `json:"m"`
}

type aeNullBad struct {
	N geta.Nullable[aeBad] `json:"n"`
}

type aeComplex struct {
	C complex128 `json:"c"`
}

type aeInEmbedsBad struct {
	aeInBad
}

type aeInBad struct {
	Q chan int `query:"q"`
}

type aeEnvelopeBadBody struct {
	Name string `header:"X-Name"`
	Body aeBad  `body:"json"`
}

type aeShape interface{ isAEShape() }

type aeBadVariant struct {
	Kind string   `json:"kind"`
	C    chan int `json:"c"`
}

func (aeBadVariant) isAEShape() {}

type aeScene struct {
	S aeShape `json:"s"`
}

func output[Out any]() geta.Table {
	return one("/t", get(func(context.Context, *empty) (*Out, error) { return nil, nil }))
}

func input[In any]() geta.Table {
	return one("/t", get(func(context.Context, *In) (*ok, error) { return nil, nil }))
}

func TestNestedTypesWithNoJSONFormAreRefused(t *testing.T) {
	for want, tbl := range map[string]geta.Table{
		"aeEmbedsBad.C: type chan int has no JSON form":  output[aeEmbedsBad](),
		"aeMapBad.M: map[string]chan int: type chan int": output[aeMapBad](),
		"aeNullBad.N: geta_test.aeBad.C: type chan int":  output[aeNullBad](),
		"aeComplex.C: type complex128 has no JSON form":  output[aeComplex](),
		"aeEnvelopeBadBody.Body: geta_test.aeBad.C":      output[aeEnvelopeBadBody](),
		"aeInBad.Q": input[aeInEmbedsBad](),
		"stream events: geta_test.aeBad.C: type chan int": output[geta.Stream[aeBad]](),
	} {
		rejects(t, tbl, want)
	}
	tbl := output[aeScene]()
	_, err := geta.New(tbl, geta.WithUnion(geta.Sealed[aeShape]("kind", geta.Case[aeBadVariant]("bad"))))
	if err == nil || !strings.Contains(err.Error(), "geta_test.aeShape variant geta_test.aeBadVariant") || !strings.Contains(err.Error(), "type chan int") {
		t.Fatalf("%v", err)
	}
}

type (
	aeSelfMap   map[string]aeSelfMap
	aeSelfSlice []aeSelfSlice
	aeSelfAnon  map[string]struct {
		M aeSelfAnon `json:"m"`
	}
	aeSelfMapOut struct {
		M aeSelfMap `json:"m"`
	}
	aeSelfSliceOut struct {
		S aeSelfSlice `json:"s"`
	}
	aeSelfAnonOut struct {
		A aeSelfAnon `json:"a"`
	}
	aeSelfMapIn struct {
		Body aeSelfMap `body:"json"`
	}
	aeSelfStruct struct {
		K map[string]aeSelfStruct `json:"k"`
		L []aeSelfStruct          `json:"l"`
	}
	aeSelfStructOut struct {
		S aeSelfStruct `json:"s"`
	}
)

// A type that holds itself other than through a named struct type has no
// schema (only a named struct is a component to refer to), so New refuses
// it, as input and as output. Through a named struct it is taken.
func TestTypesHoldingThemselvesOutsideANamedStructAreRefused(t *testing.T) {
	const why = "holds itself other than through a named struct type"
	rejects(t, output[aeSelfMapOut](), "geta_test.aeSelfMap "+why)
	rejects(t, output[aeSelfSliceOut](), "geta_test.aeSelfSlice "+why)
	rejects(t, output[aeSelfAnonOut](), "geta_test.aeSelfAnon "+why)
	rejects(t, one("/t", post(func(context.Context, *aeSelfMapIn) (*ok, error) { return nil, nil })), "geta_test.aeSelfMap "+why)
	accepts(t, output[aeSelfStructOut]())
}

// OPTIONS, which geta serves on every template, runs the root scope alone:
// a root middleware that requires scopes of a scheme the root gates do not
// require by default refuses every OPTIONS request a gate admits, so New
// refuses it, naming the OPTIONS operation, even when every route declares
// the scheme.
func TestRootScopesWithoutARootDefaultAreRefusedForOPTIONS(t *testing.T) {
	oauth := geta.Scheme{Name: "oauth", Type: "oauth2", Flows: &geta.OAuthFlows{
		ClientCredentials: &geta.OAuthFlow{TokenURL: "https://a.example/t", Scopes: map[string]string{"read": "r"}}}}
	need := geta.Ordered(geta.OrderAuthorize, noop).Scopes(oauth, "read")
	rejects(t, geta.Table{
		Root:   geta.Scope{geta.Secure(geta.Policy{Verifiers: map[string]geta.Verifier{"oauth": bearerVerifier}}), need},
		Routes: []geta.Entry{route("/x", []geta.Scheme{oauth})},
	}, "OPTIONS /x: middleware 1 (authorize) requires scopes of scheme \"oauth\", which no earlier geta.Secure gate requires")
	accepts(t, geta.Table{
		Root:   geta.Scope{geta.Secure(geta.Policy{Default: []geta.Scheme{oauth}, Verifiers: map[string]geta.Verifier{"oauth": bearerVerifier}}), need},
		Routes: []geta.Entry{route("/x", []geta.Scheme{oauth})},
	})
}

// A scope that is empty is no scope-token; a middleware's scheme whose flows
// differ from the gate's is another definition of it.
func TestScopesMistakesAreRefused(t *testing.T) {
	flow := &geta.OAuthFlow{TokenURL: "https://a.example/t", Scopes: map[string]string{"read": "r"}}
	oauth := geta.Scheme{Name: "oauth", Type: "oauth2", Flows: &geta.OAuthFlows{ClientCredentials: flow}}
	other := geta.Scheme{Name: "oauth", Type: "oauth2", Flows: &geta.OAuthFlows{ClientCredentials: flow, Password: flow}}
	gate := geta.Secure(geta.Policy{Default: []geta.Scheme{oauth}, Verifiers: map[string]geta.Verifier{"oauth": bearerVerifier}})
	rejects(t, withRoot(one("/x", get(okHandler)), gate, geta.Ordered(geta.OrderAuthorize, noop).Scopes(oauth, "")),
		`Scopes of scheme "oauth": "" is not a scope-token`)
	rejects(t, withRoot(one("/x", get(okHandler)), gate, geta.Ordered(geta.OrderAuthorize, noop).Scopes(other, "read")),
		`defines scheme "oauth" as`, "but the gate defines it as")
}

// A problem envelope's embedded struct is read as the envelope's own fields:
// a cookie there is refused as one on the envelope is, a header there is
// documented, and a header of a type no header carries is refused.
func TestProblemEnvelopeEmbeddedFields(t *testing.T) {
	type CookieMeta struct {
		Session *http.Cookie `cookie:"sid"`
	}
	type withEmbeddedCookie struct {
		CookieMeta
		Body quota `body:"json"`
	}
	type RetryMeta struct {
		RetryAfter int `header:"Retry-After"`
	}
	type withEmbeddedHeader struct {
		RetryMeta
		Body quota `body:"json"`
	}
	type structHeader struct {
		Where struct{ A int } `header:"X-Where"`
		Body  quota           `body:"json"`
	}
	table := func(row geta.Failure) geta.Table {
		return one("/f", geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *empty) (*ok, error) { return nil, nil }, geta.Doc{Failures: []geta.Failure{row}})})
	}
	rejects(t, table(geta.OnAsProblem(429, "", func(*quotaError) withEmbeddedCookie { return withEmbeddedCookie{} })),
		"Session: a problem's envelope takes no cookie field")
	rejects(t, table(geta.OnAsProblem(429, "", func(*quotaError) structHeader { return structHeader{} })), "Where")
	a := accepts(t, table(geta.OnAsProblem(429, "", func(*quotaError) withEmbeddedHeader { return withEmbeddedHeader{} })))
	if h := at(t, doc(t, a), "paths", "/f", "get", "responses", "429", "headers"); h.(map[string]any)["Retry-After"] == nil {
		t.Fatalf("%v", h)
	}
}

// Text the document states that is not UTF-8 is refused: the document,
// which is JSON, could not hold it.
func TestDocumentTextThatIsNotUTF8IsRefused(t *testing.T) {
	op := func(d geta.Doc) geta.Table { return one("/t", geta.Route{Get: geta.Op(http.StatusOK, okHandler, d)}) }
	for name, d := range map[string]geta.Doc{
		"Summary":     {Summary: "caf\xe9"},
		"Description": {Description: "caf\xe9"},
		"OperationID": {OperationID: "caf\xe9"},
		"Tags[1]":     {Tags: []string{"ok", "caf\xe9"}},
	} {
		rejects(t, op(d), "GET /t (", `: Doc.`+name+` "caf\xe9" is not UTF-8`)
	}
	rejects(t, op(geta.Doc{Failures: []geta.Failure{geta.On(errNotFound, http.StatusNotFound, "caf\xe9")}}),
		"GET /t (", `: failure row 0: failure row "errors.Is store: not found": detail "caf\xe9" is not UTF-8`)
	// Text from elsewhere, here a field's doc tag, is named by its place in
	// the document.
	rejects(t, one("/t", get(func(context.Context, *empty) (*aeBadDoc, error) { return nil, nil })),
		"OpenAPI document text at /components/schemas/aeBadDoc/properties/a/description is not UTF-8")
}

// aeBadDoc's doc tag is not UTF-8.
type aeBadDoc struct {
	A string "json:\"a\" doc:\"caf\\xe9\""
}
