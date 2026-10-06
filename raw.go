package geta

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strconv"
)

// A raw body is bytes of the one media type its body tag names
// (body:"text/csv"). An input reads it into a []byte or hands it over as an
// io.Reader; an envelope writes it from a []byte or copies it from an
// io.Reader. geta checks the media type and the body limit, not the bytes.
// The document lists the media type with no schema (OpenAPI 3.1 raw binary
// content). There is no content negotiation.

var readerType = reflect.TypeFor[io.Reader]()

// rawPlan binds an input's raw body.
type rawPlan struct {
	index     []int
	mediaType string // as the tag writes it
	essence   string // type/subtype, compared with the request's Content-Type
	optional  bool   // *[]byte
	reader    bool   // io.Reader
	method    string // the operation's method (unsupported)
	desc      string // the body's description (doc tag)
}

func newRawPlan(f reflect.StructField, mt string, index []int) *rawPlan {
	essence, _, _ := mime.ParseMediaType(mt) // checkInputField has parsed it
	return &rawPlan{index: index, mediaType: mt, essence: essence,
		optional: f.Type.Kind() == reflect.Pointer, reader: f.Type == readerType, desc: f.Tag.Get("doc")}
}

// bind reads the body into dst. A content coding or another media type is a
// 415, and an empty body is no body. A body past MaxBodyBytes is a 413: here
// for a []byte, and for an io.Reader when the handler reads past the limit
// (Read returns *http.MaxBytesError, which fail answers 413).
func (p *rawPlan) bind(w http.ResponseWriter, r *http.Request, dst reflect.Value, limits Limits) *bindError {
	// Judge a declared body before reading any of it, so a client waiting
	// for 100 Continue gets its answer without sending the body.
	n := declaredLength(r)
	if n > 0 {
		if be := p.judge(w, r); be != nil {
			return be
		}
		if !p.reader && n > limits.MaxBodyBytes {
			return tooLong(limits.MaxBodyBytes)
		}
	}
	br := bufio.NewReader(http.MaxBytesReader(w, r.Body, limits.MaxBodyBytes))
	if _, err := br.Peek(1); err != nil {
		if err == io.EOF {
			if !p.optional {
				return bodyViolation("$", "missing required request body")
			}
			return nil
		}
		return readFailure(err, limits)
	}
	if n <= 0 {
		if be := p.judge(w, r); be != nil {
			return be
		}
	}
	if p.reader {
		dst.Set(reflect.ValueOf(&readerBody{br}))
		return nil
	}
	data, err := io.ReadAll(br)
	if err != nil {
		return readFailure(err, limits)
	}
	if p.optional {
		dst.Set(reflect.ValueOf(&data))
		return nil
	}
	dst.SetBytes(data)
	return nil
}

// readerBody is a raw io.Reader body. It marks a failure to read the request
// (bodyReadError), so that dispatch answers it as it answers any body that
// could not be read, whatever the handler returns it in.
type readerBody struct{ r io.Reader }

func (b *readerBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err != nil && err != io.EOF {
		err = &bodyReadError{err}
	}
	return n, err
}

// bodyReadError is an error reading a raw io.Reader body.
type bodyReadError struct{ err error }

func (e *bodyReadError) Error() string { return e.err.Error() }
func (e *bodyReadError) Unwrap() error { return e.err }

// judge returns a 415 for a content coding or another media type, else nil.
func (p *rawPlan) judge(w http.ResponseWriter, r *http.Request) *bindError {
	if be := codingRefused(w, r); be != nil {
		return be
	}
	ct := r.Header.Get("Content-Type")
	if mt, _, err := mime.ParseMediaType(ct); err != nil || mt != p.essence {
		return unsupported(w, ct, p.mediaType, p.method)
	}
	return nil
}

// rawOut writes an envelope's raw body.
type rawOut struct {
	index     []int
	mediaType string
	reader    bool // io.Reader; else []byte
}

// writeRaw sends the envelope v with its raw body, the declared media type as
// Content-Type, and nosniff. A body that fits in limit bytes gets a
// Content-Length, and a reader failing within them is a clean 500. A longer
// body is streamed after the header is committed, so a failure then returns
// a *committedError. The caller closes a reader that is an io.Closer.
func (p *outPlan) writeRaw(w http.ResponseWriter, status int, v reflect.Value, limit int) error {
	ro := p.raw
	fv := v.FieldByIndex(ro.index)
	h := w.Header()
	if !ro.reader {
		if err := p.setHeaders(h, v); err != nil {
			return err
		}
		b := fv.Bytes()
		rawHeaders(h, ro.mediaType)
		h["Content-Length"] = []string{strconv.Itoa(len(b))}
		w.WriteHeader(status)
		if _, err := w.Write(b); err != nil {
			return &committedError{err: err, write: true}
		}
		return nil
	}
	if fv.IsNil() {
		return errors.New("the raw body's io.Reader is nil")
	}
	rd := fv.Interface().(io.Reader)
	// Hold up to limit bytes, and one more to tell whether the body ends
	// within them.
	buf := make([]byte, max(limit, 0)+1)
	n, err := io.ReadFull(rd, buf)
	switch {
	case err == io.EOF || err == io.ErrUnexpectedEOF:
		if err := p.setHeaders(h, v); err != nil {
			return err
		}
		rawHeaders(h, ro.mediaType)
		h["Content-Length"] = []string{strconv.Itoa(n)}
		w.WriteHeader(status)
		if _, err := w.Write(buf[:n]); err != nil {
			return &committedError{err: err, write: true}
		}
		return nil
	case err != nil:
		return fmt.Errorf("reading the raw body: %w", err)
	}
	if err := p.setHeaders(h, v); err != nil {
		return err
	}
	rawHeaders(h, ro.mediaType)
	delete(h, "Content-Length")
	w.WriteHeader(status)
	if _, err := w.Write(buf[:n]); err != nil {
		return &committedError{err: err, write: true}
	}
	cw := &copyWriter{w: w}
	if _, err := io.Copy(cw, rd); err != nil {
		if cw.err != nil {
			return &committedError{err: cw.err, write: true}
		}
		return &committedError{err: fmt.Errorf("reading the raw body: %w", err)}
	}
	return nil
}

// copyWriter tells a failure to write the response from a failure to read
// the body being copied.
type copyWriter struct {
	w   io.Writer
	err error
}

func (c *copyWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	if err != nil {
		c.err = err
	}
	return n, err
}

func rawHeaders(h http.Header, mt string) {
	h["Content-Type"] = []string{mt}
	h["X-Content-Type-Options"] = []string{"nosniff"}
}
