package getavet

import (
	"strings"
	"testing"
)

// A geta.FormatType carries its format only when its UnmarshalText holds a
// request to it: getavet reports one that is no text type, or that reads
// itself by JSON methods, with geta.New's text, and passes one that is,
// whatever format it names: one of geta's own (date) included.
func TestFormatTypesMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/koji-1009/geta"
)

// Email is a FormatType geta.New accepts: the application's own mail
// address, which takes a..b@docomo.ne.jp.
type Email struct{ s string }

func (Email) SchemaFormat() string           { return "email" }
func (e Email) MarshalText() ([]byte, error) { return []byte(e.s), nil }
func (e *Email) UnmarshalText(b []byte) error {
	if !strings.Contains(string(b), "@") {
		return errors.New("no @")
	}
	e.s = string(b)
	return nil
}

// Day carries geta's own format date, read the application's way.
type Day struct{ s string }

func (Day) SchemaFormat() string           { return "date" }
func (d Day) MarshalText() ([]byte, error) { return []byte(d.s), nil }
func (d *Day) UnmarshalText(b []byte) error { d.s = string(b); return nil }

// Plain names a format but is no text type.
type Plain string

func (Plain) SchemaFormat() string { return "email" }

// OwnJSON names a format but reads itself by its JSON methods.
type OwnJSON struct{ s string }

func (*OwnJSON) SchemaFormat() string          { return "email" }
func (OwnJSON) MarshalJSON() ([]byte, error)   { return []byte("\"\""), nil }
func (*OwnJSON) UnmarshalJSON(b []byte) error  { return nil }

type Good struct {
	E  Email   `+"`json:\"e\"`"+`
	Es []Email `+"`json:\"es\"`"+`
	D  Day     `+"`json:\"d\"`"+`
}

type BadP struct {
	P Plain `+"`json:\"p\"`"+`
}

type BadJ struct {
	J OwnJSON `+"`json:\"j\"`"+`
}

type In struct {
	P Plain `+"`query:\"p\"`"+`
}

type EmailIn struct {
	E *Email `+"`header:\"X-E\"`"+`
	D *Day   `+"`cookie:\"d\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *In) (*struct{}, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*BadP, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*BadJ, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *EmailIn) (*Good, error) { return nil, nil }, geta.Doc{})}},
	}})
}
`)
	const plain = "P: type lib.Plain has a SchemaFormat method but is not a text type"
	checkSame(t, diagnostics, built, []string{
		plain, plain,
		"J: type lib.OwnJSON has both a SchemaFormat method and its own JSON methods",
	})
}

// A schema tag's format on a geta.FormatType, which carries its own
// whatever SchemaFormat returns, is reported with geta.New's text, as a
// member and as a parameter; its other string keywords pass, as on any text
// type, and it is a parameter and a slice parameter's element as one.
func TestFormatOnAFormatTypeMatchesGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Email struct{ s string }

func (Email) SchemaFormat() string            { return "email" }
func (e Email) MarshalText() ([]byte, error)  { return []byte(e.s), nil }
func (e *Email) UnmarshalText(b []byte) error { e.s = string(b); return nil }

type Body struct {
	E Email  `+"`json:\"e\" schema:\"format=date\"`"+`
	F *Email `+"`json:\"f,omitzero\" schema:\"minLength=3,pattern=@\"`"+`
}

type In struct {
	H  *Email  `+"`header:\"X-H\" schema:\"maxLength=64\"`"+`
	Es []Email `+"`query:\"e\" schema:\"minItems=1\"`"+`
	Q  Email   `+"`query:\"q\" schema:\"format=password\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *In) (*struct{}, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Body, error) { return nil, nil }, geta.Doc{})}},
	}})
}
`)
	const own = "schema keyword format: the type has its own format"
	checkSame(t, diagnostics, built, []string{"E: " + own, "Q: " + own})
}

// A lower bound only the program's geta.Limits make unreachable is left to
// geta.New, which sees them: getavet does not read geta.WithLimits, so it
// reports none, even past geta.DefaultLimits. A minLength past the pattern
// ceiling beside a pattern, which no Limits moves, it reports with
// geta.New's text where a request reads it (a body's member); where only a
// response writes it, neither refuses it.
func TestLowerBoundsPastTheBackstop(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Long struct {
	S string `+"`json:\"s\" schema:\"minLength=5000\"`"+`
}

type Many struct {
	L []string `+"`json:\"l\" schema:\"minItems=9000\"`"+`
}

type Match struct {
	S string `+"`json:\"s\" schema:\"pattern=^a+$,minLength=4097\"`"+`
}

type In struct {
	Q string `+"`query:\"q\" schema:\"minLength=5000\"`"+`
}

type Body struct {
	Long  Long  `+"`json:\"long\"`"+`
	Many  Many  `+"`json:\"many\"`"+`
	Match Match `+"`json:\"match\"`"+`
}

type BodyIn struct {
	B Body `+"`body:\"json\"`"+`
}

// Written only: held to neither the Limits nor the pattern ceiling.
type OutMatch struct {
	S string `+"`json:\"s\" schema:\"pattern=^a+$,minLength=4097\"`"+`
}

type OutLong struct {
	S string `+"`json:\"s\" schema:\"minLength=5000\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *In) (*struct{}, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *BodyIn) (*struct{}, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*OutMatch, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*OutLong, error) { return nil, nil }, geta.Doc{})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		"S: minLength 4097 with a pattern exceeds the pattern ceiling of 4096 code points",
	})
	// geta.New refuses the others, by the Limits getavet does not see.
	for _, want := range []string{
		"lib.In.Q: minLength 5000 exceeds Limits.MaxStringLength 4096",
		"lib.Long.S: minLength 5000 exceeds Limits.MaxStringLength 4096",
		"lib.Many.L: minItems 9000 exceeds Limits.MaxItems 8192",
	} {
		if !strings.Contains(built, want) {
			t.Errorf("geta.New does not say %q:\n%s", want, built)
		}
	}
	for _, not := range []string{"lib.OutMatch", "lib.OutLong"} {
		if strings.Contains(built, not) {
			t.Errorf("geta.New refuses %s, which only a response writes:\n%s", not, built)
		}
	}
}

