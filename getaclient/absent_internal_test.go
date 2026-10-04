package getaclient

import (
	"encoding/json/v2"
	"testing"
)

// omitMembers ends, with the decoder's error, on JSON that breaks off or
// does not parse wherever it does: at the top, at a member's name, inside a
// member it skips, inside a member it copies, and inside an array. On each,
// PeekKind reports no kind; were the error not returned, the loop waiting
// for the closing delimiter would never end. A body json.Marshal wrote never
// does this; the test hands the function such JSON directly.
func TestOmitMembersEndsOnBrokenJSON(t *testing.T) {
	for _, b := range []string{``, `{`, `{1:2}`, `{"a":`, `{"b":`, `[`, `[1,`, `{"b":[}`} {
		out, err := omitMembers([]byte(b), []string{pathKey([]string{"a"})}, json.JoinOptions())
		if err == nil {
			t.Errorf("%q: got %q and no error", b, out)
		}
	}
}
