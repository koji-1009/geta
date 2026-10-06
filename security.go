package geta

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode"
)

// Scheme is an OpenAPI security scheme. A route declares the schemes that
// admit it in [Doc.Security]; [Secure] enforces them and the document lists
// them.
//
// Each type has its own members, and [New] refuses a member of another
// type: http takes Scheme, and BearerFormat when Scheme is bearer; apiKey
// takes In and Param; oauth2 takes Flows (required) and OAuth2MetadataURL;
// openIdConnect takes OpenIDConnectURL (required); mutualTLS takes none.
// Description and Deprecated go with any type. Every URL must be absolute
// https (http only on a loopback host).
type Scheme struct {
	// Name keys the scheme in the document and in [Policy.Verifiers].
	Name string
	// Type is the OpenAPI type: http, apiKey, oauth2, openIdConnect, or
	// mutualTLS.
	Type string
	// Scheme is the HTTP authentication scheme of an http type, such as
	// bearer.
	Scheme string
	// BearerFormat hints how an http bearer scheme's token is formatted,
	// such as JWT.
	BearerFormat string
	// In and Param locate an apiKey: header, query, or cookie, and its name.
	In    string
	Param string
	// Flows are the OAuth 2.0 flows an oauth2 type supports, at least one.
	Flows *OAuthFlows
	// OAuth2MetadataURL is the RFC 8414 authorization server metadata of an
	// oauth2 type. Only an OpenAPI 3.2 document has a place for it.
	OAuth2MetadataURL string
	// OpenIDConnectURL is the discovery URL of an openIdConnect type.
	OpenIDConnectURL string
	Description      string
	// Deprecated marks the scheme deprecated in the document. Only an
	// OpenAPI 3.2 document has a place for it.
	Deprecated bool
}

// OAuthFlows are the OAuth 2.0 flows an oauth2 [Scheme] supports (OpenAPI's
// OAuth Flows Object). Each flow takes exactly the URLs its grant has:
// Implicit an AuthorizationURL; Password and ClientCredentials a TokenURL;
// AuthorizationCode both; DeviceAuthorization (RFC 8628, OpenAPI 3.2 only)
// a DeviceAuthorizationURL and a TokenURL. Any flow may give a RefreshURL.
type OAuthFlows struct {
	Implicit            *OAuthFlow
	Password            *OAuthFlow
	ClientCredentials   *OAuthFlow
	AuthorizationCode   *OAuthFlow
	DeviceAuthorization *OAuthFlow
}

// OAuthFlow is one OAuth 2.0 flow: its endpoints, each an absolute https URL
// (http on a loopback host), and its scopes, each name (an RFC 6749
// scope-token) mapped to a short description. [Middleware.Scopes] on an
// oauth2 scheme names scopes some flow of it defines.
type OAuthFlow struct {
	AuthorizationURL       string
	DeviceAuthorizationURL string
	TokenURL               string
	RefreshURL             string
	Scopes                 map[string]string
}

// flow is one of an [OAuthFlows]' flows, with its document name and the URLs
// it requires.
type flow struct {
	name         string
	f            *OAuthFlow
	auth, device bool // whether it takes an authorizationUrl, a deviceAuthorizationUrl
	token        bool // whether it takes a tokenUrl
}

// list returns all of fs's flows in document order, nil ones included.
func (fs *OAuthFlows) list() []flow {
	return []flow{
		{name: "implicit", f: fs.Implicit, auth: true},
		{name: "password", f: fs.Password, token: true},
		{name: "clientCredentials", f: fs.ClientCredentials, token: true},
		{name: "authorizationCode", f: fs.AuthorizationCode, auth: true, token: true},
		{name: "deviceAuthorization", f: fs.DeviceAuthorization, device: true, token: true},
	}
}

// defines reports whether some flow of s defines scope. s must be an oauth2
// scheme that passed check.
func (s Scheme) defines(scope string) bool {
	for _, fl := range s.Flows.list() {
		if fl.f != nil {
			if _, ok := fl.f.Scopes[scope]; ok {
				return true
			}
		}
	}
	return false
}

