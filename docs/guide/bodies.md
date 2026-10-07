# Form, multipart, and raw bodies

## Form and multipart bodies

```go
type Applicant struct {
	Name  string    `form:"name" schema:"minLength=1"`
	Age   *int      `form:"age"`   // optional
	Tags  *[]string `form:"tag"`   // every value of tag
	Agree bool      `form:"agree"`
}
type ApplyIn struct {
	Body Applicant `body:"form"` // application/x-www-form-urlencoded
}

type Upload struct {
	Title  string       `form:"title"`
	Avatar geta.File    `form:"avatar"` // required file; *geta.File optional
	Extra  *[]geta.File `form:"extra" schema:"maxItems=2"`
}
type UploadIn struct {
	Path
	Body Upload `body:"multipart"` // multipart/form-data
}
```

- Form fields bind as query parameters do (string, bool, numbers, text types, and slices of them, with the same `schema` keywords). A violation is a 400 at `$.name`. The form is closed: an unknown field gets 400 (`additionalProperties: false`). An optional (pointer) form body may be absent.
- geta and getavet refuse an untagged form field, two fields with one name, a map field, a schema tag on the body field or on a file, a body field that is not a struct, and a second body field.
- `geta.File` goes only in `body:"multipart"` structs. `File` is required, `*File` is optional, and `[]File` (at least one) or `*[]File` holds several, bounded by `maxItems` or `MaxItems`. Its methods are `f.Filename()`, `f.ContentType()` (as sent, unchecked), `f.Size()`, and `f.Open()`. It is documented as a binary string (`contentMediaType: application/octet-stream`).
- Files are held in memory up to `Limits.MaxMultipartMemory` (1 MiB) and in temporary files beyond that. They are removed after the handler returns and the output is written, or at once on a refusal. Do not keep a `File` past that.
- A body past `MaxBodyBytes` gets 413. A multipart body that ends before its close delimiter (also after a delimiter line or inside part headers) gets 400 at `$`.
- In getatest, call `c.Form(method, path, url.Values)` or `c.Multipart(method, path, values, getatest.FilePart{...})`.

## Raw bodies: the bytes of one media type

```go
type ImportIn struct {
	Body []byte `body:"text/csv" doc:"rows to import"` // *[]byte optional; io.Reader to read it yourself
}
type Export struct {
	Disposition string    `header:"Content-Disposition"`
	Body        io.Reader `body:"application/pdf"` // or []byte
}
```

- The tag is one media type, written as `mime.FormatMediaType` writes it (`text/csv; charset=utf-8`, not `Text/CSV`, and no range). An operation has one media type, with no negotiation.
- On input, the type and subtype match in any case, and parameters (`charset`) are not compared. An empty body gets 400 for `[]byte` or `io.Reader`, and is nil for `*[]byte`. geta checks the Content-Type (415, naming the type in `Accept`), the content coding (415), and the size (413), but never the bytes. An `io.Reader` read past the limit returns `*http.MaxBytesError`, and returning that error answers 413.
- On output, geta sends the declared Content-Type, `nosniff`, and the envelope headers. A `[]byte` always gets Content-Length. An `io.Reader` gets Content-Length if it ends within `MaxResponseBuffer`, and streams otherwise. Readers that are `io.Closer`s are closed on every path, an undeclared status or a failing header included. A nil reader, a reader that fails before anything is sent, or a failing envelope header gets 500 without the envelope headers. A reader that fails after the response started aborts the connection. Behind Gzip or ETag, text raw bodies are coded, tagged, and revalidated.
- The request and the response document the media type without a schema (`"text/csv": {}`). The 415's `Accept` names it, and the 413 states the body limit.
- `geta.New` and getavet refuse a media range, JSON, a form or multipart media type on input, JSON or `text/event-stream` on output (use `*geta.Stream`), a type not written as `mime.FormatMediaType` writes it, a field that is not `[]byte`, `*[]byte` (input only), or `io.Reader`, a pointer output body, a schema tag, and a raw output body on a 204.
- In getatest, call `c.Content(method, path, contentType, body)`.

## Content-Type and Content-Encoding of a request body

- Accepted: each body takes one media type, its parameters aside (`application/json; charset=utf-8` is `application/json`), and the document's content key is it. `body:"json"` takes `application/json`, and on PATCH `application/merge-patch+json` (a PATCH body is a patch document, which `application/json` does not define, RFC 5789 §2; a merge patch's absent, null, and value members are what `omitzero` and `geta.Nullable` read, RFC 7396). Another JSON media type is another format: a `mediatype` tag beside `body:"json"` names the one the body takes instead (``Body Doc `body:"json" mediatype:"application/vnd.api+json"` ``, or `mediatype:"application/json"` on a PATCH); it is `application/json` or `application/*+json`, canonical, without parameters, and `geta.New` and getavet refuse anything else or the tag on another field. `body:"form"` takes `application/x-www-form-urlencoded`; `body:"multipart"` takes `multipart/form-data`; a raw body its one media type.
- Other or no Content-Type: 415, `Accept` naming the accepted type (plus `Accept-Patch` on PATCH, `Accept-Query` on QUERY), no `Accept-Encoding`.
- Any `Content-Encoding` but `identity` (one or several lines; elements trimmed of spaces and tabs only): 415, `Accept-Encoding: identity`, no `Accept`. geta decodes no request coding. To accept one, a root middleware decodes it, replaces `r.Body`, and deletes `Content-Encoding`; `MaxBodyBytes` then bounds decoded bytes.
- Content to an operation reading no body (any method: POST without body field, GET, HEAD, DELETE, geta's OPTIONS): 415 "the operation takes no request content", alone, no `Accept`/`Accept-Encoding`, before the handler.
- No content: empty body, `Content-Length: 0`, or unknown length ending before the first byte.
- Both 415s and their headers are documented on every operation they apply to.
- Order: coding, then media type, judged before reading past the first byte. Untaken content: 415 at any size. Taken content past `MaxBodyBytes`: 413.
- `Content-Length` above zero: judged before reading (coding 415, media type 415, then 413); `Expect: 100-continue` gets that answer, not `100 Continue`. Unknown length (chunked, HTTP/2 without `Content-Length`): judged on the first byte. Raw `io.Reader`: 413 only when the handler reads past the limit. A body a root middleware replaced is read, not judged by headers.

### Connections after a refusal

- geta's own refusal (any 400, 408, 413, 415, or 500 of taking the request in) before the body is read whole: immediate answer with `Connection: close` on HTTP/1.x; geta stops reading the connection. HTTP/2: only the stream resets.
- Refusal after the whole body, or of a request without content: connection kept.
- Other answers leaving the body unread (gate 401/403, rate limiter 429, `ConcurrencyLimit` 503, CORS preflight, handler's own, 404/405/redirect): no `Connection: close`; net/http reads the rest away (up to 256 KiB) before the header and after the handler.
- Behind a `Timeout`, that reading is bounded by the body's window (Timeout), from the first read, else from the answer; a middleware before the `Timeout` counts too. Rest in time: connection kept; else closed. A request reaching no operation uses the root scope's `Timeout`s. The next request inherits no deadline.
- Without `Timeout` nothing is bounded: geta sets no `ReadTimeout`; a stalled body after such an answer holds the connection.

