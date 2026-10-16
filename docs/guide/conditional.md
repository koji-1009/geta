# Conditional requests

## Conditional requests (304, 412, 428)

Embed `geta.Conditional` in an input to bind `If-Match`, `If-None-Match`, `If-Modified-Since`, `If-Unmodified-Since` (optional, documented header parameters; `RequireConditional` makes `If-Match` required on a write, `RequireAbsent` `If-None-Match: *`, below). After its own checks, before acting and before any 2xx, the handler calls `in.Check(etag, modified)` and returns the error as is: `etag` the current entity tag (`"v1"`, `W/"v1"`, "" for none), `modified` the last modification (zero for none). `in.CheckAbsent()`: target without representation yet (a creating PUT); `If-Match` fails, `If-None-Match: *` holds.

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
- A 304 carries the header fields a 200 would among Content-Location, Date, ETag, Vary, Cache-Control, Expires (§15.4.5 "MUST generate"). The handler returns the output it would answer beside the error, headers set, body left out: `return &Tagged{ETag: tag, CacheControl: "max-age=60"}, err`. geta sends those fields on the 304; without the output's ETag, the tag given to `Check`, else its date as Last-Modified. The output is read for a 304 alone. Where the output type declares one of these fields other than ETag (and Last-Modified), a 304 returned without the output is a logged 500. The document states, on the 304, the ETag and Last-Modified `Check` was given and the output's fields among these, typed as on the 200, all optional.
- 400: malformed tag list; list without a tag (empty field, one or several lines, commas alone). `*` alone but for spaces and tabs (beside a no-break space: 400). Several lines are one list.
- Non-HTTP-date: ignored. Only RFC 9110 §5.6.7 forms are read (IMF-fixdate, RFC 850, asctime; two-digit hour, minute, second; no fraction; names in their own case). 23:59:60 reads as 23:59:59.
- The four fields are not string parameters: bound whatever their bytes and length. An obs-text (non-UTF-8) entity tag matches as any other; non-UTF-8 date ignored; long list read whole; no documented `maxLength`.
- Error: `*geta.PreconditionError` (`Status()` 304, 400, 412, 428), answered on operations whose input embeds `Conditional`, which document 412 (304 too on GET). Returned elsewhere, or an `etag` not one entity tag: logged 500 with an `instance`; no failure row answers them, catch-all `geta.OnAs[error]` included.
- `geta.RequireConditional` instead of `Conditional`: 428 when the request carries no precondition that assures "clients are working with the correct copies" (RFC 6585 §3), documented there with the 428.
  - On a write (any method but GET and HEAD): `If-Match` listing one or more entity tags, the tag of the representation the request was made against (§13.1.1, "to prevent the 'lost update' problem"). The document states `If-Match` `required: true`. Nothing else stands for it, each a 428 alone: `If-Match: *` (holds for any current representation, whichever copy the client worked on); `If-None-Match` with tags (holds whenever the current tag is another, a stale copy's included); `If-None-Match: *` (holds only where there is no representation, which `If-Match` always fails); `If-Unmodified-Since` (read only without `If-Match`, §13.2.2; ignored where `Check` gets no date, §13.1.4; one second, within which two writes both hold). Its parameter says it is not read there. A malformed `If-Match` is a 400, as with `Conditional`.
  - A write that creates its target has no tag to send: embed `RequireAbsent` (below). `RequireConditional`'s `CheckAbsent` answers 428 or 412.
  - On a GET or HEAD: any of `If-Match`, `If-None-Match`, a valid `If-Modified-Since`, a valid `If-Unmodified-Since`; the four stay optional.
  - The 428 is `Check`'s answer, so it comes after the handler's own checks (a missing target's 404), as 412 does (§13.2.1).
- `geta.RequireAbsent` instead of `Conditional`: a write that only creates its target. 428 unless `If-None-Match` is `*`, which RFC 9110 §13.1.2 gives "to prevent an unsafe request method (e.g., PUT) from inadvertently modifying an existing representation of the target resource when the client believes that the resource does not have a current representation" (RFC 6585 §3 for the 428).
  - The handler calls `in.CheckAbsent()` where the target has no representation (`*` holds) and `in.Check(etag, modified)` where it has one (`*` matches: 412).
  - 428 alone: no `If-None-Match`; `If-None-Match` with tags (holds over a representation the client never saw); `If-Match` or a date alone. A malformed `If-None-Match` is a 400.
  - The document states `If-None-Match` `required: true` with `"const": "*"`, and the 428 and 412; the other three stay optional.
  - `geta.New` refuses it on GET and QUERY, which create nothing (§9.2.1; on a GET a matching `*` is a 304, not the 412 it states); getavet reports it where a `geta.Route` literal builds the operation in place.

```go
type CreateIn struct {
	geta.RequireAbsent
	Path
	Body model.User `body:"json"`
}

func (h Handler) Put(ctx context.Context, in *CreateIn) (*Tagged, error) {
	u, err := h.Users.Create(ctx, in.ID, in.Body, func(current *model.User) error {
		if current != nil {
			return in.Check(ETag(*current), time.Time{}) // If-None-Match: * matches: 412
		}
		return in.CheckAbsent()
	})
	if err != nil {
		return nil, err
	}
	return &Tagged{ETag: ETag(*u), User: *u}, nil
}
```

- Check before a 2xx: the handler of an operation whose input embeds `Conditional`, `RequireConditional`, or `RequireAbsent` calls `Check` or `CheckAbsent` on its input (or a copy of it) before it answers a 2xx, and returns the error either returns. A 2xx answered without either, or after either returned an error (304, 400, 412, 428; the first error stands, whatever a later call answers), is a defect, as the document states preconditions the response never evaluated, or a failed one it overrode, performing the write anyway: a logged 500 with an `instance`, whatever the failure rows, a catch-all included, without the body or the envelope's headers. Any other status (a missing target's 404, a 303) is sent as answered: the preconditions are ignored there (§13.2.1 "MUST ignore all received preconditions if its response ... would have been a status code other than a 2xx"). getavet reports a handler that leaves its input unnamed, names it `_`, or never uses it (a function literal, or a function or method of the package); a handler that uses its input is left to geta at run time.
- Binding the four headers again in the same input fails `geta.New`.
- `geta.ETag` reads `If-None-Match` as `Check` does; on a GET without `Conditional`, every precondition (below).
- A write without one of the three takes no precondition (below).
- Every status `Check` answers is documented.
- Worked case: `examples/register` `/users/{id}`: GET and PUT answer `user.Tagged` with the user's `ETag`; stale `If-Match` is a 412 writing nothing, on PUT and DELETE alike.

## A write without `Conditional` takes no precondition

A write (POST, PUT, PATCH, DELETE) whose input embeds none of `Conditional`, `RequireConditional`, `RequireAbsent` takes no precondition: that is its declared state, "none".

- A request carrying `If-Match`, `If-None-Match`, `If-Modified-Since`, or `If-Unmodified-Since` does not match its contract: a 400 before the handler runs, its `errors` naming each field (`{"in":"header","path":"If-Match","message":"is not taken: ..."}`), listed with the request's other violations. Nothing is written.
- RFC 9110 §13.2.1 evaluates preconditions "after it has successfully performed its normal request checks"; refusing a field the operation does not take is one of those checks, as a refused parameter is. So a write never answers 412 for a target that does not exist, nor acts on a precondition it cannot evaluate.
- The document states none of the four as parameters, and the 400 for them; no 412.
- To take preconditions, embed `Conditional` and call `Check` once the target is found, before acting (above). A target with no validators: `in.Check("", time.Time{})` (`If-Match` with tags 412, `*` holds; `If-None-Match: *` 412; dates ignored).

## Writes beside a GET that declares a validator

Where a route's GET declares a validator, a client holds the tag it sent and may send it back in `If-Match` "to prevent the 'lost update' problem" (§13.1.1). A PUT, PATCH, or DELETE there that takes none would answer it 400, so `geta.New` refuses it:

```
DELETE /items/{id} (items.go:31): the route's GET declares a validator (its output's ETag header field), so a client may send its tag in If-Match, but the input embeds none of geta.Conditional, geta.RequireConditional, geta.RequireAbsent, so the operation takes no precondition and answers it 400; embed geta.Conditional and call Check with the current validators
```

- The GET declares a validator when its output has an `ETag` or `Last-Modified` header field (any spelling, embedded structs included), its input embeds `Conditional` (or `RequireConditional`), or `geta.ETag` tags it (a GET answering 200 whole, not a stream).
- Judged: PUT, PATCH, DELETE, which act on the state the GET represents (§9.3.4, §14.5, §9.3.5). Not judged: POST, processed "according to the resource's own specific semantics" (§9.3.3), and QUERY, safe as GET is.
- getavet reports it where one `geta.Route` literal builds both the GET and the write in place and the GET's types declare the validator; `geta.ETag` in the chain, and an operation placed by name, are `geta.New`'s alone. Neither sees a validator a handler or middleware of your own writes by hand; embed `Conditional` in the writes there yourself.
- A header field named `If-Match`, `If-None-Match`, `If-Modified-Since`, or `If-Unmodified-Since` bound as a plain input field (`header:"If-Match"`) fails `geta.New` on any method: only `Conditional` binds and evaluates them.

## GET and QUERY without `Conditional`

A GET or QUERY whose input does not embed `Conditional` is safe (§9.2.1 "essentially read-only"), so its handler runs first, and the preconditions are evaluated against what it answered: its status, and the validators its response carries (an output `ETag` or `Last-Modified` header field, or the tag `geta.ETag` computes), as `Check` would be called with them: "the condition is true if any of the listed tags match the entity tag of the selected representation" (§13.1.1). GET and HEAD alike:

- The handler's own answers take precedence: a status other than 2xx ignores the preconditions (§13.2.1), so a missing target is its 404 whatever is sent; an upgrade's 101 ignores them too.
- Order (§13.2.2): `If-Match` (strong; no match 412), else `If-Unmodified-Since` against the `Last-Modified` (412); then `If-None-Match` (weak; match 304 on GET/HEAD, 412 on QUERY), else on GET/HEAD `If-Modified-Since` (304). `If-Match` with the tag the GET sent holds; with no `ETag`, a list never matches (412), `*` holds, and `If-None-Match: *` matches (304).
- Without a `Last-Modified`, the dates are ignored (§13.1.3, §13.1.4).
- A 304 answers only in place of a 200 (§15.4.5 "would have resulted in a 200 (OK) response"): another 2xx from a status field is sent as is, its `If-Match` still evaluated. It carries no body, and the output's header fields among Content-Location, Date, ETag, Vary, Cache-Control, Expires (and Last-Modified where it has no ETag); a stream's 304 its `Cache-Control: no-cache`.
- `If-Match` or `If-None-Match` that is neither `*` nor a list of entity tags (several lines are one list: `*` twice is none): 400, as `Check` answers.
- An output `ETag` value that is not one entity tag is no entity tag; a `Last-Modified` that is not one HTTP-date is no date.
- On a GET `geta.ETag` tags, the middleware evaluates them, against its tag (or the handler's, which it keeps): the tag is known only once the body is. Its 304 keeps the handler's other header fields. On a GET whose input embeds `Conditional`, `geta.ETag` reads only `If-None-Match`.
- OPTIONS: ignored (§13.2.1).
- Not refused as a write without `Conditional` is: a cache revalidates any stored GET response with `If-None-Match` or `If-Modified-Since` (RFC 9111 §4.3.1), and a GET acts on nothing, so geta evaluates them after the handler at no risk, where a 400 would fail every revalidation.
- Documented: `If-Match` and `If-None-Match` as optional header parameters, and, where the output declares a `Last-Modified` header field, `If-Unmodified-Since` and on a GET `If-Modified-Since`; 412; 400; 304 where the GET answers 200, stating the header fields it carries, typed as on the 200. None where no 2xx is answered (an upgrade). CORS allows the same request headers.

## Order against the request content

RFC 9110 §13.2.1: preconditions are evaluated "after it has successfully performed its normal request checks and just before it would process the request content". Decoding and validating a body is processing it; what the headers show is not.

- Before any precondition: the route (404, 405), gates, parameters (400), and what a body's headers and first byte show: another media type or a content coding (415), a declared length past `MaxBodyBytes` (413), a missing required body (400). A request refused for any of them answers so whatever its preconditions.
- GET and QUERY without `Conditional` (above): once the handler has answered.
- A write without `Conditional`: a precondition field sent is a 400 among the parameters' (above).
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

