package geta

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// prefixLines returns a multipart body of about size bytes for boundary whose
// one part's content is lines that each repeat the delimiter but its last
// byte: every such line is one closeWatcher follows to its full width.
func prefixLines(boundary string, size int) string {
	var b strings.Builder
	b.WriteString("--" + boundary + "\r\nContent-Disposition: form-data; name=\"x\"\r\n\r\n")
	line := "--" + boundary[:len(boundary)-1] + "Z\r\n"
	for b.Len() < size {
		b.WriteString(line)
	}
	b.WriteString("\r\n--" + boundary + "--\r\n")
	return b.String()
}

// scanAll passes body through a closeWatcher as mime/multipart reads it, in
// reads of at most 4096 bytes.
func scanAll(boundary, body string) *closeWatcher {
	w := newCloseWatcher(strings.NewReader(body), boundary)
	buf := make([]byte, 4096)
	for {
		if _, err := w.Read(buf); err != nil {
			return w
		}
	}
}

// timeScan returns how long scanAll takes on body.
func timeScan(boundary, body string) time.Duration {
	start := time.Now()
	scanAll(boundary, body)
	return time.Since(start)
}

// The close delimiter watch costs the body's length, whatever the boundary's
// (up to about 4 KB, the longest delimiter line mime/multipart reads): a line
// matching the delimiter's first bytes is compared a byte at a time. The two
// boundaries are timed in turn and each keeps its fastest run, so a pause on
// a shared machine falls on both or neither.
func TestCloseWatcherScanIsLinear(t *testing.T) {
	const size = 4 << 20
	short, long := strings.Repeat("b", 64), strings.Repeat("b", 4000)
	shortBody, longBody := prefixLines(short, size), prefixLines(long, size)
	if !scanAll(long, longBody).closed {
		t.Fatal("the close delimiter went unnoticed")
	}
	ts, tl := time.Duration(1<<63-1), time.Duration(1<<63-1)
	for range 5 {
		ts = min(ts, timeScan(short, shortBody))
		tl = min(tl, timeScan(long, longBody))
	}
	// The long boundary costs about what the short one does.
	if tl > 3*ts {
		t.Errorf("a 4000-byte boundary took %v, a 64-byte one %v: the scan grows with the boundary", tl, ts)
	}
}

func BenchmarkCloseWatcherBoundary(b *testing.B) {
	const size = 1 << 20
	for _, n := range []int{64, 500, 1000, 2000, 4000} {
		boundary := strings.Repeat("b", n)
		body := prefixLines(boundary, size)
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				scanAll(boundary, body)
			}
		})
	}
}
