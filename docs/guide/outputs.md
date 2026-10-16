# Output envelopes

## Output envelopes

An output struct with any field tagged `header`, `cookie`, `body`, or `status` is an envelope:

```go
type Created struct {
	Location string      `header:"Location"`
	Session  *http.Cookie `cookie:"sid"`  // the tag names it; nil sends nothing; Cookie.Valid failing is a 500
	Body     model.User  `body:"json"`    // the body (envelope fields are not JSON members: no omitzero)
}
```

- Body: `body:"json"` or a raw media type (`body:"application/pdf"`, `[]byte` or `io.Reader`; [raw bodies](bodies.md#raw-bodies-the-bytes-of-one-media-type)), or several raw media types the handler chooses among (`body:"image/png|application/pdf"`, below). Never a pointer: one status, one documented content; answer bodiless under another status instead.
- No body field: headers and status only.
- Header names must be HTTP tokens. `Content-Length`, `X-Content-Type-Options`, `Set-Cookie` cannot be set (use a cookie field); `Content-Type` only to choose among a raw body's listed media types (below). Output headers, cookies, and body take no `schema` tag.
- Cookie: `*http.Cookie` written by net/http; one `Cookie.Valid` refuses is a 500 without Set-Cookie.
- A failed envelope leaves none of its headers or cookies on the 500, only middleware's.
- Header text: bool `true`/`false`, integer decimal, float as its JSON (shortest round-trip form; `1.1` for float32; `1e+21` past 1e21). The schema states what a header field value carries. 500: a value not carriable as written (control character but tab, whitespace at either end), float NaN or infinity.
- Untagged embedded struct: its fields are the envelope's, written and documented by their own tags, exported or not; tags only there still make an envelope. Refused: untagged embedded pointer, embedded text type, `schema` tag on an embedded struct.

### Several success statuses

- ``Status int `status:"200|201"` `` lists statuses the handler may choose: e.g. `geta.Op(http.StatusOK, h.Put, doc)`, PUT setting `out.Status = http.StatusCreated` on create; `status:"202|204"` on a bodiless DELETE envelope.
- The operation's status must be listed; zero value sends it. Unlisted value: 500 defect.
- The document lists every status with the output's schema and headers (pointer header for one only some statuses send).
- Refused by `geta.New` and getavet: non-int field; status not 2xx/3xx, or 304, 305, 306; status twice; second status field; bodiless status (204, 205) beside a body.

### Several media types

```go
type Download struct {
	ContentType string    `header:"Content-Type"` // the stored file's type, one of the body's
	Disposition string    `header:"Content-Disposition"`
	Body        io.Reader `body:"image/png|application/pdf"`
}

func (h Handler) Get(ctx context.Context, in *FileIn) (*Download, error) {
	f, err := h.Files.Open(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return &Download{ContentType: f.Type, Disposition: "attachment", Body: f}, nil
}
```

- A raw body tag lists the media types a response may carry, as a status tag lists statuses; a string field tagged `header:"Content-Type"` holds the one the handler chooses per response, and geta sends it as `Content-Type` (with `nosniff`, as for one type).
- The document lists every listed type as a content key of each success status (`"image/png": {}`, `"application/pdf": {}`), and states no `Content-Type` header (OpenAPI ignores a response header of that name; the content keys say what it may be).
- Runtime: a value the tag does not list, the empty string and another spelling (`Image/PNG`) included, is a 500 defect, without the envelope's headers, the reader closed; a precondition never answers 304 in its place. Compare the stored type with the listed ones before returning, and return your own error for a file of an unlisted type.
- `Accept` is not read: the choice follows the resource (the type the file was stored as), not the request, so there is no negotiation, no 406, and no `Vary: Accept` (RFC 9110 §12.4.1: an origin server may disregard `Accept`, "treating the response as if it is not subject to content negotiation").
- Refused by `geta.New` and getavet: several listed types without a `Content-Type` field; a `Content-Type` field beside a JSON body, a body of one type, or no body (geta writes that `Content-Type` itself), in a failure row's envelope too; a `Content-Type` field that is not a `string` (a pointer, a number, a text type) or takes a `schema` or `doc` tag; a type listed twice; a listed type a raw output body cannot be (a range, JSON, `text/event-stream`, a type not written as `mime.FormatMediaType` writes it). `|` separates types, so a listed type cannot hold one.
- An input's raw body takes one media type.

### Redirects

- Status (own or listed) 301, 302, 303, 307, 308 needs an envelope with a `Location` header field (any case; string or text type): `geta.Op(http.StatusSeeOther, h.Post, doc)` with ``Location string `header:"Location"` ``. 300 needs none.
- Refused by `geta.New` and getavet: `geta.OpNoBody`, plain output, envelope without the field, number or bool `Location`.
- Runtime: nil or empty `Location` on a redirect is a 500 defect, without the envelope's headers. Beside a status sending none (`status:"200|303"`), declare `*string`, nil for the 200; an empty `string` is sent as an empty header.

