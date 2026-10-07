# Performance

This page compares geta with the same two operations written in net/http alone. It makes no claim about other frameworks.

## What is compared

The operations are those of `benchApp` in [`bench_test.go`](../bench_test.go): `GET /users/{id}` answers a map lookup as JSON, and `POST /users` binds a JSON body of four members, each with schema-tag bounds, and answers 201 with a `Location` header.

- **geta**: `BenchmarkGet` and `BenchmarkPost`, the operations as geta serves them.
- **net/http, checked**: `BenchmarkGetNetHTTP/checked` and `BenchmarkPostNetHTTP/checked`, an `http.ServeMux` that does by hand what `benchApp`'s types and tags ask of geta. It checks the path value's length, the request's media type (415 otherwise), the body limit of 1 MiB, unknown, missing, and out-of-range members, answers each refusal with an RFC 9457 problem, and sends `X-Content-Type-Options: nosniff`.
- **net/http, plain**: `BenchmarkGetNetHTTP/plain` and `BenchmarkPostNetHTTP/plain`, the same mux trusting the request: no checks, no problems.

All three use the same JSON package, encoding/json/v2, so the difference is the work around it, not the encoder.

The checked baseline still does less than geta. It answers a refusal with one problem whose `detail` names the members, where geta lists every violation with its path and message in `errors`, and it does not refuse a `Content-Encoding`. It also stands for code an application would have to write, test, and keep in step with its OpenAPI document by hand.

## Results

Apple M4, darwin/arm64, Go 1.27.1. Medians of 10 runs, in process through `httptest`: no network, so the difference is larger here than over a connection, where the network's time is added to every row.

| Benchmark | ns/op | vs checked | B/op | allocs/op |
| --- | --- | --- | --- | --- |
| GET, net/http plain | 1304 | 0.98× | 6367 | 23 |
| GET, net/http checked | 1329 | 1.00× | 6399 | 24 |
| GET, geta | 1548 | 1.16× | 6787 | 23 |
| POST, net/http plain | 2019 | 0.90× | 6968 | 28 |
| POST, net/http checked | 2232 | 1.00× | 7216 | 35 |
| POST, geta | 2435 | 1.09× | 8003 | 34 |

geta takes about 16% more time than the checked baseline for the GET and about 9% more for the POST, with one allocation fewer in each and about 6% and 11% more bytes.

## Reproduce

```
go test -run '^$' -bench '^Benchmark(Get|Post)(NetHTTP)?$' -benchmem -count 10 .
```

Run all variants in one invocation and compare their ratios; absolute times depend on the machine. `go run golang.org/x/perf/cmd/benchstat@latest` reads the output and prints the medians.
