package geta_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/internal/vet"
)

// A problem whose detail is not UTF-8, as the application wrote it, keeps
// its body, each invalid byte written as U+FFFD, inside an App and outside
// one. It once went out with Content-Length: 0 and no body at all. (A
// failure row's detail is in the document, which New refuses when it is not
// UTF-8.)
func TestAProblemDetailThatIsNotUTF8KeepsItsBody(t *testing.T) {
	check := func(rec *httptest.ResponseRecorder) {
		t.Helper()
		var p geta.Problem
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != 400 || p.Detail != "bad � byte" || p.Status != 400 ||
			rec.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
			t.Fatalf("%d %q %v", rec.Code, rec.Body, err)
		}
	}
	rec := httptest.NewRecorder()
	geta.WriteProblem(rec, http.StatusBadRequest, "bad \xff byte")
	check(rec)
	a := accepts(t, one("/x", get(okHandler), geta.Scope{geta.Ordered(geta.OrderAuthorize, func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			geta.WriteProblem(w, http.StatusBadRequest, "bad \xff byte")
		})
	}).Answers(http.StatusBadRequest, "bad")}))
	check(do(t, a, "GET", "/x"))
	if _, err := geta.New(one("/f", geta.Route{Get: geta.Op(http.StatusOK, okHandler,
		geta.Doc{Failures: []geta.Failure{geta.On(errNotFound, 404, "caf\xe9")}})})); err == nil || !strings.Contains(err.Error(), `detail "caf\xe9" is not UTF-8`) {
		t.Fatalf("%v", err)
	}
}

// Password's String and GoString redact, as its Format does.
func TestPasswordStringAndGoStringRedact(t *testing.T) {
	p := geta.Password("hunter2")
	if p.String() != "[redacted]" || p.GoString() != `geta.Password("[redacted]")` {
		t.Fatalf("%s %s", p.String(), p.GoString())
	}
}

// NewTimeOfDay refuses a clock reading that is no time of day and a
// fraction that is no fraction of a second, and writes a negative offset
// with its sign.
func TestNewTimeOfDayRanges(t *testing.T) {
	for _, c := range []struct{ h, m, s, ns, off int }{{24, 0, 0, 0, 0}, {0, 60, 0, 0, 0}, {0, 0, 61, 0, 0}, {-1, 0, 0, 0, 0}, {0, 0, 0, -1, 0}, {0, 0, 0, 1e9, 0}} {
		if _, err := geta.NewTimeOfDay(c.h, c.m, c.s, c.ns, c.off); err == nil {
			t.Errorf("%v accepted", c)
		}
	}
	tod, err := geta.NewTimeOfDay(10, 30, 0, 0, -90*60)
	if err != nil || tod.String() != "10:30:00-01:30" {
		t.Fatalf("%v %v", tod, err)
	}
}

func duration(t *testing.T, s string) geta.Duration {
	t.Helper()
	var d geta.Duration
	if err := d.UnmarshalText([]byte(s)); err != nil {
		t.Fatal(err)
	}
	return d
}

// A Duration whose component passes a uint64 is an error to Std and AddTo,
// as is one past time.Time's calendar arithmetic or past a time.Duration;
// a number with no unit is no duration.
func TestDurationArithmeticLimits(t *testing.T) {
	huge := duration(t, "PT99999999999999999999S")
	if _, err := huge.Std(); err == nil || !strings.Contains(err.Error(), "does not fit a uint64") {
		t.Errorf("Std: %v", err)
	}
	if _, err := huge.AddTo(time.Now()); err == nil || !strings.Contains(err.Error(), "does not fit a uint64") {
		t.Errorf("AddTo: %v", err)
	}
	if _, err := duration(t, "P2147483648Y").AddTo(time.Now()); err == nil || !strings.Contains(err.Error(), "calendar arithmetic") {
		t.Errorf("years: %v", err)
	}
	if _, err := duration(t, "P1DT9999999999999H").AddTo(time.Now()); err == nil || !strings.Contains(err.Error(), "exceeds a time.Duration") {
		t.Errorf("hours: %v", err)
	}
	var d geta.Duration
	if err := d.UnmarshalText([]byte("P12")); err == nil {
		t.Error("P12 accepted")
	}
}

// A Nullable refuses a truncated null and a value of another type.
func TestNullableUnmarshalRefusals(t *testing.T) {
	var n geta.Nullable[int]
	if err := json.Unmarshal([]byte("nul"), &n); err == nil {
		t.Error("nul accepted")
	}
	if err := json.Unmarshal([]byte(`"x"`), &n); err == nil {
		t.Error(`"x" accepted into a Nullable[int]`)
	}
}

// The gzip encoder refuses a Flush after its Close.
func TestGzipFlushAfterClose(t *testing.T) {
	var buf bytes.Buffer
	enc, err := geta.GzipCoding().NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	f := enc.(interface{ Flush() error })
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	enc.Close()
	if err := f.Flush(); err != geta.ErrEncoderClosed {
		t.Fatalf("%v", err)
	}
}

