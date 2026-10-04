package model

import (
	"errors"
	"strconv"
	"strings"
)

// Money is an amount in a currency's minor unit, written in JSON as a
// decimal string with two places ("12.50"), never as a float. It writes its
// own JSON, so geta reads and writes it by its methods; main gives geta its
// schema with geta.WithSchema (MoneySchema), which the document states and a
// request is checked against before UnmarshalJSON runs.
type Money struct{ cents int64 }

// MoneyPattern is the wire form: whole units, a dot, two places.
const MoneyPattern = `^[0-9]{1,9}\.[0-9]{2}$`

// Cents builds an amount from its minor units.
func Cents(n int64) Money { return Money{n} }

// Cents is the amount in minor units.
func (m Money) Cents() int64 { return m.cents }

// MarshalJSON writes "units.cents".
func (m Money) MarshalJSON() ([]byte, error) {
	if m.cents < 0 {
		return nil, errors.New("money: a negative amount has no wire form")
	}
	return []byte(`"` + m.String() + `"`), nil
}

// UnmarshalJSON reads "units.cents".
func (m *Money) UnmarshalJSON(b []byte) error {
	s, ok := strings.CutPrefix(string(b), `"`)
	if !ok {
		return errors.New("money: not a string")
	}
	s, ok = strings.CutSuffix(s, `"`)
	units, cents, dot := strings.Cut(s, ".")
	if !ok || !dot || len(cents) != 2 || units == "" || len(units) > 9 {
		return errors.New("money: not units.cents")
	}
	u, err1 := strconv.ParseUint(units, 10, 32)
	c, err2 := strconv.ParseUint(cents, 10, 8)
	if err1 != nil || err2 != nil {
		return errors.New("money: not units.cents")
	}
	m.cents = int64(u)*100 + int64(c)
	return nil
}

// String writes "units.cents".
func (m Money) String() string {
	c := m.cents % 100
	return strconv.FormatInt(m.cents/100, 10) + "." + string(rune('0'+c/10)) + string(rune('0'+c%10))
}
