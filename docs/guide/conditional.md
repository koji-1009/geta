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
- `geta.ETag` reads `If-None-Match` as `Check` does; on a GET without `Conditional`, every precondition (below).
- Every status `Check` answers is documented.
- Worked case: `examples/register` `/users/{id}`: GET and PUT answer `user.Tagged` with the user's `ETag`; stale `If-Match` is a 412 writing nothing, on PUT and DELETE alike.

## Operations without `Conditional`

An operation whose input does not embed `Conditional` and that declares no validator (a GET that does, below, is the exception) has geta evaluate the preconditions itself (RFC 9110 §13.1 "MUST evaluate … prior to performing the method"), against a current representation with no entity tag and no modification date, before the handler runs:

- `If-Match` other than `*` (any list, any value that is no list): 412; no listed tag matches a representation with none (§13.1.1). `DELETE` with `If-Match: "x"` is a 412 deleting nothing.
- `If-Match: *`: holds; the handler runs. A missing target is the handler's 404, which takes precedence (§13.2.1).
- `If-None-Match: *`: 412 on any method but GET and HEAD (§13.1.2); 304 on a GET that answers 200 (§15.4.5: a 304 stands for a 200), with no body and no validator. Not evaluated on a GET answering no 200.
- `If-None-Match` list: holds (no tag to match).
- `If-Modified-Since`, `If-Unmodified-Since`: ignored; no modification date (§13.1.3, §13.1.4 "MUST ignore … if the resource does not have a modification date available").
- OPTIONS: ignored (§13.2.1).
- Several lines are one list: `*` twice is a list, not `*`.
- Documented on every such operation: 412, and on a GET answering 200, 304. The fields are not parameters.
- Cost: geta cannot look the target up, so a missing target answers 412 (or 304) rather than 404 to `If-Match: "x"` and `If-None-Match: *`, and an operation that creates its target (PUT) runs on `If-Match: *`. Embed `Conditional` (and `CheckAbsent`) for the full order.

## A GET that declares a validator without `Conditional`

A GET whose input does not embed `Conditional` but whose output has an `ETag` or `Last-Modified` header field, or that `geta.ETag` tags, has a validator: the one it sends. GET is safe (§9.2.1 "essentially read-only"), so its handler runs first, and the preconditions are evaluated against what the response carries, as `Check` would be called with it: "the condition is true if any of the listed tags match the entity tag of the selected representation" (§13.1.1). GET and HEAD alike:

