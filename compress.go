package geta

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// CompressThreshold is the smallest body size, in bytes, that [Compress] and
// [Gzip] code in any coding.
const CompressThreshold = 1024

// Coding is a content coding (RFC 9110 §8.4.1) that [Compress] can apply to
// a response body. Name is its token, such as "zstd" or "br": it is sent in
// Content-Encoding, matched against Accept-Encoding without case, and
// appended to the coded form's entity tag. NewWriter returns an encoder that
// writes the coded form of its input to w.
//
// Compress calls NewWriter once per response it codes, writes the whole body
// in one Write, and calls Close once; it never codes a stream. A pooled
// encoder can be reset onto w in NewWriter and returned to the pool by Close.
// An error from NewWriter, Write, or Close sends the body uncoded.
type Coding struct {
	Name      string
	NewWriter func(w io.Writer) (io.WriteCloser, error)
}

// GzipCoding returns the gzip coding, using compress/gzip at its default
// level. An Accept-Encoding of x-gzip also selects it. Its encoders are
// pooled; a Write, Flush, or Close after Close returns [ErrEncoderClosed].
func GzipCoding() Coding {
	return Coding{Name: "gzip", NewWriter: func(w io.Writer) (io.WriteCloser, error) {
		e, _ := gzipEncoders.Get().(*gzipEncoder)
		if e == nil {
			e = new(gzipEncoder)
			e.zw = gzip.NewWriter(&e.out)
		} else {
			e.zw.Reset(&e.out)
		}
		e.out.w = w
		return &gzipWriter{e: e}, nil
	}}
}

// ErrEncoderClosed is returned by a [GzipCoding] writer used after its Close.
var ErrEncoderClosed = errors.New("geta: gzip writer used after Close")

// gzipEncoders holds the idle encoders of [GzipCoding].
var gzipEncoders sync.Pool

// gzipEncoder is a pooled gzip writer. It writes through out, whose
// destination is cleared on return to the pool.
type gzipEncoder struct {
	zw  *gzip.Writer
	out sink
}

// sink writes to w.
type sink struct{ w io.Writer }

func (s *sink) Write(p []byte) (int, error) { return s.w.Write(p) }

// gzipWriter is one use of a pooled encoder. Close drops the encoder, so
// later calls cannot reach one another response holds.
type gzipWriter struct{ e *gzipEncoder }

func (w *gzipWriter) Write(p []byte) (int, error) {
	if w.e == nil {
		return 0, ErrEncoderClosed
	}
	return w.e.zw.Write(p)
}

// Flush writes what the encoder holds to the destination.
func (w *gzipWriter) Flush() error {
	if w.e == nil {
		return ErrEncoderClosed
	}
	return w.e.zw.Flush()
}

// Close finishes the gzip stream and puts the encoder back in the pool.
func (w *gzipWriter) Close() error {
	e := w.e
	if e == nil {
		return ErrEncoderClosed
	}
	w.e = nil
	err := e.zw.Close()
	e.out.w = nil
	gzipEncoders.Put(e)
	return err
}

// Gzip is [Compress] with gzip alone.
func Gzip() Middleware {
	m := Compress(GzipCoding())
	m.name = "gzip"
	return m
}

