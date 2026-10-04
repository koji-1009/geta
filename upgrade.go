package geta

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Upgrade switches the connection to another protocol. An operation whose
// output is *Upgrade answers 101. Every middleware in front of the handler,
// the gate included, runs first, so an unauthenticated request is refused
// before anything switches.
//
// A request that does not ask to upgrade to Protocol answers 426, before
// either mode below runs. Set exactly one of them:
//
//   - Accept hands the request to a library that performs the handshake on an
//     http.ResponseWriter, as WebSocket libraries do (coder/websocket's
//     Accept, gorilla/websocket's Upgrader.Upgrade). The library validates
//     the handshake, writes the 101 or its own refusal, and takes the
//     connection.
//   - Serve is for a protocol with no such library: geta writes the 101,
//     with Header, and hands Serve the connection.
//
// Once the connection is switched, the request's [ConcurrencyLimit] slot is
// freed and every [Timeout] deadline on its context is lifted.
type Upgrade struct {
	// Protocol is the Upgrade token the request must offer, such as
	// websocket, compared without case; a request may add a version
	// ("websocket/13"). A Protocol that is not a token is a 500 defect.
	Protocol string
	// Accept runs the library's handshake on w and r, and the protocol after
	// it, for as long as the connection lasts. w implements http.Hijacker
	// itself. The library's own refusals (a 400 for a malformed key) are not
	// in the document; declare the handshake's request headers as header
	// parameters so geta checks them. An Accept that returns having written
	// nothing is a 500 defect.
	Accept func(w http.ResponseWriter, r *http.Request)
	// Header is sent on the 101 geta writes for Serve, such as a protocol's
	// own accept header. Setting it with Accept is a 500 defect; pass the
	// library its headers instead. geta writes Upgrade and Connection, and
	// Deprecation and Sunset on a deprecated operation; a Header setting any
	// of them is a 500 defect.
	Header http.Header
	// Serve speaks the new protocol after geta has written the 101. The
	// connection is closed when it returns.
	Serve func(conn net.Conn, rw *bufio.ReadWriter)
}

type upgradePlan struct{}

func (*Upgrade) plan(*registry) (specialPlan, error) { return upgradePlan{}, nil }

func (upgradePlan) status() int { return http.StatusSwitchingProtocols }

func (upgradePlan) answers() []answer {
	return []answer{{status: http.StatusUpgradeRequired, reason: "The request does not ask to switch to this protocol"}}
}

func (upgradePlan) document(r map[string]any, _ oasFeatures) {
	r["description"] = "Switching Protocols"
	r["headers"] = map[string]any{
		"Upgrade":    map[string]any{"required": true, "schema": map[string]any{"type": "string"}},
		"Connection": map[string]any{"required": true, "schema": map[string]any{"type": "string"}},
	}
}

func (upgradePlan) write(w http.ResponseWriter, r *http.Request, a *App, op *compiledOp, out any) {
	u := out.(*Upgrade)
	switch {
	case u.Protocol == "" || (u.Serve == nil) == (u.Accept == nil):
		op.writeDefect(w, r, "geta: an upgrade needs a protocol and exactly one of Accept and Serve", errors.New("invalid geta.Upgrade"))
		return
	case !validHeaderName(u.Protocol):
		// No request could name it, and it would go on the 101 as is.
		op.writeDefect(w, r, fmt.Sprintf("geta: Upgrade.Protocol %q is not a token", u.Protocol),
			errors.New("geta.Upgrade with a protocol that is not a token"))
		return
	case u.Accept != nil && u.Header != nil:
		op.writeDefect(w, r, "geta: Upgrade.Header is set with Accept",
			errors.New("geta.Upgrade with Accept and Header"))
		return
	case headerNamed(u.Header, "Upgrade", "Connection") != "":
		// geta writes both from Protocol; a second Upgrade could name
		// another protocol.
		op.writeDefect(w, r, "geta: Upgrade.Header sets "+headerNamed(u.Header, "Upgrade", "Connection")+", which geta writes",
			errors.New("geta.Upgrade with Header naming the switch"))
		return
	case op.deprecated != nil && headerNamed(u.Header, op.deprecated.names()...) != "":
		// geta writes these on the 101 too, and each holds one date.
		name := headerNamed(u.Header, op.deprecated.names()...)
		op.writeDefect(w, r, "geta: Upgrade.Header sets "+name+", which geta writes from Doc."+name,
			errors.New("geta.Upgrade with Header naming a deprecation header"))
		return
	}
	if !headerHasToken(r.Header, "Connection", "upgrade") || !headerHasToken(r.Header, "Upgrade", u.Protocol) {
		w.Header().Set("Upgrade", u.Protocol)
		w.Header().Set("Connection", "Upgrade")
		writeProblem(w, r, http.StatusUpgradeRequired, "this resource requires an upgrade to "+u.Protocol, nil)
		return
	}
	if u.Accept != nil {
		aw := &acceptWriter{ResponseWriter: w, ctx: r.Context()}
		u.Accept(aw, r)
		if !aw.wrote && !aw.hijacked {
			op.writeDefect(w, r, "geta: Upgrade.Accept returned without switching or answering", errors.New("no response"))
		}
		return
	}
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		op.writeDefect(w, r, "geta: the connection cannot be switched", err)
		return
	}
	defer conn.Close()
	switched(r.Context())
	rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: " + u.Protocol + "\r\nConnection: Upgrade\r\n")
	u.Header.Write(rw)
	if op.deprecated != nil {
		// The 101 is a response of the operation too (Doc.Deprecation).
		h := http.Header{}
		op.deprecated.set(h)
		h.Write(rw)
	}
	rw.WriteString("\r\n")
	if err := rw.Flush(); err != nil {
		return
	}
	u.Serve(conn, rw)
}

// switched frees the request's [ConcurrencyLimit] slot and lifts its
// [Timeout] deadlines.
func switched(ctx context.Context) {
	releaseSlot(ctx)
	liftDeadline(ctx)
}

// acceptWriter is the writer [Upgrade.Accept] hands a library. It implements
// http.Hijacker for libraries that assert it (gorilla/websocket) and unwraps
// for those that walk the chain (coder/websocket). A successful hijack calls
// switched.
type acceptWriter struct {
	http.ResponseWriter
	ctx      context.Context
	wrote    bool
	hijacked bool
}

func (a *acceptWriter) WriteHeader(code int) {
	a.wrote = true
	a.ResponseWriter.WriteHeader(code)
}

func (a *acceptWriter) Write(b []byte) (int, error) {
	a.wrote = true
	return a.ResponseWriter.Write(b)
}

func (a *acceptWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(a.ResponseWriter).Hijack()
	if err == nil {
		a.hijacked = true
		switched(a.ctx)
	}
	return conn, rw, err
}

func (a *acceptWriter) Unwrap() http.ResponseWriter { return a.ResponseWriter }

// headerNamed returns the canonical name of the first header in h matching
// one of names in any case, or "".
func headerNamed(h http.Header, names ...string) string {
	for name := range h {
		for _, n := range names {
			if strings.EqualFold(name, n) {
				return http.CanonicalHeaderKey(name)
			}
		}
	}
	return ""
}

// headerHasToken reports whether a comma-separated header lists token in
// ASCII case. Each element is trimmed of OWS only (RFC 9110 §5.6.1), cut
// before any "/" version, and must be a token (§5.6.2).
func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for t := range strings.SplitSeq(v, ",") {
			t, _, _ = strings.Cut(strings.Trim(t, " \t"), "/")
			if validHeaderName(t) && strings.EqualFold(t, token) {
				return true
			}
		}
	}
	return false
}
