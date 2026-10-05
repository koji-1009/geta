package geta

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
	"uuid"
)

// Problem is the RFC 9457 body of every failure geta writes.
type Problem struct {
	// Type identifies the kind of problem; about:blank unless the failure
	// row gave one ([Failure.Type]).
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
	// Instance identifies this occurrence. A 500 carries a urn:uuid that
	// its log line carries too.
	Instance string      `json:"instance,omitempty"`
	Errors   []Violation `json:"errors,omitempty"`
	// Omitted counts the violations found but not listed in Errors: past
	// the first 50, or past 16 KiB of listed violations. Zero is left out.
	Omitted int `json:"omitted,omitzero"`
}

// ProblemContentType is the media type of a [Problem].
const ProblemContentType = "application/problem+json"

// WriteProblem answers status with an RFC 9457 body. An empty detail sends
// only the status's standard text. A byte of detail that is not UTF-8 is
// written as U+FFFD. Middleware that answers on its own, such as an
// authorization check, uses it so every failure has one shape.
//
// If the body write fails, WriteProblem panics with http.ErrAbortHandler to
// abort the connection, without logging.
func WriteProblem(w http.ResponseWriter, status int, detail string) {
	if err := sendProblem(w, plainProblem(status, detail, nil)); err != nil {
		panic(http.ErrAbortHandler)
	}
}

// writeProblem is WriteProblem for geta's own answers. It logs refusals
// (logRefusal).
func writeProblem(w http.ResponseWriter, r *http.Request, status int, detail string, errs []Violation) {
	writeRefusal(w, r, status, detail, detail, errs, 0)
}

// writeRefusal is writeProblem that logs logged in place of a detail that
// holds the request's own values. omitted counts violations found past errs;
// listViolations cuts errs further.
func writeRefusal(w http.ResponseWriter, r *http.Request, status int, detail, logged string, errs []Violation, omitted int) {
	errs, omitted = listViolations(errs, omitted)
	logRefusal(r, status, logged, errs, omitted)
	p := plainProblem(status, detail, errs)
	p.Omitted = omitted
	if err := sendProblem(w, p); err != nil {
		abortUntaken(r, err)
	}
}

// maxViolationBytes bounds the listed violations of a problem, as JSON.
const maxViolationBytes = 16 << 10

// listViolations returns the violations a problem lists and the count it
// leaves out: at most maxViolations, each path cut by capPath, and no more
// than fit, as the errors array's JSON, in maxViolationBytes, but always the
// first, so that a refusal names a cause (its path is bounded, and its
// message quotes at most one value of the request, cut by clip). omitted
// counts those already left out.
func listViolations(errs []Violation, omitted int) ([]Violation, int) {
	size := len("[")
	for i := range errs {
		if i == maxViolations {
			return errs[:i], omitted + len(errs) - i
		}
		errs[i].Path = capPath(errs[i].Path)
		v := &errs[i]
		// {"in":"","path":"","message":""}, and a comma or the ].
		size += 33 + jsonStringLen(v.In) + jsonStringLen(v.Path) + jsonStringLen(v.Message)
		if i > 0 && size > maxViolationBytes {
			return errs[:i], omitted + len(errs) - i
		}
	}
	return errs, omitted
}

// jsonStringLen returns the bytes of s as sendProblem writes it in a string,
// quotes aside, exactly: with EscapeForHTML and AllowInvalidUTF8 alone, '"',
// '\\', \b, \f, \n, \r, and \t are two bytes, the other control bytes and
// '<', '>', and '&' are six (\u00XX), each byte of invalid UTF-8 is U+FFFD,
// and every other rune, U+2028 and U+2029 included (EscapeForJS is off),
// is as written.
func jsonStringLen(s string) int {
	n := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch {
			case c == '"' || c == '\\' || c == '\b' || c == '\f' || c == '\n' || c == '\r' || c == '\t':
				n += 2
			case c < 0x20 || c == '<' || c == '>' || c == '&':
				n += 6
			default:
				n++
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			n += utf8.RuneLen(utf8.RuneError)
		} else {
			n += size
		}
		i += size
	}
	return n
}

