package geta

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net/http"
	"reflect"
	"runtime/debug"
	"strings"
	"time"
)

// Stream is a server-sent event stream. An operation returns a *Stream[T]
// as its output; each value its source yields is one event whose data is the
// value's JSON. Every middleware in front of the handler, the gate included,
// has run before the stream opens, and the document lists the stream with
// T's schema. A HEAD request gets the headers only.
//
// A T with an EventName() string method names each event; one with an
// EventID() string method gives it an id. Neither may contain CR, LF, or NUL.
//
// The source runs until it ends, the client goes away, the server shuts
// down, or a bound passes. A source that waits must watch the handler's
// context, which ends when the stream does. Once the stream opens, the
// [Timeout] deadline is lifted. A panic in the source is logged and aborts
// the connection.
type Stream[T any] struct {
	// Events is the source. A nil source is a defect.
	Events iter.Seq[T]
	// KeepAlive, when set, sends a comment after this much silence, so
	// proxies keep the connection open. It does not count as activity for
	// MaxIdle.
	KeepAlive time.Duration
	// MaxIdle, when set, ends the stream after this long without an event.
	MaxIdle time.Duration
	// MaxLifetime, when set, ends the stream this long after it opened.
	MaxLifetime time.Duration
}

// EventNamer names an event of a stream.
type EventNamer interface{ EventName() string }

// EventIdentifier gives an event of a stream its id.
type EventIdentifier interface{ EventID() string }

func (*Stream[T]) plan(r *registry) (specialPlan, error) {
	c, err := r.codecFor(reflect.TypeFor[T]())
	if err != nil {
		return nil, fmt.Errorf("stream events: %w", err)
	}
	return &streamPlan[T]{c: c, opts: r.encOpts, unions: markUnions(c)}, nil
}

type streamPlan[T any] struct {
	c      *codec
	opts   json.Options // how events are written (registry.encOpts)
	unions bool         // an event can hold a sealed type: check none is nil
}

func (p *streamPlan[T]) status() int { return http.StatusOK }

// eventCodec returns the event type's codec, for [App.Conforms].
func (p *streamPlan[T]) eventCodec() *codec { return p.c }

func (p *streamPlan[T]) document(r map[string]any, f oasFeatures) {
	r["description"] = "An event stream; each event's data is one JSON value"
	if !f.itemSchema {
		r["content"] = map[string]any{"text/event-stream": map[string]any{"schema": p.c.use().document(nil)}}
		return
	}
	// OpenAPI 3.2 §4.14.4: each item is a parsed event. event and id are
	// required when T implements the interface, optional when T is an
	// interface.
	props := map[string]any{"data": map[string]any{
		"type":             "string",
		"contentMediaType": "application/json",
		"contentSchema":    p.c.use().document(nil),
	}}
	required := []string{"data"}
	t := reflect.TypeFor[T]()
	for _, f := range []struct {
		name string
		i    reflect.Type
	}{{"event", reflect.TypeFor[EventNamer]()}, {"id", reflect.TypeFor[EventIdentifier]()}} {
		switch {
		case t.Implements(f.i):
			props[f.name] = map[string]any{"type": "string"}
			required = append(required, f.name)
		case t.Kind() == reflect.Interface:
			props[f.name] = map[string]any{"type": "string"}
		}
	}
	r["content"] = map[string]any{"text/event-stream": map[string]any{"itemSchema": map[string]any{
		"type":       "object",
		"required":   required,
		"properties": props,
	}}}
}

func (p *streamPlan[T]) write(w http.ResponseWriter, r *http.Request, a *App, op *compiledOp, out any) {
	s := out.(*Stream[T])
	if s.Events == nil {
		op.writeDefect(w, r, "geta: stream with a nil source", errors.New("Stream.Events is nil"))
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// A HEAD gets the headers only; the source does not run.
	if r.Method == http.MethodHead {
		return
	}
	// An open stream holds no concurrency slot and no deadline. The slot is
	// freed before the headers reach the client, which may send its next
	// request as soon as it reads them.
	releaseSlot(r.Context())
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		op.defect(r, "geta: the response cannot stream", err)
		return
	}
	liftDeadline(r.Context())

	// A panic on the source's goroutine would end the process: recover and
	// log it there, then abort the connection here (panicked is published by
	// done).
	events := make(chan T)
	done := make(chan struct{})
	var panicked bool
	go func() {
		defer close(done)
		defer func() {
			if p := recover(); p != nil {
				panicked = true
				a.log.LogAttrs(r.Context(), slog.LevelError, "geta: panic in a stream source",
					slog.String("method", op.method), slog.String("route", op.path),
					slog.Any("panic", p), slog.String("stack", string(debug.Stack())))
			}
		}()
		for v := range s.Events {
			select {
			case events <- v:
			case <-ctx.Done():
				return
			}
		}
	}()

	timer := func(d time.Duration) (<-chan time.Time, *time.Timer) {
		if d <= 0 {
			return nil, nil
		}
		t := time.NewTimer(d)
		return t.C, t
	}
	idleC, idle := timer(s.MaxIdle)
	lifeC, life := timer(s.MaxLifetime)
	keepC, keep := timer(s.KeepAlive)
	defer func() {
		for _, t := range []*time.Timer{idle, life, keep} {
			if t != nil {
				t.Stop()
			}
		}
	}()
	drain := draining(r.Context())
	var buf []byte
	for {
		select {
		case v := <-events:
			b, err := p.event(buf[:0], v)
			if err != nil {
				op.defect(r, "geta: stream event could not be written", err)
				return
			}
			buf = b
			if _, err := w.Write(b); err != nil {
				return
			}
			if rc.Flush() != nil {
				return
			}
			if idle != nil {
				idle.Reset(s.MaxIdle)
			}
			if keep != nil {
				keep.Reset(s.KeepAlive)
			}
		case <-keepC:
			if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil || rc.Flush() != nil {
				return
			}
			keep.Reset(s.KeepAlive)
		case <-idleC:
			a.log.LogAttrs(r.Context(), slog.LevelDebug, "geta: stream idle", slog.String("route", op.path))
			return
		case <-lifeC:
			a.log.LogAttrs(r.Context(), slog.LevelDebug, "geta: stream lifetime reached", slog.String("route", op.path))
			return
		case <-drain:
			return
		case <-ctx.Done():
			return
		case <-done:
			if panicked {
				panic(http.ErrAbortHandler)
			}
			return
		}
	}
}

// event renders one event: event, id, then one data line.
func (p *streamPlan[T]) event(b []byte, v T) ([]byte, error) {
	if n, ok := any(v).(EventNamer); ok {
		name := n.EventName()
		if strings.ContainsAny(name, "\r\n\x00") {
			return nil, fmt.Errorf("event name %q contains CR, LF, or NUL", name)
		}
		b = append(append(append(b, "event: "...), name...), '\n')
	}
	if n, ok := any(v).(EventIdentifier); ok {
		id := n.EventID()
		if strings.ContainsAny(id, "\r\n\x00") {
			return nil, fmt.Errorf("event id %q contains CR, LF, or NUL", id)
		}
		b = append(append(append(b, "id: "...), id...), '\n')
	}
	if p.unions {
		if err := nilUnion(p.c, reflect.ValueOf(&v).Elem(), "$"); err != nil {
			return nil, err
		}
	}
	// Compact v2 output has no raw newline, so one data line holds it.
	data, err := marshal(&v, p.opts)
	if err != nil {
		return nil, err
	}
	b = append(append(b, "data: "...), data...)
	return append(b, '\n', '\n'), nil
}
