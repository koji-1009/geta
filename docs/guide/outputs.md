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

- Body: `body:"json"` or a raw media type (`body:"application/pdf"`, `[]byte` or `io.Reader`; [raw bodies](bodies.md#raw-bodies-the-bytes-of-one-media-type)). Never a pointer: one status, one documented content; answer bodiless under another status instead.
- No body field: headers and status only.
- Header names must be HTTP tokens. `Content-Type`, `Content-Length`, `X-Content-Type-Options`, `Set-Cookie` cannot be set (use a cookie field). Output headers, cookies, and body take no `schema` tag.
- Cookie: `*http.Cookie` written by net/http; one `Cookie.Valid` refuses is a 500 without Set-Cookie.
- A failed envelope leaves none of its headers or cookies on the 500, only middleware's.
- Header text: bool `true`/`false`, integer decimal, float as its JSON (shortest round-trip form; `1.1` for float32; `1e+21` past 1e21). The schema states what a header field value carries. 500: a value not carriable as written (control character but tab, whitespace at either end), float NaN or infinity.
- Untagged embedded struct: its fields are the envelope's, written and documented by their own tags, exported or not; tags only there still make an envelope. Refused: untagged embedded pointer, embedded text type, `schema` tag on an embedded struct.

### Several success statuses

- ``Status int `status:"200|201"` `` lists statuses the handler may choose: e.g. `geta.Op(http.StatusOK, h.Put, doc)`, PUT setting `out.Status = http.StatusCreated` on create; `status:"202|204"` on a bodiless DELETE envelope.
- The operation's status must be listed; zero value sends it. Unlisted value: 500 defect.
- The document lists every status with the output's schema and headers (pointer header for one only some statuses send).
- Refused by `geta.New` and getavet: non-int field; status not 2xx/3xx, or 304, 305, 306; status twice; second status field; bodiless status (204, 205) beside a body.

### Redirects

- Status (own or listed) 301, 302, 303, 307, 308 needs an envelope with a `Location` header field (any case; string or text type): `geta.Op(http.StatusSeeOther, h.Post, doc)` with ``Location string `header:"Location"` ``. 300 needs none.
- Refused by `geta.New` and getavet: `geta.OpNoBody`, plain output, envelope without the field, number or bool `Location`.
- Runtime: nil or empty `Location` on a redirect is a 500 defect, without the envelope's headers. Beside a status sending none (`status:"200|303"`), declare `*string`, nil for the 200; an empty `string` is sent as an empty header.

