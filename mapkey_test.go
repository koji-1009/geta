package geta

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// keyCalls counts the calls encoding/json/v2 makes to a map key's methods:
// its text methods that write, UnmarshalText, and its JSON methods.
var keyCalls struct{ write, read, json int }

// A key's methods write "k-" before what it holds and read it back off.
func writeKey(s string) []byte {
	keyCalls.write++
	return []byte("k-" + s)
}

func readKey(dst *string, b []byte) error {
	keyCalls.read++
	s, ok := strings.CutPrefix(string(b), "k-")
	if !ok {
		return errors.New("no k- prefix")
	}
	if dst != nil {
		*dst = s
	}
	return nil
}

func writeJSONKey(s string) []byte {
	keyCalls.json++
	return []byte(`"k-` + s + `"`)
}

func readJSONKey(dst *string, b []byte) error {
	keyCalls.json++
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	s, ok := strings.CutPrefix(s, "k-")
	if !ok {
		return errors.New("no k- prefix")
	}
	*dst = s
	return nil
}

// mk<M><A><U> is a string key with MarshalText (M), AppendText (A), and
// UnmarshalText (U) on no receiver (N), the value (V), or the pointer (P).
type (
	mkNNN string
	mkNNV string
	mkNNP string
	mkNVN string
	mkNVV string
	mkNVP string
	mkNPN string
	mkNPV string
	mkNPP string
	mkVNN string
	mkVNV string
	mkVNP string
	mkVVN string
	mkVVV string
	mkVVP string
	mkVPN string
	mkVPV string
	mkVPP string
	mkPNN string
	mkPNV string
	mkPNP string
	mkPVN string
	mkPVV string
	mkPVP string
	mkPPN string
	mkPPV string
	mkPPP string
)

func (k mkVNN) MarshalText() ([]byte, error)  { return writeKey(string(k)), nil }
func (k mkVNV) MarshalText() ([]byte, error)  { return writeKey(string(k)), nil }
func (k mkVNP) MarshalText() ([]byte, error)  { return writeKey(string(k)), nil }
func (k mkVVN) MarshalText() ([]byte, error)  { return writeKey(string(k)), nil }
func (k mkVVV) MarshalText() ([]byte, error)  { return writeKey(string(k)), nil }
func (k mkVVP) MarshalText() ([]byte, error)  { return writeKey(string(k)), nil }
func (k mkVPN) MarshalText() ([]byte, error)  { return writeKey(string(k)), nil }
func (k mkVPV) MarshalText() ([]byte, error)  { return writeKey(string(k)), nil }
func (k mkVPP) MarshalText() ([]byte, error)  { return writeKey(string(k)), nil }
func (k *mkPNN) MarshalText() ([]byte, error) { return writeKey(string(*k)), nil }
func (k *mkPNV) MarshalText() ([]byte, error) { return writeKey(string(*k)), nil }
func (k *mkPNP) MarshalText() ([]byte, error) { return writeKey(string(*k)), nil }
func (k *mkPVN) MarshalText() ([]byte, error) { return writeKey(string(*k)), nil }
func (k *mkPVV) MarshalText() ([]byte, error) { return writeKey(string(*k)), nil }
func (k *mkPVP) MarshalText() ([]byte, error) { return writeKey(string(*k)), nil }
func (k *mkPPN) MarshalText() ([]byte, error) { return writeKey(string(*k)), nil }
func (k *mkPPV) MarshalText() ([]byte, error) { return writeKey(string(*k)), nil }
func (k *mkPPP) MarshalText() ([]byte, error) { return writeKey(string(*k)), nil }

