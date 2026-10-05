package geta_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
	"uuid"

	"github.com/koji-1009/geta"
)

type fuzzAddress struct {
	Street string            `json:"street" schema:"minLength=1,maxLength=40"`
	Zip    *string           `json:"zip,omitzero" schema:"pattern=^[0-9]{3}-[0-9]{4}$"`
	Extra  map[string]uint16 `json:"extra"`
}

type fuzzBody struct {
	Name     string        `json:"name" schema:"minLength=1,maxLength=20"`
	Age      *int8         `json:"age,omitzero" schema:"minimum=0"`
	Score    float32       `json:"score" schema:"exclusiveMaximum=100,multipleOf=0.5"`
	Tags     []string      `json:"tags" schema:"maxItems=4,uniqueItems=true"`
	When     *time.Time    `json:"when,omitzero"`
	ID       uuid.UUID     `json:"id"`
	Home     fuzzAddress   `json:"home"`
	Others   []fuzzAddress `json:"others"`
	Raw      []byte        `json:"raw"`
	Children []fuzzBody    `json:"children" schema:"maxItems=2"`
}

type fuzzIn struct {
	Seg   int64      `path:"seg" schema:"minimum=-5"`
	Limit *uint8     `query:"limit"`
	Mode  *string    `query:"mode" schema:"enum=a|b"`
	Many  *[]float64 `query:"n"`
	Trace *uuid.UUID `header:"X-Trace"`
	Body  *fuzzBody  `body:"json"`
}

// FuzzBinding feeds arbitrary input to a contract with every kind of field.
// Whatever arrives, binding answers 200, 400, or 413: an input can never
// reach the author's posture (500), and it never panics.
//
// The limits are low enough to reach within a few hundred bytes: the body
// limit (a 413), and the ceilings on a string's length, an array's items,
// and nesting. Each text is cut to fuzzTextMax bytes and the body to twice
// the body limit (fzCut), a 413 as one just past the limit is.
func FuzzBinding(f *testing.F) {
	const fuzzTextMax = 128
	h := func(ctx context.Context, in *fuzzIn) (*ok, error) { return &ok{true}, nil }
	limits := geta.DefaultLimits
	limits.MaxBodyBytes = 192
	limits.MaxStringLength = 64
	limits.MaxItems = 16
	limits.MaxDepth = 16
	app, err := geta.New(one("/x/{seg}", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}), geta.WithLimits(limits))
	if err != nil {
		f.Fatal(err)
	}
	f.Add("1", "limit=3&mode=a&n=1.5&n=2", "123e4567-e89b-12d3-a456-426614174000",
		[]byte(`{"name":"a","score":1.5,"tags":["x"],"id":"123e4567-e89b-12d3-a456-426614174000","home":{"street":"s","extra":{}},"others":[],"raw":"AA==","children":[]}`))
	f.Add("-9", "limit=300&mode=c", "x", []byte(`{"name":"","age":200,"score":100,"tags":["x","x"],"home":{"street":""},"children":[{}]}`))
	f.Add("x", "%zz", "", []byte(`{"a":1,"a":2}`))
	f.Add("1", "", "", []byte(`[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[`))
	f.Add("1", "", "", []byte(`{"name":"a","raw":"!!","when":"2020-13-01T00:00:00Z","others":[null]}`))
	f.Fuzz(func(t *testing.T, seg, query, trace string, body []byte) {
		seg, query, trace = fzCut(seg, fuzzTextMax), fzCut(query, fuzzTextMax), fzCut(trace, fuzzTextMax)
		body = fzCut(body, 2*int(limits.MaxBodyBytes))
		u := "/x/" + url.PathEscape(seg) + "?" + query
		req, err := http.NewRequest(http.MethodPost, "http://example.com"+u, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if trace != "" {
			req.Header.Set("X-Trace", trace)
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		switch rec.Code {
		case 200, 400, 413:
		case 404, 301, 307, 308: // ServeMux: no match, or a path it cleans by redirect
		default:
			t.Fatalf("status %d for %q %q %q: %s", rec.Code, u, trace, body, rec.Body)
		}
	})
}
