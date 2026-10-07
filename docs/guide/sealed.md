# Sealed types

## Sealed types (oneOf)

```go
type Shape interface{ isShape() }
type Circle struct {
	Kind   string  `json:"kind"`   // the discriminator: a required string member of every variant
	Radius float64 `json:"radius"`
}
type Square struct {
	Kind string `json:"kind"`
	Side int    `json:"side"`
}
func (Circle) isShape() {}
func (Square) isShape() {}

var Shapes = geta.Sealed[Shape]("kind", geta.Case[Circle]("circle"), geta.Case[Square]("square"))
// geta.New(table, geta.WithUnion(Shapes))
```

- `Shape`, `[]Shape`, `map[string]Shape` fields are documented as `oneOf` with a discriminator mapping.
- Requests are checked against the variant `kind` selects (discriminator in any member position). Missing, non-string, or unknown `kind`: 400 naming it.
- Output 500 (never written): a non-variant value; a variant whose `kind` is not its tag; a required `Shape` nil (also through or as a pointer variant). Optional: `Shape` tagged `json:"name,omitzero"`.
- Refused at assembly: non-interface; unnamed discriminator; no variants; empty or repeated tag; repeated variant; variant not a named struct or not implementing the interface; discriminator member missing, optional, or non-string; type declared twice; `default` on the discriminator; a schema tag on it refusing the variant's own tag.
- An interface reached without `WithUnion` fails `geta.New`, even one listing JSON methods (`interface{ MarshalJSON() ([]byte, error) }`), which v2 cannot read into. `WithSchema` refuses an interface.
- Clients decode with `Shapes.JSONOptions()`; getatest does it for you.
- Worked case: `examples/booking` `model.Change` (`rename`, `reschedule`, `cancel`), body of `POST /bookings/{booking}/changes` and member of each history entry.

