package geta_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	jsonv2 "encoding/json/v2"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/internal/vet"
)

// Rules of the document that no other test asserts.

// specNamed names each of its events and gives none an id.
type specNamed struct {
	N int `json:"n"`
}

func (specNamed) EventName() string { return "tick" }

// In 3.1 a stream's 200 is the event type's schema under text/event-stream;
// in 3.2 each event is described, its event property present and required
// exactly when the type names its events, its id likewise.
func TestStreamResponsesAreDocumented(t *testing.T) {
	events := func(context.Context, *empty) (*geta.Stream[specNamed], error) {
		return &geta.Stream[specNamed]{Events: func(func(specNamed) bool) {}}, nil
	}
	tbl := one("/s", get(events))
	m := doc(t, accepts(t, tbl))
	if got := compact(t, at(t, m, "paths", "/s", "get", "responses", "200")); got != `{"content":{"text/event-stream":{"schema":{"$ref":"#/components/schemas/specNamed"}}},"description":"An event stream; each event's data is one JSON value"}` {
		t.Error("3.1:", got)
	}
	a, err := geta.New(tbl, geta.WithOpenAPI(geta.OpenAPI32))
	if err != nil {
		t.Fatal(err)
	}
	item := at(t, doc(t, a), "paths", "/s", "get", "responses", "200", "content", "text/event-stream", "itemSchema")
	if got := compact(t, item); got != `{"properties":{"data":{"contentMediaType":"application/json","contentSchema":{"$ref":"#/components/schemas/specNamed"},"type":"string"},"event":{"type":"string"}},"required":["data","event"],"type":"object"}` {
		t.Error("3.2:", got)
	}
}

// In 3.2 every response's summary is its status's reason phrase, or
// "Status <code>" for a code that has none.
func TestResponseSummariesAreReasonPhrasesIn32(t *testing.T) {
	a, err := geta.New(richTable(), geta.WithOpenAPI(geta.OpenAPI32))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for path, item := range at(t, doc(t, a), "paths").(map[string]any) {
		for method, op := range item.(map[string]any) {
			for status, r := range at(t, op, "responses").(map[string]any) {
				code, _ := strconv.Atoi(status)
				want := http.StatusText(code)
				if want == "" {
					want = "Status " + status
				}
				if got := at(t, r, "summary"); got != want {
					t.Errorf("%s %s %s: summary %v, want %q", method, path, status, got, want)
				}
				n++
			}
		}
	}
	if n == 0 {
		t.Fatal("no responses")
	}
	if got := at(t, doc(t, a), "paths", "/weird", "get", "responses", "499", "summary"); got != "Status 499" {
		t.Fatal(got)
	}
}

// An upgrade's 101 states the two headers that switch, and its 426 is listed.
func TestUpgradeIsDocumented(t *testing.T) {
	h := func(context.Context, *empty) (*geta.Upgrade, error) {
		return &geta.Upgrade{Protocol: "echo", Serve: func(net.Conn, *bufio.ReadWriter) {}}, nil
	}
	m := doc(t, accepts(t, one("/ws", geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, h, geta.Doc{})})))
	responses := at(t, m, "paths", "/ws", "get", "responses")
	if got := compact(t, at(t, responses, "101")); got != `{"description":"Switching Protocols","headers":{"Connection":{"required":true,"schema":{"type":"string"}},"Upgrade":{"required":true,"schema":{"type":"string"}}}}` {
		t.Error(got)
	}
	if got := at(t, responses, "426", "description"); got != "The request does not ask to switch to this protocol" {
		t.Error(got)
	}
}

// What a table leaves unsaid is absent, or geta's default: info's title and
// version, no top-level tags, no top-level security, no security schemes;
// an operation that nothing secures requires nothing.
func TestDocumentDefaults(t *testing.T) {
	m := doc(t, accepts(t, one("/x", get(okHandler))))
	if got := compact(t, at(t, m, "info")); got != `{"title":"API","version":"0.0.0"}` {
		t.Error(got)
	}
	for _, k := range []string{"tags", "security"} {
		if _, has := m[k]; has {
			t.Errorf("top-level %s: %v", k, m[k])
		}
	}
	if _, has := at(t, m, "components").(map[string]any)["securitySchemes"]; has {
		t.Error("securitySchemes without a scheme")
	}
	if got := compact(t, at(t, m, "paths", "/x", "get", "security")); got != `[]` {
		t.Error(got)
	}
	if got := at(t, m, "paths", "/x", "options", "summary"); got != "The methods this URL serves" {
		t.Error(got)
	}
}

