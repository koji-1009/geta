// Package routes is the bookmarks example's route tree. Each directory below
// is one URL; zz_routes.go is the table geta sync writes from the tree.
package routes

import (
	"net/http"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/bookmarks/app"
	"github.com/koji-1009/geta/examples/bookmarks/auth"
	"github.com/koji-1009/geta/examples/bookmarks/clientip"
	"github.com/koji-1009/geta/examples/bookmarks/ratelimit"
)

// Env is the one name the table refers to.
type Env = *app.Env

// Scope wraps the whole dispatch, 404 and 405 included:
//
//   - the per-route counts (geta.Use, unordered), first, so they count what
//     every middleware after them answers;
//   - CORS for the page's origin alone, with credentials, so the page's
//     fetch(..., {credentials: "include"}) sends the session cookie and may
//     read the answer. "*" cannot be combined with credentials (geta.New
//     refuses it), and a page on any other origin gets no CORS headers, so
//     its browser keeps the answer from it;
//   - cross-origin protection: CORS decides what a page may read, not what
//     it may send, and a page on another origin can still make a browser
//     POST with the user's cookie. net/http's CrossOriginProtection refuses
//     any request but GET, HEAD and OPTIONS that a browser marks as coming
//     from another origin (Sec-Fetch-Site, or Origin against Host), but the
//     page's own; requests from programs, which send neither, pass. It sits
//     after CORS, so its 403 carries the headers that let the page read it;
//   - recover; the rate limit per client address, read through the proxies
//     this deployment runs (clientip.Key), which is this example's own
//     limiter (ratelimit.PerKey) declaring its 429 and Retry-After; and the
//     gate, whose default is the session cookie.
func Scope(env Env) geta.Scope {
	cop := http.NewCrossOriginProtection()
	if err := cop.AddTrustedOrigin(env.PageOrigin); err != nil {
		return geta.Scope{geta.Invalid("cross-origin protection", err)}
	}
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		geta.WriteProblem(w, http.StatusForbidden, "cross-origin request refused")
	}))
	return geta.Scope{
		env.Metrics.Middleware(),
		geta.CORS(geta.AllowOrigins(env.PageOrigin), geta.AllowCredentials(), geta.MaxAge(10*time.Minute)),
		geta.Use(cop.Handler).Answers(http.StatusForbidden, "A browser request from another origin than the page's"),
		geta.Recover(env.Log),
		ratelimit.PerKey(clientip.Key(env.TrustedProxies), env.RatePerMinute, time.Minute),
		geta.Secure(auth.Policy(env.Sessions)),
	}
}
