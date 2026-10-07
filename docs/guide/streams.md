# Streams and upgrades

## Streams and upgrades

```go
func (h Handler) Get(ctx context.Context, _ *struct{}) (*geta.Stream[Event], error) {
	return &geta.Stream[Event]{Events: h.Feed.Subscribe(ctx), KeepAlive: 15 * time.Second, MaxIdle: 0, MaxLifetime: 0}, nil
}
```

### Streams

- `Events` is an `iter.Seq[T]`; each value is one SSE event with JSON data. `EventName() string` / `EventID() string` on `T` set `event:` / `id:`. The source must stop when `ctx` ends.
- Status 200; `X-Accel-Buffering: no`; HEAD gets headers only.
- A name or id that would split the event, or an unencodable event (NaN, required sealed value nil): not sent, defect logged, stream ended. A zero stream is a defect. A panicking source is logged and aborts its connection.
- `KeepAlive` sends a comment, not extending `MaxIdle`. Each event restarts both. `MaxLifetime` holds despite activity.
- Compress and ETag pass streams uncoded and untagged (a set tag is sent as the identity form's). Streams hold no concurrency slot, ignore `Timeout`, end when the server drains.

### Upgrades

- `*geta.Upgrade` with status 101 switches protocols after the gate; unauthenticated upgrades are refused first.
- Not asking to switch to `Protocol`: 426, after middleware and handler, carrying `Upgrade: Protocol` and `Connection: Upgrade` (stated in the document, optional, since a row may answer 426 too). Asking: `Connection` lists `upgrade`, `Upgrade` lists the protocol (any `/version`); elements trimmed of spaces and tabs only, ASCII case-insensitive (no-break space, long s, Kelvin sign ask nothing).
- Set exactly one of `Accept`, `Serve`. 500 defect, even without a switch request: both; neither; `Header` with `Accept`; `Protocol` not a token (`websocket`; no version of its own).
- `Accept: func(w http.ResponseWriter, r *http.Request)`: a library validates the handshake, writes the 101 (or its refusal, sent as written), and hijacks. Inside, call `websocket.Accept(w, r, nil)` (coder/websocket) or `Upgrader.Upgrade(w, r, nil)` (gorilla/websocket) and use the connection there for its life. `w` is an `http.Hijacker`, unwrapping through every geta middleware's writer. Never frame WebSocket yourself. Declare `Sec-WebSocket-Key` and `-Version` header parameters so a malformed handshake gets geta's 400 (library refusals are undocumented). Non-switch requests get 426 before the library runs. `Accept` writing nothing: 500. Subscribe after accept succeeds. Example: `examples/booking` `/rooms/{room}/live` (coder/websocket).
- `Serve: func(conn net.Conn, rw *bufio.ReadWriter)`, for protocols without a library: geta writes the 101 with `Header`, hands over the connection, closes it when `Serve` returns. `Upgrade` and `Connection` on the 101 are geta's. 500 defect: `Header` setting either, or the `Deprecation`/`Sunset` the operation's `Doc` sends (any case). Unhijackable connection: 500.
- After the switch, the `ConcurrencyLimit` slot is freed and `Timeout` deadlines lifted.