func (k mkNVN) AppendText(b []byte) ([]byte, error)  { return append(b, writeKey(string(k))...), nil }
func (k mkNVV) AppendText(b []byte) ([]byte, error)  { return append(b, writeKey(string(k))...), nil }
func (k mkNVP) AppendText(b []byte) ([]byte, error)  { return append(b, writeKey(string(k))...), nil }
func (k mkVVN) AppendText(b []byte) ([]byte, error)  { return append(b, writeKey(string(k))...), nil }
func (k mkVVV) AppendText(b []byte) ([]byte, error)  { return append(b, writeKey(string(k))...), nil }
func (k mkVVP) AppendText(b []byte) ([]byte, error)  { return append(b, writeKey(string(k))...), nil }
func (k mkPVN) AppendText(b []byte) ([]byte, error)  { return append(b, writeKey(string(k))...), nil }
func (k mkPVV) AppendText(b []byte) ([]byte, error)  { return append(b, writeKey(string(k))...), nil }
func (k mkPVP) AppendText(b []byte) ([]byte, error)  { return append(b, writeKey(string(k))...), nil }
func (k *mkNPN) AppendText(b []byte) ([]byte, error) { return append(b, writeKey(string(*k))...), nil }
func (k *mkNPV) AppendText(b []byte) ([]byte, error) { return append(b, writeKey(string(*k))...), nil }
func (k *mkNPP) AppendText(b []byte) ([]byte, error) { return append(b, writeKey(string(*k))...), nil }
func (k *mkVPN) AppendText(b []byte) ([]byte, error) { return append(b, writeKey(string(*k))...), nil }
func (k *mkVPV) AppendText(b []byte) ([]byte, error) { return append(b, writeKey(string(*k))...), nil }
func (k *mkVPP) AppendText(b []byte) ([]byte, error) { return append(b, writeKey(string(*k))...), nil }
func (k *mkPPN) AppendText(b []byte) ([]byte, error) { return append(b, writeKey(string(*k))...), nil }
func (k *mkPPV) AppendText(b []byte) ([]byte, error) { return append(b, writeKey(string(*k))...), nil }
func (k *mkPPP) AppendText(b []byte) ([]byte, error) { return append(b, writeKey(string(*k))...), nil }

// A value receiver's UnmarshalText cannot set the key; v2 calls it still.
func (k mkNNV) UnmarshalText(b []byte) error  { return readKey(nil, b) }
func (k mkNVV) UnmarshalText(b []byte) error  { return readKey(nil, b) }
func (k mkNPV) UnmarshalText(b []byte) error  { return readKey(nil, b) }
func (k mkVNV) UnmarshalText(b []byte) error  { return readKey(nil, b) }
func (k mkVVV) UnmarshalText(b []byte) error  { return readKey(nil, b) }
func (k mkVPV) UnmarshalText(b []byte) error  { return readKey(nil, b) }
func (k mkPNV) UnmarshalText(b []byte) error  { return readKey(nil, b) }
func (k mkPVV) UnmarshalText(b []byte) error  { return readKey(nil, b) }
func (k mkPPV) UnmarshalText(b []byte) error  { return readKey(nil, b) }
func (k *mkNNP) UnmarshalText(b []byte) error { return readKey((*string)(k), b) }
func (k *mkNVP) UnmarshalText(b []byte) error { return readKey((*string)(k), b) }
func (k *mkNPP) UnmarshalText(b []byte) error { return readKey((*string)(k), b) }
func (k *mkVNP) UnmarshalText(b []byte) error { return readKey((*string)(k), b) }
func (k *mkVVP) UnmarshalText(b []byte) error { return readKey((*string)(k), b) }
func (k *mkVPP) UnmarshalText(b []byte) error { return readKey((*string)(k), b) }
func (k *mkPNP) UnmarshalText(b []byte) error { return readKey((*string)(k), b) }
func (k *mkPVP) UnmarshalText(b []byte) error { return readKey((*string)(k), b) }
func (k *mkPPP) UnmarshalText(b []byte) error { return readKey((*string)(k), b) }

// String keys with JSON methods, which v2 calls in place of text methods.
type (
	jkVP   string // MarshalJSON on the value, UnmarshalJSON on the pointer
	jkPP   string // both on the pointer
	jkVN   string // MarshalJSON alone
	jkNP   string // UnmarshalJSON alone
	jkVPM  string // jkVP, and MarshalText alone
	jkVU   string // MarshalJSON, and UnmarshalText
	jkTo   string // MarshalJSONTo and UnmarshalJSONFrom
	jkPPMU string // jkPP, and both text methods
)

