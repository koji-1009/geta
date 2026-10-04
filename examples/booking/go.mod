module github.com/koji-1009/geta/examples/booking

go 1.27

require (
	github.com/MicahParks/jwkset v0.11.3
	github.com/MicahParks/keyfunc/v3 v3.8.2
	github.com/coder/websocket v1.8.15
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/gorilla/websocket v1.5.3
	github.com/klauspost/compress v1.20.1
	github.com/koji-1009/geta v0.0.0
	github.com/koji-1009/geta/getaotel v0.0.0
	github.com/quic-go/quic-go v0.63.0
	go.opentelemetry.io/otel v1.46.0
	go.opentelemetry.io/otel/sdk v1.46.0
	go.opentelemetry.io/otel/sdk/metric v1.46.0
	golang.org/x/time v0.15.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.71.0 // indirect
	go.opentelemetry.io/otel/metric v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.46.0 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
)

replace (
	github.com/koji-1009/geta => ../..
	github.com/koji-1009/geta/getaotel => ../../getaotel
)
