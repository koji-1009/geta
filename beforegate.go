package geta

import (
	"fmt"
	"net/http"
	"slices"
)

// firstGate returns the index of chain's first [Secure] gate, or -1.
func firstGate(chain []Middleware) int {
	return slices.IndexFunc(chain, func(m Middleware) bool { return m.gate != nil })
}

// checkBeforeGate returns an error for a Doc.BeforeGate holding an invalid
// middleware, a root-only one, or a gate.
func checkBeforeGate(s Scope, where string) error {
	if err := checkScope(s, where); err != nil {
		return err
	}
	if err := checkBelowRoot(s, where); err != nil {
		return err
	}
	for i, m := range s {
		if m.gate != nil {
			return fmt.Errorf("%s: middleware %d is a geta.Secure gate; use Doc.Scope", where, i)
		}
	}
	return nil
}

// rootChain returns root with a rootSlot inserted before its first gate when
// some operation's Doc.BeforeGate runs in the root scope.
func (a *App) rootChain(root Scope) Scope {
	byMatch := map[*Match]Scope{}
	for m, op := range a.byMatch {
		if len(op.before) > 0 {
			byMatch[m] = op.before
		}
	}
	if len(byMatch) == 0 {
		return root
	}
	g := firstGate(root)
	return slices.Concat(root[:g], Scope{rootSlot(byMatch)}, root[g:])
}

// rootSlot runs the matched operation's Doc.BeforeGate, then the rest of the
// root scope. A request that will be redirected runs none. The match is
// recorded so dispatch can refuse an operation whose BeforeGate did not run.
func rootSlot(byMatch map[*Match]Scope) Middleware {
	return Use(func(next http.Handler) http.Handler {
		hs := make(map[*Match]http.Handler, len(byMatch))
		for m, s := range byMatch {
			hs[m] = wrap(s, next)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rc := requestFrom(r.Context()); rc != nil {
				rc.rematch(r)
				if h := hs[rc.match]; h != nil {
					if _, _, served := rc.served(); served {
						rc.before = rc.match
						h.ServeHTTP(w, r)
						return
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	})
}

// skippedBefore reports whether rt's root-scope Doc.BeforeGate did not run
// for it, because a root middleware after the gate moved the request.
func (c *requestContext) skippedBefore(rt *route) bool {
	return len(rt.op.before) > 0 && c.before != rt.match
}

// refuseSkippedBefore answers 500 for an operation whose Doc.BeforeGate did
// not run.
func (c *requestContext) refuseSkippedBefore(w http.ResponseWriter, r *http.Request, rt *route) {
	rt.op.writeDefect(w, r, "geta: a root middleware rewrote the request after a Secure gate",
		fmt.Errorf("request reached %s without its Doc.BeforeGate; move the rewrite before geta.Secure", rt.pattern))
}
