package geta

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"sync"
)

// marshalOptions write every response body: deterministic (map keys sorted)
// and safe to embed in HTML. With the codec's rules (a pointer field is
// omitzero, an element is never a pointer), a nil slice is [], a nil map is
// {}, and a nil pointer member is absent, never null.
var marshalOptions = json.JoinOptions(json.Deterministic(true), jsontext.EscapeForHTML(true))

// problemOptions write a Problem: invalid UTF-8 in a string becomes U+FFFD,
// so writing a Problem never fails.
var problemOptions = json.JoinOptions(marshalOptions, jsontext.AllowInvalidUTF8(true))

// marshal writes v with encoding/json/v2. opts are marshalOptions plus the
// app's sealed types (registry.encOpts).
func marshal(v any, opts json.Options) ([]byte, error) {
	return json.Marshal(v, opts)
}

// bodyBuffers pools the buffers problem bodies are written into; return them
// with releaseBuffer.
var bodyBuffers = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func releaseBuffer(b *bytes.Buffer) {
	if b.Cap() > maxPooledBody {
		return
	}
	b.Reset()
	bodyBuffers.Put(b)
}
