package geta_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/koji-1009/geta"
)

type reportQuotaErr struct{}

func (reportQuotaErr) Error() string { return "q" }

type reportQuota struct {
	Left int `json:"left"`
}

// A middleware's plain problem is a cause of its status even when a row
// that describes its problem gives the same reason: the status is either.
func TestAMiddlewaresProblemBesideARowOfTheSameReason(t *testing.T) {
	mw := geta.Use(func(h http.Handler) http.Handler { return h }).Answers(403, "Forbidden")
	tbl := withRoot(one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Failures: []geta.Failure{
		geta.OnAsProblem(403, "", func(reportQuotaErr) reportQuota { return reportQuota{} }),
	}})}), mw)
	got := compact(t, at(t, doc(t, accepts(t, tbl)), "paths", "/x", "get", "responses", "403", "content"))
	if !strings.Contains(got, `"anyOf"`) || !strings.Contains(got, `{"$ref":"#/components/schemas/GetaProblem"}`) {
		t.Error(got)
	}
}

// Paths differing only in case share their OPTIONS' operationId, and are
// refused, whatever methods they serve.
func TestPathsSharingAnOptionsIDAreRefused(t *testing.T) {
	rejects(t, geta.Table{Routes: []geta.Entry{
		{Path: "/aB", Route: get(okHandler)},
		{Path: "/a/b", Route: geta.Route{Post: geta.Op(http.StatusOK, okHandler, geta.Doc{})}},
	}}, `operationId "optionsAB"`)
}

type hijack struct{ geta.Upgrade }

// Only geta's own types are special outputs: a type embedding geta.Upgrade
// is an output like any other, refused for what it holds, never served as
// an upgrade it is not.
func TestATypeEmbeddingASpecialOutputIsNone(t *testing.T) {
	rejects(t, one("/x", geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, func(context.Context, *empty) (*hijack, error) { return nil, nil }, geta.Doc{})}),
		"has no JSON form")
}

type reportBody struct {
	X int `json:"x"`
}

type reportIn struct {
	Body reportBody `body:"json"`
}

type reportCondIn struct{ geta.Conditional }

// A page may read the headers geta sends with its own answers: a 415's
// Accept-Encoding, and a Conditional 304's ETag.
func TestCORSExposesGetasOwnHeaders(t *testing.T) {
	a := accepts(t, withRoot(geta.Table{Routes: []geta.Entry{
		{Path: "/p", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *reportIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
		{Path: "/c", Route: get(func(_ context.Context, in *reportCondIn) (*ok, error) {
			if err := in.Check(`"v"`, time.Time{}); err != nil {
				return nil, err
			}
			return &ok{true}, nil
		})},
	}}, geta.CORS(geta.AllowOrigins("https://a.example"))))
	r := do(t, a, "POST", "/p", "Origin", "https://a.example", "Content-Type", "text/plain")
	if !strings.Contains(r.Header().Get("Access-Control-Expose-Headers"), "Accept-Encoding") {
		t.Errorf("%d: %q", r.Code, r.Header().Get("Access-Control-Expose-Headers"))
	}
	r = do(t, a, "GET", "/c", "Origin", "https://a.example", "If-None-Match", `"v"`)
	if r.Code != http.StatusNotModified || !strings.Contains(r.Header().Get("Access-Control-Expose-Headers"), "ETag") {
		t.Errorf("%d: %q", r.Code, r.Header().Get("Access-Control-Expose-Headers"))
	}
}
