package geta_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
)

type limitedNote struct {
	Text string `json:"text"`
}

type limitedNoteIn struct {
	Body limitedNote `body:"json"`
}

type limitedTags struct {
	Tags []string `json:"tags"`
}

type limitedTagsIn struct {
	Body limitedTags `body:"json"`
}

type limitedQuery struct {
	Q string `query:"q"`
}

func postNote(_ context.Context, in *limitedNoteIn) (*ok, error) { return &ok{true}, nil }

// raise returns a Doc.Limits that changes the App's limits by change.
func raise(change func(*geta.Limits)) func(geta.Limits) geta.Limits {
	return func(l geta.Limits) geta.Limits {
		change(&l)
		return l
	}
}

// An operation's Doc.Limits replace the App's for it alone: its body, its
// parameters, and its body's members are read under them, and its document
// states them (its 413's body limit, its backstops). Two operations sharing a
// component under limits that differ only where the component states nothing
// share it.
func TestOperationLimitsAreTheOperationsOwn(t *testing.T) {
	big := raise(func(l *geta.Limits) { l.MaxBodyBytes = 4 << 20 })
	short := raise(func(l *geta.Limits) { l.MaxStringLength = 8 })
	a := accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/small", Route: geta.Route{Post: geta.Op(http.StatusOK, postNote, geta.Doc{})}},
		{Path: "/big", Route: geta.Route{Post: geta.Op(http.StatusOK, postNote, geta.Doc{Limits: big})}},
		{Path: "/short", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *limitedQuery) (*ok, error) { return &ok{true}, nil },
			geta.Doc{Limits: short})}},
	}})
	// 2 MiB of JSON whitespace around a short note.
	body := `{"text":"a"}` + strings.Repeat(" ", 2<<20)
	post := func(path string) int {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := post("/small"); got != http.StatusRequestEntityTooLarge {
		t.Errorf("/small: %d", got)
	}
	if got := post("/big"); got != http.StatusOK {
		t.Errorf("/big: %d", got)
	}
	if got := do(t, a, http.MethodGet, "/short?q=12345678").Code; got != http.StatusOK {
		t.Errorf("8 code points: %d", got)
	}
	if got := do(t, a, http.MethodGet, "/short?q=123456789").Code; got != http.StatusBadRequest {
		t.Errorf("9 code points: %d", got)
	}
	m := doc(t, a)
	for path, want := range map[string]string{"/small": "1048576", "/big": "4194304"} {
		if d := at(t, m, "paths", path, "post", "responses", "413", "description").(string); !strings.Contains(d, want+" bytes") {
			t.Errorf("%s 413: %q", path, d)
		}
	}
	if got := at(t, m, "paths", "/short", "get", "parameters").([]any)[0].(map[string]any)["schema"].(map[string]any)["maxLength"]; got != 8.0 {
		t.Errorf("/short q maxLength %v", got)
	}
	// The note is read by both under limits that state it alike.
	if got := at(t, m, "components", "schemas", "limitedNote", "properties", "text", "maxLength"); got != 4096.0 {
		t.Errorf("limitedNote.text maxLength %v", got)
	}
}

// A component read only under an operation's own limits states them; one
// read by two operations under limits that would state it differently is
// refused, unless its fields declare the bounds that replace the backstops;
// limits New refuses of WithLimits are refused of Doc.Limits too.
func TestOperationLimitsKeepComponentsTruthful(t *testing.T) {
	few := raise(func(l *geta.Limits) { l.MaxItems = 3 })
	postTags := func(_ context.Context, in *limitedTagsIn) (*ok, error) { return &ok{true}, nil }
	a := accepts(t, one("/tags", geta.Route{Post: geta.Op(http.StatusOK, postTags, geta.Doc{Limits: few})}))
	if got := at(t, doc(t, a), "components", "schemas", "limitedTags", "properties", "tags", "maxItems"); got != 3.0 {
		t.Errorf("maxItems %v", got)
	}
	req := httptest.NewRequest(http.MethodPost, "/tags", strings.NewReader(`{"tags":["a","b","c","d"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("four tags: %d", rec.Code)
	}

	long := raise(func(l *geta.Limits) { l.MaxStringLength = 10000 })
	rejects(t, geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Post: geta.Op(http.StatusOK, postNote, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Post: geta.Op(http.StatusOK, postNote, geta.Doc{Limits: long})}},
	}}, "POST /a and POST /b read geta_test.limitedNote under conflicting limits",
		"MaxStringLength 4096 and 10000")

	type declared struct {
		Text string `json:"text" schema:"maxLength=100"`
	}
	type declaredIn struct {
		Body declared `body:"json"`
	}
	postDeclared := func(_ context.Context, in *declaredIn) (*ok, error) { return &ok{true}, nil }
	accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Post: geta.Op(http.StatusOK, postDeclared, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Post: geta.Op(http.StatusOK, postDeclared, geta.Doc{Limits: long})}},
	}})

	rejects(t, one("/x", geta.Route{Post: geta.Op(http.StatusOK, postNote, geta.Doc{Limits: raise(func(l *geta.Limits) { l.MaxItems = 0 })})}),
		"Doc.Limits: ", "MaxItems")
}
