package geta_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
)

type fzJSONBody struct {
	A string `json:"a"`
}

type fzJSONIn struct {
	Body fzJSONBody `body:"json"`
}

type fzRawIn struct {
	Body []byte `body:"text/csv"`
}

type fzRawOut struct {
	Body []byte `body:"text/plain"`
}

type fzForm struct {
	A *string `form:"a"`
}

type fzFormIn struct {
	Body fzForm `body:"form"`
}

type fzBig struct {
	Text string `json:"text"`
}

// refCodings are the content codings Content-Encoding lines name (RFC 9110
// §8.4): comma-separated, trimmed of spaces and tabs, empty elements and
// identity (in any case) dropped.
func refCodings(lines []string) []string {
	var out []string
	for _, e := range refListElements(lines) {
		if !strings.EqualFold(e, "identity") {
			out = append(out, e)
		}
	}
	return out
}

var (
	refToken  = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
	refQValue = regexp.MustCompile(`^(0(\.[0-9]{0,3})?|1(\.0{0,3})?)$`)
)

// refAcceptEncoding reads Accept-Encoding lines (RFC 9110 §12.5.3): gzip's
// weight (named as gzip or x-gzip, else through "*", else -1: not named) and
// identity's (named, else through "*", else -1). An element is a coding and
// an optional weight, OWS ";" OWS "q=" qvalue ("q" in either case, RFC 9110
// §12.4.2); anything else after its ";" is a weight out of the grammar, which
// refuses what it names, as q=0 does. clean is false for lines that name a
// coding twice: the weights are then not compared. An element whose name is
// no token names no coding.
func refAcceptEncoding(lines []string) (gzipQ, identityQ float64, clean bool) {
	q := map[string]float64{}
	for _, e := range refListElements(lines) {
		name, params, weighted := strings.Cut(e, ";")
		name = strings.Trim(name, " \t")
		if !refToken.MatchString(name) {
			continue // names no coding
		}
		if name = strings.ToLower(name); name == "x-gzip" {
			name = "gzip"
		}
		if _, twice := q[name]; twice {
			return 0, 0, false
		}
		w := 1.0
		if weighted {
			w = 0
			params = strings.TrimLeft(params, " \t")
			if len(params) >= 2 && strings.EqualFold(params[:2], "q=") && refQValue.MatchString(params[2:]) {
				fmt.Sscan(params[2:], &w)
			}
		}
		q[name] = w
	}
	weight := func(name string) float64 {
		if w, ok := q[name]; ok {
			return w
		}
		if w, ok := q["*"]; ok {
			return w
		}
		return -1
	}
	return weight("gzip"), weight("identity"), true
}

// refMediaType reports whether the Content-Type ct names the media type
// want, one of application/json (which application/*+json is too),
// text/csv, and application/x-www-form-urlencoded: it parses as a media type
// (RFC 9110 §8.3.1), whose type and subtype, in any case, are want's.
func refMediaType(ct, want string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	if want == "application/json" && strings.HasPrefix(mt, "application/") && strings.HasSuffix(mt, "+json") {
		return true
	}
	return mt == want
}