// Compress codes a response body of at least [CompressThreshold] bytes with a
// text-like media type (text/*, JSON, XML, JavaScript, and +json and +xml
// types) in the coding the client prefers: of the codings Accept-Encoding
// accepts with a q-value above 0, named or through "*", the highest q-value
// wins, and the earlier in codings wins a tie. "x-gzip" names gzip. A weight
// outside the RFC 9110 §12.4.2 grammar counts as q=0.
//
// A body already encoded, a 204, a 206 (its Content-Range counts uncoded
// bytes), a 304, and any response to a request without Accept-Encoding pass
// uncoded. Every response but a stream gets
// Vary: Accept-Encoding. A stream is never coded and gets no Vary
// (RFC 9110 §12.5.5).
//
// Identity is preferred only when Accept-Encoding gives it a higher q-value
// than the winner. When identity is refused and a coding accepted, every
// non-empty body not already encoded is coded, whatever its size and media
// type. When identity is refused and no coding accepted, the body is sent
// uncoded rather than as a 406 (RFC 9110 §12.4.1).
//
// A strong ETag on a coded body gets "-" and the coding's name appended to
// its opaque tag ("v1" becomes "v1-gzip"), since a strong tag names one
// sequence of bytes (RFC 9110 §8.8.1). Compress maps such tags in If-Match
// and If-None-Match back to the handler's before the request goes on, so
// [Conditional.Check] and [ETag] see the handler's own tag. A 304 to a
// request that named a coded form's tag carries that tag. Weak tags are left
// as they are.
//
// A handler's strong tag that already looks like a coded form's goes out
// with "-identity" appended and is read back without it. For the same
// reason, [New] refuses codings whose names cannot be told apart at the end
// of a tag: a name that is not a token, "identity", "*", "x-gzip", one ending
// in "-identity", two equal without case, or one ending in "-" and another's
// name ("x-br" and "br"). New also refuses Compress with no coding or a
// coding without NewWriter.
func Compress(codings ...Coding) Middleware {
	cs, bad := newCodings(codings)
	m := Ordered(OrderNegotiate, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tw := &tagWriter{ResponseWriter: w, cs: cs}
			b := newBuffer(tw)
			returned := false
			defer func() {
				if !returned {
					b.lost()
				}
			}()
			next.ServeHTTP(b, cs.decodeConditions(r))
			returned = true
			if !b.held() {
				return
			}
			if b.status == 0 {
				// Nothing was written: net/http sends the header past
				// tagWriter, so set its tag and Vary now.
				addVary(b.header, "Accept-Encoding")
				tw.identity()
				return
			}
			tw.done = true // the tag is set below
			h := b.header
			addVary(h, "Accept-Encoding")
			body := b.body.Bytes()
			tag := h.Get("ETag")
			// A valid tag that is not weak is a quoted opaque tag.
			opaque, strong := strongOpaque(tag)
			strong = strong && validETag(tag)
			ranked, identity := cs.negotiate(r.Header.Values("Accept-Encoding"))
			var use *coding
			if len(ranked) > 0 && ranked[0].q >= identity {
				use = ranked[0].c
			}
			var coded []byte
			// A 206's Content-Range counts the bytes of the representation
			// uncoded, so its body is never coded.
			if use != nil && !bodyless(b.status) && b.status != http.StatusPartialContent && len(body) > 0 && h.Get("Content-Encoding") == "" &&
				(identity == 0 || len(body) >= CompressThreshold && compressible(h.Get("Content-Type"))) {
				coded = use.encode(body)
			}
			if strong {
				h.Set("ETag", cs.identityTag(tag))
				switch {
				case coded != nil:
					h.Set("ETag", codedTag(opaque, use))
				case b.status == http.StatusNotModified:
					// The client holds a coded form, and asked with its tag.
					held := etagList(r.Header.Values("If-None-Match"))
					for _, ch := range ranked {
						if t := codedTag(opaque, ch.c); slices.Contains(held, t) {
							h.Set("ETag", t)
							break
						}
					}
				}
			}
			if coded == nil {
				b.send(r, b.status, body)
				return
			}
			h.Set("Content-Encoding", use.name)
			b.send(r, b.status, coded)
		})
	})
	m.name = "compress"
	m.bad = bad
	return m
}

// coding is a validated [Coding].
type coding struct {
	name      string // lowercased
	suffix    string // "-" + name, ending the coded form's opaque tag
	newWriter func(io.Writer) (io.WriteCloser, error)
}

// encode returns body coded, or nil if the encoder failed.
func (c *coding) encode(body []byte) []byte {
	var z bytes.Buffer
	zw, err := c.newWriter(&z)
	if err != nil || zw == nil {
		return nil
	}
	if _, err := zw.Write(body); err != nil {
		zw.Close()
		return nil
	}
	if err := zw.Close(); err != nil {
		return nil
	}
	return z.Bytes()
}

// codings are the codings of one [Compress], in the server's order of
// preference.
type codings []*coding

