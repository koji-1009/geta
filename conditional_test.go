package geta_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

type condIn struct{ geta.Conditional }

type requiredIn struct{ geta.RequireConditional }

// The current representation every conditional route below answers for: a
// strong tag, and a modification half a second into 03:04:05, which an
// HTTP-date names to the second.
var (
	current    = `"v2"`
	modifiedAt = time.Date(2024, 1, 2, 3, 4, 5, 500_000_000, time.UTC)
)

const (
	dateEarly = "Tue, 02 Jan 2024 03:04:04 GMT"
	dateExact = "Tue, 02 Jan 2024 03:04:05 GMT"
	dateLate  = "Tue, 02 Jan 2024 03:04:06 GMT"
)

// conditionalTable serves, on /r, a GET and a PUT that check the current
// representation; on /absent, a PUT that checks a target with none; on
// /dated, a GET of a representation with a date and no tag; on /weak, a GET
// and a PUT of one with a weak tag; and on /req, a PUT that requires a
// precondition.
func conditionalTable() geta.Table {
	check := func(etag string, mod time.Time) func(context.Context, *condIn) (*ok, error) {
		return func(_ context.Context, in *condIn) (*ok, error) {
			if err := in.Check(etag, mod); err != nil {
				return nil, err
			}
			return &ok{true}, nil
		}
	}
	put := func(etag string, mod time.Time) func(context.Context, *condIn) error {
		return func(_ context.Context, in *condIn) error { return in.Check(etag, mod) }
	}
	return geta.Table{Routes: []geta.Entry{
		{Path: "/r", Route: geta.Route{
			Get: geta.Op(http.StatusOK, check(current, modifiedAt), geta.Doc{}),
			Put: geta.OpNoBody(http.StatusNoContent, put(current, modifiedAt), geta.Doc{}),
		}},
		{Path: "/absent", Route: geta.Route{
			Put: geta.OpNoBody(http.StatusNoContent, func(_ context.Context, in *condIn) error { return in.CheckAbsent() }, geta.Doc{}),
		}},
		{Path: "/dated", Route: geta.Route{Get: geta.Op(http.StatusOK, check("", modifiedAt), geta.Doc{})}},
		{Path: "/weak", Route: geta.Route{
			Get: geta.Op(http.StatusOK, check(`W/"w"`, time.Time{}), geta.Doc{}),
			Put: geta.OpNoBody(http.StatusNoContent, put(`W/"w"`, time.Time{}), geta.Doc{}),
		}},
		{Path: "/req", Route: geta.Route{
			Put: geta.OpNoBody(http.StatusNoContent, func(_ context.Context, in *requiredIn) error {
				return in.Check(current, modifiedAt)
			}, geta.Doc{}),
		}},
	}}
}

