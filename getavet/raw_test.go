package getavet

import "testing"

// A body tag naming a media type is a raw body: getavet reports what
// geta.New refuses of one, with its text (a media range, JSON, a form, an
// event stream as an output, a type that cannot hold the bytes, a schema
// tag, a media type not written canonically), and reads no JSON form of its
// bytes. Sound ones pass.
func TestRawBodiesMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"io"
	"net/http"

	"github.com/koji-1009/geta"
)

type Out struct {
	OK bool `+"`json:\"ok\"`"+`
}

type RangeIn struct {
	Body []byte `+"`body:\"image/*\"`"+`
}

type JSONIn struct {
	Body []byte `+"`body:\"application/problem+json\"`"+`
}

type StringIn struct {
	Body string `+"`body:\"text/plain\"`"+`
}

type SchemaIn struct {
	Body []byte `+"`body:\"text/plain\" schema:\"maxLength=3\"`"+`
}

type CaseIn struct {
	Body []byte `+"`body:\"Text/Plain\"`"+`
}

type SoundIn struct {
	ID   string    `+"`path:\"id\"`"+`
	Body io.Reader `+"`body:\"application/octet-stream\" doc:\"the blob\"`"+`
}

type MaybeIn struct {
	Body *[]byte `+"`body:\"text/csv\"`"+`
}

type StreamOut struct {
	Body []byte `+"`body:\"text/event-stream\"`"+`
}

type StringOut struct {
	Body string `+"`body:\"text/plain\"`"+`
}

type SoundOut struct {
	Name string    `+"`header:\"Content-Disposition\"`"+`
	Body io.Reader `+"`body:\"application/pdf\"`"+`
}

func h[In, Out any](context.Context, *In) (*Out, error) { return nil, nil }

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Post: geta.Op(http.StatusOK, h[RangeIn, Out], geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Post: geta.Op(http.StatusOK, h[JSONIn, Out], geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Post: geta.Op(http.StatusOK, h[StringIn, Out], geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Post: geta.Op(http.StatusOK, h[SchemaIn, Out], geta.Doc{})}},
		{Path: "/e", Route: geta.Route{Post: geta.Op(http.StatusOK, h[CaseIn, Out], geta.Doc{})}},
		{Path: "/f/{id}", Route: geta.Route{Put: geta.Op(http.StatusOK, h[SoundIn, Out], geta.Doc{})}},
		{Path: "/g", Route: geta.Route{Put: geta.Op(http.StatusOK, h[MaybeIn, Out], geta.Doc{})}},
		{Path: "/h", Route: geta.Route{Get: geta.Op(http.StatusOK, h[struct{}, StreamOut], geta.Doc{})}},
		{Path: "/i", Route: geta.Route{Get: geta.Op(http.StatusOK, h[struct{}, StringOut], geta.Doc{})}},
		{Path: "/j", Route: geta.Route{Get: geta.Op(http.StatusOK, h[struct{}, SoundOut], geta.Doc{})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		`Body: body tag "image/*" is a media range`,
		`Body: body tag "application/problem+json" is JSON; use body:"json"`,
		`Body: a body:"text/plain" field has type string, not []byte, *[]byte, or io.Reader`,
		`Body: a body:"text/plain" field takes no schema tag`,
		`Body: body tag "Text/Plain" is not canonical; write "text/plain"`,
		`Body: body tag "text/event-stream" is an event stream; return a *geta.Stream`,
		`Body: a body:"text/plain" field has type string, not []byte or io.Reader`,
	})
}
