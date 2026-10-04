package getavet

import "testing"

// A map's key type is judged as a value of its type is, and the format it
// carries is its keys': a FormatType key that is no text type is reported,
// and a propertyNames.format on a key type that carries a format (a
// FormatType, geta.Password) is, as format= on such a value is, beside a
// value of any kind; so is a key or value length keyword no string of its
// format meets. Sound ones are not.
func TestKeyFormatsMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/koji-1009/geta"
)

// Email is a FormatType key.
type Email string

func (Email) SchemaFormat() string { return "email" }

func (e Email) MarshalText() ([]byte, error) { return []byte(e), nil }

func (e *Email) UnmarshalText(b []byte) error {
	if !strings.Contains(string(b), "@") {
		return errors.New("no @")
	}
	*e = Email(b)
	return nil
}

// NoText names a format but is no text type.
type NoText string

func (NoText) SchemaFormat() string { return "tag" }

// Money reads itself; WithSchema declares it a string.
type Money struct{ v string }

func (m Money) MarshalJSON() ([]byte, error)  { return []byte(m.v), nil }
func (m *Money) UnmarshalJSON(b []byte) error { m.v = string(b); return nil }

type Sound struct {
	Mail    map[Email]int         `+"`json:\"mail\" schema:\"propertyNames.maxLength=64\"`"+`
	Secrets map[geta.Password]int `+"`json:\"secrets\" schema:\"propertyNames.minLength=1\"`"+`
	IDs     map[string]int        `+"`json:\"ids\" schema:\"propertyNames.format=uuid\"`"+`
	V6      map[string]int        `+"`json:\"v6\" schema:\"propertyNames.format=ipv6,propertyNames.maxLength=39\"`"+`
}

type F1 struct {
	OwnFormat map[Email]int `+"`json:\"o\" schema:\"propertyNames.format=uuid\"`"+`
}

type F2 struct {
	Password map[geta.Password]int `+"`json:\"p\" schema:\"propertyNames.format=date\"`"+`
}

type F3 struct {
	OwnValues map[Email]Money `+"`json:\"v\" schema:\"propertyNames.format=date\"`"+`
}

type F4 struct {
	NotText map[NoText]int `+"`json:\"n\"`"+`
}

type F5 struct {
	ShortKey map[string]int `+"`json:\"s\" schema:\"propertyNames.format=uuid,propertyNames.maxLength=30\"`"+`
}

type F6 struct {
	LongDay string `+"`json:\"d\" schema:\"format=date,minLength=11\"`"+`
}

type F7 struct {
	ShortDate geta.Date `+"`json:\"d\" schema:\"maxLength=9\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Sound, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/f1", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*F1, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/f2", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*F2, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/f3", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*F3, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/f4", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*F4, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/f5", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*F5, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/f6", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*F6, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/f7", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*F7, error) { return nil, nil }, geta.Doc{})}},
	}}, geta.WithSchema[Money]("string", ""))
}
`)
	checkSame(t, diagnostics, built, []string{
		"OwnFormat: propertyNames: schema keyword format: the type has its own format",
		`Password: propertyNames: schema keyword format: the type already has format "password"`,
		"OwnValues: propertyNames: schema keyword format: the type has its own format",
		"type lib.NoText has a SchemaFormat method but is not a text type",
		"ShortKey: propertyNames: maxLength 30 is below format uuid's minimum length 36",
		"LongDay: minLength 11 exceeds format date's maximum length 10",
		"ShortDate: maxLength 9 is below format date's minimum length 10",
	})
}