// RFC 9110 §13.2.2, row by row: which field decides, in which order, and
// what each answers by method.
func TestConditionalEvaluationOrder(t *testing.T) {
	a := accepts(t, conditionalTable())
	for _, c := range []struct {
		name, method, path string
		headers            []string
		want               int
	}{
		{"no precondition GET", "GET", "/r", nil, 200},
		{"no precondition PUT", "PUT", "/r", nil, 204},

		// Step 1: If-Match, compared strongly.
		{"If-Match current", "PUT", "/r", header("If-Match", `"v2"`), 204},
		{"If-Match stale", "PUT", "/r", header("If-Match", `"v1"`), 412},
		{"If-Match stale on GET is 412, not 304", "GET", "/r", header("If-Match", `"v1"`), 412},
		{"If-Match *", "PUT", "/r", header("If-Match", "*"), 204},
		{"If-Match weak form of the current tag", "PUT", "/r", header("If-Match", `W/"v2"`), 412},
		{"If-Match list holding the current tag", "PUT", "/r", header("If-Match", `"v1", "v2"`), 204},
		{"If-Match lines read as one list", "PUT", "/r", []string{"If-Match", `"v1"`, "If-Match", `"v2"`}, 204},
		{"If-Match list with empty elements", "PUT", "/r", header("If-Match", `, "v1" ,, "v2",`), 204},
		{"If-Match tag holding a comma", "PUT", "/r", header("If-Match", `"v,2"`), 412},
		{"If-Match on a weak current tag never matches", "PUT", "/weak", header("If-Match", `W/"w"`), 412},
		{"If-Match strong form of a weak current tag", "PUT", "/weak", header("If-Match", `"w"`), 412},
		{"If-Match * on a weak current tag", "PUT", "/weak", header("If-Match", "*"), 204},

		// Step 2: If-Unmodified-Since, only without If-Match, to the second.
		{"If-Unmodified-Since before the change", "PUT", "/r", header("If-Unmodified-Since", dateEarly), 412},
		{"If-Unmodified-Since the second of the change", "PUT", "/r", header("If-Unmodified-Since", dateExact), 204},
		{"If-Unmodified-Since after the change", "PUT", "/r", header("If-Unmodified-Since", dateLate), 204},
		{"If-Unmodified-Since in RFC 850 form", "PUT", "/r", header("If-Unmodified-Since", "Tuesday, 02-Jan-24 03:04:04 GMT"), 412},
		{"If-Unmodified-Since RFC 850 year within 50 years ahead", "PUT", "/r", header("If-Unmodified-Since", "Wednesday, 01-Jan-70 00:00:00 GMT"), 204},
		{"If-Unmodified-Since in asctime form", "PUT", "/r", header("If-Unmodified-Since", "Tue Jan  2 03:04:04 2024"), 412},
		{"If-Unmodified-Since not in GMT is ignored", "PUT", "/r", header("If-Unmodified-Since", "Tue, 02 Jan 2024 03:04:04 UTC"), 204},
		{"If-Unmodified-Since RFC 850 not in GMT is ignored", "PUT", "/r", header("If-Unmodified-Since", "Tuesday, 02-Jan-24 03:04:04 PST"), 204},
		{"If-Unmodified-Since malformed is ignored", "PUT", "/r", header("If-Unmodified-Since", "yesterday"), 204},
		{"If-Unmodified-Since list is ignored", "PUT", "/r", []string{"If-Unmodified-Since", dateEarly, "If-Unmodified-Since", dateEarly}, 204},
		{"If-Unmodified-Since ignored beside If-Match", "PUT", "/r", []string{"If-Match", `"v2"`, "If-Unmodified-Since", dateEarly}, 204},
		{"If-Unmodified-Since without a date to compare", "PUT", "/weak", header("If-Unmodified-Since", dateEarly), 204},

		// Step 3: If-None-Match, compared weakly; 304 on GET and HEAD.
		{"If-None-Match current GET", "GET", "/r", header("If-None-Match", `"v2"`), 304},
		{"If-None-Match current HEAD", "HEAD", "/r", header("If-None-Match", `"v2"`), 304},
		{"If-None-Match current PUT", "PUT", "/r", header("If-None-Match", `"v2"`), 412},
		{"If-None-Match weak form GET", "GET", "/r", header("If-None-Match", `W/"v2"`), 304},
		{"If-None-Match weak form PUT", "PUT", "/r", header("If-None-Match", `W/"v2"`), 412},
		{"If-None-Match strong form of a weak tag", "GET", "/weak", header("If-None-Match", `"w"`), 304},
		{"If-None-Match * GET", "GET", "/r", header("If-None-Match", "*"), 304},
		{"If-None-Match * PUT", "PUT", "/r", header("If-None-Match", "*"), 412},
		{"If-None-Match stale GET", "GET", "/r", header("If-None-Match", `"v1"`), 200},
		{"If-None-Match stale PUT", "PUT", "/r", header("If-None-Match", `"v1"`), 204},
		{"If-Match holds, then If-None-Match decides", "PUT", "/r", []string{"If-Match", `"v2"`, "If-None-Match", `"v2"`}, 412},
		{"If-Match fails before If-None-Match", "GET", "/r", []string{"If-Match", `"v1"`, "If-None-Match", `"v2"`}, 412},
		{"If-Unmodified-Since holds, then If-None-Match decides", "PUT", "/r", []string{"If-Unmodified-Since", dateLate, "If-None-Match", "*"}, 412},
		{"If-Unmodified-Since fails before If-None-Match", "GET", "/r", []string{"If-Unmodified-Since", dateEarly, "If-None-Match", `"v2"`}, 412},

		// Step 4: If-Modified-Since, on GET and HEAD without If-None-Match.
		{"If-Modified-Since the second of the change", "GET", "/r", header("If-Modified-Since", dateExact), 304},
		{"If-Modified-Since after the change", "GET", "/r", header("If-Modified-Since", dateLate), 304},
		{"If-Modified-Since before the change", "GET", "/r", header("If-Modified-Since", dateEarly), 200},
		{"If-Modified-Since HEAD", "HEAD", "/r", header("If-Modified-Since", dateExact), 304},
		{"If-Modified-Since ignored on PUT", "PUT", "/r", header("If-Modified-Since", dateExact), 204},
		{"If-Modified-Since malformed is ignored", "GET", "/r", header("If-Modified-Since", "now"), 200},
		{"If-Modified-Since list is ignored", "GET", "/r", []string{"If-Modified-Since", dateExact, "If-Modified-Since", dateExact}, 200},
		{"If-Modified-Since ignored beside If-None-Match", "GET", "/r", []string{"If-None-Match", `"v1"`, "If-Modified-Since", dateLate}, 200},
		{"If-Modified-Since without a date to compare", "GET", "/weak", header("If-Modified-Since", dateLate), 200},

		// A list that is not one is refused, not guessed at.
		{"If-Match * in a list", "PUT", "/r", header("If-Match", `"v1", *`), 400},
		{"If-Match unquoted", "PUT", "/r", header("If-Match", "v2"), 400},
		{"If-Match unterminated", "PUT", "/r", header("If-Match", `"v2`), 400},
		{"If-Match * twice", "PUT", "/r", []string{"If-Match", "*", "If-Match", "*"}, 400},
		{"If-None-Match unquoted", "GET", "/r", header("If-None-Match", "v2"), 400},
		{"If-Match empty names no condition", "PUT", "/r", header("If-Match", ""), 400},
		{"If-None-Match empty names no condition", "PUT", "/r", header("If-None-Match", ""), 400},

		// A target with no current representation.
		{"absent: If-None-Match *", "PUT", "/absent", header("If-None-Match", "*"), 204},
		{"absent: If-None-Match a tag", "PUT", "/absent", header("If-None-Match", `"v1"`), 204},
		{"absent: If-Match *", "PUT", "/absent", header("If-Match", "*"), 412},
		{"absent: If-Match a tag", "PUT", "/absent", header("If-Match", `"v1"`), 412},
		{"absent: If-Unmodified-Since", "PUT", "/absent", header("If-Unmodified-Since", dateEarly), 204},

		// RFC 6585: a required precondition.
		{"required: none", "PUT", "/req", nil, 428},
		{"required: If-Modified-Since is not one on PUT", "PUT", "/req", header("If-Modified-Since", dateEarly), 428},
		{"required: a malformed date is not one", "PUT", "/req", header("If-Unmodified-Since", "soon"), 428},
		{"required: If-Match", "PUT", "/req", header("If-Match", `"v2"`), 204},
		{"required: If-Match stale", "PUT", "/req", header("If-Match", `"v1"`), 412},
		{"required: If-Unmodified-Since", "PUT", "/req", header("If-Unmodified-Since", dateExact), 204},
		{"required: If-None-Match *", "PUT", "/req", header("If-None-Match", "*"), 412},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, a, c.method, c.path, c.headers...)
			if rec.Code != c.want {
				t.Fatalf("%d, want %d: %s", rec.Code, c.want, rec.Body)
			}
			switch c.want {
			case 304:
				if rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
					t.Fatalf("a 304 has a body: %q %q", rec.Header().Get("Content-Type"), rec.Body)
				}
			case 400, 412, 428:
				if rec.Header().Get("Content-Type") != geta.ProblemContentType {
					t.Fatalf("not a problem: %q", rec.Header().Get("Content-Type"))
				}
			}
		})
	}
}