// The vet rules getavet applies: a request's tag that is no tag, kinds that
// name nothing, a form body that is no struct, a deepObject member that is
// no scalar, an unexported problem field, a status tag that is no tag, and
// the parameter rules that leave a bad tag to CheckSchemaTag.
func TestVetRulesOnMalformedInput(t *testing.T) {
	if err := vet.CheckRequestSchemaTag("bogus=1", "string"); err == nil {
		t.Error("CheckRequestSchemaTag took an unknown keyword")
	}
	for _, kind := range []string{"?bogus", "map[string", "map[bogus]int", "map[string]bogus"} {
		if err := vet.CheckSchemaTag("", kind); err == nil {
			t.Errorf("kind %q accepted", kind)
		}
	}
	field := func(tag, kind string) vet.Field {
		return vet.Field{Name: "F", Exported: true, Tag: reflect.StructTag(tag), Type: "string", Kind: kind}
	}
	if _, err := vet.CheckInputField(field(`body:"form"`, "string"), map[string]string{}); err == nil ||
		!strings.Contains(err.Error(), `a body:"form" field has type string, not a struct`) {
		t.Errorf("form body: %v", err)
	}
	if _, err := vet.CheckInputField(field(`cookie:"c" schema:"enum=é|a"`, "string"), map[string]string{}); err == nil ||
		!strings.Contains(err.Error(), `enum member "é" is not a valid cookie value`) {
		t.Errorf("cookie enum: %v", err)
	}
	for _, tag := range []string{`query:"q" schema:"bogus=1"`, `query:"q" schema:"maxLength=1,default=abc"`} {
		if _, err := vet.CheckInputField(field(tag, "string"), map[string]string{}); err != nil {
			t.Errorf("%s: %v; a bad tag is CheckSchemaTag's to name", tag, err)
		}
	}
	member := vet.Field{Name: "F", Exported: true, Tag: `form:"m"`, Type: "[]string", Kind: "[]string"}
	if _, err := vet.CheckDeepObjectField(member, map[string]string{}); err == nil || !strings.Contains(err.Error(), `deepObject member "m" has non-scalar type []string`) {
		t.Errorf("deepObject: %v", err)
	}
	if err := vet.CheckProblemField(vet.Field{Name: "f", Tag: `cookie:"c"`}); err != nil {
		t.Errorf("an unexported problem field: %v", err)
	}
	if err := vet.CheckEnvelopeStatus(200, "abc"); err != nil {
		t.Errorf("a malformed status tag is CheckEnvelopeField's to name: %v", err)
	}
}

// invalidToken is a refusal that names its own challenge.
type invalidToken struct{}

func (invalidToken) Error() string     { return "expired" }
func (invalidToken) Unwrap() error     { return geta.ErrUnauthenticated }
func (invalidToken) Challenge() string { return `Bearer error="invalid_token"` }

// A Challenger's challenge is the 401's in place of the scheme's.
func TestAChallengersChallengeReplacesTheSchemes(t *testing.T) {
	a := accepts(t, withRoot(one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{geta.Bearer}})}),
		geta.Secure(geta.Policy{Verifiers: map[string]geta.Verifier{"bearer": func(*http.Request) (context.Context, error) { return nil, invalidToken{} }}})))
	r := do(t, a, "GET", "/x")
	if r.Code != 401 || fmt.Sprint(r.Header().Values("WWW-Authenticate")) != `[Bearer error="invalid_token"]` {
		t.Fatal(r.Code, r.Header())
	}
}

type peShape interface{ isPEShape() }

type PEBase struct {
	Kind string `json:"kind"`
}

type PENote struct {
	Note string `json:"note"`
}

// peBox carries its discriminator in an embedded struct, after another.
type peBox struct {
	PENote
	PEBase
	W int `json:"w"`
}

// peBare declares no discriminator at all, which only New would refuse.
type peBare struct {
	W int `json:"w"`
}

func (peBox) isPEShape()   {}
func (*peBare) isPEShape() {}

// A sealed type's JSONOptions write a variant whose discriminator is
// promoted from an embedded struct, and refuse a variant that carries
// another tag or none; a nil pointer is written as null, as encoding/json/v2
// writes one without calling a marshaler.
func TestSealedOptionsWriteOnlyDeclaredVariants(t *testing.T) {
	u := geta.Sealed[peShape]("kind", geta.Case[peBox]("box"), geta.Case[peBare]("bare"))
	b, err := json.Marshal(peShape(peBox{PEBase: PEBase{Kind: "box"}, W: 2}), u.JSONOptions())
	if err != nil || !strings.Contains(string(b), `"kind":"box"`) {
		t.Fatalf("%s %v", b, err)
	}
	for v, want := range map[peShape]string{
		peBox{PEBase: PEBase{Kind: "bare"}}: `has kind "bare", but its variant's tag is "box"`,
		&peBare{W: 1}:                       `has kind "", but its variant's tag is "bare"`,
	} {
		if _, err := json.Marshal(v, u.JSONOptions()); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%#v: %v, want %q", v, err, want)
		}
	}
	if b, err := json.Marshal(peShape((*peBare)(nil)), u.JSONOptions()); err != nil || string(b) != "null" {
		t.Errorf("a nil pointer: %s %v", b, err)
	}
}