// The bytes: JSON whose object keys are sorted, indented by two spaces, with
// a trailing newline; lists in declaration order.
func TestDocumentBytes(t *testing.T) {
	got := accepts(t, richTable()).OpenAPI()
	var v any
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	enc := json.NewEncoder(&want) // encoding/json sorts a map's keys and ends with a newline
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("the document is not sorted, two-space indented JSON with a trailing newline:\n%s", got)
	}
}

// An operationId geta derives is the method in lower case, then each run of
// letters and digits of the path with its first letter upper case; the root
// path is Root.
func TestDerivedOperationIDs(t *testing.T) {
	type roleIn struct {
		Role string `path:"role"`
	}
	m := doc(t, accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/", Route: get(okHandler)},
		{Path: "/users/by-role/{role}", Route: get(func(context.Context, *roleIn) (*ok, error) { return nil, nil })},
		{Path: "/a_b/c2", Route: geta.Route{Post: geta.Op(http.StatusOK, okHandler, geta.Doc{})}},
	}}))
	for _, c := range [][3]string{
		{"/", "get", "getRoot"}, {"/", "options", "optionsRoot"},
		{"/users/by-role/{role}", "get", "getUsersByRoleRole"},
		{"/a_b/c2", "post", "postABC2"},
	} {
		if got := at(t, m, "paths", c[0], c[1], "operationId"); got != c[2] {
			t.Errorf("%s %s: %v, want %s", c[1], c[0], got, c[2])
		}
	}
}

// geta writes no title: info's and the Problem component's member named
// title are the only ones.
func TestNoTitleIsWritten(t *testing.T) {
	for name, tbl := range map[string]geta.Table{"meta": metaTable(), "rich": richTable(), "described": describedTable(), "deep": deepTable()} {
		m := doc(t, accepts(t, tbl))
		delete(m, "info")
		delete(at(t, m, "components", "schemas").(map[string]any), "Problem")
		if s := compact(t, m); strings.Contains(s, `"title"`) {
			t.Errorf("%s: a title: %s", name, s)
		}
	}
}

// The Problem component is always listed, and is RFC 9457's members as geta
// writes them.
func TestTheProblemComponent(t *testing.T) {
	m := doc(t, accepts(t, one("/x", get(okHandler))))
	if got := compact(t, at(t, m, "components", "schemas", "Problem")); got != `{"properties":{"detail":{"type":"string"},"errors":{"items":{"properties":{"in":{"type":"string"},"message":{"type":"string"},"path":{"type":"string"}},"required":["in","path","message"],"type":"object"},"type":"array"},"instance":{"format":"uri-reference","type":"string"},"omitted":{"minimum":1,"type":"integer"},"status":{"type":"integer"},"title":{"type":"string"},"type":{"format":"uri-reference","type":"string"}},"required":["type","title","status"],"type":"object"}` {
		t.Error(got)
	}
}

type specPair[K, V any] struct {
	K K `json:"k"`
	V V `json:"v"`
}

type specOpt[T any] struct {
	V T `json:"v,omitzero"`
}

type specTags []string

type specHolder struct {
	Tags  specTags `json:"tags"`
	Inner struct {
		N int `json:"n"`
	} `json:"inner"`
}

// A named struct is a component, a generic one named with its type
// arguments' names joined by _; a named slice and an anonymous struct are
// written in place.
func TestWhatIsAComponent(t *testing.T) {
	m := doc(t, accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/p", Route: get(func(context.Context, *empty) (*specPair[string, ok], error) { return nil, nil })},
		{Path: "/o", Route: get(func(context.Context, *empty) (*specOpt[*ok], error) { return nil, nil })},
		{Path: "/h", Route: get(func(context.Context, *empty) (*specHolder, error) { return nil, nil })},
	}}))
	schemas := at(t, m, "components", "schemas").(map[string]any)
	for path, want := range map[string]string{"/p": "specPair_string_ok", "/o": "specOpt_ok"} {
		if got := compact(t, at(t, m, "paths", path, "get", "responses", "200", "content", "application/json", "schema")); got != `{"$ref":"#/components/schemas/`+want+`"}` {
			t.Error(path, got)
		}
	}
	if _, has := schemas["specTags"]; has {
		t.Error("a named slice is a component")
	}
	if got := compact(t, at(t, schemas, "specHolder", "properties")); got != `{"inner":{"additionalProperties":false,"properties":{"n":{"format":"int64","type":"integer"}},"required":["n"],"type":"object"},"tags":{"items":{"type":"string"},"type":"array"}}` {
		t.Error(got)
	}
	names := make([]string, 0, len(schemas))
	for n := range schemas {
		names = append(names, n)
	}
	if len(schemas) != 5 { // Problem, ok, specHolder, specPair_string_ok, specOpt_ok
		t.Errorf("components %v", names)
	}
}

