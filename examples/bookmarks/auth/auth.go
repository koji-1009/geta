// Package auth is the bookmarks example's cookie sessions: a login starts
// one, its id travels in the sid cookie, and the gate admits a request whose
// cookie names a live session. geta ships no sessions; it matches each
// route's declared scheme to the verifier here.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/koji-1009/geta"
)

// Principal is who the gate admitted.
type Principal struct {
	User    string
	Session string
}

// Caller carries the principal from the gate to the handlers.
var Caller = geta.NewKey[Principal]("caller")

// CookieName is the session cookie's name.
const CookieName = "sid"

// CookieAuth is the session id carried in the sid cookie, the scheme every
// route requires unless it declares otherwise.
var CookieAuth = geta.Scheme{Name: "cookieAuth", Type: "apiKey", In: "cookie", Param: CookieName}

// TTL is how long a session lives from its login.
const TTL = 8 * time.Hour

// Demo credentials. A real application compares an Argon2id hash.
var credentials = map[string]string{"ada": "ada-pass", "bo": "bo-pass"}

// ErrInvalidCredentials is a login with an unknown name or a wrong
// password.
var ErrInvalidCredentials = errors.New("invalid credentials")

// Sessions is the session store, safe for concurrent use.
type Sessions struct {
	mu       sync.Mutex
	now      func() time.Time
	sessions map[string]session
}

type session struct {
	user    string
	expires time.Time
}

// NewSessions returns an empty store on the system clock.
func NewSessions() *Sessions { return &Sessions{now: time.Now, sessions: map[string]session{}} }

// Login checks a name and password and starts a session.
func (s *Sessions) Login(ctx context.Context, user, password string) (string, error) {
	want, ok := credentials[user]
	if !ok || subtle.ConstantTimeCompare([]byte(want), []byte(password)) != 1 {
		return "", ErrInvalidCredentials
	}
	b := make([]byte, 16)
	rand.Read(b)
	sid := hex.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for id, ss := range s.sessions {
		if !now.Before(ss.expires) {
			delete(s.sessions, id)
		}
	}
	s.sessions[sid] = session{user: user, expires: now.Add(TTL)}
	return sid, nil
}

// Logout ends a session; ending one that has ended is no error.
func (s *Sessions) Logout(ctx context.Context, sid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sid)
	return nil
}

func (s *Sessions) user(sid string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[sid]
	if !ok || !s.now().Before(ss.expires) {
		return "", false
	}
	return ss.user, true
}

// Policy requires the session cookie wherever a route declares nothing.
func Policy(s *Sessions) geta.Policy {
	return geta.Policy{
		Default: []geta.Scheme{CookieAuth},
		Verifiers: map[string]geta.Verifier{
			CookieAuth.Name: func(r *http.Request) (context.Context, error) {
				c, err := r.Cookie(CookieName)
				if err != nil {
					return nil, geta.ErrUnauthenticated
				}
				user, ok := s.user(c.Value)
				if !ok {
					return nil, geta.ErrUnauthenticated
				}
				return Caller.With(r.Context(), Principal{User: user, Session: c.Value}), nil
			},
		},
	}
}

// Cookie is the session cookie a login sets, and with an empty sid and a
// negative MaxAge, the one a logout sets to remove it.
//
// HttpOnly keeps the id from scripts. SameSite=Lax sends it on requests from
// the same site — a page at https://app.example.com calling
// https://api.example.com — and keeps it off requests another site's page
// makes; a page on another site altogether needs SameSite=None, which a
// browser takes only with Secure. secure is false only for a server on plain
// HTTP that is not localhost, which no browser should be logging in to.
func Cookie(sid string, maxAge int, secure bool) *http.Cookie {
	return &http.Cookie{Value: sid, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode}
}
