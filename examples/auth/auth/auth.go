// Package auth is the auth example's credential checks: a bearer token table
// and a cookie session store. geta ships no auth; it matches each route's
// declared schemes to these verifiers.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/koji-1009/geta"
)

// Principal is who a verifier admitted.
type Principal struct {
	Role    string
	Session string // set only by the cookie verifier
}

// Caller carries the principal from the gate to the handlers.
var Caller = geta.NewKey[Principal]("caller")

// CookieAuth is a session id carried in the sid cookie.
var CookieAuth = geta.Scheme{Name: "cookieAuth", Type: "apiKey", In: "cookie", Param: "sid"}

// A stand-in token table. A real app verifies a JWT here, as
// examples/booking does with its jwtauth package and an issuer's key set.
var tokens = map[string]string{"admin-token": "admin", "member-token": "member"}

// Demo credentials. A real app compares an Argon2id hash, never plaintext.
var credentials = map[string]string{"admin": "admin-pass", "member": "member-pass"}

// ErrInvalidCredentials: a login with an unknown name or a wrong password.
var ErrInvalidCredentials = errors.New("invalid credentials")

// SessionTTL is how long a session lives from its login. The cookie's
// Max-Age says the same, but a cookie's lifetime is the client's to keep:
// the server holds every session to its expiry itself.
const SessionTTL = time.Hour

// sweepEvery is how often a login also removes the expired sessions, so
// sessions nobody logs out of do not pile up in memory.
const sweepEvery = time.Minute

// Sessions is the session store, safe for concurrent use. Revoking a
// session, or its expiry, closes its channel, which ends any open feed on it.
type Sessions struct {
	mu       sync.Mutex
	now      func() time.Time
	sessions map[string]*session
	sweepAt  time.Time
}

type session struct {
	role    string
	expires time.Time
	revoked chan struct{}
}

// NewSessions returns an empty store on the system clock.
func NewSessions() *Sessions { return NewSessionsAt(time.Now) }

// NewSessionsAt returns an empty store that reads the time from now (a test
// passes a clock it moves).
func NewSessionsAt(now func() time.Time) *Sessions {
	return &Sessions{now: now, sessions: map[string]*session{}}
}

// Login checks a name and password and starts a session, which expires
// SessionTTL from now.
func (s *Sessions) Login(ctx context.Context, name, password string) (string, error) {
	if want, ok := credentials[name]; !ok || want != password {
		return "", ErrInvalidCredentials
	}
	b := make([]byte, 16)
	rand.Read(b)
	sid := hex.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if !now.Before(s.sweepAt) {
		s.sweep(now)
		s.sweepAt = now.Add(sweepEvery)
	}
	s.sessions[sid] = &session{role: name, expires: now.Add(SessionTTL), revoked: make(chan struct{})}
	return sid, nil
}

// Logout ends a session and closes its live feeds. Logging out twice is
// still logged out.
func (s *Sessions) Logout(ctx context.Context, sid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.end(sid)
	return nil
}

// Len is the number of sessions held, expired ones not yet removed
// included.
func (s *Sessions) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// Revoked returns a channel closed when sid is revoked, and how long the
// session has left, after which it is revoked by expiry. An unknown or
// expired session is revoked already — a logout may land between the gate
// admitting the cookie and this call — so its channel is closed.
func (s *Sessions) Revoked(sid string) (<-chan struct{}, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ss, ok := s.live(sid); ok {
		return ss.revoked, ss.expires.Sub(s.now())
	}
	return closed, 0
}

// live returns sid's session if it has not expired; an expired one is
// ended on the way. The caller holds mu.
func (s *Sessions) live(sid string) (*session, bool) {
	ss, ok := s.sessions[sid]
	if !ok {
		return nil, false
	}
	if !s.now().Before(ss.expires) {
		s.end(sid)
		return nil, false
	}
	return ss, true
}

// end removes sid and closes its channel. The caller holds mu.
func (s *Sessions) end(sid string) {
	if ss, ok := s.sessions[sid]; ok {
		close(ss.revoked)
		delete(s.sessions, sid)
	}
}

// sweep ends every session expired at now. The caller holds mu.
func (s *Sessions) sweep(now time.Time) {
	for sid, ss := range s.sessions {
		if !now.Before(ss.expires) {
			s.end(sid)
		}
	}
}

// closed is the channel of every unknown session.
var closed = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

func (s *Sessions) role(sid string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.live(sid)
	if !ok {
		return "", false
	}
	return ss.role, true
}

// Policy is the gate's half of the declarations. Routes that declare
// nothing require a bearer token: forgetting to think about auth is a 401.
func Policy(s *Sessions) geta.Policy {
	return geta.Policy{
		Default: []geta.Scheme{geta.Bearer},
		Verifiers: map[string]geta.Verifier{
			geta.Bearer.Name: func(r *http.Request) (context.Context, error) {
				token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
				role, known := tokens[token]
				if !ok || !known {
					return nil, geta.ErrUnauthenticated
				}
				return Caller.With(r.Context(), Principal{Role: role}), nil
			},
			CookieAuth.Name: func(r *http.Request) (context.Context, error) {
				c, err := r.Cookie("sid")
				if err != nil {
					return nil, geta.ErrUnauthenticated
				}
				role, ok := s.role(c.Value)
				if !ok {
					return nil, geta.ErrUnauthenticated
				}
				return Caller.With(r.Context(), Principal{Role: role, Session: c.Value}), nil
			},
		},
	}
}

// RequireRole answers 403 unless the caller holds role. Authorization is
// ordinary middleware: the gate answers "who are you", this answers "may
// you", which keeps 401 and 403 apart.
func RequireRole(role string) geta.Middleware {
	return geta.Ordered(geta.OrderAuthorize, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if p, ok := Caller.Value(r.Context()); !ok || p.Role != role {
				geta.WriteProblem(w, http.StatusForbidden, `requires the "`+role+`" role`)
				return
			}
			next.ServeHTTP(w, r)
		})
	}).Answers(http.StatusForbidden, "The caller lacks the role")
}