// logRefusal logs at Debug a 400, 408, 412, 413, 415, 426, or 428 geta
// answers itself. The line carries the method, route template, status,
// detail, the count and locations of violations, and the count omitted;
// never the raw path, a value, or a violation's text, which may quote the
// request.
func logRefusal(r *http.Request, status int, detail string, errs []Violation, omitted int) {
	switch status {
	case http.StatusBadRequest, http.StatusRequestTimeout, http.StatusPreconditionFailed,
		http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType,
		http.StatusUpgradeRequired, http.StatusPreconditionRequired:
	default:
		return
	}
	ctx := r.Context()
	log := loggerFrom(ctx)
	if !log.Enabled(ctx, slog.LevelDebug) {
		return
	}
	method := r.Method
	if rc := requestFrom(ctx); rc != nil {
		method = rc.method
	}
	attrs := make([]slog.Attr, 0, 7)
	attrs = append(attrs, slog.String("method", method), routeAttr(ctx), slog.Int("status", status))
	if detail != "" {
		attrs = append(attrs, slog.String("detail", detail))
	}
	if len(errs) > 0 {
		var in []string
		for _, v := range errs {
			if !slices.Contains(in, v.In) {
				in = append(in, v.In)
			}
		}
		attrs = append(attrs, slog.Int("violations", len(errs)), slog.String("in", strings.Join(in, ",")))
	}
	if omitted > 0 {
		attrs = append(attrs, slog.Int("omitted", omitted))
	}
	log.LogAttrs(ctx, slog.LevelDebug, "geta: request refused", attrs...)
}

func plainProblem(status int, detail string, errs []Violation) *Problem {
	return &Problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Detail: detail, Errors: errs}
}

// sendProblem writes p, HTML-safe, and returns the body write's error.
func sendProblem(w http.ResponseWriter, p *Problem) error {
	b := bodyBuffers.Get().(*bytes.Buffer)
	defer releaseBuffer(b)
	_ = json.MarshalWrite(b, p, problemOptions) // cannot fail (problemOptions)
	// As Header.Set would, with the values in one allocation.
	h := w.Header()
	v := make([]string, 3)
	v[0], v[1], v[2] = ProblemContentType, strconv.Itoa(b.Len()), "nosniff"
	h["Content-Type"], h["Content-Length"], h["X-Content-Type-Options"] = v[0:1:1], v[1:2:2], v[2:3:3]
	w.WriteHeader(p.Status)
	_, err := w.Write(b.Bytes())
	return err
}

// abortUntaken logs at Info a response write that failed, and aborts the
// connection so the client cannot take a partial body for the whole.
func abortUntaken(r *http.Request, err error) {
	method := r.Method
	if rc := requestFrom(r.Context()); rc != nil {
		method = rc.method
	}
	loggerFrom(r.Context()).LogAttrs(r.Context(), slog.LevelInfo, "geta: the response could not be written",
		slog.String("method", method), routeAttr(r.Context()), slog.Any("error", err))
	panic(http.ErrAbortHandler)
}

// routeAttr returns the "route" log attribute (routeName). A log line never
// records the raw path.
func routeAttr(ctx context.Context) slog.Attr {
	return slog.String("route", routeName(ctx))
}

// routeName returns the matched operation's template, or UnmatchedRoute.
func routeName(ctx context.Context) string {
	if m, ok := Matched(ctx); ok && m.Operation {
		return m.Template
	}
	return UnmatchedRoute
}

// writeDefect records a defect under a fresh occurrence id and answers 500
// with the same id as the problem's instance. Nothing of err reaches the
// response.
func writeDefect(w http.ResponseWriter, r *http.Request, log *slog.Logger, msg string, err any, attrs ...slog.Attr) {
	instance := "urn:uuid:" + uuid.New().String()
	attrs = append(attrs, slog.String("instance", instance), slog.Any("error", err))
	log.LogAttrs(r.Context(), slog.LevelError, msg, attrs...)
	if err := sendProblem(w, &Problem{
		Type:     "about:blank",
		Title:    http.StatusText(http.StatusInternalServerError),
		Status:   http.StatusInternalServerError,
		Instance: instance,
	}); err != nil {
		abortUntaken(r, err)
	}
}

// problemSchema documents [Problem].
func problemSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"type", "title", "status"},
		"properties": map[string]any{
			"type":     map[string]any{"type": "string", "format": "uri-reference"},
			"title":    map[string]any{"type": "string"},
			"status":   map[string]any{"type": "integer"},
			"detail":   map[string]any{"type": "string"},
			"instance": map[string]any{"type": "string", "format": "uri-reference"},
			"errors": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":     "object",
					"required": []string{"in", "path", "message"},
					"properties": map[string]any{
						"in":      map[string]any{"type": "string"},
						"path":    map[string]any{"type": "string"},
						"message": map[string]any{"type": "string"},
					},
				},
			},
			"omitted": map[string]any{"type": "integer", "minimum": 1},
		},
	}
}
