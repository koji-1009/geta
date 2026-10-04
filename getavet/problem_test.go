package getavet

import "testing"

// What a geta.OnAsProblem row's function returns: getavet reports what
// geta.New refuses of it, with its text (a type that is not a struct, a
// member that is the problem's own, a detail that is no string, a cookie,
// status, or raw body field, a body that is not a struct). Sound ones pass.
func TestProblemDescriptionsMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Out struct {
	OK bool `+"`json:\"ok\"`"+`
}

type QuotaError struct{ Wait int }

func (*QuotaError) Error() string { return "quota" }

type Quota struct {
	RetryAfter int          `+"`header:\"Retry-After\"`"+`
	Body       QuotaMembers `+"`body:\"json\"`"+`
}

type QuotaMembers struct {
	Detail    string `+"`json:\"detail\"`"+`
	Remaining int    `+"`json:\"remaining\"`"+`
}

type OwnStatus struct {
	Status int `+"`json:\"status\"`"+`
}

type NumberDetail struct {
	Detail int `+"`json:\"detail\"`"+`
}

type WithCookie struct {
	Session *http.Cookie `+"`cookie:\"sid\"`"+`
}

type ListBody struct {
	Body []string `+"`body:\"json\"`"+`
}

func h(context.Context, *struct{}) (*Out, error) { return nil, nil }

func rows(f ...geta.Failure) geta.Route {
	return geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{Failures: f})}
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: rows(geta.OnAsProblem(429, "q", func(e *QuotaError) Quota { return Quota{RetryAfter: e.Wait} }))},
		{Path: "/b", Route: rows(geta.OnAsProblem(429, "q", func(*QuotaError) int { return 0 }))},
		{Path: "/c", Route: rows(geta.OnAsProblem(429, "q", func(*QuotaError) OwnStatus { return OwnStatus{} }))},
		{Path: "/d", Route: rows(geta.OnAsProblem(429, "q", func(*QuotaError) NumberDetail { return NumberDetail{} }))},
		{Path: "/e", Route: rows(geta.OnAsProblem(429, "q", func(*QuotaError) WithCookie { return WithCookie{} }))},
		{Path: "/f", Route: rows(geta.OnAsProblem(429, "q", func(*QuotaError) ListBody { return ListBody{} }))},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		"geta.OnAsProblem: int is not a struct",
		`geta.OnAsProblem: member "status" is reserved`,
		`geta.OnAsProblem: member "detail" has kind int, not string`,
		"Session: a problem's envelope takes no cookie field",
		"geta.OnAsProblem: []string is not a struct",
	})
}

// A problem's envelope is walked as geta.New walks it: an embedded struct's
// fields are its own, an unexported untagged field is nothing, a field with
// no envelope tag is refused; its body's members are walked through an
// embedded struct, past a member json:"-" drops, and a member refused as
// one is named once, as a member. P a type parameter is judged where the
// row is instantiated, by geta.New.
func TestProblemEnvelopeWalkMatchesGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Out struct {
	OK bool `+"`json:\"ok\"`"+`
}

type QuotaError struct{ Wait int }

func (*QuotaError) Error() string { return "quota" }

type Retry struct {
	RetryAfter int `+"`header:\"Retry-After\"`"+`
}

type Counts struct {
	Remaining int    `+"`json:\"remaining\"`"+`
	Title     string `+"`json:\"title\"`"+`
}

type Members struct {
	Counts
	Skip string `+"`json:\"-\"`"+`
}

type Walked struct {
	Retry
	note string
	Body Members `+"`body:\"json\"`"+`
}

type Pointed struct {
	Body struct {
		Ptr *int `+"`json:\"ptr\"`"+`
	} `+"`body:\"json\"`"+`
}

type Loose struct {
	RetryAfter int `+"`header:\"Retry-After\"`"+`
	Extra      int
}

var _ = Walked{}.note

func h(context.Context, *struct{}) (*Out, error) { return nil, nil }

func rows(f ...geta.Failure) geta.Route {
	return geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{Failures: f})}
}

func described[P any]() geta.Failure {
	return geta.OnAsProblem(429, "q", func(*QuotaError) P { var p P; return p })
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: rows(geta.OnAsProblem(429, "q", func(*QuotaError) Walked { return Walked{} }))},
		{Path: "/b", Route: rows(geta.OnAsProblem(429, "q", func(*QuotaError) Loose { return Loose{} }))},
		{Path: "/c", Route: rows(described[Retry]())},
		{Path: "/d", Route: rows(geta.OnAsProblem(429, "q", func(*QuotaError) Pointed { return Pointed{} }))},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		`geta.OnAsProblem: member "title" is reserved`,
		"Ptr is a pointer without omitzero; tag it `json:\"ptr,omitzero\"`",
		"Extra: an envelope field needs a header, cookie, or body tag",
	})
}
