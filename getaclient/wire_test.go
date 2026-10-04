package getaclient_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaclient"
)

// What a call writes on the wire for each kind of input field, and what it
// reads back from a response, beyond the rules other tests assert.

// sent is the last request a recorder received.
type sent struct {
	mu     sync.Mutex
	n      int
	method string
	uri    string
	header http.Header
	body   []byte
}

func (s *sent) get() (n int, method, uri string, header http.Header, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n, s.method, s.uri, s.header, s.body
}

// recorder answers every request with status, header, and body, and keeps
// the last request it received.
func recorder(t *testing.T, status int, header http.Header, body string) (*getaclient.Client, *sent) {
	t.Helper()
	s := &sent{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.n++
		s.method, s.uri, s.header, s.body = r.Method, r.RequestURI, r.Header.Clone(), b
		s.mu.Unlock()
		for k, vs := range header {
			w.Header()[k] = vs
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return &getaclient.Client{Base: srv.URL, HTTP: srv.Client()}, s
}

type rawPtrIn struct {
	Body *[]byte `body:"text/csv"`
}

type FormBase struct {
	A string `form:"a"`
}

type formIn struct {
	Body struct {
		FormBase
		Skip string
		B    uint    `form:"b"`
		F    float64 `form:"f"`
	} `body:"form"`
}

type numIn struct {
	U uint8   `query:"u"`
	F float32 `query:"f"`
}

// A media type's bytes given through a pointer are sent as they are; a form
// body's untagged embedded struct contributes its fields and an untagged
// field nothing; an unsigned integer and a float are written in decimal,
// the float in its shortest form; a template of "/" is the path "/".
func TestInputsAreWrittenAsGetaReadsThem(t *testing.T) {
	c, s := recorder(t, http.StatusNoContent, nil, "")
	ctx := context.Background()

	csv := []byte("a,b\n1,2\n")
	if err := getaclient.CallNoBody(ctx, c, http.MethodPost, "/csv", &rawPtrIn{Body: &csv}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, h, b := s.get(); h.Get("Content-Type") != "text/csv" || string(b) != string(csv) {
		t.Errorf("a *[]byte body: %q %q", h.Get("Content-Type"), b)
	}

	var fin formIn
	fin.Body.A, fin.Body.Skip, fin.Body.B, fin.Body.F = "x", "never", 7, 1.5
	if err := getaclient.CallNoBody(ctx, c, http.MethodPost, "/form", &fin); err != nil {
		t.Fatal(err)
	}
	if _, _, _, h, b := s.get(); h.Get("Content-Type") != "application/x-www-form-urlencoded" || string(b) != "a=x&b=7&f=1.5" {
		t.Errorf("a form body: %q %q", h.Get("Content-Type"), b)
	}

	if err := getaclient.CallNoBody(ctx, c, http.MethodGet, "/", &numIn{U: 255, F: 0.1}); err != nil {
		t.Fatal(err)
	}
	if _, _, uri, _, _ := s.get(); uri != "/?f=0.1&u=255" {
		t.Errorf("the root with numbers: %q", uri)
	}
}

type BadQueryBase struct {
	M map[string]int `query:"m"`
}

type badEmbeddedIn struct {
	BadQueryBase
}

type badDeepIn struct {
	Q struct {
		M map[string]int `form:"m"`
	} `query:"q"`
}

type badListIn struct {
	L []map[string]int `query:"l"`
}

type BadFormBase struct {
	M map[string]int `form:"m"`
}

type badFormIn struct {
	Body struct {
		BadFormBase
	} `body:"form"`
}

type badMultipartIn struct {
	Body struct {
		MM map[string]int `form:"mm"`
	} `body:"multipart"`
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("disk gone") }

type fileIn struct {
	Body struct {
		F geta.File `form:"f"`
	} `body:"multipart"`
}

// What a call cannot write is an error naming the field, and nothing is
// sent: a value of no parameter type in an untagged embedded struct, in a
// deepObject's member, in a list's element, or in a form body's embedded
// struct; a file whose content cannot be read; a method no request can
// carry.
func TestWhatCannotBeWrittenIsNotSent(t *testing.T) {
	c, s := recorder(t, http.StatusNoContent, nil, "")
	ctx := context.Background()
	for want, call := range map[string]func() error{
		`query parameter "m": type map[string]int is not a parameter type`: func() error {
			return getaclient.CallNoBody(ctx, c, http.MethodGet, "/x", &badEmbeddedIn{BadQueryBase{M: map[string]int{}}})
		},
		`query parameter "q" member "m": type map[string]int is not a parameter type`: func() error {
			return getaclient.CallNoBody(ctx, c, http.MethodGet, "/x", &badDeepIn{})
		},
		`query parameter "l": type map[string]int is not a parameter type`: func() error {
			return getaclient.CallNoBody(ctx, c, http.MethodGet, "/x", &badListIn{L: []map[string]int{{}}})
		},
		`body: form field "m": type map[string]int is not a parameter type`: func() error {
			return getaclient.CallNoBody(ctx, c, http.MethodPost, "/x", &badFormIn{})
		},
		`body: form field "mm": type map[string]int is not a parameter type`: func() error {
			return getaclient.CallNoBody(ctx, c, http.MethodPost, "/x", &badMultipartIn{})
		},
		`body: form field "f": disk gone`: func() error {
			var in fileIn
			in.Body.F = geta.NewFile("a.txt", "text/plain", failingReader{})
			return getaclient.CallNoBody(ctx, c, http.MethodPost, "/x", &in)
		},
		`invalid method "BAD METHOD"`: func() error {
			return getaclient.CallNoBody[struct{}](ctx, c, "BAD METHOD", "/x", nil)
		},
	} {
		err := call()
		if err == nil || !strings.HasPrefix(err.Error(), "getaclient: ") || !strings.Contains(err.Error(), want) {
			t.Errorf("got %v\nwant %q", err, want)
		}
	}
	if n, _, _, _, _ := s.get(); n != 0 {
		t.Fatalf("%d requests sent", n)
	}
}

// A file the server received is valid until its handler returns: sent on
// afterwards, once geta has removed the temporary file that held it, it
// cannot be opened, and the call is an error naming the field before
// anything is sent.
func TestAReceivedFileSentAfterItsHandlerIsAnError(t *testing.T) {
	limits := geta.DefaultLimits
	limits.MaxMultipartMemory = 0 // every file is held in a temporary file
	var kept geta.File
	keep := func(_ context.Context, in *fileIn) error { kept = in.Body.F; return nil }
	a, err := geta.New(geta.Table{Routes: []geta.Entry{{Path: "/keep", Route: geta.Route{
		Post: geta.OpNoBody(http.StatusNoContent, keep, geta.Doc{}),
	}}}}, geta.WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	var in fileIn
	in.Body.F = geta.NewFile("a.txt", "text/plain", strings.NewReader("content"))
	if err := getaclient.CallNoBody(ctx, &getaclient.Client{Base: srv.URL, HTTP: srv.Client()}, http.MethodPost, "/keep", &in); err != nil {
		t.Fatal(err)
	}

	c, s := recorder(t, http.StatusNoContent, nil, "")
	in.Body.F = kept
	err = getaclient.CallNoBody(ctx, c, http.MethodPost, "/x", &in)
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), `body: form field "f": `) {
		t.Fatalf("%v", err)
	}
	if n, _, _, _, _ := s.get(); n != 0 {
		t.Fatalf("%d requests sent", n)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, errors.New("connection reset") }
func (brokenBody) Close() error             { return nil }

// A request the transport cannot send is its error, and a success whose body
// cannot be read is an error naming the call; neither is an *Error, which is
// a response the application sent.
func TestTransportFailuresAreNoError(t *testing.T) {
	ctx := context.Background()
	down := errors.New("connection refused")
	c := &getaclient.Client{Base: "http://app.test", HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, down
	})}}
	_, err := getaclient.Call[struct{}, specOut](ctx, c, http.MethodGet, "/x", nil)
	if !errors.Is(err, down) {
		t.Errorf("no response: %v", err)
	}
	if _, isErr := errors.AsType[*getaclient.Error](err); isErr {
		t.Error("no response is an *Error")
	}

	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: brokenBody{}, Request: r}, nil
	})}
	_, err = getaclient.Call[struct{}, specOut](ctx, c, http.MethodGet, "/x", nil)
	if err == nil || err.Error() != "getaclient: GET /x: connection reset" {
		t.Errorf("a body cut short: %v", err)
	}
	if _, isErr := errors.AsType[*getaclient.Error](err); isErr {
		t.Error("a body cut short is an *Error")
	}
}