// newCodings builds the codings of list and returns every problem with it
// as one error.
func newCodings(list []Coding) (codings, error) {
	var cs codings
	var errs []error
	if len(list) == 0 {
		errs = append(errs, errors.New("geta.Compress has no coding"))
	}
	for _, c := range list {
		name := strings.ToLower(c.Name)
		switch {
		case !validHeaderName(c.Name):
			errs = append(errs, fmt.Errorf("geta.Compress: coding name %q is not a token", c.Name))
		case name == "identity" || name == "*":
			errs = append(errs, fmt.Errorf("geta.Compress: %q names no coding", c.Name))
		case name == "x-gzip":
			errs = append(errs, fmt.Errorf("geta.Compress: %q is an alias; use gzip", c.Name))
		case strings.HasSuffix(name, identityTagSuffix):
			errs = append(errs, fmt.Errorf("geta.Compress: coding name %q ends in reserved suffix %q", c.Name, identityTagSuffix))
		case c.NewWriter == nil:
			errs = append(errs, fmt.Errorf("geta.Compress: coding %q has no NewWriter", c.Name))
		}
		for _, o := range cs {
			switch {
			case o.name == name:
				errs = append(errs, fmt.Errorf("geta.Compress: coding %q is given twice", c.Name))
			case strings.HasSuffix(name, o.suffix), strings.HasSuffix(o.name, "-"+name):
				errs = append(errs, fmt.Errorf("geta.Compress: codings %q and %q end their entity tags alike", o.name, name))
			}
		}
		cs = append(cs, &coding{name: name, suffix: "-" + name, newWriter: c.NewWriter})
	}
	return cs, errors.Join(errs...)
}

// lookup returns the index of the coding an Accept-Encoding token,
// lowercased, names, or -1.
func (cs codings) lookup(token string) int {
	if token == "x-gzip" {
		token = "gzip"
	}
	return slices.IndexFunc(cs, func(c *coding) bool { return c.name == token })
}

// choice is an accepted coding and its q-value.
type choice struct {
	c *coding
	q float64
}

// negotiate applies RFC 9110 §12.5.3 to the Accept-Encoding values. ranked
// holds the accepted codings, highest q-value first, ties in the server's
// order. identity is identity's q-value, named or through "*", or -1 when
// neither is given.
func (cs codings) negotiate(values []string) (ranked []choice, identity float64) {
	named := make([]float64, len(cs))
	for i := range named {
		named[i] = -1
	}
	wild := -1.0
	identity = -1
	for _, v := range values {
		for part := range strings.SplitSeq(v, ",") {
			// Trim OWS only (RFC 9110 §5.6.1), not Unicode spaces.
			token, params, weighted := strings.Cut(strings.Trim(part, " \t"), ";")
			q := 1.0
			if weighted {
				q = qvalue(params)
			}
			// A token, so ToLower folds ASCII only (the Kelvin sign is no k).
			if token = strings.Trim(token, " \t"); !validHeaderName(token) {
				continue
			}
			switch token = strings.ToLower(token); token {
			case "*":
				wild = q
			case "identity":
				identity = q
			default:
				if i := cs.lookup(token); i >= 0 {
					named[i] = q
				}
			}
		}
	}
	for i, c := range cs {
		q := named[i]
		if q < 0 {
			q = wild
		}
		if q > 0 {
			ranked = append(ranked, choice{c, q})
		}
	}
	slices.SortStableFunc(ranked, func(a, b choice) int { return cmp.Compare(b.q, a.q) })
	if identity < 0 {
		identity = wild
	}
	return ranked, identity
}

// qvalue parses the weight after the ";" of an Accept-Encoding element
// (RFC 9110 §12.4.2: OWS "q=" qvalue, "q" in either case). Anything else,
// such as another parameter or a malformed number, reads as 0.
func qvalue(params string) float64 {
	v, ok := strings.CutPrefix(strings.TrimLeft(params, " \t"), "q=")
	if !ok {
		v, ok = strings.CutPrefix(strings.TrimLeft(params, " \t"), "Q=")
	}
	if !ok || !isQValue(v) {
		return 0
	}
	f, _ := strconv.ParseFloat(v, 64)
	return f
}

// isQValue reports whether s is a qvalue (RFC 9110 §12.4.2):
//
//	qvalue = ( "0" [ "." 0*3DIGIT ] ) / ( "1" [ "." 0*3("0") ] )
func isQValue(s string) bool {
	if s == "" || s[0] != '0' && s[0] != '1' {
		return false
	}
	rest := s[1:]
	if rest == "" {
		return true
	}
	if rest[0] != '.' || len(rest) > 4 {
		return false
	}
	for i := 1; i < len(rest); i++ {
		if c := rest[i]; c < '0' || c > '9' || s[0] == '1' && c != '0' {
			return false
		}
	}
	return true
}

// identityTagSuffix marks an identity form's tag that would otherwise read
// as a coded form's.
const identityTagSuffix = "-identity"