type specScalars struct {
	S     string    `json:"s"`
	B     bool      `json:"b"`
	I     int       `json:"i"`
	I64   int64     `json:"i64"`
	I32   int32     `json:"i32"`
	I16   int16     `json:"i16"`
	U     uint      `json:"u"`
	U64   uint64    `json:"u64"`
	U32   uint32    `json:"u32"`
	U8    uint8     `json:"u8"`
	F32   float32   `json:"f32"`
	F64   float64   `json:"f64"`
	Bytes []byte    `json:"bytes"`
	ID    uuid.UUID `json:"id"`
}

// Each Go scalar has its schema: its JSON type, its format, and the range of
// an integer narrower than 64 bits or unsigned.
func TestScalarSchemas(t *testing.T) {
	m := doc(t, accepts(t, one("/s", get(func(context.Context, *empty) (*specScalars, error) { return nil, nil }))))
	props := at(t, m, "components", "schemas", "specScalars", "properties")
	for name, want := range map[string]string{
		"s":     `{"type":"string"}`,
		"b":     `{"type":"boolean"}`,
		"i":     `{"format":"int64","type":"integer"}`,
		"i64":   `{"format":"int64","type":"integer"}`,
		"i32":   `{"format":"int32","type":"integer"}`,
		"i16":   `{"maximum":32767,"minimum":-32768,"type":"integer"}`,
		"u":     `{"minimum":0,"type":"integer"}`,
		"u64":   `{"minimum":0,"type":"integer"}`,
		"u32":   `{"maximum":4294967295,"minimum":0,"type":"integer"}`,
		"u8":    `{"maximum":255,"minimum":0,"type":"integer"}`,
		"f32":   `{"format":"float","type":"number"}`,
		"f64":   `{"format":"double","type":"number"}`,
		"bytes": `{"contentEncoding":"base64","type":"string"}`,
		"id":    `{"format":"uuid","type":"string"}`,
	} {
		if got := compact(t, at(t, props, name)); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
}

// A cause's text is listed once on its status, however many rows give it.
func TestOneTextIsListedOnce(t *testing.T) {
	errA, errB := errors.New("a"), errors.New("b")
	m := doc(t, accepts(t, one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Failures: []geta.Failure{
		geta.On(errA, http.StatusNotFound, "gone"),
		geta.On(errB, http.StatusNotFound, "gone"),
	}})})))
	if got := at(t, m, "paths", "/x", "get", "responses", "404", "description"); got != "gone" {
		t.Fatal(got)
	}
	// A status no described row answers is the plain problem.
	if got := compact(t, at(t, m, "paths", "/x", "get", "responses", "404", "content")); got != `{"application/problem+json":{"schema":{"$ref":"#/components/schemas/Problem"}}}` {
		t.Fatal(got)
	}
}

type specModeIn struct {
	Mode string `header:"X-Mode" schema:"default=fast"`
}

// A request body is required unless its field is a pointer; a header with a
// default is not required.
func TestRequiredFollowsThePointerAndTheDefault(t *testing.T) {
	m := doc(t, accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/r", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *bodyIn) (*ok, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/o", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *optionalBodyIn) (*ok, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/h", Route: get(func(context.Context, *specModeIn) (*ok, error) { return nil, nil })},
	}}))
	if got := at(t, m, "paths", "/r", "post", "requestBody", "required"); got != true {
		t.Error("/r:", got)
	}
	if got := at(t, m, "paths", "/o", "post", "requestBody", "required"); got != false {
		t.Error("/o:", got)
	}
	if got := at(t, m, "paths", "/h", "get", "parameters").([]any)[0].(map[string]any)["required"]; got != false {
		t.Error("/h:", got)
	}
}