type EnvHead struct {
	N int `header:"X-N"`
}

type envOut struct {
	EnvHead
	note string
	Body specOut `body:"json"`
}

type listHeaderOut struct {
	L    []string `header:"X-L"`
	Body specOut  `body:"json"`
}

// An envelope reads its embedded struct's headers as its own and leaves an
// unexported field alone; a header that does not parse as its field, a
// header field of no header type, and a body that does not decode are
// errors naming the header or the body.
func TestEnvelopeReading(t *testing.T) {
	ctx := context.Background()
	c, _ := recorder(t, http.StatusOK, http.Header{"X-N": {"7"}, "Content-Type": {"application/json"}}, `{"n":3}`)
	out, err := getaclient.Call[struct{}, envOut](ctx, c, http.MethodGet, "/x", nil)
	if err != nil || out.N != 7 || out.Body.N != 3 || out.note != "" {
		t.Fatalf("%+v %v", out, err)
	}
	for _, r := range []struct {
		header http.Header
		body   string
		call   func(*getaclient.Client) error
		want   string
	}{
		{http.Header{"X-N": {"seven"}, "Content-Type": {"application/json"}}, `{"n":3}`, func(c *getaclient.Client) error {
			_, err := getaclient.Call[struct{}, envOut](ctx, c, http.MethodGet, "/x", nil)
			return err
		}, `getaclient: GET /x: header X-N: strconv.ParseInt: parsing "seven": invalid syntax`},
		{http.Header{"X-N": {"7"}, "Content-Type": {"application/json"}}, `not json`, func(c *getaclient.Client) error {
			_, err := getaclient.Call[struct{}, envOut](ctx, c, http.MethodGet, "/x", nil)
			return err
		}, "getaclient: GET /x: body: "},
		{http.Header{"X-L": {"a"}, "Content-Type": {"application/json"}}, `{"n":3}`, func(c *getaclient.Client) error {
			_, err := getaclient.Call[struct{}, listHeaderOut](ctx, c, http.MethodGet, "/x", nil)
			return err
		}, "getaclient: GET /x: header X-L: type []string is not a header type"},
	} {
		c, _ := recorder(t, http.StatusOK, r.header, r.body)
		if err := r.call(c); err == nil || !strings.HasPrefix(err.Error(), r.want) {
			t.Errorf("got %v\nwant %q", err, r.want)
		}
	}
}

