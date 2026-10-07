# Routes, operations, and input

## A route (canonical form)

```go
package user

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
	"example.com/app/app"
	"example.com/app/model"
	"example.com/app/store"
)

// Route is /users/{id}.
func Route(env *app.Env) geta.Route {
	h := Handler{Users: env.Users}
	notFound := geta.On(store.ErrNotFound, http.StatusNotFound, "user not found")
	return geta.Route{
		Get: geta.Op(http.StatusOK, h.Get, geta.Doc{
			Summary:  "Fetch a user",
			Failures: []geta.Failure{notFound},
		}),
		Delete: geta.OpNoBody(http.StatusNoContent, h.Delete, geta.Doc{
			Summary:  "Delete a user",
			Failures: []geta.Failure{notFound},
		}),
	}
}

// Store is only what this route uses.
type Store interface {
	Find(ctx context.Context, id string) (*model.User, error)
	Delete(ctx context.Context, id string) error
}

type Handler struct{ Users Store }

type Path struct {
	ID string `path:"id" schema:"maxLength=64"`
}

type GetIn struct{ Path }
type DeleteIn struct{ Path }

func (h Handler) Get(ctx context.Context, in *GetIn) (*model.User, error) {
	return h.Users.Find(ctx, in.ID)
}

func (h Handler) Delete(ctx context.Context, in *DeleteIn) error {
	return h.Users.Delete(ctx, in.ID)
}
```

### Operations

- `geta.Route` has one field per method: `Get`, `Post`, `Put`, `Patch`, `Delete`, and `Query`. HEAD comes with GET, and OPTIONS comes from geta.
- `geta.Op(status, handler, doc)` takes a `func(context.Context, *In) (*Out, error)`. `geta.OpNoBody(status, handler, doc)` takes a `func(context.Context, *In) error`. The compiler checks the shape on that line.
- A success status is required. It is a 2xx or 3xx status other than 304, 305, and 306. A 204 needs `OpNoBody` or an output without a body field. A redirect (301, 302, 303, 307, 308) needs an envelope with a `Location` header field. Several success statuses need an output status field. [Output envelopes](outputs.md) covers both.
- `Get` and `Delete` take no body. Any body field (JSON, form, multipart, or raw; required or not; embedded too) fails `geta.New` with an error that points to `Query` or `Post`. Use parameters, or serve the operation as `Query` or `Post`. getavet reports it where a `geta.Route` literal builds the operation in place (`Get: geta.Op(...)`).
- `Query` serves HTTP QUERY (`geta.MethodQuery`), which is safe and idempotent like GET and carries a JSON, form, or multipart body. It needs OpenAPI 3.2 (`geta.WithOpenAPI(geta.OpenAPI32)` in `options.go`); otherwise `geta.New` refuses it. QUERY is listed in Allow, OPTIONS, `OPTIONS *`, 405 answers, and CORS preflights. A 307 keeps the method and the body. Its 415 adds `Accept-Query`. A matching `If-None-Match` gets 412, and `geta.ETag` does not tag a QUERY response. In getatest, call `c.Query(path, body)`.

### Deprecation

- `Doc{Deprecated: true}` puts `deprecated: true` in the document and sends no headers.
- Dates add headers. `Doc{Deprecated: true, Deprecation: time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC), Sunset: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}` sends `Deprecation: @1782777600` (RFC 9745; the date may be in the future) and `Sunset: Fri, 01 Jan 2027 00:00:00 GMT` (RFC 8594). Either date works alone.
- The headers go on all of the operation's responses: successes, HEAD, 304, problems, middleware answers, streams, and `Serve`'s 101. They never go on a 404, a 405, a clean-path redirect, or OPTIONS. A root rewrite moves them to the served operation. `geta.CORS` exposes them.
- Each response documents them as required, with the value as `const` (not required on an upgrade's 101).
- `geta.New` refuses a date without `Deprecated: true`, a date that is not a whole second or is past the year 9999, a `Sunset` before the `Deprecation`, and an output header, a described row's header, or a middleware `.Header` of the same name on the operation. An `Upgrade.Header` that sets one is a 500 defect.
- To send `Link` with `rel="deprecation"` or `rel="sunset"`, send it yourself from an output or a middleware.

### Input

- `In` is a struct. An operation that reads nothing takes `*struct{}`.
- Each exported `In` field has exactly one tag: `path:"name"`, `query:"name"`, `header:"Name"`, `cookie:"name"`, or a body tag (`body:"json"`, `body:"form"`, `body:"multipart"`, or a media type such as `body:"text/csv"`). An `In` has at most one body field. `header` and `cookie` names are HTTP tokens.
- Untagged embedded structs are flattened, so operations can share a `Path` struct. geta refuses an untagged embedded pointer, an untagged embedded text type (tag it to make it a parameter), and a `schema` tag on an embedded struct.
- Each `{param}` in the URL needs a `path` field in every operation's `In`, and each `path` field must be in the URL.
- The type decides presence. A non-pointer is required, and a pointer is optional. A non-pointer with a schema `default` is optional, and the default is bound when the value is absent. Path parameters cannot be pointers.
- Parameter types are string, bool, the integer types, the unsigned integer types, the float types, and text types (`encoding.TextUnmarshaler` plus `encoding.TextMarshaler` or `encoding.TextAppender`; a type with only one side fails `geta.New`). Format types are text types.
- The text of a parameter or form field is read as its JSON value. Integers must be written as JSON writes them (`007` and `+7` get 400), numbers as JSON numbers, and booleans as `true` or `false`. Strings and text types are taken only if they are UTF-8. A form field name that is not UTF-8 is named with U+FFFD.
- For repeated values, use `*[]string`, or `[]T` when at least one value is required.
- A slice header is a list (style `simple`, RFC 9110 §5.6.1). `X-Tag: a, b`, or two lines `a` and `b`, binds `["a","b"]`. Elements are trimmed, empty ones are ignored, and each is held to the element schema (which states that it holds no comma).
- Header and cookie schemas state what the place carries as an enforced `pattern`, joined to yours in `allOf`. A header carries a header field value: no control character but a tab, and no whitespace at either end. A cookie carries what net/http reads: printable ASCII except `"`, `;`, and `\`. Formats whose every value is carried (date-time, date, time, duration, ipv4, ipv6, uuid) get no pattern. An enum member the place cannot carry fails `geta.New`.

### deepObject query parameters

- A query parameter of a struct type without text or JSON methods is a deepObject. ``Filter *Filter `query:"filter"` `` binds `?filter[min]=1&filter[status]=open`. The fields of `Filter` are tagged like `form:"min"` and are bound as form fields are (a scalar or text type; no slices or files).
- The document states `style: deepObject`, `explode: true`, an object schema of the members, and `additionalProperties: false`.
- An unknown key (`filter[x]`, `filter[min][y]`) gets 400. Violations are named like `filter[min]`. With no `filter[...]` key, the parameter is absent (nil for a pointer, 400 when required).
- The struct's schema tag takes only `deprecated=true`. A member without a form tag, or an array member, fails `geta.New`. Struct parameters are allowed only in the query.
- Another query parameter named as one of its keys (`filter[min]` beside `filter`) fails `geta.New` and getavet.

