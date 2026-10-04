package jwtauth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/koji-1009/geta"
)

// A token with several faults is refused for the most specific of them, in
// the order the challenge's reasons are listed: expired before not valid
// yet, before invalid issuer, before invalid audience.
func TestTheMostSpecificReasonIsGiven(t *testing.T) {
	c := app(t, static(t))
	for reason, over := range map[string]map[string]any{
		"token is expired":           {"exp": 1, "nbf": 4102444800, "iss": "https://other.example", "aud": "other"},
		"token is not valid yet":     {"nbf": 4102444000, "iss": "https://other.example", "aud": "other"},
		"token has invalid issuer":   {"iss": "https://other.example", "aud": "other"},
		"token has invalid audience": {"aud": "other"},
	} {
		res := c.With("Authorization", "Bearer "+token(t, "RS256", "rs", over)).Get("/me")
		want := `Bearer error="invalid_token", error_description="` + reason + `"`
		if res.Status != 401 || !slices.Equal(res.Header.Values("WWW-Authenticate"), []string{want}) {
			t.Errorf("%s: %d %q", reason, res.Status, res.Header.Values("WWW-Authenticate"))
		}
	}
}

// The Verifier's refusal of a bad token is an error that reads
// "invalid_token: " and the reason, and is geta.ErrUnauthenticated and the
// reason both.
func TestARefusalNamesItsReason(t *testing.T) {
	verify, err := Verifier(validator(t, static(t)))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/me", nil)
	r.Header.Set("Authorization", "Bearer "+token(t, "RS256", "rs", map[string]any{"exp": 1}))
	_, err = verify(r)
	if err == nil || err.Error() != "invalid_token: token is expired" ||
		!errors.Is(err, geta.ErrUnauthenticated) || !errors.Is(err, jwt.ErrTokenExpired) {
		t.Fatalf("%v", err)
	}
}

// Claims.Raw is the whole payload, every claim the token carries.
func TestRawIsTheWholePayload(t *testing.T) {
	c, err := validate(t, token(t, "RS256", "rs", map[string]any{"tenant": "t1"}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"iss": "https://issuer.example", "aud": "api", "sub": "ada", "exp": json.Number("4102444800"),
		"scope": "read write", "tenant": "t1"}
	if len(c.Raw) != len(want) {
		t.Fatalf("Raw %v", c.Raw)
	}
	for k, v := range want {
		if c.Raw[k] != v {
			t.Errorf("Raw[%q] = %#v, want %#v", k, c.Raw[k], v)
		}
	}
}