type problemEnvelope struct {
	Retry int `header:"Retry-After"`
	Body  struct {
		Reason string `json:"reason"`
	} `body:"json"`
}

// ProblemAs into an envelope reports false when a header the envelope
// declares does not parse as its field.
func TestProblemAsEnvelopeWithAnUnreadableHeader(t *testing.T) {
	e := &getaclient.Error{Status: http.StatusTooManyRequests, Problem: &geta.Problem{Status: 429},
		Header: http.Header{"Retry-After": {"soon"}}, Body: []byte(`{"title":"Too Many Requests","reason":"burst"}`)}
	if p, ok := getaclient.ProblemAs[problemEnvelope](e); ok {
		t.Fatalf("an unreadable header read: %+v", p)
	}
	e.Header.Set("Retry-After", "3")
	if p, ok := getaclient.ProblemAs[problemEnvelope](e); !ok || p.Retry != 3 || p.Body.Reason != "burst" {
		t.Fatalf("%+v %v", p, ok)
	}
}

type AbsEmb struct {
	E int `json:"e" schema:"default=2"`
	X int `json:"x"`
}

type absLine struct {
	Q int `json:"q" schema:"default=1"`
	X int `json:"x"`
}

// ownText is a member that writes itself as JSON.
type ownText struct{ v string }

func (o ownText) MarshalJSON() ([]byte, error) { return json.Marshal(o.v) }

type absBody struct {
	AbsEmb
	Q    int       `json:"q" schema:"default=1"`
	P    *absLine  `json:"p,omitzero"`
	T    time.Time `json:"t"`
	R    ownText   `json:"r"`
	Skip string    `json:"-"`
	N    absLine   `json:"n"`
	L    []absLine `json:"l"`
	Bad  *int      `json:"bad"`
}