- Order (§13.2.2): `If-Match` (strong; no match 412), else `If-Unmodified-Since` against the `Last-Modified` (412); then `If-None-Match` (weak; match 304), else `If-Modified-Since` (304). `If-Match` with the tag the GET sent holds.
- A 304 carries the `ETag` the 200 would, or else its `Last-Modified` (§15.4.5), no body; it answers only in place of a 200 (§15.4.5 "would have resulted in a 200 (OK) response"): another 2xx from a status field is sent as is, its `If-Match` still evaluated.
- The handler's own answers take precedence: a missing target is its 404, whatever `If-Match` names; a status other than 2xx ignores the preconditions (§13.2.1 "MUST ignore all received preconditions if its response … would have been a status code other than a 2xx").
- `If-Match` or `If-None-Match` that is neither `*` nor a list of entity tags: 400, as `Check` answers.
- An output `ETag` value that is not one entity tag is no entity tag (`If-Match: "x"` is a 412); a `Last-Modified` that is not one HTTP-date is no date.
- On a GET `geta.ETag` tags, the middleware evaluates them, against its tag (or the handler's, which it keeps): the tag is known only once the body is. geta evaluates nothing before the handler there, so nothing is evaluated twice. On a GET whose input embeds `Conditional`, `geta.ETag` reads only `If-None-Match`, as before.
- Documented: 304 where the GET answers 200, stating the `ETag` or `Last-Modified` its output carries; 412; 400.

## Writes beside a GET that declares a validator

A route whose GET declares a validator has one, so its writes evaluate the preconditions against it. `geta.New` refuses a PUT, PATCH, or DELETE on that route whose input embeds neither `Conditional` nor `RequireConditional`: evaluated against no validator, `If-Match` with the tag the GET sent would be a 412 (RFC 9110 §13.1.1) and `If-Unmodified-Since` would be ignored (§13.1.4). Embed one and call `Check` with the current validators, inside the store's write as `Put` above does.

```
DELETE /items/{id} (items.go:31): the route's GET declares a validator (its output's ETag header field), but the input embeds neither geta.Conditional nor geta.RequireConditional, so geta would evaluate its preconditions against none; embed one and call Check with the current validators
```

- The GET declares a validator when its output has an `ETag` or `Last-Modified` header field (any spelling, embedded structs included), its input embeds `Conditional` or `RequireConditional`, or `geta.ETag` tags it (a GET answering 200 whole, not a stream, with `geta.ETag` in the root, a directory's, or its own scope).
- Judged: PUT, PATCH, DELETE, which act on the state of the target resource the GET represents (§9.3.4 "the state of the target resource be created or replaced", §14.5 "partial updates", §9.3.5 "remove the association between the target resource and its current functionality").
- Not judged: POST, whose content is processed "according to the resource's own specific semantics" (§9.3.3), and QUERY, safe as GET is (§9.2.1). Without `Conditional`, they keep geta's own evaluation above: `If-Match` with the GET's tag is a 412.
- A route whose GET declares no validator, or that has no GET, keeps geta's own evaluation.
- getavet reports it where one `geta.Route` literal builds both the GET and the write in place and the GET's types declare the validator. `geta.ETag` in the chain, and an operation placed by name, are `geta.New`'s alone.
- Neither can see a validator the GET's handler or a middleware of your own sets by writing the header itself; there, embed `Conditional` in the writes yourself.
- `geta.ETag` computes its tag over the GET's body, which a write's handler does not have; to check it, let the GET set its own `ETag` (geta.ETag keeps it) from what the write can compute too.

## Order against the request content

RFC 9110 §13.2.1: preconditions are evaluated "after it has successfully performed its normal request checks and just before it would process the request content". Decoding and validating a body is processing it; what the headers show is not.

- Before any precondition: the route (404, 405), gates, parameters (400), and what a body's headers and first byte show: another media type or a content coding (415), a declared length past `MaxBodyBytes` (413), a missing required body (400). A request refused for any of them answers so whatever its preconditions.
- geta's own evaluation (above): after those, before the body is read.
- `Conditional`: the handler decides, so a body bound before the handler is validated first: a stale `If-Match` with an invalid body is a 400. Declare the body `geta.Deferred[T]` to have the handler's checks answer first:

```go
type PutIn struct {
	geta.Conditional
	Path
	Body geta.Deferred[model.User] `body:"json"`
}

func (h Handler) Put(ctx context.Context, in *PutIn) (*Tagged, error) {
	u, err := h.Users.Update(ctx, in.ID, func(current model.User) (model.User, error) {
		if err := in.Check(ETag(current), time.Time{}); err != nil {
			return current, err // 412 (or 428), whatever the body
		}
		return in.Body.Value() // 400 for an invalid body, returned as is
	})
	if err != nil {
		return nil, err // the store's not-found row: 404 first
	}
	return &Tagged{ETag: ETag(*u), User: *u}, nil
}
```

- `Deferred[T]`: a `body:"json"` field only (geta.New refuses it elsewhere, a pointer to one, or one embedded). `T` is the body's type: same schema, validation, and document; `Deferred[*T]` for an optional body (`Value` returns nil when none is sent).
- `Value` reads and validates on its first call and returns the same after. Its error, returned as is or wrapped, is answered as a body bound before the handler (400 listing every violation, 408, 413), whatever the failure rows.
- A request whose parameters are refused still lists the body's violations in its 400.
- A `Deferred` built by hand holds no body: `Value` returns the zero value and nil.
- form, multipart, and raw `[]byte` bodies are bound before the handler; a raw `io.Reader` body is read by the handler, after its checks.

