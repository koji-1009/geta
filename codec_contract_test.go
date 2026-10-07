package geta_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
)

func postJSON(t *testing.T, a *geta.App, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	return rec
}

type jsonIface interface{ MarshalJSON() ([]byte, error) }

type jsonIfaceIn struct {
	Body struct {
		V jsonIface `json:"v"`
	} `body:"json"`
}

// An interface listing JSON methods is still an interface: v2 cannot read
// into it, so it needs geta.WithUnion like any other, and WithSchema refuses
// it.
func TestAnInterfaceWithJSONMethodsIsASealedTypeOrNothing(t *testing.T) {
	rejects(t, one("/x", post(func(context.Context, *jsonIfaceIn) (*ok, error) { return nil, nil })),
		"interface type geta_test.jsonIface has no JSON form; declare it with geta.WithUnion")
	_, err := geta.New(one("/x", get(okHandler)), geta.WithSchema[jsonIface]("object", ""))
	if err == nil || !strings.Contains(err.Error(), "geta_test.jsonIface is an interface; declare it with geta.WithUnion") {
		t.Fatal(err)
	}
}

type namedByte byte

type namedBytesBody struct {
	Data []namedByte `json:"data"`
	Raw  []byte      `json:"raw"`
}

type namedBytesIn struct {
	Body namedBytesBody `body:"json"`
}

