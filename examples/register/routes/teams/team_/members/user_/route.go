package member

import (
	"context"
	"net/http"
	"slices"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/failures"
	"github.com/koji-1009/geta/examples/register/model"
	team "github.com/koji-1009/geta/examples/register/routes/teams/team_"
	"github.com/koji-1009/geta/examples/register/teams"
)

// Route is /teams/{team}/members/{user}. Only an administrator reaches it
// (../scope.go).
func Route(env *app.Env) geta.Route {
	h := Handler{Teams: env.Teams, Users: env.Users}
	return geta.Route{
		// Adding a member reads the user store, which may be a database:
		// its unavailable and transient errors are 503s here as on /users.
		Put: geta.OpNoBody(http.StatusNoContent, h.Put, geta.Doc{
			Summary: "Add a user to a team",
			Failures: slices.Concat([]geta.Failure{
				team.TeamNotFound,
				failures.UserNotFound,
				geta.On(teams.ErrAlreadyMember, http.StatusConflict, "the user is already a member"),
			}, failures.Store),
		}),
		Delete: geta.OpNoBody(http.StatusNoContent, h.Delete, geta.Doc{
			Summary: "Remove a user from a team",
			Failures: []geta.Failure{
				team.TeamNotFound,
				geta.On(teams.ErrNotMember, http.StatusNotFound, "the user is not a member"),
			},
		}),
	}
}

type Teams interface {
	AddMember(ctx context.Context, team, user string) error
	RemoveMember(ctx context.Context, team, user string) error
}

type Users interface {
	Find(ctx context.Context, id string) (*model.User, error)
}

type Handler struct {
	Teams Teams
	Users Users
}

type Path struct {
	Team string `path:"team" schema:"maxLength=64"`
	User string `path:"user" schema:"maxLength=64"`
}

type PutIn struct{ Path }
type DeleteIn struct{ Path }

func (h Handler) Put(ctx context.Context, in *PutIn) error {
	if _, err := h.Users.Find(ctx, in.User); err != nil {
		return err
	}
	return h.Teams.AddMember(ctx, in.Team, in.User)
}

func (h Handler) Delete(ctx context.Context, in *DeleteIn) error {
	return h.Teams.RemoveMember(ctx, in.Team, in.User)
}
