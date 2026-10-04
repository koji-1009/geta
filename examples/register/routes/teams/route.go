package teamlist

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/auth"
	"github.com/koji-1009/geta/examples/register/teams"
)

// Route is /teams. Any caller may list; only an administrator creates, as
// with /users.
func Route(env *app.Env) geta.Route {
	h := Handler{Teams: env.Teams}
	return geta.Route{
		Get: geta.Op(http.StatusOK, h.List, geta.Doc{Summary: "List teams"}),
		Post: geta.Op(http.StatusCreated, h.Create, geta.Doc{
			Summary:  "Create a team",
			Failures: []geta.Failure{geta.On(teams.ErrTeamExists, http.StatusConflict, "a team with this id exists")},
			Scope:    geta.Scope{auth.RequireAdmin()},
		}),
	}
}

type Store interface {
	List(ctx context.Context) ([]teams.Team, error)
	Create(ctx context.Context, t teams.Team) error
}

type Handler struct{ Teams Store }

type TeamList struct {
	Items []teams.Team `json:"items"`
}

func (h Handler) List(ctx context.Context, _ *struct{}) (*TeamList, error) {
	ts, err := h.Teams.List(ctx)
	if err != nil {
		return nil, err
	}
	return &TeamList{Items: ts}, nil
}

type NewTeam struct {
	ID          string  `json:"id" schema:"minLength=1,maxLength=64,pattern=^[a-z0-9-]+$"`
	Name        string  `json:"name" schema:"minLength=1,maxLength=100"`
	Description *string `json:"description,omitzero" schema:"maxLength=500"`
}

type CreateIn struct {
	Body NewTeam `body:"json"`
}

type TeamCreated struct {
	Location string `header:"Location"`
}

func (h Handler) Create(ctx context.Context, in *CreateIn) (*TeamCreated, error) {
	if err := h.Teams.Create(ctx, teams.Team{ID: in.Body.ID, Name: in.Body.Name, Description: in.Body.Description}); err != nil {
		return nil, err
	}
	return &TeamCreated{Location: "/teams/" + in.Body.ID}, nil
}
