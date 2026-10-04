package geta_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaclient"
	"github.com/koji-1009/geta/getatest"
)

type deepFilter struct {
	Min    *int       `form:"min" schema:"minimum=0"`
	Max    *int       `form:"max"`
	Status string     `form:"status" schema:"enum=open|closed,default=open"`
	Since  *geta.Date `form:"since" doc:"Not before this day"`
}

type deepPage struct {
	Size   int     `form:"size" schema:"default=20,maximum=100"`
	Cursor *string `form:"cursor" schema:"maxLength=16"`
}

type deepIn struct {
	Filter *deepFilter `query:"filter" doc:"What to keep"`
	Page   deepPage    `query:"page"`
	Q      *string     `query:"q"`
}

type deepOut struct {
	Filtered bool   `json:"filtered"`
	Min      int    `json:"min"`
	Status   string `json:"status"`
	Since    string `json:"since"`
	Size     int    `json:"size"`
	Cursor   string `json:"cursor"`
}

func deepTable() geta.Table {
	h := func(_ context.Context, in *deepIn) (*deepOut, error) {
		out := &deepOut{Size: in.Page.Size}
		if in.Page.Cursor != nil {
			out.Cursor = *in.Page.Cursor
		}
		if f := in.Filter; f != nil {
			out.Filtered, out.Status = true, f.Status
			if f.Min != nil {
				out.Min = *f.Min
			}
			if f.Since != nil {
				out.Since = f.Since.String()
			}
		}
		return out, nil
	}
	return one("/items", get(h))
}

// A query parameter of a struct type is a deepObject: filter[min]=1 binds
// the member min, as a form binds its fields; a member the struct does not
// name is refused, as a form's is; and the parameter is absent when the
// query has no filter[...] key, which an optional one takes and a required
// one refuses.
func TestDeepObjectQueryParameters(t *testing.T) {
	c := getatest.New(t, deepTable())
	for path, want := range map[string]deepOut{
		"/items?page[size]=5":   {Size: 5},
		"/items?page[cursor]=x": {Size: 20, Cursor: "x"},
		"/items?filter[min]=1&filter[since]=2026-05-01&page[size]=1":              {Filtered: true, Min: 1, Status: "open", Since: "2026-05-01", Size: 1},
		"/items?filter%5Bstatus%5D=closed&page%5Bsize%5D=2&q=x":                   {Filtered: true, Status: "closed", Size: 2},
		"/items?filter[max]=9&page[size]=3&filter=ignored&filterx[min]=1&page=no": {Filtered: true, Status: "open", Size: 3},
	} {
		res := c.Get(path)
		if res.Status != http.StatusOK {
			t.Errorf("%s: %d %s", path, res.Status, res.Text())
			continue
		}
		if got := res.JSON[deepOut](); got != want {
			t.Errorf("%s: %+v, want %+v", path, got, want)
		}
	}
	for path, want := range map[string]string{
		"/items":                                          "page: missing required parameter",
		"/items?page[size]=101":                           "page[size]: 101 is greater than maximum 100",
		"/items?page[size]=1&filter[min]=-1":              "filter[min]: -1 is less than minimum 0",
		"/items?page[size]=1&filter[bogus]=1":             "filter[bogus]: unknown field",
		"/items?page[size]=1&filter[min]=1&filter[min]=2": `filter[min]: given 2 times, but takes one value`,
		"/items?page[size]=1&filter[status]=gone":         `filter[status]: "gone" is not one of open, closed`,
		"/items?page[size]=1&filter[min][x]=1":            "filter[min][x]: unknown field",
	} {
		res := c.Get(path)
		p := res.Problem()
		if res.Status != http.StatusBadRequest || len(p.Errors) != 1 || p.Errors[0].In != "query" || p.Errors[0].Path+": "+p.Errors[0].Message != want {
			t.Errorf("%s: %d %+v; want %s", path, res.Status, p.Errors, want)
		}
	}

	// The typed client sends each member it holds as name[member].
	since, _ := geta.ParseDate("2026-01-02")
	minimum := 3
	out, err := getaclient.Call[deepIn, deepOut](t.Context(), c.Typed(), http.MethodGet, "/items",
		&deepIn{Filter: &deepFilter{Min: &minimum, Status: "closed", Since: &since}, Page: deepPage{Size: 7}})
	if err != nil || *out != (deepOut{Filtered: true, Min: 3, Status: "closed", Since: "2026-01-02", Size: 7}) {
		t.Fatal(out, err)
	}
	out, err = getaclient.Call[deepIn, deepOut](t.Context(), c.Typed(), http.MethodGet, "/items", &deepIn{Page: deepPage{Size: 7}})
	if err != nil || out.Filtered {
		t.Fatal(out, err)
	}
}