// equal reports whether s and o define the same scheme, comparing flows by
// value.
func (s Scheme) equal(o Scheme) bool {
	sf, of := s.Flows, o.Flows
	s.Flows, o.Flows = nil, nil
	if s != o || (sf == nil) != (of == nil) {
		return false
	}
	if sf == nil {
		return true
	}
	a, b := sf.list(), of.list()
	for i := range a {
		x, y := a[i].f, b[i].f
		if (x == nil) != (y == nil) {
			return false
		}
		if x != nil && (x.AuthorizationURL != y.AuthorizationURL || x.DeviceAuthorizationURL != y.DeviceAuthorizationURL ||
			x.TokenURL != y.TokenURL || x.RefreshURL != y.RefreshURL || !maps.Equal(x.Scopes, y.Scopes)) {
			return false
		}
	}
	return true
}

// containsScheme reports whether list holds a scheme equal to s.
func containsScheme(list []Scheme, s Scheme) bool {
	return slices.ContainsFunc(list, s.equal)
}

// Bearer is an HTTP bearer token in the Authorization header.
var Bearer = Scheme{Name: "bearer", Type: "http", Scheme: "bearer"}

// APIKeyHeader returns an apiKey scheme named name, carried in the request
// header header.
func APIKeyHeader(name, header string) Scheme {
	return Scheme{Name: name, Type: "apiKey", In: "header", Param: header}
}

// Verifier authenticates a request under one scheme. It returns the context
// the request continues with, carrying the principal under a [Key]; a nil
// context keeps the request's own. geta's own request state is kept even in a
// context that does not derive from the request's.
//
// It returns [ErrUnauthenticated] when the credentials are absent or wrong,
// and [ErrUnavailable] when the source it checks against cannot be reached.
// Any other error is a defect and answers 500.
type Verifier func(*http.Request) (context.Context, error)

// Errors a [Verifier] returns.
var (
	// ErrUnauthenticated: the request did not prove who it is. 401.
	ErrUnauthenticated = errors.New("geta: unauthenticated")
	// ErrUnavailable: the credential source could not be reached. 503.
	ErrUnavailable = errors.New("geta: credential source unavailable")
)

// Challenger is an [ErrUnauthenticated] error that supplies its
// WWW-Authenticate value, such as an RFC 6750 invalid_token challenge.
// Otherwise the gate sends the scheme's default challenge: an http scheme's
// name (Bearer, Basic, or as declared), Bearer for oauth2 and openIdConnect,
// and the type itself for the rest (apiKey in="header", name="X-API-Key";
// mutualTLS). Every 401 thus carries a challenge (RFC 9110 §15.5.2).
type Challenger interface {
	error
	Challenge() string
}

// Policy is the runtime half of the security declarations: the default
// schemes for a route that declares none, and a verifier for each scheme.
// [Secure] copies Default and Verifiers; later changes to them have no effect.
type Policy struct {
	Default   []Scheme
	Verifiers map[string]Verifier
}