type absIn struct {
	Body absBody `body:"json"`
}

// Absent leaves JSON members out through an embedded struct, past a nil
// pointer, a time, a member that writes itself, and a member json:"-"
// drops; a field it names that declares no default is an error wherever it
// sits (an embedded struct, a nested struct, a slice's element), and one
// geta.New would refuse as a member (a pointer without omitzero) is no field
// of the input the call sends.
func TestAbsentWalksTheBodyAsGetaReadsIt(t *testing.T) {
	c, s := recorder(t, http.StatusNoContent, nil, "")
	ctx := context.Background()
	in := absIn{Body: absBody{T: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), R: ownText{"own"}, Skip: "x", L: []absLine{{Q: 5}}}}
	if err := getaclient.CallNoBody(ctx, c, http.MethodPost, "/x", &in, getaclient.Absent(&in.Body.E, &in.Body.Q, &in.Body.N.Q, &in.Body.L[0].Q)); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, b := s.get()
	if want := `{"x":0,"t":"2026-01-02T00:00:00Z","r":"own","n":{"x":0},"l":[{"x":0}],"bad":null}`; string(b) != want {
		t.Errorf("sent %s\nwant %s", b, want)
	}

	for _, c2 := range []struct {
		field any
		want  string
	}{
		{&in.Body.X, `field X (member "x") declares no default`},
		{&in.Body.N.X, `field X (member "x") declares no default`},
		{&in.Body.L[0].X, `field X (member "x") declares no default`},
		{&in.Body.Bad, "Absent: *int is not a field of the input"},
	} {
		err := getaclient.CallNoBody(ctx, c, http.MethodPost, "/x", &in, getaclient.Absent(c2.field))
		if err == nil || !strings.Contains(err.Error(), c2.want) {
			t.Errorf("got %v\nwant %q", err, c2.want)
		}
	}
	if n, _, _, _, _ := s.get(); n != 1 {
		t.Fatalf("%d requests sent; want the first alone", n)
	}
}

type optsAbsIn struct {
	Body struct {
		Q int            `json:"q" schema:"default=1"`
		S string         `json:"s"`
		V jsontext.Value `json:"v"`
	} `body:"json"`
}

// Absent keeps the Client's JSON options: a body they let carry duplicate
// names is sent, not refused, and one they escape for HTML stays escaped,
// as the same body is sent without Absent.
func TestAbsentKeepsTheClientsJSONOptions(t *testing.T) {
	c, s := recorder(t, http.StatusNoContent, nil, "")
	c.JSON = json.JoinOptions(jsontext.AllowDuplicateNames(true), jsontext.EscapeForHTML(true))
	var in optsAbsIn
	in.Body.S, in.Body.V = "<a>", jsontext.Value(`{"k":1,"k":2}`)
	if err := getaclient.CallNoBody(context.Background(), c, http.MethodPost, "/x", &in, getaclient.Absent(&in.Body.Q)); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, b := s.get()
	want, err := json.Marshal(struct {
		S string         `json:"s"`
		V jsontext.Value `json:"v"`
	}{in.Body.S, in.Body.V}, c.JSON)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(want) || strings.Contains(string(b), "<") {
		t.Errorf("sent %s\nwant %s", b, want)
	}
}

type multiAbsIn struct {
	Body struct {
		N int       `form:"n" schema:"default=3"`
		F geta.File `form:"f"`
	} `body:"multipart"`
}

// Absent leaves a multipart body's text field out: no part of its name is
// sent.
func TestAbsentLeavesAMultipartFieldOut(t *testing.T) {
	c, s := recorder(t, http.StatusNoContent, nil, "")
	var in multiAbsIn
	in.Body.F = geta.NewFile("a.txt", "", strings.NewReader("hi"))
	if err := getaclient.CallNoBody(context.Background(), c, http.MethodPost, "/x", &in, getaclient.Absent(&in.Body.N)); err != nil {
		t.Fatal(err)
	}
	_, _, _, h, b := s.get()
	_, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	mr := multipart.NewReader(strings.NewReader(string(b)), params["boundary"])
	var names []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, p.FormName()+":"+p.Header.Get("Content-Type"))
	}
	if strings.Join(names, ",") != "f:application/octet-stream" {
		t.Fatalf("parts %q", names)
	}
}