// strongOpaque returns the opaque tag of a strong entity tag, without its
// quotes; ok is false for a weak tag or anything else.
func strongOpaque(tag string) (opaque string, ok bool) {
	if len(tag) < 2 || tag[0] != '"' || tag[len(tag)-1] != '"' {
		return "", false
	}
	return tag[1 : len(tag)-1], true
}

// reserved reports whether an opaque tag reads as one [Compress] gave: it
// ends in "-" and a coding's name, possibly followed by "-identity"s.
func (cs codings) reserved(opaque string) bool {
	for strings.HasSuffix(opaque, identityTagSuffix) {
		opaque = strings.TrimSuffix(opaque, identityTagSuffix)
	}
	return cs.ending(opaque) != nil
}

// ending returns the coding whose suffix ends opaque, or nil. newCodings
// ensures at most one does.
func (cs codings) ending(opaque string) *coding {
	for _, c := range cs {
		if strings.HasSuffix(opaque, c.suffix) {
			return c
		}
	}
	return nil
}

// codedTag returns the strong tag of c's coded form of opaque.
func codedTag(opaque string, c *coding) string {
	return `"` + opaque + c.suffix + `"`
}

// identityTag returns the tag [Compress] sends for an uncoded body: tag
// itself, or tag with identityTagSuffix if it would read as a coded form's.
func (cs codings) identityTag(tag string) string {
	opaque, ok := strongOpaque(tag)
	if !ok || !cs.reserved(opaque) {
		return tag
	}
	return `"` + opaque + identityTagSuffix + `"`
}

// decodeTag inverts [codedTag] and [codings.identityTag], returning the
// handler's tag for a tag [Compress] sent. Any other tag is returned as is.
// The inverse is unique: no tag is the output of two inputs.
func (cs codings) decodeTag(tag string) string {
	opaque, ok := strongOpaque(tag)
	if !ok {
		return tag
	}
	if base, ok := strings.CutSuffix(opaque, identityTagSuffix); ok && cs.reserved(base) {
		return `"` + base + `"`
	}
	if c := cs.ending(opaque); c != nil {
		return `"` + strings.TrimSuffix(opaque, c.suffix) + `"`
	}
	return tag
}

// decodeConditions returns r with the tags in If-Match and If-None-Match
// decoded, or r itself when none changes. A value that is not a list of
// entity tags, such as "*", is left as is.
func (cs codings) decodeConditions(r *http.Request) *http.Request {
	var header http.Header
	for _, k := range []string{"If-Match", "If-None-Match"} {
		values := r.Header.Values(k)
		var decoded []string
		for i, v := range values {
			tags, ok := appendETags(nil, v)
			if !ok {
				continue
			}
			changed := false
			for j, t := range tags {
				tags[j] = cs.decodeTag(t)
				changed = changed || tags[j] != t
			}
			if changed {
				if decoded == nil {
					decoded = slices.Clone(values)
				}
				decoded[i] = strings.Join(tags, ", ")
			}
		}
		if decoded != nil {
			if header == nil {
				header = r.Header.Clone()
			}
			header[k] = decoded
		}
	}
	if header == nil {
		return r
	}
	r = r.WithContext(r.Context())
	r.Header = header
	return r
}

// tagWriter applies identityTag to a response [Compress] does not hold, such
// as a stream or one the handler wrote nothing of.
type tagWriter struct {
	http.ResponseWriter
	cs   codings
	done bool
}

func (t *tagWriter) WriteHeader(code int) {
	if code >= 200 {
		t.identity()
	}
	t.ResponseWriter.WriteHeader(code)
}

// identity sets the tag as an identity form's, once.
func (t *tagWriter) identity() {
	if t.done {
		return
	}
	t.done = true
	if h := t.Header(); h.Get("ETag") != "" {
		h.Set("ETag", t.cs.identityTag(h.Get("ETag")))
	}
}

// Write sets the tag if no status has, and writes b. It writes no status
// itself: the buffer above always has, and after a 101 a 200 here would be a
// superfluous WriteHeader.
func (t *tagWriter) Write(b []byte) (int, error) {
	t.identity()
	return t.ResponseWriter.Write(b)
}

func (t *tagWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }

func compressible(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	switch {
	case strings.HasPrefix(mt, "text/"),
		strings.HasSuffix(mt, "+json"), strings.HasSuffix(mt, "+xml"),
		mt == "application/json", mt == "application/xml",
		mt == "application/javascript", mt == "application/x-ndjson":
		return true
	}
	return false
}
