# OpenTelemetry

## OpenTelemetry (getaotel)

- `getaotel.Middleware(getaotel.WithTracerProvider(tp), getaotel.WithMeterProvider(mp), getaotel.WithPropagators(p))` at `OrderObserve`. Put it first in the root scope, like `AccessLog`, to trace 404 and 405. No option: otel globals; the global propagator extracts nothing until set.
- Span name `METHOD /template`; 404/405: method alone; redirect: target template; geta's OPTIONS: the template answered for. `http.route` is on the span and `http.server.request.duration`, so a template's paths share a series. Incoming `traceparent` is continued.
- Nothing the client chooses is recorded as fact: no `url.path`; `server.address`/`server.port` only from `getaotel.WithServerName("api.example.com")`, never the `Host` header (empty without it), so Hosts share a series; `client.address` is the connection's peer, never `X-Forwarded-For`; `user_agent.original` and `http.request.method_original` are cut to 128 bytes.
- Method and route are those served, after a root rewrite. HEAD served by GET stays HEAD. Unserved requests have no route, even under a ServeMux pattern. A rewrite to a non-standard method records `_OTHER` and the original.
- 5xx is an error; 4xx is not.
- Streams flush and upgrades switch through it; upgrades record 101.
- `http.ErrAbortHandler` abort: recorded with route and sent status (500 if none), span an error, `error.type` `aborted` on span and metrics. Other panic values no inner `Recover` caught: same, `error.type` `panic`, an exception event with value and stack; then goes on with its own value, writing nothing.
- QUERY: span `QUERY /template`, method QUERY; its metrics (duration, request and response body size) record `http.request.method` QUERY, not otelhttp's `_OTHER`. Other methods' series are untouched.

