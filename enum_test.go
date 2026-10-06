package geta_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
	"github.com/koji-1009/geta/internal/vet"
)

type enumBody struct {
	// Past 2^53, where a float64 holds 9007199254740993 as 9007199254740992.
	Code  int64    `json:"code" schema:"enum=9007199254740993|5"`
	Ratio *float64 `json:"ratio,omitzero" schema:"enum=0.5|1.5|1e1"`
	Small *float32 `json:"small,omitzero" schema:"enum=0.1"`
}

type enumIn struct {
	Level int8     `query:"level" schema:"enum=-1|2|3"`
	Port  *uint16  `header:"X-Port" schema:"enum=80|443"`
	Body  enumBody `body:"json"`
}

type enumOut struct {
	Level int8 `json:"level" schema:"enum=-1|2|3"`
}

func enumTable() geta.Table {
	h := func(_ context.Context, in *enumIn) (*enumOut, error) { return &enumOut{Level: in.Level}, nil }
	return one("/e", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})})
}

// An enum on an integer or a number is a set of numbers, and a request's
// number is a member when it equals one in value, exactly as written, as a
// JSON Schema validator compares them: 1.50 is 1.5, and 9007199254740992 is
// not 9007199254740993, though a float64 holds both as one.
func TestNumberEnumsAreCheckedExactly(t *testing.T) {
	c := getatest.New(t, enumTable())
	for _, tc := range []struct {
		query, header, body string
		status              int
	}{
		{"level=2", "", `{"code":5}`, 200},
		{"level=-1", "443", `{"code":9007199254740993,"ratio":1.50,"small":0.1}`, 200},
		{"level=2", "", `{"code":5,"ratio":10.0}`, 200},
		{"level=2", "", `{"code":5,"ratio":1e1}`, 200},
		{"level=4", "", `{"code":5}`, 400},
		{"level=2.0", "", `{"code":5}`, 400},
		{"level=2", "8080", `{"code":5}`, 400},
		{"level=2", "", `{"code":9007199254740992}`, 400},
		{"level=2", "", `{"code":5,"ratio":0.50000000000000001}`, 400},
		{"level=2", "", `{"code":5,"small":0.10000000149011612}`, 400},
	} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, c.URL()+"/e?"+tc.query, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		if tc.header != "" {
			req.Header.Set("X-Port", tc.header)
		}
		res := c.Send(req)
		if res.Status != tc.status {
			t.Errorf("%s %s %s: %d, want %d: %s", tc.query, tc.header, tc.body, res.Status, tc.status, res.Text())
		}
	}
	res := c.Post("/e?level=4", map[string]any{"code": 6})
	p := res.Problem()
	if len(p.Errors) != 2 || p.Errors[0].Message != "4 is not one of -1, 2, 3" || p.Errors[1].Message != "6 is not one of 9007199254740993, 5" {
		t.Fatalf("%+v", p.Errors)
	}

	// The document writes each member as given, a JSON number: read as the
	// bytes, since a float64 would hold 9007199254740993 as another number.
	var raw bytes.Buffer
	if err := json.Compact(&raw, c.App().OpenAPI()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"enum":[-1,2,3],"maximum":127,"minimum":-128,"type":"integer"`, `"enum":[80,443]`,
		`"code":{"enum":[9007199254740993,5],"format":"int64","type":"integer"}`,
		`"enum":[0.5,1.5,1e1],"format":"double","type":"number"`,
	} {
		if !strings.Contains(raw.String(), want) {
			t.Errorf("the document lacks %s", want)
		}
	}
}

// A number's enum is refused where a member is no JSON number of the type,
// past the Go type's range, held by the type as another number, or outside
// the schema's own bounds; getavet gives the same verdict (CheckSchemaTag).
func TestNumberEnumMistakesAreRefused(t *testing.T) {
	for _, c := range []struct {
		tag, kind, want string
	}{
		{"enum=1.0", "int", `member "1.0" is not an integer`},
		{"enum=1e2", "int", `member "1e2" is not an integer`},
		{"enum=x", "float64", `member "x" is not a JSON number`},
		{"enum=01", "int", `member "01" is not a JSON number`},
		{"enum=+1", "int", `member "+1" is not a JSON number`},
		{"enum=300", "int8", "enum member 300 is outside the range of int8"},
		{"enum=-1", "uint", "enum member -1 is outside the range of uint"},
		{"enum=9223372036854775808", "int64", "outside the range of int64"},
		{"enum=1e40", "float32", "outside the range of float32"},
		{"enum=0.3000000000000000001", "float64", "is not exact as a float64; use 0.3"},
		{"enum=5,maximum=3", "int", "enum member 5 does not meet the schema's bounds or multipleOf"},
		{"enum=3,multipleOf=2", "int", "enum member 3 does not meet"},
		{"enum=true", "bool", "enum applies to string or integer or number, not boolean"},
	} {
		err := vet.CheckSchemaTag(c.tag, c.kind)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s on %s: %v; want %q", c.tag, c.kind, err, c.want)
		}
	}
	for _, c := range []struct{ tag, kind string }{
		{"enum=-1|0|1", "int8"}, {"enum=18446744073709551615", "uint64"}, {"enum=0.1|2.5", "float32"},
		{"enum=1|2,minimum=1", "int"}, {"enum=0.5|-0", "float64"},
	} {
		if err := vet.CheckSchemaTag(c.tag, c.kind); err != nil {
			t.Errorf("%s on %s: %v", c.tag, c.kind, err)
		}
	}
	type bad struct {
		N int8 `query:"n" schema:"enum=1|300"`
	}
	rejects(t, one("/x", get(func(context.Context, *bad) (*ok, error) { return nil, nil })), "bad.N", "enum member 300 is outside the range of int8")
}