// FuzzContentHeaders sends fuzzed content (its bytes, its length declared
// or not) with a fuzzed Content-Type, Content-Encoding, and Accept-Encoding
// to an operation taking a JSON body (POST and PATCH), a raw text/csv body,
// a form, and none (GET and POST), behind geta.Gzip, and holds the answer to
// the rules: zero bytes are no content (a required body is missing: 400);
// content to an operation that reads none is a 415 with no Accept or
// Accept-Encoding; then a coding but identity is a 415 with Accept-Encoding:
// identity and no Accept; then a media type the body does not take is a 415
// naming it in Accept (and Accept-Patch on a PATCH) with no Accept-Encoding;
// then content past MaxBodyBytes is a 413. Never a 5xx; every 4xx a problem
// of its status. Every response varies on Accept-Encoding and is coded only
// in gzip, which decodes to the uncoded body; where Accept-Encoding is the
// field's grammar it is coded exactly when gzip is accepted at least as
// much as identity and the body is 1024 bytes or more, or identity is
// refused. The raw body echoes back the content as sent.
func FuzzContentHeaders(f *testing.F) {
	limits := geta.DefaultLimits
	limits.MaxBodyBytes = 2048
	jsonOp := geta.Op(http.StatusOK, func(context.Context, *fzJSONIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})
	big := func(context.Context, *empty) (*fzBig, error) { return &fzBig{Text: strings.Repeat("x", 1500)}, nil }
	app, err := geta.New(withRoot(geta.Table{Routes: []geta.Entry{
		{Path: "/j", Route: geta.Route{Post: jsonOp, Patch: jsonOp}},
		{Path: "/r", Route: geta.Route{Post: geta.Op(http.StatusOK, func(_ context.Context, in *fzRawIn) (*fzRawOut, error) {
			return &fzRawOut{Body: in.Body}, nil
		}, geta.Doc{})}},
		{Path: "/f", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *fzFormIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
		{Path: "/n", Route: geta.Route{Get: geta.Op(http.StatusOK, big, geta.Doc{}), Post: geta.Op(http.StatusOK, big, geta.Doc{})}},
	}}, geta.Gzip()), geta.WithLimits(limits))
	if err != nil {
		f.Fatal(err)
	}
	// route: 0 /j, 1 /r, 2 /f, 3 /n. flags: bit 0 the length undeclared;
	// bit 1 Content-Type sent though empty; bit 2 PATCH on /j, POST on /n;
	// bit 3 no Accept-Encoding; bit 4 Content-Encoding sent though empty.
	// Lines of a field are split at NUL.
	long := bytes.Repeat([]byte("a,b\n"), 400)
	f.Add(uint8(0), "application/json", "", "gzip", []byte(`{"a":"x"}`), uint8(0))
	f.Add(uint8(0), "application/merge-patch+json; charset=utf-8", "", "", []byte(`{"a":"x"}`), uint8(0b0100))
	f.Add(uint8(0), "Application/JSON", "identity", "", []byte(`{}`), uint8(0b0001))
	f.Add(uint8(0), "application/json", "gzip", "", []byte(`{}`), uint8(0))
	f.Add(uint8(0), "text/plain", "gzip", "", []byte(`x`), uint8(0))
	f.Add(uint8(0), "application/json", "identity, br", "", []byte(`x`), uint8(0))
	f.Add(uint8(0), "application/json", "identity\x00GZIP", "", []byte(`x`), uint8(0))
	f.Add(uint8(0), "application/json", ",", "", []byte(`{}`), uint8(0b10000))
	f.Add(uint8(0), "application/json", "identity\xc2\xa0", "", []byte(`{}`), uint8(0))
	f.Add(uint8(0), "", "", "", []byte(`{}`), uint8(0))
	f.Add(uint8(0), "", "", "", []byte(`{}`), uint8(0b0010))
	f.Add(uint8(0), "application/json; charset", "", "", []byte(`{}`), uint8(0))
	f.Add(uint8(0), "application/json", "gzip", "", []byte{}, uint8(0))
	f.Add(uint8(0), "application/json", "", "", bytes.Repeat([]byte(" "), 3000), uint8(0))
	f.Add(uint8(0), "application/json", "", "", bytes.Repeat([]byte(" "), 3000), uint8(0b0001))
	f.Add(uint8(1), "text/csv; charset=utf-8", "", "gzip", long, uint8(0))
	f.Add(uint8(1), "TEXT/CSV", "", "gzip;q=0.5, identity;q=0.5", long, uint8(0))
	f.Add(uint8(1), "text/csv", "", "identity;q=0, br", []byte("a"), uint8(0))
	f.Add(uint8(1), "text/csv", "", "*;q=0", []byte("a"), uint8(0))
	f.Add(uint8(1), "text/csv", "", "x-gzip;Q=1, *;q=0", []byte("a"), uint8(0))
	f.Add(uint8(1), "text/csv", "", "gzip;q=2", long, uint8(0))
	f.Add(uint8(1), "text/csv", "", "gzip;q=0.001, identity;q=0.0001", long, uint8(0))
	f.Add(uint8(1), "text/csvx", "", "", []byte("a"), uint8(0))
	f.Add(uint8(1), "text/csv", "", "gzip\xc2\xa0", long, uint8(0))
	f.Add(uint8(1), "text/csv", "", "identity\xc2\xa0, gzip;q=0.5", long, uint8(0))
	// Weights out of the grammar, each refusing what its element names.
	f.Add(uint8(1), "text/csv", "", "gzip;q=1\xc2\xa0", long, uint8(0))
	f.Add(uint8(1), "text/csv", "", "*, gzip;q=0\xc2\xa0", long, uint8(0))
	f.Add(uint8(1), "text/csv", "", "gzip;q=abc", long, uint8(0))
	f.Add(uint8(1), "text/csv", "", "gzip;q = 1", long, uint8(0))
	f.Add(uint8(1), "text/csv", "", "gzip;q=0.5;x=1", long, uint8(0))
	f.Add(uint8(1), "text/csv", "", "identity;q=1x, gzip;q=0.001", []byte("a"), uint8(0))
	f.Add(uint8(1), "text/csv", "", "gzip; Q=1.000, identity;q=1.", long, uint8(0))
	f.Add(uint8(2), "application/x-www-form-urlencoded", "", "", []byte("a=1"), uint8(0))
	f.Add(uint8(2), "application/x-www-form-urlencoded", "", "", []byte("b=1&%zz"), uint8(0))
	f.Add(uint8(2), "multipart/form-data; boundary=x", "", "", []byte("a=1"), uint8(0))
	f.Add(uint8(3), "", "", "gzip", []byte{}, uint8(0))
	f.Add(uint8(3), "", "", "gzip", []byte("x"), uint8(0b0101))
	f.Add(uint8(3), "application/json", "gzip", "", []byte("x"), uint8(0b0001))
	f.Fuzz(func(t *testing.T, route uint8, ct, ce, ae string, body []byte, flags uint8) {
		path := []string{"/j", "/r", "/f", "/n"}[route%4]
		method := http.MethodPost
		switch {
		case path == "/j" && flags&4 != 0:
			method = http.MethodPatch
		case path == "/n" && flags&4 == 0:
			method = http.MethodGet
		}
		req := httptest.NewRequest(method, path, nil)
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		if flags&1 != 0 {
			req.ContentLength = -1
		} else if len(body) == 0 {
			req.Body = http.NoBody
		}
		if ct != "" || flags&2 != 0 {
			req.Header["Content-Type"] = []string{ct}
		}
		var ceLines, aeLines []string
		if ce != "" || flags&16 != 0 {
			ceLines = strings.Split(ce, "\x00")
			req.Header["Content-Encoding"] = ceLines
		}
		if flags&8 == 0 {
			aeLines = strings.Split(ae, "\x00")
			req.Header["Accept-Encoding"] = aeLines
		}
		where := fmt.Sprintf("%s %s (length %d) %q, %d bytes", method, path, req.ContentLength, req.Header, len(body))
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)

		// The reference.
		taken := map[string]string{"/j": "application/json", "/r": "text/csv", "/f": "application/x-www-form-urlencoded"}[path]
		var want []int
		var accept, acceptEncoding string
		switch {
		case len(body) == 0 && path == "/n":
			want = []int{200}
		case len(body) == 0:
			want = []int{400}
		case path == "/n":
			want = []int{415}
		case len(refCodings(ceLines)) > 0:
			want, acceptEncoding = []int{415}, "identity"
		case !refMediaType(req.Header.Get("Content-Type"), taken):
			want, accept = []int{415}, taken
		case len(body) > int(limits.MaxBodyBytes):
			want = []int{413}
		case path == "/r":
			want = []int{200}
		default:
			want = []int{200, 400}
		}
		if !slices.Contains(want, rec.Code) {
			t.Fatalf("%s: %d, want %v: %s", where, rec.Code, want, rec.Body)
		}
		h := rec.Header()
		if rec.Code == 415 {
			if h.Get("Accept") != accept || h.Get("Accept-Encoding") != acceptEncoding {
				t.Fatalf("%s: a 415 with Accept %q and Accept-Encoding %q, want %q and %q", where, h.Get("Accept"), h.Get("Accept-Encoding"), accept, acceptEncoding)
			}
			patch := ""
			if method == http.MethodPatch {
				patch = accept
			}
			if h.Get("Accept-Patch") != patch {
				t.Fatalf("%s: a 415 with Accept-Patch %q, want %q", where, h.Get("Accept-Patch"), patch)
			}
		}
		// What Compress sent, and what it decodes to.
		if !strings.Contains(h.Get("Vary"), "Accept-Encoding") {
			t.Fatalf("%s: Vary %q", where, h.Values("Vary"))
		}
		got := rec.Body.Bytes()
		coded := false
		switch enc := h.Get("Content-Encoding"); enc {
		case "":
		case "gzip":
			coded = true
			zr, err := gzip.NewReader(bytes.NewReader(got))
			if err != nil {
				t.Fatalf("%s: a gzip body that does not read: %v", where, err)
			}
			if got, err = io.ReadAll(zr); err != nil {
				t.Fatalf("%s: a gzip body that does not read: %v", where, err)
			}
		default:
			t.Fatalf("%s: Content-Encoding %q", where, enc)
		}
		if gz, id, clean := refAcceptEncoding(aeLines); clean && aeLines != nil {
			wantCoded := gz > 0 && gz >= id && len(got) > 0 && (id == 0 || len(got) >= geta.GzipThreshold)
			if coded != wantCoded {
				t.Fatalf("%s: coded %v, want %v (gzip q %v, identity q %v, %d bytes)", where, coded, wantCoded, gz, id, len(got))
			}
		} else if aeLines == nil && coded {
			t.Fatalf("%s: coded with no Accept-Encoding", where)
		}
		switch {
		case rec.Code >= 400:
			fuzzProblem(t, where, rec.Code, h, got)
		case path == "/r":
			if !bytes.Equal(got, body) {
				t.Fatalf("%s: echoed %q", where, got)
			}
		case path == "/n":
			var b fzBig
			if err := json.Unmarshal(got, &b); err != nil || len(b.Text) != 1500 {
				t.Fatalf("%s: %q (%v)", where, got, err)
			}
		}
	})
}