// A slice of a named byte type is an array of numbers, as v2 reads and
// writes it; a []byte is base64.
func TestANamedByteSliceIsAnArray(t *testing.T) {
	a := accepts(t, one("/x", post(func(_ context.Context, in *namedBytesIn) (*namedBytesBody, error) { return &in.Body, nil })))
	props := at(t, doc(t, a), "components", "schemas", "namedBytesBody", "properties")
	if got := compact(t, at(t, props, "data")); got != `{"items":{"maximum":255,"minimum":0,"type":"integer"},"type":"array"}` {
		t.Error(got)
	}
	if got := compact(t, at(t, props, "raw")); got != `{"contentEncoding":"base64","type":"string"}` {
		t.Error(got)
	}
	rec := postJSON(t, a, `{"data":[1,2,3],"raw":"AQID"}`)
	if rec.Code != http.StatusOK || rec.Body.String() != `{"data":[1,2,3],"raw":"AQID"}` {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if rec := postJSON(t, a, `{"data":"AQID","raw":"AQID"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("base64 for a named byte slice: %d %s", rec.Code, rec.Body)
	}
}

// A declaration geta would never apply is refused: of a pointer type, read
// through to its element, and of a Nullable, read as its value or null.
func TestDeclarationsThatWouldNeverApplyAreRefused(t *testing.T) {
	for _, c := range []struct {
		opt  geta.Option
		want string
	}{
		{geta.WithSchema[*wideNum]("integer", ""),
			"is a pointer, which geta reads through; declare geta.WithSchema[github.com/koji-1009/geta_test.wideNum]"},
		{geta.WithSchema[geta.Nullable[wideNum]]("integer", ""),
			"a geta.Nullable's schema is its value's, or null; declare geta.WithSchema[github.com/koji-1009/geta_test.wideNum]"},
	} {
		if _, err := geta.New(one("/x", get(okHandler)), c.opt); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v; want %q", err, c.want)
		}
	}
}

type keyed string

func (k keyed) MarshalJSON() ([]byte, error)  { return []byte(`"` + string(k) + `"`), nil }
func (k *keyed) UnmarshalJSON(b []byte) error { *k = keyed(b[1 : len(b)-1]); return nil }

type keyedMap struct {
	M map[keyed]int `json:"m"`
}

// A declaration of a map key type, which keys never read through, is
// refused.
func TestADeclaredMapKeyIsRefused(t *testing.T) {
	_, err := geta.New(one("/x", get(func(context.Context, *empty) (*keyedMap, error) { return nil, nil })), geta.WithSchema[keyed]("string", "maxLength=3"))
	if err == nil || !strings.Contains(err.Error(), "does not apply to a map key; constrain the keys with propertyNames.") {
		t.Error(err)
	}
}

type quotaDetailErr struct{}

func (quotaDetailErr) Error() string { return "quota" }

type shortDetail struct {
	Detail *string `json:"detail,omitzero" schema:"maxLength=3"`
}

type fixedDetail struct {
	Detail string `json:"detail" schema:"maxLength=3"`
}

// A row's detail, kept when its description's detail is nil, must meet the
// schema the document gives that detail.
func TestARowsDetailMeetsItsDescriptionsDetailSchema(t *testing.T) {
	row := func(describe geta.Failure) geta.Table {
		return one("/d", geta.Route{Get: geta.Op(http.StatusOK,
			func(context.Context, *empty) (*ok, error) { return nil, quotaDetailErr{} },
			geta.Doc{Failures: []geta.Failure{describe}})})
	}
	rejects(t, row(geta.OnAsProblem(http.StatusConflict, "quota exceeded", func(quotaDetailErr) shortDetail { return shortDetail{} })),
		`failure row 0 (errors.As geta_test.quotaDetailErr, described by geta_test.shortDetail): geta_test.shortDetail.detail: the row's detail "quota exceeded", kept when the description's is nil, fails the member's schema`)
	// A detail that fits, no detail, and a detail always replaced are taken.
	for _, f := range []geta.Failure{
		geta.OnAsProblem(http.StatusConflict, "abc", func(quotaDetailErr) shortDetail { return shortDetail{} }),
		geta.OnAsProblem(http.StatusConflict, "", func(quotaDetailErr) shortDetail { return shortDetail{} }),
		geta.OnAsProblem(http.StatusConflict, "quota exceeded", func(quotaDetailErr) fixedDetail { return fixedDetail{"q"} }),
	} {
		a := accepts(t, row(f))
		rec := do(t, a, http.MethodGet, "/d")
		if err := a.Conforms(geta.Match{Template: "/d", Method: http.MethodGet}, rec.Code, nil, rec.Body.Bytes()); err != nil {
			t.Errorf("%s: %v", rec.Body, err)
		}
	}
}

type uintBody struct {
	U   uint64 `json:"u"`
	Max uint   `json:"max" schema:"maximum=5"`
	Big uint64 `json:"big" schema:"maximum=1e30"`
}

type uintIn struct {
	Body uintBody `body:"json"`
}

// uint and uint64 are documented below 2^64, exclusively, and held there.
func TestUint64IsDocumentedBelowTwoToThe64(t *testing.T) {
	a := accepts(t, one("/x", post(func(context.Context, *uintIn) (*ok, error) { return &ok{true}, nil })))
	// The document's text, since a float64 would round 2^64's digits.
	raw := strings.Join(strings.Fields(string(a.OpenAPI())), "")
	for _, want := range []string{
		`"u":{"exclusiveMaximum":18446744073709551616,"minimum":0,"type":"integer"}`,
		`"max":{"maximum":5,"minimum":0,"type":"integer"}`,
		`"big":{"exclusiveMaximum":18446744073709551616,"minimum":0,"type":"integer"}`,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("the document lacks %s", want)
		}
	}
	if rec := postJSON(t, a, `{"u":18446744073709551615,"max":5,"big":18446744073709551615}`); rec.Code != http.StatusOK {
		t.Errorf("2^64-1: %d %s", rec.Code, rec.Body)
	}
	if rec := postJSON(t, a, `{"u":18446744073709551616,"max":5,"big":1}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "18446744073709551616 is out of range for uint64") {
		t.Errorf("2^64: %d %s", rec.Code, rec.Body)
	}
}

type wideNum struct{ s string }

func (w wideNum) MarshalJSON() ([]byte, error)  { return []byte(w.s), nil }
func (w *wideNum) UnmarshalJSON(p []byte) error { w.s = string(p); return nil }

type wideIn struct {
	Body struct {
		N wideNum `json:"n"`
	} `body:"json"`
}

// A declared integer or number is read by its own type, so a float64 need
// not hold it; the keywords still compare it exactly, on both paths.
func TestADeclaredNumberNeedNotFitAFloat64(t *testing.T) {
	huge := "1" + strings.Repeat("0", 400)
	for _, c := range []struct {
		constraints, n string
		want           int
	}{
		{"", huge, 200},
		{"", "-" + huge, 200},
		{"minimum=0", huge, 200},
		{"minimum=0", "-" + huge, 400},
		{"maximum=10", huge, 400},
		{"maximum=10", "-" + huge, 200},
		{"exclusiveMinimum=1e300", huge, 200},
		{"multipleOf=10", huge, 200},
		{"multipleOf=3", huge, 400},
	} {
		a, err := geta.New(one("/x", post(func(context.Context, *wideIn) (*ok, error) { return &ok{true}, nil })),
			geta.WithSchema[wideNum]("integer", c.constraints))
		if err != nil {
			t.Fatal(err)
		}
		if rec := postJSON(t, a, `{"n":`+c.n+`}`); rec.Code != c.want {
			t.Errorf("%q, %.5s…: %d, want %d: %s", c.constraints, c.n, rec.Code, c.want, rec.Body)
		}
	}
}