// Secure is the gate. For each request it reads the matched operation's
// [Doc.Security], or the policy's default when the operation declares none
// or the URL matches nothing, and admits the request when any one scheme's
// verifier does. It reads the operation the request matches as the gate sees
// it. If a root middleware after the gate moves the request to an operation
// the gate did not check, dispatch answers 500 as a defect, unless that
// operation requires nothing of the root scope's gates.
//
// A refusal is a 401 with one WWW-Authenticate challenge per refusing scheme
// ([Challenger]), or a 503 when a verifier returned [ErrUnavailable]. [New]
// rejects a scheme with no verifier (a root gate's default included), a
// scheme it could not document or challenge for, and an operation that
// requires a scheme with no gate in its chain.
//
// Every gate in a chain must admit the request. An operation that declares
// schemes requires one of them through each gate; one that declares none
// requires one scheme of each gate's default, and the document lists every
// such combination as a requirement.
func Secure(p Policy) Middleware {
	policy := Policy{Default: slices.Clone(p.Default), Verifiers: maps.Clone(p.Verifiers)}
	m := Ordered(OrderAuthenticate, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			schemes := policy.Default
			if op, ok := matchedFor(r); ok && op.op.doc.Security != nil {
				schemes = op.op.doc.Security
			}
			if len(schemes) == 0 {
				next.ServeHTTP(w, r)
				return
			}
			var unavailable bool
			var defect error
			var challenges []string
			for _, s := range schemes {
				ctx, err := policy.Verifiers[s.Name](r)
				switch {
				case err == nil:
					if ctx != nil {
						r = r.WithContext(keepRequest(ctx, r))
					}
					next.ServeHTTP(w, r)
					return
				case errors.Is(err, ErrUnauthenticated):
					if c, ok := errors.AsType[Challenger](err); ok && c.Challenge() != "" {
						challenges = append(challenges, c.Challenge())
					} else {
						challenges = append(challenges, s.challenge())
					}
				case errors.Is(err, ErrUnavailable):
					unavailable = true
				default:
					defect = err
				}
			}
			switch {
			case defect != nil:
				writeDefect(w, r, loggerFrom(r.Context()), "geta: verifier failed", defect,
					methodAttr(r), routeAttr(r.Context()))
			case unavailable:
				writeProblem(w, r, http.StatusServiceUnavailable, "the credential source is unavailable", nil)
			default:
				for _, c := range challenges {
					w.Header().Add("WWW-Authenticate", c)
				}
				writeProblem(w, r, http.StatusUnauthorized, "", nil)
			}
		})
	})
	m.name = "secure"
	m.gate = &policy
	m.answers = []answer{
		{status: http.StatusUnauthorized, reason: "Unauthenticated"},
		{status: http.StatusServiceUnavailable, reason: "The credential source is unavailable"},
	}
	m.headers = []mwHeader{{status: http.StatusUnauthorized, name: "WWW-Authenticate",
		description: "A challenge for each scheme that refused the request, sent with the gate's 401 (RFC 9110 section 11.6.1)"}}
	return m
}

// check returns an error for a scheme geta could not document or challenge
// for, or that sets a member of another type.
func (s Scheme) check() error {
	// The members set, by their OpenAPI names.
	set := map[string]bool{
		"scheme":            s.Scheme != "",
		"bearerFormat":      s.BearerFormat != "",
		"in":                s.In != "",
		"name":              s.Param != "",
		"flows":             s.Flows != nil,
		"oauth2MetadataUrl": s.OAuth2MetadataURL != "",
		"openIdConnectUrl":  s.OpenIDConnectURL != "",
	}
	var takes []string
	switch s.Type {
	case "http":
		takes = []string{"scheme", "bearerFormat"}
		if !validHeaderName(s.Scheme) {
			return fmt.Errorf("http scheme: Scheme %q is not a token", s.Scheme)
		}
		if s.BearerFormat != "" && !strings.EqualFold(s.Scheme, "bearer") {
			return fmt.Errorf("http scheme: BearerFormat %q with Scheme %q, not bearer", s.BearerFormat, s.Scheme)
		}
	case "apiKey":
		takes = []string{"in", "name"}
		if s.In != "header" && s.In != "query" && s.In != "cookie" {
			return fmt.Errorf(`apiKey scheme: In %q is not "header", "query", or "cookie"`, s.In)
		}
		if s.Param == "" || strings.ContainsFunc(s.Param, func(r rune) bool { return r < 0x20 || r == 0x7f }) ||
			s.In == "header" && !validHeaderName(s.Param) ||
			s.In == "cookie" && !validCookieName(s.Param) ||
			s.In == "query" && !validQueryKey(s.Param) {
			return fmt.Errorf("apiKey scheme: Param %q is not a valid %s name", s.Param, s.In)
		}
	case "oauth2":
		takes = []string{"flows", "oauth2MetadataUrl"}
		if err := s.Flows.check(); err != nil {
			return err
		}
		if s.OAuth2MetadataURL != "" {
			if err := checkURL("OAuth2MetadataURL", s.OAuth2MetadataURL); err != nil {
				return err
			}
		}
	case "openIdConnect":
		takes = []string{"openIdConnectUrl"}
		if s.OpenIDConnectURL == "" {
			return errors.New("openIdConnect scheme: missing OpenIDConnectURL")
		}
		if err := checkURL("OpenIDConnectURL", s.OpenIDConnectURL); err != nil {
			return err
		}
	case "mutualTLS":
	default:
		return fmt.Errorf(`unknown Type %q`, s.Type)
	}
	for _, member := range []string{"scheme", "bearerFormat", "in", "name", "flows", "oauth2MetadataUrl", "openIdConnectUrl"} {
		if set[member] && !slices.Contains(takes, member) {
			return fmt.Errorf("%s scheme: %s does not apply", s.Type, goField[member])
		}
	}
	return nil
}