func (k jkVP) MarshalJSON() ([]byte, error)    { return writeJSONKey(string(k)), nil }
func (k *jkVP) UnmarshalJSON(b []byte) error   { return readJSONKey((*string)(k), b) }
func (k *jkPP) MarshalJSON() ([]byte, error)   { return writeJSONKey(string(*k)), nil }
func (k *jkPP) UnmarshalJSON(b []byte) error   { return readJSONKey((*string)(k), b) }
func (k jkVN) MarshalJSON() ([]byte, error)    { return writeJSONKey(string(k)), nil }
func (k *jkNP) UnmarshalJSON(b []byte) error   { return readJSONKey((*string)(k), b) }
func (k jkVPM) MarshalJSON() ([]byte, error)   { return writeJSONKey(string(k)), nil }
func (k *jkVPM) UnmarshalJSON(b []byte) error  { return readJSONKey((*string)(k), b) }
func (k jkVPM) MarshalText() ([]byte, error)   { return writeKey(string(k)), nil }
func (k jkVU) MarshalJSON() ([]byte, error)    { return writeJSONKey(string(k)), nil }
func (k *jkVU) UnmarshalText(b []byte) error   { return readKey((*string)(k), b) }
func (k *jkPPMU) MarshalJSON() ([]byte, error) { return writeJSONKey(string(*k)), nil }
func (k *jkPPMU) UnmarshalJSON(b []byte) error { return readJSONKey((*string)(k), b) }
func (k jkPPMU) MarshalText() ([]byte, error)  { return writeKey(string(k)), nil }
func (k *jkPPMU) UnmarshalText(b []byte) error { return readKey((*string)(k), b) }

func (k jkTo) MarshalJSONTo(enc *jsontext.Encoder) error {
	keyCalls.json++
	return enc.WriteToken(jsontext.String("k-" + string(k)))
}

func (k *jkTo) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	keyCalls.json++
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	s, ok := strings.CutPrefix(tok.String(), "k-")
	if tok.Kind() != '"' || !ok {
		return errors.New("no k- string")
	}
	*k = jkTo(s)
	return nil
}

type mapKeyIn[K ~string] struct {
	Body map[K]int `body:"json"`
}

// A string map key is accepted exactly when encoding/json/v2 writes and
// reads it alike, whatever receivers its methods have: by text methods both
// ways, by neither, or by a JSON method. An accepted map is read, on the
// single pass and the reference path, and written as v2 reads and writes
// it. geta once refused a key with a value MarshalText as having no string
// form, and accepted the same key with a pointer one; and refused a key
// whose text methods were one-sided under JSON methods v2 calls instead.
func TestMapKeysFollowV2ForEveryReceiver(t *testing.T) {
	for name, run := range map[string]func(*testing.T){
		"NNN": mapKeyCase[mkNNN], "NNV": mapKeyCase[mkNNV], "NNP": mapKeyCase[mkNNP],
		"NVN": mapKeyCase[mkNVN], "NVV": mapKeyCase[mkNVV], "NVP": mapKeyCase[mkNVP],
		"NPN": mapKeyCase[mkNPN], "NPV": mapKeyCase[mkNPV], "NPP": mapKeyCase[mkNPP],
		"VNN": mapKeyCase[mkVNN], "VNV": mapKeyCase[mkVNV], "VNP": mapKeyCase[mkVNP],
		"VVN": mapKeyCase[mkVVN], "VVV": mapKeyCase[mkVVV], "VVP": mapKeyCase[mkVVP],
		"VPN": mapKeyCase[mkVPN], "VPV": mapKeyCase[mkVPV], "VPP": mapKeyCase[mkVPP],
		"PNN": mapKeyCase[mkPNN], "PNV": mapKeyCase[mkPNV], "PNP": mapKeyCase[mkPNP],
		"PVN": mapKeyCase[mkPVN], "PVV": mapKeyCase[mkPVV], "PVP": mapKeyCase[mkPVP],
		"PPN": mapKeyCase[mkPPN], "PPV": mapKeyCase[mkPPV], "PPP": mapKeyCase[mkPPP],
		"jVP": mapKeyCase[jkVP], "jPP": mapKeyCase[jkPP], "jVN": mapKeyCase[jkVN], "jNP": mapKeyCase[jkNP],
		"jVPM": mapKeyCase[jkVPM], "jVU": mapKeyCase[jkVU], "jTo": mapKeyCase[jkTo], "jPPMU": mapKeyCase[jkPPMU],
	} {
		t.Run(name, run)
	}
}

