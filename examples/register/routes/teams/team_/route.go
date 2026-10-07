package team

import (
	"context"
	"net/http"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/auth"
	"github.com/koji-1009/geta/examples/register/teams"
)

// TeamNotFound is shared by every operation under /teams/{team}.
var TeamNotFound = geta.On(teams.ErrTeamNotFound, http.StatusNotFound, "team not found")

// Route is /teams/{team}. Any caller may fetch; only an administrator
// writes. Every answer carries the team's ETag, and every write may be
// conditional on it:
//
//   - PUT creates the team or replaces it, the client choosing the id.
//     If-None-Match: * makes it a create that refuses to replace (412 when
//     the team exists); If-Match with a tag makes it a replace of that
//     version (412 when the team has changed or does not exist).
//   - PATCH changes some members of the team and keeps the rest, as a JSON
//     Merge Patch (RFC 7396) does: a member left out is kept, null clears an
//     optional member, and a value sets it. If-Match makes it a change of the
//     version the client read.
func Route(env *app.Env) geta.Route {
	h := Handler{Teams: env.Teams}
	admin := geta.Scope{auth.RequireAdmin()}
	return geta.Route{
		Get: geta.Op(http.StatusOK, h.Get, geta.Doc{
			Summary:  "Fetch a team",
			Failures: []geta.Failure{TeamNotFound},
		}),
		Put: geta.Op(http.StatusOK, h.Put, geta.Doc{
			Summary: "Create or replace a team",
			Description: "Creates the team when there is none and replaces its name and description when there is; " +
				"its members are kept. Send If-None-Match: * to create only, or If-Match: <ETag> to replace only the version you hold.",
			Scope: admin,
		}),
		Patch: geta.Op(http.StatusOK, h.Patch, geta.Doc{
			Summary:     "Change a team",
			Description: "A JSON Merge Patch: a member left out is kept, null clears it, a value sets it.",
			Failures:    []geta.Failure{TeamNotFound},
			Scope:       admin,
		}),
		Delete: geta.OpNoBody(http.StatusNoContent, h.Delete, geta.Doc{
			Summary: "Delete an empty team",
			Failures: []geta.Failure{
				TeamNotFound,
				geta.On(teams.ErrTeamNotEmpty, http.StatusConflict, "the team still has members"),
			},
			Scope: admin,
		}),
	}
}

type Store interface {
	Find(ctx context.Context, id string) (*teams.Team, string, error)
	Put(ctx context.Context, t teams.Team, check func(current *teams.Team, tag string) error) (*teams.Team, string, bool, error)
	Update(ctx context.Context, id string, apply func(current teams.Team, tag string) (teams.Team, error)) (*teams.Team, string, error)
	Delete(ctx context.Context, id string) error
}

type Handler struct{ Teams Store }

type Path struct {
	Team string `path:"team" schema:"minLength=1,maxLength=64,pattern=^[a-z0-9-]+$"`
}

type GetIn struct {
	geta.Conditional
	Path
}

// TeamFields are what a PUT sends: the whole team but its id, which is the
// path's, and its members, which change through /teams/{team}/members.
type TeamFields struct {
	Name        string  `json:"name" schema:"minLength=1,maxLength=100"`
	Description *string `json:"description,omitzero" schema:"maxLength=500"`
}

type PutIn struct {
	geta.Conditional
	Path
	Body TeamFields `body:"json" doc:"The team as it is to be; a description left out is none"`
}

// TeamPatch is a JSON Merge Patch of a team. Every member is optional
// (omitzero), and a member left out keeps its value. Name is a pointer: it
// may be left out, but not cleared, and a null for it is a 400. Description
// is a geta.Nullable: left out, null, and a value are three states, keep,
// clear, and set. A client sends it as application/merge-patch+json, the
// media type a PATCH's body:"json" takes.
type TeamPatch struct {
	Name        *string               `json:"name,omitzero" doc:"The new name; left out, the name is kept" schema:"minLength=1,maxLength=100"`
	Description geta.Nullable[string] `json:"description,omitzero" doc:"The new description; null removes it, left out it is kept" schema:"maxLength=500"`
}

type PatchIn struct {
	geta.Conditional
	Path
	Body TeamPatch `body:"json"`
}

type DeleteIn struct{ Path }

// Tagged is a team and its entity tag.
type Tagged struct {
	ETag string     `header:"ETag" doc:"The team's version; send it in If-Match to write only that version"`
	Team teams.Team `body:"json"`
}

func (h Handler) Get(ctx context.Context, in *GetIn) (*Tagged, error) {
	t, tag, err := h.Teams.Find(ctx, in.Team)
	if err != nil {
		return nil, err
	}
	if err := in.Check(tag, time.Time{}); err != nil {
		return nil, err
	}
	return &Tagged{ETag: tag, Team: *t}, nil
}

func (h Handler) Put(ctx context.Context, in *PutIn) (*Tagged, error) {
	want := teams.Team{ID: in.Team, Name: in.Body.Name, Description: in.Body.Description}
	// The preconditions are checked inside the store's write, against the
	// team as it is then: CheckAbsent where there is none (If-Match fails,
	// If-None-Match: * holds), Check against its tag where there is one.
	t, tag, _, err := h.Teams.Put(ctx, want, func(current *teams.Team, tag string) error {
		if current == nil {
			return in.CheckAbsent()
		}
		return in.Check(tag, time.Time{})
	})
	if err != nil {
		return nil, err
	}
	return &Tagged{ETag: tag, Team: *t}, nil
}

func (h Handler) Patch(ctx context.Context, in *PatchIn) (*Tagged, error) {
	t, tag, err := h.Teams.Update(ctx, in.Team, func(current teams.Team, tag string) (teams.Team, error) {
		if err := in.Check(tag, time.Time{}); err != nil {
			return current, err
		}
		p := in.Body
		if p.Name != nil {
			current.Name = *p.Name
		}
		if d, ok := p.Description.Get(); ok {
			current.Description = &d
		} else if p.Description.IsNull() {
			current.Description = nil
		}
		return current, nil
	})
	if err != nil {
		return nil, err
	}
	return &Tagged{ETag: tag, Team: *t}, nil
}

func (h Handler) Delete(ctx context.Context, in *DeleteIn) error {
	return h.Teams.Delete(ctx, in.Team)
}
