# Conditional requests

## Conditional requests (304, 412, 428)

Embed `geta.Conditional` in an input to bind `If-Match`, `If-None-Match`, `If-Modified-Since`, `If-Unmodified-Since` (optional, documented header parameters). After its own checks, before acting, the handler calls `in.Check(etag, modified)` and returns the error as is: `etag` the current entity tag (`"v1"`, `W/"v1"`, "" for none), `modified` the last modification (zero for none). `in.CheckAbsent()`: target without representation yet (a creating PUT); `If-Match` fails, `If-None-Match: *` holds.

```go
type PutIn struct {
	geta.Conditional
	Path
	Body model.User `body:"json"`
}

type Tagged struct {
	ETag string     `header:"ETag"`
	User model.User `body:"json"`
}

func (h Handler) Put(ctx context.Context, in *PutIn) (*Tagged, error) {
	// Check against the stored user inside the store's write, so no other write lands between.
	u, err := h.Users.Replace(ctx, in.Body, func(current model.User) error {
		return in.Check(ETag(current), time.Time{})
	})
	if err != nil {
		return nil, err
	}
	return &Tagged{ETag: ETag(*u), User: *u}, nil
}
```

- Order (RFC 9110 §13.2.2): If-Match (strong; no match 412), else If-Unmodified-Since (412); then If-None-Match (weak; match 304 on GET/HEAD, else 412), else on GET/HEAD If-Modified-Since (304).
- A 304 carries the ETag (or Last-Modified) a 200 would. The document states both on the 304, optional.
- 400: malformed tag list; list without a tag (empty field, one or several lines, commas alone). `*` alone but for spaces and tabs (beside a no-break space: 400). Several lines are one list.
- Non-HTTP-date: ignored. Only RFC 9110 §5.6.7 forms are read (IMF-fixdate, RFC 850, asctime; two-digit hour, minute, second; no fraction; names in their own case). 23:59:60 reads as 23:59:59.
- The four fields are not string parameters: bound whatever their bytes and length. An obs-text (non-UTF-8) entity tag matches as any other; non-UTF-8 date ignored; long list read whole; no documented `maxLength`.
- Error: `*geta.PreconditionError` (`Status()` 304, 400, 412, 428), answered on operations whose input embeds `Conditional`, which document 412 (304 too on GET). Returned elsewhere, or an `etag` not one entity tag: logged 500 with an `instance`; no failure row answers them, catch-all `geta.OnAs[error]` included.
- `geta.RequireConditional` instead of `Conditional`: 428 without a precondition (RFC 6585), documented there.
- Binding the four headers again in the same input fails `geta.New`.
- `geta.ETag` reads `If-None-Match` as `Check` does.
- Every status `Check` answers is documented.
- Worked case: `examples/register` `/users/{id}`: GET and PUT answer `user.Tagged` with the user's `ETag`; stale `If-Match` is a 412 writing nothing.

