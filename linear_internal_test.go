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

func timeScan(boundary, body string) time.Duration {
	best := time.Duration(1<<63 - 1)
	for range 5 {
		start := time.Now()
		scanAll(boundary, body)
		best = min(best, time.Since(start))
	}
	return best
}

// The close delimiter watch costs the body's length, whatever the boundary's:
// a line matching the delimiter's first bytes is compared a byte at a time,
// not as a prefix that grows with each byte. Comparing the prefix made a 1 MB
// body of such lines cost its length times the boundary's (up to about 4 KB,
// the longest delimiter line mime/multipart reads).
func TestCloseWatcherScanIsLinear(t *testing.T) {
	const size = 1 << 20
	short, long := strings.Repeat("b", 64), strings.Repeat("b", 4000)
	ts, tl := timeScan(short, prefixLines(short, size)), timeScan(long, prefixLines(long, size))
	if !scanAll(long, prefixLines(long, size)).closed {
		t.Fatal("the close delimiter went unnoticed")
	}
	// Linear, the long boundary costs no more than the short one; quadratic,
	// it cost over 5 times as much.
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