func mapKeyCase[K ~string](t *testing.T) {
	// What v2 does: which of the key's methods it calls to write and read.
	keyCalls = struct{ write, read, json int }{}
	written, werr := json.Marshal(map[K]int{"a": 1, "b": 2}, marshalOptions)
	wrote, wroteJSON := keyCalls.write > 0, keyCalls.json > 0
	keyCalls = struct{ write, read, json int }{}
	_ = json.Unmarshal([]byte(`{"k-a":1}`), new(map[K]int))
	read, readJSON := keyCalls.read > 0, keyCalls.json > 0
	wantAccepted := wroteJSON || readJSON || wrote == read

	fixed := map[K]int{"a": 1, "b": 2}
	app, err := New(Table{Routes: []Entry{{Path: "/m", Route: Route{
		Post: Op(http.StatusOK, func(_ context.Context, in *mapKeyIn[K]) (*map[K]int, error) { return &in.Body, nil }, Doc{}),
		Get:  Op(http.StatusOK, func(context.Context, *struct{}) (*map[K]int, error) { return &fixed, nil }, Doc{}),
	}}}})
	if (err == nil) != wantAccepted {
		t.Fatalf("v2 writes by text %v, reads by text %v, calls JSON methods %v; geta.New: %v",
			wrote, read, wroteJSON || readJSON, err)
	}
	if err != nil {
		return
	}
	if !bytes.Contains(app.OpenAPI(), []byte(`"additionalProperties"`)) {
		t.Errorf("document lacks the map:\n%s", app.OpenAPI())
	}

	// Writing.
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/m", nil))
	if werr != nil {
		t.Fatal(werr)
	}
	if got := bytes.TrimSpace(rec.Body.Bytes()); rec.Code != http.StatusOK || !bytes.Equal(got, written) {
		t.Errorf("GET: %d %s, v2 writes %s", rec.Code, got, written)
	}

	// Reading, on both paths, against v2.
	for _, body := range []string{string(written), `{}`, `{"k-a":1}`, `{"a":1}`, `{"k-a":1,"k-b":2}`, `{"k-a":1,"a":2}`} {
		var want map[K]int
		verr := json.Unmarshal([]byte(body), &want)
		var ref map[K]int
		errs := reference(newRegistry(), mustCodec[map[K]int](t), mustCodec[map[K]int](t).use(), []byte(body), DefaultLimits,
			reflect.ValueOf(&ref).Elem())
		if (verr == nil) != (len(errs) == 0) || verr == nil && !reflect.DeepEqual(ref, want) {
			t.Errorf("%s: reference %v %v, v2 %v %v", body, ref, errs, want, verr)
		}
		agree[map[K]int](t, []byte(body), DefaultLimits)
		req := httptest.NewRequest(http.MethodPost, "/m", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if verr != nil {
			if rec.Code != http.StatusBadRequest {
				t.Errorf("POST %s: %d %s, v2 refuses it: %v", body, rec.Code, rec.Body, verr)
			}
			continue
		}
		echoed, err := json.Marshal(want, marshalOptions)
		if err != nil {
			t.Fatal(err)
		}
		if got := bytes.TrimSpace(rec.Body.Bytes()); rec.Code != http.StatusOK || !bytes.Equal(got, echoed) {
			t.Errorf("POST %s: %d %s, v2 reads and writes %s", body, rec.Code, got, echoed)
		}
	}
}