// A deepObject is documented as OpenAPI defines one: style deepObject,
// explode true, an object schema of its members, closed as a form is.
func TestDeepObjectIsDocumented(t *testing.T) {
	m := doc(t, accepts(t, deepTable()))
	params := at(t, m, "paths", "/items", "get", "parameters").([]any)
	if got := compact(t, params[0]); got != `{"description":"What to keep","explode":true,"in":"query","name":"filter","required":false,`+
		`"schema":{"additionalProperties":false,"properties":{"max":{"format":"int64","type":"integer"},"min":{"format":"int64","minimum":0,"type":"integer"},`+
		`"since":{"description":"Not before this day","format":"date","type":"string"},"status":{"default":"open","enum":["open","closed"],"type":"string"}},"type":"object"},"style":"deepObject"}` {
		t.Fatal(got)
	}
	if got := compact(t, params[1]); got != `{"explode":true,"in":"query","name":"page","required":true,`+
		`"schema":{"additionalProperties":false,"properties":{"cursor":{"maxLength":16,"type":"string"},"size":{"default":20,"format":"int64","maximum":100,"type":"integer"}},"type":"object"},"style":"deepObject"}` {
		t.Fatal(got)
	}
}

// What a deepObject cannot hold is refused: a member that is an array,
// which OpenAPI does not define for one, a file, a field with no form tag,
// and a constraint on the object itself; a struct is a deepObject in a query
// alone.
func TestDeepObjectMistakesAreRefused(t *testing.T) {
	type withSlice struct {
		Tags []string `form:"tags"`
	}
	type sliceIn struct {
		F withSlice `query:"f"`
	}
	type untagged struct {
		A string
	}
	type untaggedIn struct {
		F untagged `query:"f"`
	}
	type constrained struct {
		F deepPage `query:"f" schema:"maxLength=3"`
	}
	type deprecated struct {
		F deepPage `query:"f" schema:"deprecated=true"`
	}
	type header struct {
		F ok `header:"X-F"`
	}
	type withFile struct {
		F geta.File `form:"f"`
	}
	type fileIn struct {
		F withFile `query:"f"`
	}
	// A deepObject binds every key filter[...]: a plain parameter named one
	// of them, before or after it, would be bound twice.
	type plainAfter struct {
		F   *deepPage `query:"filter"`
		Min *int      `query:"filter[min]"`
	}
	type plainBefore struct {
		Min *string   `query:"filter[x]"`
		F   *deepPage `query:"filter"`
	}
	type nested struct {
		F *deepPage `query:"filter"`
		G *deepPage `query:"filter[a]"`
	}
	for name, c := range map[string]struct {
		tbl  geta.Table
		want string
	}{
		"slice member":  {one("/x", get(func(context.Context, *sliceIn) (*ok, error) { return nil, nil })), `Tags: deepObject member "tags" has non-scalar type []string`},
		"untagged":      {one("/x", get(func(context.Context, *untaggedIn) (*ok, error) { return nil, nil })), "A has no form tag"},
		"constrained":   {one("/x", get(func(context.Context, *constrained) (*ok, error) { return nil, nil })), "on a struct type"},
		"header struct": {one("/x", get(func(context.Context, *header) (*ok, error) { return nil, nil })), `header parameter "X-F" has unsupported type geta_test.ok`},
		"file member":   {one("/x", get(func(context.Context, *fileIn) (*ok, error) { return nil, nil })), `form field "f" has file type geta.File outside a multipart body`},
		"plain after":   {one("/x", get(func(context.Context, *plainAfter) (*ok, error) { return nil, nil })), `Min binds query parameter "filter[min]", which deepObject "filter" (F) also binds`},
		"plain before":  {one("/x", get(func(context.Context, *plainBefore) (*ok, error) { return nil, nil })), `F: deepObject "filter" also binds query parameter "filter[x]" (Min)`},
		"nested":        {one("/x", get(func(context.Context, *nested) (*ok, error) { return nil, nil })), `G binds query parameter "filter[a]", which deepObject "filter" (F) also binds`},
	} {
		_, err := geta.New(c.tbl)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v\nwant %q", name, err, c.want)
		}
	}
	m := doc(t, accepts(t, one("/x", get(func(context.Context, *deprecated) (*ok, error) { return nil, nil }))))
	if got := at(t, m, "paths", "/x", "get", "parameters").([]any)[0].(map[string]any)["deprecated"]; got != true {
		t.Fatal(got)
	}
}