// Security as the document states it: one requirement object per
// alternative, the schemes a gate default names listed whether or not an
// operation requires them, each scheme by its members; a gate's 401 and 503
// only where an operation requires a scheme.
func TestSecurityIsRendered(t *testing.T) {
	a, c := geta.APIKeyHeader("a", "X-A"), geta.APIKeyHeader("c", "X-C")
	gate := geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer}, Verifiers: map[string]geta.Verifier{"bearer": admit, "a": admit, "c": admit}})
	m := doc(t, accepts(t, geta.Table{Root: geta.Scope{gate}, Routes: []geta.Entry{
		{Path: "/open", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}})}},
		{Path: "/either", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{a, c}})}},
	}}))
	if got := compact(t, at(t, m, "components", "securitySchemes")); got != `{"a":{"in":"header","name":"X-A","type":"apiKey"},"bearer":{"scheme":"bearer","type":"http"},"c":{"in":"header","name":"X-C","type":"apiKey"}}` {
		t.Error(got)
	}
	if got := compact(t, at(t, m, "paths", "/either", "get", "security")); got != `[{"a":[]},{"c":[]}]` {
		t.Error(got)
	}
	open := at(t, m, "paths", "/open", "get", "responses").(map[string]any)
	for _, s := range []string{"401", "503"} {
		if _, has := open[s]; has {
			t.Errorf("a public operation lists the gate's %s", s)
		}
	}
	either := at(t, m, "paths", "/either", "get", "responses").(map[string]any)
	for s, want := range map[string]string{"401": "Unauthenticated", "503": "The credential source is unavailable"} {
		if got := at(t, either, s, "description"); got != want {
			t.Errorf("%s: %v", s, got)
		}
	}
	// A gate default no operation requires is still a scheme of the document.
	m = doc(t, accepts(t, geta.Table{Root: geta.Scope{gate}, Routes: []geta.Entry{
		{Path: "/open", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}})}},
	}}))
	if got := compact(t, at(t, m, "components", "securitySchemes")); got != `{"bearer":{"scheme":"bearer","type":"http"}}` {
		t.Error(got)
	}
}

// What geta's own middleware answer, each on the operations behind it:
// ConcurrencyLimit's 503, Timeout's 504, and ETag's 304 on a GET whose
// successes include 200 alone.
func TestBuiltInMiddlewareAnswersAreDocumented(t *testing.T) {
	accepted := func(context.Context, *empty) (*ok, error) { return nil, nil }
	m := doc(t, accepts(t, withRoot(geta.Table{Routes: []geta.Entry{
		{Path: "/x", Route: get(okHandler)},
		{Path: "/y", Route: geta.Route{Get: geta.Op(http.StatusAccepted, accepted, geta.Doc{})}},
	}}, geta.ConcurrencyLimit(1), geta.Timeout(time.Second), geta.ETag())))
	x := at(t, m, "paths", "/x", "get", "responses")
	for s, want := range map[string]string{
		"304": "The representation has not changed",
		"503": "Server at capacity",
		"504": "The deadline passed before a response",
	} {
		if got := at(t, x, s, "description"); got != want {
			t.Errorf("%s: %v, want %q", s, got, want)
		}
	}
	if _, has := at(t, m, "paths", "/y", "get", "responses").(map[string]any)["304"]; has {
		t.Error("a GET answering 202 lists ETag's 304")
	}
}

// The App's own description of what it serves: its operations, sorted, the
// options ones among them; each operation's types; and the JSON options that
// read and write its sealed types, which are its unions'.
func TestAppIntrospection(t *testing.T) {
	del := func(context.Context, *empty) error { return nil }
	app, err := geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/b", Route: get(okHandler)},
		{Path: "/a", Route: geta.Route{
			Post:   geta.Op(http.StatusOK, echoDrawing, geta.Doc{}),
			Delete: geta.OpNoBody(http.StatusNoContent, del, geta.Doc{}),
		}},
	}}, geta.WithUnion(shapes))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(app.Operations(), "|"); got != "DELETE /a|GET /b|OPTIONS /a|OPTIONS /b|POST /a" {
		t.Error(got)
	}
	if in, out, ok := app.Types(http.MethodPost, "/a"); !ok || in != reflect.TypeFor[drawingIn]() || out != reflect.TypeFor[drawing]() {
		t.Error("POST /a:", in, out, ok)
	}
	if in, out, ok := app.Types(http.MethodDelete, "/a"); !ok || in != reflect.TypeFor[empty]() || out != nil {
		t.Error("DELETE /a:", in, out, ok)
	}
	if _, _, ok := app.Types(http.MethodGet, "/a"); ok {
		t.Error("GET /a has types")
	}
	body := []byte(`{"main":{"kind":"circle","radius":1},"others":[{"kind":"square","side":2}]}`)
	for name, opts := range map[string]jsonv2.Options{"App": app.JSONOptions(), "Union": shapes.JSONOptions()} {
		var d drawing
		if err := jsonv2.Unmarshal(body, &d, opts); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if d.Main.(circle).Radius != 1 || d.Others[0].(square).Side != 2 {
			t.Fatalf("%s: %#v", name, d)
		}
		out, err := jsonv2.Marshal(d, opts)
		if err != nil || string(out) != string(body) {
			t.Fatalf("%s: %s %v", name, out, err)
		}
	}
	var d drawing
	if err := jsonv2.Unmarshal(body, &d); err == nil {
		t.Fatal("a sealed type read without the options")
	}
}