// An enum member past the backstop no request carries. Past the pattern
// ceiling beside a pattern, which no Limits moves, getavet reports it with
// geta.New's text; past MaxStringLength alone it reports none, since it does
// not see the Limits, and geta.New refuses it. A type only a response
// writes neither refuses.
func TestEnumMembersPastTheBackstop(t *testing.T) {
	long := strings.Repeat("a", 4097)
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Match struct {
	S string `+"`json:\"s\" schema:\"pattern=^a+$,enum=a|"+long+"\"`"+`
}

type Short struct {
	S string `+"`json:\"s\" schema:\"enum=a|bbbbbbbb\"`"+`
}

type MatchIn struct {
	B Match `+"`body:\"json\"`"+`
}

type ShortIn struct {
	B Short `+"`body:\"json\"`"+`
}

// Written only: held to neither.
type OutMatch struct {
	S string `+"`json:\"s\" schema:\"pattern=^a+$,enum=a|"+long+"\"`"+`
}

type OutShort struct {
	S string `+"`json:\"s\" schema:\"enum=a|bbbbbbbb\"`"+`
}

func Build() (*geta.App, error) {
	lim := geta.DefaultLimits
	lim.MaxStringLength = 7
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *MatchIn) (*struct{}, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *ShortIn) (*struct{}, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*OutMatch, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*OutShort, error) { return nil, nil }, geta.Doc{})}},
	}}, geta.WithLimits(lim))
}
`)
	checkSame(t, diagnostics, built, []string{
		"S: enum member \"" + long + "\" with a pattern exceeds the pattern ceiling of 4096 code points",
	})
	if want := `lib.Short.S: enum member "bbbbbbbb" exceeds Limits.MaxStringLength 7`; !strings.Contains(built, want) {
		t.Errorf("geta.New does not say %q:\n%s", want, built)
	}
	for _, not := range []string{"lib.OutMatch", "lib.OutShort"} {
		if strings.Contains(built, not) {
			t.Errorf("geta.New refuses %s, which only a response writes:\n%s", not, built)
		}
	}
}