// validQueryKey reports whether name can be sent unescaped as a query key:
// it round-trips through a query string as one key and holds none of
// & = ; # + % or whitespace.
func validQueryKey(name string) bool {
	if name == "" || strings.ContainsAny(name, "&=;#+%") || strings.ContainsFunc(name, unicode.IsSpace) {
		return false
	}
	q, err := url.ParseQuery(url.Values{name: {"x"}}.Encode())
	return err == nil && len(q) == 1 && q.Get(name) == "x"
}

// goField maps each Security Scheme Object member to its Scheme field.
var goField = map[string]string{
	"scheme": "Scheme", "bearerFormat": "BearerFormat", "in": "In", "name": "Param", "flows": "Flows",
	"oauth2MetadataUrl": "OAuth2MetadataURL", "openIdConnectUrl": "OpenIDConnectURL",
}

// check returns an error for no flow, a flow missing a URL its grant has or
// holding one it lacks, a bad URL, or a scope that is not an RFC 6749
// scope-token.
func (fs *OAuthFlows) check() error {
	if fs == nil {
		return errors.New("oauth2 scheme: missing Flows")
	}
	given := false
	for _, fl := range fs.list() {
		f := fl.f
		if f == nil {
			continue
		}
		given = true
		for _, u := range []struct {
			field, value string
			takes        bool
		}{
			{"AuthorizationURL", f.AuthorizationURL, fl.auth},
			{"DeviceAuthorizationURL", f.DeviceAuthorizationURL, fl.device},
			{"TokenURL", f.TokenURL, fl.token},
		} {
			switch {
			case u.takes && u.value == "":
				return fmt.Errorf("%s flow: missing %s", fl.name, u.field)
			case !u.takes && u.value != "":
				return fmt.Errorf("%s flow: %s does not apply", fl.name, u.field)
			case u.value != "":
				if err := checkURL(fl.name+" flow's "+u.field, u.value); err != nil {
					return err
				}
			}
		}
		if f.RefreshURL != "" {
			if err := checkURL(fl.name+" flow's RefreshURL", f.RefreshURL); err != nil {
				return err
			}
		}
		for _, name := range slices.Sorted(maps.Keys(f.Scopes)) {
			if !scopeToken(name) {
				return fmt.Errorf("%s flow: scope %q is not a scope-token", fl.name, name)
			}
		}
	}
	if !given {
		return errors.New("oauth2 scheme: Flows has no flow")
	}
	return nil
}

// checkURL requires raw to be an absolute https URL with a host, no fragment,
// and no whitespace; http is allowed on a loopback host. A relative URL is
// refused because a geta document states no server to resolve it against.
func checkURL(field, raw string) error {
	if strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return fmt.Errorf("%s %q contains whitespace or a control character", field, raw)
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.Opaque != "" {
		return fmt.Errorf("%s %q is not an absolute URL", field, raw)
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("%s %q has a fragment", field, raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("%s %q is not https (http only on a loopback host)", field, raw)
		}
	default:
		return fmt.Errorf("%s %q is not https", field, raw)
	}
	return nil
}

// scopeToken reports whether s is an RFC 6749 §3.3 scope-token.
func scopeToken(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if c := s[i]; c < 0x21 || c > 0x7e || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

// challenge returns s's default WWW-Authenticate challenge, described at
// [Challenger].
func (s Scheme) challenge() string {
	switch s.Type {
	case "http":
		return httpSchemeName(s.Scheme)
	case "oauth2", "openIdConnect":
		return "Bearer"
	case "apiKey":
		return fmt.Sprintf(`apiKey in=%s, name=%s`, quoted(s.In), quoted(s.Param))
	}
	return s.Type
}

// quoted returns s as an RFC 9110 quoted-string.
func quoted(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

func httpSchemeName(s string) string {
	switch strings.ToLower(s) {
	case "bearer":
		return "Bearer"
	case "basic":
		return "Basic"
	}
	return s
}
