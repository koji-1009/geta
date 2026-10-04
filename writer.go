package geta

import (
	"bufio"
	"bytes"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// observer records what was written through it. It unwraps, so
// http.ResponseController reaches the server's writer for deadlines and full
// duplex.
type observer struct {
	http.ResponseWriter
	status    int
	bytes     int64
	streaming bool // the response was flushed before it ended
	upgraded  bool // the connection was hijacked
}

func (o *observer) WriteHeader(code int) {
	if o.status == 0 && code >= 200 || code == http.StatusSwitchingProtocols {
		o.status = code
	}
	o.ResponseWriter.WriteHeader(code)
}

func (o *observer) Write(b []byte) (int, error) {
	if o.status == 0 {
		o.status = http.StatusOK
	}
	n, err := o.ResponseWriter.Write(b)
	o.bytes += int64(n)
	return n, err
}

func (o *observer) Flush() {
	o.streaming = true
	http.NewResponseController(o.ResponseWriter).Flush()
}

func (o *observer) FlushError() error {
	o.streaming = true
	return http.NewResponseController(o.ResponseWriter).Flush()
}

func (o *observer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	o.upgraded = true
	if o.status == 0 {
		o.status = http.StatusSwitchingProtocols
	}
	return http.NewResponseController(o.ResponseWriter).Hijack()
}

func (o *observer) Unwrap() http.ResponseWriter { return o.ResponseWriter }

// buffer holds a response until the handler returns, so a middleware can
// rewrite it whole. An event stream, a 101, a flush, or a hijack switches it
// to pass-through.
type buffer struct {
	w       http.ResponseWriter
	header  http.Header
	status  int
	body    bytes.Buffer
	through bool // passing through: nothing is held
}

func newBuffer(w http.ResponseWriter) *buffer {
	return &buffer{w: w, header: w.Header()}
}

func (b *buffer) Header() http.Header { return b.header }

func (b *buffer) WriteHeader(code int) {
	if b.through {
		b.w.WriteHeader(code)
		return
	}
	if code < 200 && code != http.StatusSwitchingProtocols {
		b.w.WriteHeader(code) // an informational response goes out at once
		return
	}
	if b.status != 0 {
		return
	}
	b.status = code
	if code == http.StatusSwitchingProtocols || isEventStream(b.header) {
		b.passThrough()
	}
}

func (b *buffer) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.WriteHeader(http.StatusOK)
	}
	if b.through {
		return b.w.Write(p)
	}
	return b.body.Write(p)
}

// passThrough sends what is held and stops holding.
func (b *buffer) passThrough() error {
	if b.through {
		return nil
	}
	b.through = true
	if b.status != 0 {
		b.w.WriteHeader(b.status)
	}
	if b.body.Len() > 0 {
		_, err := b.w.Write(b.body.Bytes())
		b.body.Reset()
		return err
	}
	return nil
}

func (b *buffer) Flush() {
	b.FlushError()
}

func (b *buffer) FlushError() error {
	if b.status == 0 {
		b.WriteHeader(http.StatusOK)
	}
	if err := b.passThrough(); err != nil {
		return err
	}
	return http.NewResponseController(b.w).Flush()
}

func (b *buffer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	b.through = true
	return http.NewResponseController(b.w).Hijack()
}

func (b *buffer) Unwrap() http.ResponseWriter { return b.w }

// held reports whether the whole response is held, ready to rewrite.
func (b *buffer) held() bool { return !b.through }

// send writes the held response with status and body, setting
// Content-Length. A failed body write aborts the connection (abortUntaken).
func (b *buffer) send(r *http.Request, status int, body []byte) {
	if bodyless(status) {
		b.header.Del("Content-Length")
		b.w.WriteHeader(status)
		return
	}
	b.header.Set("Content-Length", strconv.Itoa(len(body)))
	b.w.WriteHeader(status)
	if len(body) == 0 {
		return
	}
	if _, err := b.w.Write(body); err != nil {
		abortUntaken(r, err)
	}
}

func bodyless(status int) bool {
	return status == http.StatusNoContent || status == http.StatusNotModified || status < 200
}

func isEventStream(h http.Header) bool {
	return strings.HasPrefix(strings.ToLower(h.Get("Content-Type")), "text/event-stream")
}

// addVary adds token to Vary unless it, in any case, or "*" is already there.
func addVary(h http.Header, token string) {
	for _, v := range h.Values("Vary") {
		for t := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) || strings.TrimSpace(t) == "*" {
				return
			}
		}
	}
	h.Add("Vary", token)
}
