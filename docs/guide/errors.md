# Errors

## Errors

- Handlers return the lower layer's error unchanged (wrap with `fmt.Errorf("...: %w", err)` if useful). No HTTP error type.
- `Doc.Failures` maps errors to statuses, top to bottom: `geta.On(sentinel, status, detail)` uses `errors.Is`; `geta.OnAs[*MyErr](status, detail)` uses `errors.AsType`. Statuses 4xx/5xx; invalid rows fail `geta.New`. Share rows via variables or slices (`slices.Concat`).
- `geta.On(...).Type("https://errors.example/gone")` sets the problem `type` (URI reference, checked at `geta.New`), documented with the row; without it `about:blank`. Distinguishes rows sharing a status.
- Unmatched error: 500, logged with its chain. `err.Error()` never reaches the response; `detail` does (empty detail: status text only).
- Every 500 (unmatched error, panic, verifier defect) carries `instance: "urn:uuid:..."`, also in its log line.
- Logged 500 whatever the rows (catch-all `OnAs[error]` included): nil output with nil error; `Conditional.Check` given an etag not one entity tag; `*geta.PreconditionError` from an operation not answering it.
- `context.DeadlineExceeded`: 504, documented on every operation (geta's `options` operation only where a root middleware answers one); likewise a derived context's `context.Canceled` after the request deadline passed. `context.Canceled` with the client gone writes nothing. Judged before rows: a row for `context.DeadlineExceeded` (wrapped or not) fails `geta.New`. `context.Canceled` with the request alive (handler cancelled its own context) reaches the rows.
- Bodies: RFC 9457 `application/problem+json` (`geta.Problem`, the document's `GetaProblem` component): `type`, `title`, `status`, `detail`, `instance`, and for a 400 of violations `errors` (each `in`, `path`, `message`) and `omitted` (the count not listed, at least 1, absent at 0). Middleware answering itself uses `geta.WriteProblem(w, status, detail)`; non-UTF-8 detail bytes become U+FFFD.

### Problems that carry the error's data: `geta.OnAsProblem`

- `geta.OnAsProblem(status, detail, func(e *store.QuotaError) Quota {...})`: the function lives in the route; the error type stays HTTP-free.
- The returned P is written as an output. A plain struct's JSON members, or an envelope's `body:"json"` struct's, are RFC 9457 extension members beside `type`/`title`/`status`. Member `detail` (string; nil `*string` keeps the row's) is the problem's detail. A `*string` detail's `schema` tag must take the row's detail, which it keeps; `geta.New` refuses the row otherwise. Envelope `header` fields (``RetryAfter int `header:"Retry-After"` ``) are response headers. Nothing of `err.Error()` is sent.
- Documented on that status: `allOf` GetaProblem plus an open object of the members, in place (P is a component only if a request or response refers to it); `anyOf` with GetaProblem where other causes share the status; a header required only where every cause sets it. CORS exposes the headers.
- Refused by `geta.New` and getavet: non-struct P or body; members `type`, `title`, `status`, `instance`, `errors`, `omitted`; non-string `detail`; cookie, status, or raw body field; no function. Refused by `geta.New` alone: two schemas for one header on a status, and a row's detail its description's `*string` detail schema refuses.
- Unwritable description (header no header carries, required sealed member nil, member not encoding): logged 500, none of its headers.
- Read back: getatest `res.ProblemAs[Quota]()`. `res.Problem()` ignores extension members.

