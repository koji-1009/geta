package main

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/koji-1009/geta/examples/booking/model"
	"github.com/koji-1009/geta/examples/booking/routes/rooms"
)

// GET /rooms filters by a repeated parameter (feature), a deepObject
// (capacity[min], capacity[max]), and an integer enum (floor), and pages by a
// parameter with a default (limit). Each value is held to its contract
// before the handler runs.
func TestListRoomsByFilter(t *testing.T) {
	h := newHarness(t)
	desk, viewer := h.as("frontdesk"), h.as("viewer")
	add := func(name string, capacity, floor int, features ...string) {
		t.Helper()
		body := map[string]any{"name": name, "capacity": capacity, "opens": "08:00:00Z", "closes": "18:00:00Z",
			"hourlyRate": "1000.00", "floor": floor, "features": features}
		status(t, desk.Post("/rooms", body), http.StatusCreated)
	}
	add("Ume", 4, 1, "whiteboard")
	add("Sakura", 8, 2, "projector", "whiteboard")
	add("Kaede", 12, 2, "projector", "video")
	add("Hinoki", 30, 3, "projector", "video", "whiteboard")
	// A room created without a floor is on floor 1, the default, and one
	// without features has none.
	res := status(t, desk.Post("/rooms", map[string]any{"name": "Kiri", "capacity": 2, "opens": "08:00:00Z", "closes": "18:00:00Z",
		"hourlyRate": "500.00"}), http.StatusCreated)
	if r := res.JSON[model.Room](); r.Floor != 1 || r.Features == nil || len(r.Features) != 0 {
		t.Fatalf("a room created with defaults: %+v", r)
	}

	names := func(query string) []string {
		t.Helper()
		list := status(t, viewer.Get("/rooms"+query), http.StatusOK).JSON[rooms.RoomList]()
		var out []string
		for _, r := range list.Items {
			out = append(out, r.Name)
		}
		return out
	}
	for query, want := range map[string][]string{
		"":                                    {"Ume", "Sakura", "Kaede", "Hinoki", "Kiri"},
		"?feature=projector":                  {"Sakura", "Kaede", "Hinoki"},
		"?feature=projector&feature=video":    {"Kaede", "Hinoki"},
		"?capacity[min]=8&capacity[max]=12":   {"Sakura", "Kaede"},
		"?capacity[min]=10":                   {"Kaede", "Hinoki"},
		"?floor=2":                            {"Sakura", "Kaede"},
		"?floor=2&feature=whiteboard":         {"Sakura"},
		"?feature=whiteboard&limit=2":         {"Ume", "Sakura"},
		"?feature=sauna":                      nil,
		"?capacity%5Bmin%5D=20&feature=video": {"Hinoki"},
	} {
		if got := names(query); !slices.Equal(got, want) {
			t.Errorf("GET /rooms%s: %v, want %v", query, got, want)
		}
	}

	// What the contract refuses is a 400 naming the parameter.
	for query, path := range map[string]string{
		"?floor=4":           "floor",
		"?floor=one":         "floor",
		"?capacity[min]=0":   "capacity[min]",
		"?capacity[seats]=4": "capacity[seats]",
		"?limit=0":           "limit",
		"?limit=101":         "limit",
		"?feature=a&feature=b&feature=c&feature=d&feature=e&feature=f&feature=g&feature=h&feature=i": "feature",
	} {
		res := status(t, viewer.Get("/rooms"+query), http.StatusBadRequest)
		if p := res.Problem(); len(p.Errors) != 1 || p.Errors[0].Path != path {
			t.Errorf("GET /rooms%s: %+v, want one violation at %s", query, p.Errors, path)
		}
	}
	status(t, desk.Post("/rooms", map[string]any{"name": "X", "capacity": 1, "opens": "08:00:00Z", "closes": "09:00:00Z",
		"hourlyRate": "1.00", "floor": 9}), http.StatusBadRequest)
}

// The document says what each parameter means and takes: its description,
// the default and examples of limit, the deepObject style of capacity, and
// the floors there are.
func TestListParametersAreDocumented(t *testing.T) {
	h := newHarness(t)
	var doc struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				Required    bool           `json:"required"`
				Style       string         `json:"style"`
				Explode     *bool          `json:"explode"`
				Schema      map[string]any `json:"schema"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(h.anon.App().OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	params := map[string]map[string]any{}
	for _, p := range doc.Paths["/rooms"]["get"].Parameters {
		if p.Description == "" || p.Required {
			t.Errorf("%s: description %q, required %v", p.Name, p.Description, p.Required)
		}
		params[p.Name] = p.Schema
		if p.Name == "capacity" && (p.Style != "deepObject" || p.Explode == nil || !*p.Explode) {
			t.Errorf("capacity is %q, explode %v", p.Style, p.Explode)
		}
	}
	limit, floor, feature := params["limit"], params["floor"], params["feature"]
	if limit["default"] != 20.0 || !slices.Equal(limit["examples"].([]any), []any{5.0, 50.0}) {
		t.Errorf("limit: %v", limit)
	}
	if !slices.Equal(floor["enum"].([]any), []any{1.0, 2.0, 3.0}) {
		t.Errorf("floor: %v", floor)
	}
	if feature["type"] != "array" {
		t.Errorf("feature: %v", feature)
	}
}
