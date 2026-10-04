package geta

import (
	"log/slog"
	"reflect"
)

// Option configures [New].
type Option func(*config)

type config struct {
	log     *slog.Logger
	limits  Limits
	info    Info
	schemas []declaredSchema
	unions  []Union
	oas     OpenAPIVersion
}

type declaredSchema struct {
	t                     reflect.Type
	jsonType, constraints string
}

// WithSchema narrows the schema of T, a type that writes its own JSON (it
// implements json.Marshaler or json.Unmarshaler, or their v2 forms, as
// decimal.Decimal and big.Int do). Without it, T's schema is {} and a
// request value is valid when T's UnmarshalJSON accepts it. With it, the
// document lists the narrower schema and a request value is checked against
// it first.
//
// jsonType is "string", "integer", "number", "boolean", "array", or
// "object". constraints uses the schema tag vocabulary
// ("pattern=^-?[0-9]+(\\.[0-9]+)?$"), without the items. and
// additionalProperties. keywords. In a request, an array with no maxItems or
// an object with no maxProperties is bounded by [Limits].MaxItems, and the
// request's schema states that bound.
func WithSchema[T any](jsonType, constraints string) Option {
	return func(c *config) {
		c.schemas = append(c.schemas, declaredSchema{reflect.TypeFor[T](), jsonType, constraints})
	}
}

// Info is the document's info object.
type Info struct {
	Title       string
	Version     string
	Description string
}

// WithLogger sets where geta records defects: unmatched errors, panics, and
// verifier failures. The default is slog.Default(). A nil l fails [New]; use
// slog.New(slog.DiscardHandler) to record nothing.
func WithLogger(l *slog.Logger) Option { return func(c *config) { c.log = l } }

// WithLimits replaces [DefaultLimits] whole: a field l leaves zero is zero,
// not its default. See [Limits] for what [New] refuses.
func WithLimits(l Limits) Option { return func(c *config) { c.limits = l } }

// WithInfo sets the OpenAPI document's info object.
func WithInfo(i Info) Option { return func(c *config) { c.info = i } }
