package getaclient

import (
	"encoding/json/v2"
	"math"
	"reflect"
	"testing"
	"time"
)

// Leaving out a member of each of n elements costs time linear in n: each
// field is looked up, not searched for.
func TestAbsentIsLinear(t *testing.T) {
	type line struct {
		Qty int `json:"qty" schema:"default=1"`
	}
	type body struct {
		Lines []line `json:"lines"`
	}
	// The least of a few runs, against a machine's noise.
	cost := func(n int) time.Duration {
		v := body{Lines: make([]line, n)}
		fields := make([]any, n)
		for i := range v.Lines {
			fields[i] = &v.Lines[i].Qty
		}
		least := time.Duration(math.MaxInt64)
		for range 5 {
			start := time.Now()
			abs, err := newAbsences(fields)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := jsonBody(reflect.ValueOf(&v).Elem(), json.Deterministic(true), abs); err != nil {
				t.Fatal(err)
			}
			least = min(least, time.Since(start))
		}
		return least
	}
	// Eight times the fields: about eight times the time, where searching
	// each would take sixty-four.
	if small, large := cost(1000), cost(8000); large > 24*small {
		t.Fatalf("1000 fields: %v, 8000: %v", small, large)
	}
}

// omitMembers ends, with the decoder's error, on JSON that breaks off or
// does not parse wherever it does: at the top, at a member's name, inside a
// member it skips, inside a member it copies, and inside an array. On each,
// PeekKind reports no kind; were the error not returned, the loop waiting
// for the closing delimiter would never end. A body json.Marshal wrote never
// does this; the test hands the function such JSON directly.
func TestOmitMembersEndsOnBrokenJSON(t *testing.T) {
	for _, b := range []string{``, `{`, `{1:2}`, `{"a":`, `{"b":`, `[`, `[1,`, `{"b":[}`} {
		out, err := omitMembers([]byte(b), map[string]bool{pathKey([]string{"a"}): true}, json.JoinOptions())
		if err == nil {
			t.Errorf("%q: got %q and no error", b, out)
		}
	}
}
