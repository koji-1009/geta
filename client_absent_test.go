package geta_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaclient"
	"github.com/koji-1009/geta/getatest"
)

// A field with a default is one geta binds its default to when a request
// leaves it out. The typed client sends a field as it holds it, its zero
// value included, unless the call names it with getaclient.Absent: then it
// sends nothing for it, in every location, and the default applies.

type dfltPage struct {
	Size int     `form:"size" schema:"default=20,maximum=100"`
	Sort *string `form:"sort"`
}

type dfltLine struct {
	Qty  int    `json:"qty" schema:"default=1,minimum=1"`
	Note string `json:"note" schema:"default=none"`
}

type dfltOrder struct {
	Qty   int        `json:"qty" schema:"default=1,minimum=1"`
	Lines []dfltLine `json:"lines"`
	Plain string     `json:"plain"`
}

type dfltIn struct {
	Limit int       `query:"limit" schema:"default=20,maximum=100"`
	Mode  string    `header:"X-Mode" schema:"default=fast,enum=fast|slow"`
	Theme string    `cookie:"theme" schema:"default=dark,enum=dark|light"`
	Page  *dfltPage `query:"page"`
	Plain string    `query:"plain"`
	Body  dfltOrder `body:"json"`
}

type dfltOut struct {
	Limit int       `json:"limit"`
	Mode  string    `json:"mode"`
	Theme string    `json:"theme"`
	Size  int       `json:"size"`
	Order dfltOrder `json:"order"`
}

type dfltForm struct {
	Count int    `form:"count" schema:"default=3,minimum=1"`
	Label string `form:"label"`
}

type dfltFormIn struct {
	Body dfltForm `body:"form"`
}

func dfltApp(t *testing.T) *getatest.Client {
	echo := func(_ context.Context, in *dfltIn) (*dfltOut, error) {
		out := &dfltOut{Limit: in.Limit, Mode: in.Mode, Theme: in.Theme, Order: in.Body}
		if in.Page != nil {
			out.Size = in.Page.Size
		}
		return out, nil
	}
	form := func(_ context.Context, in *dfltFormIn) (*dfltForm, error) { return &in.Body, nil }
	return getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/d", Route: geta.Route{Post: geta.Op(http.StatusOK, echo, geta.Doc{})}},
		{Path: "/f", Route: geta.Route{Post: geta.Op(http.StatusOK, form, geta.Doc{})}},
	}})
}

func TestAbsentFieldsTakeTheirDefaults(t *testing.T) {
	c := dfltApp(t)
	tc := c.Typed()
	ctx := t.Context()

	// Named absent: nothing is sent for them, and geta binds every default,
	// a query, a header, a cookie, a deepObject's member (of a deepObject
	// the request holds: with no member sent, it is absent), and a JSON
	// member at the top and inside a slice's element.
	sort := "name"
	in := dfltIn{Page: &dfltPage{Sort: &sort}, Body: dfltOrder{Lines: []dfltLine{{Qty: 2}, {}}}}
	out, err := getaclient.Call[dfltIn, dfltOut](ctx, tc, http.MethodPost, "/d", &in,
		getaclient.Absent(&in.Limit, &in.Mode, &in.Theme, &in.Page.Size, &in.Body.Qty, &in.Body.Lines[1].Qty, &in.Body.Lines[1].Note))
	if err != nil {
		t.Fatal(err)
	}
	want := dfltOut{Limit: 20, Mode: "fast", Theme: "dark", Size: 20,
		Order: dfltOrder{Qty: 1, Lines: []dfltLine{{Qty: 2, Note: ""}, {Qty: 1, Note: "none"}}}}
	if !equalOut(*out, want) {
		t.Fatalf("got %+v\nwant %+v", *out, want)
	}

	// Not named: the zero values are sent, as the field holds them; where
	// the schema admits zero, the handler gets it, not the default.
	in = dfltIn{Mode: "slow", Theme: "light", Page: &dfltPage{}, Body: dfltOrder{Qty: 5}}
	out, err = getaclient.Call[dfltIn, dfltOut](ctx, tc, http.MethodPost, "/d", &in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Limit != 0 || out.Size != 0 || out.Order.Qty != 5 {
		t.Fatalf("got %+v; want the zero values sent", *out)
	}
	// And where the schema does not admit zero, the request is refused.
	in = dfltIn{Mode: "fast", Theme: "dark"}
	_, err = getaclient.Call[dfltIn, dfltOut](ctx, tc, http.MethodPost, "/d", &in)
	if e, ok := errors.AsType[*getaclient.Error](err); !ok || e.Status != 400 || !strings.Contains(e.Error(), "qty") {
		t.Fatalf("%v", err)
	}

	// A form field, likewise.
	fin := dfltFormIn{Body: dfltForm{Label: "x"}}
	f, err := getaclient.Call[dfltFormIn, dfltForm](ctx, tc, http.MethodPost, "/f", &fin, getaclient.Absent(&fin.Body.Count))
	if err != nil || f.Count != 3 || f.Label != "x" {
		t.Fatal(f, err)
	}
}

func equalOut(a, b dfltOut) bool {
	if a.Limit != b.Limit || a.Mode != b.Mode || a.Theme != b.Theme || a.Size != b.Size || a.Order.Qty != b.Order.Qty ||
		a.Order.Plain != b.Order.Plain || len(a.Order.Lines) != len(b.Order.Lines) {
		return false
	}
	for i := range a.Order.Lines {
		if a.Order.Lines[i] != b.Order.Lines[i] {
			return false
		}
	}
	return true
}

// Absent names fields of the call's input that declare a default; anything
// else is an error before anything is sent.
func TestAbsentRefusesWhatHasNoDefault(t *testing.T) {
	c := dfltApp(t)
	tc := c.Typed()
	in := dfltIn{}
	var elsewhere int
	for want, abs := range map[string][]any{
		`field Plain (query "plain") declares no default`:  {&in.Plain},
		`field Plain (member "plain") declares no default`: {&in.Body.Plain},
		"int is not a field of the input":                  {&elsewhere},
		"not a pointer":                                    {in.Limit},
	} {
		_, err := getaclient.Call[dfltIn, dfltOut](t.Context(), tc, http.MethodPost, "/d", &in, getaclient.Absent(abs...))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v\nwant %q", err, want)
		}
		if _, isProblem := errors.AsType[*getaclient.Error](err); isProblem {
			t.Errorf("%v was sent", err)
		}
	}
}