// geta sets the rules getavet shares with geta.New in internal/vet, with
// these signatures; each refuses a declaration geta.New refuses.
func TestTheSharedRulesAreSet(t *testing.T) {
	field := func(v any, name string) vet.Field {
		f, _ := reflect.TypeOf(v).FieldByName(name)
		return vet.FieldOf(f)
	}
	var (
		schemaTag        func(string, string) error                             = vet.CheckSchemaTag
		requestSchemaTag func(string, string) error                             = vet.CheckRequestSchemaTag
		inputType        func(string, bool) error                               = vet.CheckInputType
		inputField       func(vet.Field, map[string]string) (bool, error)       = vet.CheckInputField
		deepObjectField  func(vet.Field, map[string]string) (bool, error)       = vet.CheckDeepObjectField
		formField        func(vet.Field, bool, map[string]string) (bool, error) = vet.CheckFormField
		envelopeField    func(vet.Field, map[string]bool) error                 = vet.CheckEnvelopeField
		envelopeStatus   func(int, string) error                                = vet.CheckEnvelopeStatus
		memberField      func(vet.Field, map[string]bool) (string, bool, error) = vet.CheckMemberField
		structMembers    func(string, int, int) error                           = vet.CheckStructMembers
		jsonType         func(vet.Type) error                                   = vet.CheckJSONType
		schemaName       func(string) error                                     = vet.CheckSchemaName
		problemType      func(string, bool) error                               = vet.CheckProblemType
		problemField     func(vet.Field) error                                  = vet.CheckProblemField
		problemMember    func(string, string) error                             = vet.CheckProblemMember
	)
	_, inputErr := inputField(field(struct {
		X string `query:"a" header:"b"`
	}{}, "X"), map[string]string{})
	_, deepErr := deepObjectField(field(struct{ A string }{}, "A"), map[string]string{})
	_, formErr := formField(field(struct{ A string }{}, "A"), false, map[string]string{})
	_, _, memberErr := memberField(field(struct {
		O string `json:"o,omitempty"`
	}{}, "O"), map[string]bool{})
	for name, err := range map[string]error{
		"CheckSchemaTag":        schemaTag("maxLength=abc", "string"),
		"CheckRequestSchemaTag": requestSchemaTag("pattern=a,maxLength=5000", "string"),
		"CheckInputType":        inputType("int", false),
		"CheckInputField":       inputErr,
		"CheckDeepObjectField":  deepErr,
		"CheckFormField":        formErr,
		"CheckEnvelopeField": envelopeField(field(struct {
			CT string `header:"Content-Type"`
		}{}, "CT"), map[string]bool{}),
		"CheckEnvelopeStatus": envelopeStatus(http.StatusAccepted, "200|201"),
		"CheckMemberField":    memberErr,
		"CheckStructMembers":  structMembers("lib.Hidden", 1, 0),
		"CheckJSONType":       jsonType(vet.Type{Type: "chan int", Kind: "chan"}),
		"CheckSchemaName":     schemaName("lib.利用者"),
		"CheckProblemType":    problemType("int", false),
		"CheckProblemField": problemField(field(struct {
			Session *http.Cookie `cookie:"sid"`
		}{}, "Session")),
		"CheckProblemMember": problemMember("status", "int"),
	} {
		if err == nil {
			t.Errorf("%s refuses nothing", name)
		}
	}
}