// A 304 carries the validator a 200 would: the tag, or, for a
// representation without one, its date, to the second.
func TestNotModifiedCarriesTheValidator(t *testing.T) {
	a := accepts(t, conditionalTable())
	rec := do(t, a, "GET", "/r", header("If-None-Match", `W/"v2"`)...)
	if rec.Code != 304 || rec.Header().Get("ETag") != `"v2"` || rec.Header().Get("Last-Modified") != "" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	rec = do(t, a, "GET", "/dated", header("If-Modified-Since", dateExact)...)
	if rec.Code != 304 || rec.Header().Get("ETag") != "" || rec.Header().Get("Last-Modified") != dateExact {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	// No tag to compare: If-None-Match holds, and If-Modified-Since is ignored.
	if rec := do(t, a, "GET", "/dated", "If-None-Match", `"x"`, "If-Modified-Since", dateExact); rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
}

// The problems say which field failed, and a malformed list is a contract
// violation naming the header.
func TestPreconditionProblems(t *testing.T) {
	a := accepts(t, conditionalTable())
	problem := func(method, path string, headers ...string) geta.Problem {
		t.Helper()
		var p geta.Problem
		if err := json.Unmarshal(do(t, a, method, path, headers...).Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := problem("PUT", "/r", "If-Match", `"v1"`); p.Status != 412 || p.Title != "Precondition Failed" || !strings.HasPrefix(p.Detail, "If-Match failed") {
		t.Errorf("%+v", p)
	}
	if p := problem("PUT", "/r", "If-Unmodified-Since", dateEarly); p.Status != 412 || !strings.HasPrefix(p.Detail, "If-Unmodified-Since failed") {
		t.Errorf("%+v", p)
	}
	if p := problem("PUT", "/r", "If-None-Match", "*"); p.Status != 412 || !strings.HasPrefix(p.Detail, "If-None-Match failed") {
		t.Errorf("%+v", p)
	}
	if p := problem("PUT", "/req"); p.Status != 428 || p.Title != "Precondition Required" || !strings.Contains(p.Detail, "If-Match") {
		t.Errorf("%+v", p)
	}
	p := problem("PUT", "/r", "If-None-Match", `"v1", *`)
	if p.Status != 400 || len(p.Errors) != 1 || p.Errors[0].In != "header" || p.Errors[0].Path != "If-None-Match" {
		t.Errorf("%+v", p)
	}
}

// An If-Match or If-None-Match whose list holds no entity tag (an empty
// line, several, or only commas) names no condition: a client that sent one
// lost the tag it meant to send, and evaluated it would be a 412 that no tag
// matches, or for If-None-Match a write with no condition, past
// RequireConditional's 428. It is a 400, on one line as on several.
func TestEmptyPreconditionListsAreRefused(t *testing.T) {
	a := accepts(t, conditionalTable())
	for _, path := range []string{"/r", "/req"} {
		for _, name := range []string{"If-Match", "If-None-Match"} {
			for _, lines := range [][]string{{""}, {"", ""}, {" , "}} {
				var headers []string
				for _, l := range lines {
					headers = append(headers, name, l)
				}
				var p geta.Problem
				if err := json.Unmarshal(do(t, a, "PUT", path, headers...).Body.Bytes(), &p); err != nil {
					t.Fatal(err)
				}
				if p.Status != 400 || len(p.Errors) != 1 || p.Errors[0].In != "header" || p.Errors[0].Path != name {
					t.Errorf("PUT %s %s %q: %+v", path, name, lines, p)
				}
			}
		}
	}
}

// A date precondition is an HTTP-date only as RFC 9110 §5.6.7's grammar
// writes it. time.Parse takes more (a one-digit hour, a fraction of a
// second, an asctime day with no space before it, a run of spaces, a month
// or day name in another case), which a recipient must ignore; and it
// refuses the leap second 23:59:60, which the grammar admits and which is
// read as 23:59:59. Each date below would decide the PUT were it read.
func TestDatePreconditionsAreReadAsTheGrammarWritesThem(t *testing.T) {
	a := accepts(t, conditionalTable())
	for date, want := range map[string]int{
		"Tue, 02 Jan 2024 3:04:04 GMT":      204,
		"Tue, 02 Jan 2024 03:04:04.9 GMT":   204,
		"Tue Jan 2 03:04:04 2024":           204,
		"Tue,  02 Jan 2024 03:04:04 GMT":    204,
		"Tue, 02 JAN 2024 03:04:04 GMT":     204,
		"tue, 02 Jan 2024 03:04:04 GMT":     204,
		"Tuesday, 02-Jan-24 3:04:04 GMT":    204,
		"Tue, 02 Jan 2024 03:04:04 gmt":     204,
		"Tue, 02 Jan 2024 03:04:60 GMT":     204, // a leap second is at 23:59 alone
		"Tue Jan  2 03:04:04 2024":          412,
		"Mon, 01 Jan 2024 23:59:60 GMT":     412, // before the change
		"Monday, 01-Jan-24 23:59:60 GMT":    412,
		"Mon Jan  1 23:59:60 2024":          412,
		"Wed, 02 Jan 2024 03:04:04 GMT":     412, // the day name is only checked to be one
		"Tue, 02 Jan 2024 03:04:04 GMT":     412,
		"Tuesday, 02-Jan-24 03:04:04 GMT":   412,
		"Tue, 02 Jan 2024 23:59:60 GMT":     204, // after it
		"Wednesday, 01-Jan-70 00:00:00 GMT": 204,
	} {
		if rec := do(t, a, "PUT", "/r", "If-Unmodified-Since", date); rec.Code != want {
			t.Errorf("If-Unmodified-Since: %s: %d, want %d", date, rec.Code, want)
		}
	}
	if rec := do(t, a, "GET", "/r", "If-Modified-Since", "Tue, 02 Jan 2024 23:59:60 GMT"); rec.Code != 304 {
		t.Errorf("If-Modified-Since a leap second after the change: %d", rec.Code)
	}
	if rec := do(t, a, "GET", "/r", "If-Modified-Since", "Tue, 02 Jan 2024 3:04:06 GMT"); rec.Code != 200 {
		t.Errorf("If-Modified-Since with a one-digit hour: %d", rec.Code)
	}
}

// "*" is the value "*" alone, but for the spaces and tabs a field value's
// list may hold around it: with any other space around it, a no-break space
// or U+0085 among them, it is neither "*" nor a list of entity tags, and a
// 400, whether Check or geta.ETag reads it.
func TestStarIsAloneButForSpacesAndTabs(t *testing.T) {
	a := accepts(t, conditionalTable())
	for _, v := range []string{"*\xc2\xa0", "\xc2\xa0*", "*\xc2\x85", "*\xe2\x80\x83"} {
		for _, name := range []string{"If-Match", "If-None-Match"} {
			if rec := do(t, a, "PUT", "/r", name, v); rec.Code != 400 {
				t.Errorf("%s: %q: %d", name, v, rec.Code)
			}
		}
	}
	e := accepts(t, withRoot(one("/x", get(textHandler("hi"))), geta.ETag()))
	for v, want := range map[string]int{"*\xc2\xa0": 400, "*\xc2\x85": 400, " *\t": 304, "*": 304} {
		if rec := do(t, e, "GET", "/x", "If-None-Match", v); rec.Code != want {
			t.Errorf("ETag: %q: %d, want %d", v, rec.Code, want)
		}
	}
}

// The verdict is an error the handler returns; a test of the handler alone
// reads its status. Unbound, a Conditional has no method, so a matching
// If-None-Match is a 412, as on any unsafe method.
func TestPreconditionErrorStatus(t *testing.T) {
	in := geta.Conditional{IfMatch: new(`"v1"`)}
	pe, ok := errors.AsType[*geta.PreconditionError](in.Check(current, modifiedAt))
	if !ok || pe.Status() != http.StatusPreconditionFailed || !strings.Contains(pe.Error(), "If-Match") {
		t.Fatal(pe, ok)
	}
	in = geta.Conditional{IfNoneMatch: new("*")}
	if pe, ok := errors.AsType[*geta.PreconditionError](in.Check(current, modifiedAt)); !ok || pe.Status() != http.StatusPreconditionFailed {
		t.Fatal(pe, ok)
	}
	if err := new(geta.RequireConditional).CheckAbsent(); err == nil {
		t.Fatal("an unconditional request passed a required precondition")
	} else if pe, ok := errors.AsType[*geta.PreconditionError](err); !ok || pe.Status() != http.StatusPreconditionRequired {
		t.Fatal(err)
	}
	if err := new(geta.Conditional).Check(current, modifiedAt); err != nil {
		t.Fatal(err)
	}
}

// A handler that passes something other than an entity tag has a defect:
// the request answers 500, never a precondition's status.
func TestCheckRefusesATagThatIsNotOne(t *testing.T) {
	for _, tag := range []string{"v2", `"v2" `, `"a", "b"`, `W/v2`} {
		a := accepts(t, one("/x", get(func(_ context.Context, in *condIn) (*ok, error) {
			return &ok{}, in.Check(tag, time.Time{})
		})))
		if rec := do(t, a, "GET", "/x", "If-None-Match", "*"); rec.Code != 500 {
			t.Errorf("%q: %d", tag, rec.Code)
		}
	}
}

// Only an operation whose input embeds a Conditional answers its verdict:
// anywhere else, the document lists no 412, and the error is a defect.
func TestPreconditionErrorElsewhereIsADefect(t *testing.T) {
	verdict := (&geta.Conditional{IfMatch: new(`"x"`)}).Check(current, time.Time{})
	a := accepts(t, one("/x", get(func(context.Context, *empty) (*ok, error) { return nil, verdict })))
	if rec := do(t, a, "GET", "/x"); rec.Code != 500 {
		t.Fatal(rec.Code)
	}
	// A 428 on an operation that does not require a precondition is not
	// listed either.
	required := new(geta.RequireConditional).Check(current, time.Time{})
	a = accepts(t, one("/x", get(func(context.Context, *condIn) (*ok, error) { return nil, required })))
	if rec := do(t, a, "GET", "/x"); rec.Code != 500 {
		t.Fatal(rec.Code)
	}
}

// A catch-all row swallows neither defect: an etag that is not one entity
// tag, and a PreconditionError where the operation answers none, are logged
// 500s carrying the instance their log line carries, whatever the rows.
func TestACatchAllRowDoesNotSwallowAConditionalDefect(t *testing.T) {
	catchAll := geta.Doc{Failures: []geta.Failure{geta.OnAs[error](http.StatusServiceUnavailable, "")}}
	for name, r := range map[string]geta.Route{
		"bad tag": {Get: geta.Op(http.StatusOK, func(_ context.Context, in *condIn) (*ok, error) {
			return &ok{}, in.Check("v2", time.Time{})
		}, catchAll)},
		"elsewhere": {Get: geta.Op(http.StatusOK, func(context.Context, *empty) (*ok, error) {
			return nil, (&geta.Conditional{IfMatch: new(`"x"`)}).Check(current, time.Time{})
		}, catchAll)},
		"not listed": {Get: geta.Op(http.StatusOK, func(context.Context, *condIn) (*ok, error) {
			return nil, new(geta.RequireConditional).Check(current, time.Time{})
		}, catchAll)},
	} {
		log, buf := logger()
		a, err := geta.New(one("/x", r), geta.WithLogger(log))
		if err != nil {
			t.Fatal(err)
		}
		// No precondition is sent: geta evaluates one itself where the input
		// embeds no Conditional.
		rec := do(t, a, "GET", "/x")
		var p geta.Problem
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != 500 || p.Instance == "" ||
			!strings.Contains(buf.String(), "level=ERROR") || !strings.Contains(buf.String(), p.Instance) {
			t.Errorf("%s: %d %s %s", name, rec.Code, rec.Body, buf)
		}
	}
}

// The document lists the four header parameters, optional and described,
// and what Check answers: 412 everywhere, 304 on a GET, 428 where a
// precondition is required.
func TestConditionalIsDocumented(t *testing.T) {
	var doc struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name        string `json:"name"`
				In          string `json:"in"`
				Required    bool   `json:"required"`
				Description string `json:"description"`
			} `json:"parameters"`
			Responses map[string]struct {
				Description string         `json:"description"`
				Content     map[string]any `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(accepts(t, conditionalTable()).OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path, method string
		listed, not  []string
	}{
		{"/r", "get", []string{"304", "412"}, []string{"428"}},
		{"/r", "put", []string{"412"}, []string{"304", "428"}},
		{"/req", "put", []string{"412", "428"}, []string{"304"}},
	} {
		op := doc.Paths[c.path][c.method]
		var names []string
		for _, p := range op.Parameters {
			if p.In != "header" || p.Required || p.Description == "" {
				t.Errorf("%s %s: %+v", c.method, c.path, p)
			}
			names = append(names, p.Name)
		}
		if got := strings.Join(names, ","); got != "If-Match,If-None-Match,If-Modified-Since,If-Unmodified-Since" {
			t.Errorf("%s %s: parameters %s", c.method, c.path, got)
		}
		for _, s := range c.listed {
			if _, ok := op.Responses[s]; !ok {
				t.Errorf("%s %s does not list %s", c.method, c.path, s)
			}
		}
		for _, s := range c.not {
			if _, ok := op.Responses[s]; ok {
				t.Errorf("%s %s lists %s", c.method, c.path, s)
			}
		}
		if r := op.Responses["304"]; r.Content != nil {
			t.Errorf("%s %s: a 304 with content", c.method, c.path)
		}
		if r := op.Responses["412"]; r.Content[geta.ProblemContentType] == nil {
			t.Errorf("%s %s: 412 is not a problem", c.method, c.path)
		}
	}
	if op := doc.Paths["/r"]["get"]; !strings.Contains(op.Responses["412"].Description, "If-Match") || strings.Contains(op.Responses["412"].Description, "If-None-Match") {
		t.Errorf("GET 412: %q", op.Responses["412"].Description)
	}
	// The 304 states the validator it carries.
	var m map[string]any
	if err := json.Unmarshal(accepts(t, conditionalTable()).OpenAPI(), &m); err != nil {
		t.Fatal(err)
	}
	if got := compact(t, at(t, m, "paths", "/r", "get", "responses", "304", "headers")); got != `{"ETag":{"description":"The entity tag given to Conditional.Check, when one was","required":false,"schema":{"type":"string"}},"Last-Modified":{"description":"The modification date given to Conditional.Check, an HTTP-date, when no entity tag was","required":false,"schema":{"type":"string"}}}` {
		t.Error(got)
	}
}

// Every status Check answers is one the document lists: getatest fails a
// test that provokes any other.
func TestConditionalStatusesAreDocumentedOnTheWire(t *testing.T) {
	c := getatest.New(t, conditionalTable())
	for _, res := range []*getatest.Response{
		c.With("If-None-Match", `"v2"`).Get("/r"),
		c.With("If-None-Match", `"v2"`).Do(http.MethodHead, "/r", nil),
		c.With("If-Match", `"v1"`).Get("/r"),
		c.With("If-Match", `"v1"`).Put("/r", nil),
		c.With("If-Match", "v1").Put("/r", nil),
		c.Put("/req", nil),
	} {
		if res.Status < 300 {
			t.Errorf("%d", res.Status)
		}
	}
}

// Beside the ETag middleware: a handler's verdict passes through it, and the
// two compare If-None-Match alike, so the handler's tag answers 304 the same
// whether Check or the middleware decides.
func TestConditionalBesideTheETagMiddleware(t *testing.T) {
	type tagged struct {
		ETag string `header:"ETag"`
		Body ok     `body:"json"`
	}
	checked := true
	tbl := withRoot(one("/x", get(func(_ context.Context, in *condIn) (*tagged, error) {
		if checked {
			if err := in.Check(current, time.Time{}); err != nil {
				return nil, err
			}
		}
		return &tagged{ETag: current, Body: ok{true}}, nil
	})), geta.ETag())
	a := accepts(t, tbl)
	for _, check := range []bool{true, false} {
		checked = check
		for v, want := range map[string]int{`"v2"`: 304, `W/"v2"`: 304, `"v1", W/"v2"`: 304, "*": 304, `"v1"`: 200} {
			rec := do(t, a, "GET", "/x", "If-None-Match", v)
			if rec.Code != want || rec.Header().Get("ETag") != current {
				t.Errorf("checked %v, If-None-Match %s: %d %q", check, v, rec.Code, rec.Header().Get("ETag"))
			}
		}
	}
}

// A precondition is no string parameter: Check reads it by RFC 9110's
// grammar, as ETag reads If-None-Match. An entity tag may hold obs-text
// (§8.8.3), which is no UTF-8, so a tag the handler sent with it comes back
// and matches; a list is read whatever its length, past MaxStringLength
// included; an empty line beside "*" is an empty element; a date of no UTF-8
// is no HTTP-date, and ignored (§13.1.4).
func TestPreconditionsAreReadByTheirGrammarAlike(t *testing.T) {
	const obs = "\"\xff\""
	type tagged struct {
		ETag string `header:"ETag"`
		Body ok     `body:"json"`
	}
	checked := true
	a := accepts(t, withRoot(one("/x", geta.Route{
		Get: geta.Op(http.StatusOK, func(_ context.Context, in *condIn) (*tagged, error) {
			if checked {
				if err := in.Check(obs, time.Time{}); err != nil {
					return nil, err
				}
			}
			return &tagged{ETag: obs, Body: ok{true}}, nil
		}, geta.Doc{}),
		Put: geta.OpNoBody(http.StatusNoContent, func(_ context.Context, in *condIn) error { return in.Check(obs, modifiedAt) }, geta.Doc{}),
	}), geta.ETag()))
	if r := do(t, a, "GET", "/x"); r.Code != 200 || r.Header().Get("ETag") != obs {
		t.Fatal(r.Code, r.Header())
	}
	long := strings.Repeat(`"x", `, 1000) + obs
	for _, check := range []bool{true, false} {
		checked = check
		for _, h := range [][]string{
			{"If-None-Match", obs},
			{"If-None-Match", `W/` + obs},
			{"If-None-Match", long},
			{"If-None-Match", "", "If-None-Match", "*"},
		} {
			if r := do(t, a, "GET", "/x", h...); r.Code != 304 || r.Header().Get("ETag") != obs {
				t.Errorf("checked %v, %q: %d %q", check, h, r.Code, r.Header().Get("ETag"))
			}
		}
		if r := do(t, a, "GET", "/x", "If-None-Match", "\"\xfe\""); r.Code != 200 {
			t.Errorf("checked %v, another tag: %d", check, r.Code)
		}
	}
	for _, c := range []struct {
		h    []string
		want int
	}{
		{[]string{"If-Match", obs}, 204},
		{[]string{"If-Match", long}, 204},
		{[]string{"If-Match", "\"\xfe\""}, 412},
		{[]string{"If-Match", `W/` + obs}, 412},
		{[]string{"If-Match", "\"\xff"}, 400},
		{[]string{"If-None-Match", obs}, 412},
		{[]string{"If-Unmodified-Since", dateEarly + "\xff"}, 204},
		{[]string{"If-Unmodified-Since", dateEarly}, 412},
	} {
		if r := do(t, a, "PUT", "/x", c.h...); r.Code != c.want {
			t.Errorf("PUT %q: %d, want %d: %s", c.h, r.Code, c.want, r.Body)
		}
	}
}

// The Conditional's headers are bound like any other: a field of the input
// that binds one of them again is refused.
func TestConditionalHeaderBoundTwiceIsRefused(t *testing.T) {
	type both struct {
		geta.RequireConditional
		Extra string `header:"If-Match"`
	}
	rejects(t, one("/x", get(func(context.Context, *both) (*ok, error) { return nil, nil })), "If-Match", "already bound")
}
