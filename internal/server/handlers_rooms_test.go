package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tylergoza/event-planner/internal/store"
)

func roomsTracker(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer mt_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/places":
			io.WriteString(w, `{"places":[
				{"id":1,"kind":"building","name":"Main","path":"Main"},
				{"id":2,"parent_id":1,"kind":"room","name":"Fellowship Hall","number":"101","path":"Main › Fellowship Hall"},
				{"id":3,"parent_id":1,"kind":"shelf","name":"Hall shelf","path":"Main › Hall shelf"}]}`)
		case "/api/v1/places/2":
			io.WriteString(w, `{"id":2,"parent_id":1,"kind":"room","name":"Fellowship Hall","path":"Main › Fellowship Hall"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"not found"}`)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestRoomsTab(t *testing.T) {
	c, st := newTestServer(t)
	tr := roomsTracker(t)
	c.post("/setup", "/setup", url.Values{
		"site_name": {"Events"}, "username": {"pat"}, "display_name": {"Pat"},
		"password": {"a long password"}, "password_confirm": {"a long password"},
	}, 200)
	c.post("/admin/settings", "/admin/settings", url.Values{"site_name": {"Events"}, "tracker_url": {tr.URL + "/"}, "tracker_token": {"mt_test"}}, 200)
	c.post("/events/new", "/events", url.Values{"title": {"Dinner"}, "status": {"planning"}, "starts_on": {"2099-01-01"}}, 200)

	if body := c.get("/events/1/rooms", 200); !strings.Contains(body, "No rooms yet") {
		t.Error("expected empty state")
	}
	// Search shows rooms and areas, not shelves.
	body := c.get("/events/1/rooms?q=hall", 200)
	if !strings.Contains(body, "Fellowship Hall") || strings.Contains(body, "Hall shelf") {
		t.Errorf("search results wrong:\n%s", body)
	}
	// Add from the tracker: the path is stored.
	c.post("/events/1/rooms", "/events/1/rooms", url.Values{"place_id": {"2"}}, 200)
	rooms, _ := st.ListRooms(1)
	if len(rooms) != 1 || rooms[0].PlaceID != 2 || rooms[0].PlaceName != "Main › Fellowship Hall" {
		t.Fatalf("rooms %+v", rooms)
	}
	body = c.get("/events/1/rooms", 200)
	if !strings.Contains(body, "/places/2") {
		t.Error("room should link to the tracker's place page")
	}
	// Twice is refused, friendly.
	if body := c.post("/events/1/rooms", "/events/1/rooms", url.Values{"place_id": {"2"}}, 422); !strings.Contains(body, "already one of this event") {
		t.Error("expected duplicate message")
	}
	// A place the tracker doesn't have.
	c.post("/events/1/rooms", "/events/1/rooms", url.Values{"place_id": {"77"}}, 422)
	// Free text.
	c.post("/events/1/rooms", "/events/1/rooms", url.Values{"place_name": {"Park shelter"}}, 200)
	c.post("/events/1/rooms", "/events/1/rooms", url.Values{"place_name": {" "}}, 422)
	rooms, _ = st.ListRooms(1)
	if len(rooms) != 2 || rooms[1].PlaceID != 0 {
		t.Fatalf("rooms %+v", rooms)
	}

	// Edit.
	if body := c.get("/rooms/2/edit", 200); !strings.Contains(body, `name="place_name"`) {
		t.Error("free-text room should have an editable name")
	}
	if body := c.get("/rooms/1/edit", 200); strings.Contains(body, `name="place_name"`) {
		t.Error("tracker room name shouldn't be editable")
	}
	c.post("/rooms/1/edit", "/rooms/1", url.Values{"purpose": {"Seating"}, "setup": {"8 tables\n64 chairs"}, "notes": {"n"}, "place_name": {"hack"}}, 200)
	rooms, _ = st.ListRooms(1)
	if rooms[0].Purpose != "Seating" || rooms[0].Setup != "8 tables\n64 chairs" || rooms[0].PlaceName != "Main › Fellowship Hall" {
		t.Errorf("after edit %+v", rooms[0])
	}
	c.post("/rooms/2/edit", "/rooms/2", url.Values{"place_name": {""}}, 422)

	// Move, print, delete.
	c.post("/events/1/rooms", "/rooms/2/move", url.Values{"dir": {"up"}, "next": {"/events/1/rooms"}}, 200)
	if rooms, _ = st.ListRooms(1); rooms[0].ID != 2 {
		t.Error("move up didn't reorder")
	}
	if body := c.get("/events/1/rooms?print=1", 200); !strings.Contains(body, "64 chairs") || !strings.Contains(body, "room set-up") {
		t.Error("print sheet missing setup")
	}
	c.post("/events/1/rooms", "/rooms/1/delete", url.Values{}, 200)
	if rooms, _ = st.ListRooms(1); len(rooms) != 1 {
		t.Errorf("after delete %+v", rooms)
	}
}

func TestRoomsTrackerDown(t *testing.T) {
	c, st := newTestServer(t)
	c.post("/setup", "/setup", url.Values{
		"site_name": {"Events"}, "username": {"pat"}, "display_name": {"Pat"},
		"password": {"a long password"}, "password_confirm": {"a long password"},
	}, 200)
	c.post("/admin/settings", "/admin/settings", url.Values{
		"site_name": {"P"}, "tracker_url": {"http://127.0.0.1:1"}, "tracker_token": {"mt_x"},
	}, 200)
	c.post("/events/new", "/events", url.Values{"title": {"Dinner"}, "status": {"planning"}, "starts_on": {"2099-01-01"}}, 200)
	st.AddRoom(1, 2, "Main › Fellowship Hall")
	body := c.get("/events/1/rooms", 200)
	if !strings.Contains(body, "Fellowship Hall") {
		t.Error("cached room name should still show")
	}
	if body := c.get("/events/1/rooms?q=hall", 200); !strings.Contains(body, "Couldn't check the maintenance tracker") {
		t.Error("search should say the tracker couldn't be reached")
	}
	if body := c.post("/events/1/rooms", "/events/1/rooms", url.Values{"place_id": {"3"}}, 422); !strings.Contains(body, "reach the maintenance tracker") {
		t.Error("add should say the tracker couldn't be reached")
	}
}

func TestRoomsLocked(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	st.CreateUser("bob", "", "a long password", store.RolePlanner)
	boss, bob := c.as("boss"), c.as("bob")
	boss.post("/events/new", "/events", url.Values{"title": {"Secret"}, "status": {"planning"}, "starts_on": {"2099-01-01"}, "access": {"locked"}}, 200)
	st.AddRoom(1, 0, "Hall")
	boss.get("/rooms/1/edit", 200)
	bob.get("/rooms/1/edit", 404)
	bob.get("/events/1/rooms", 404)
	bob.post("/events", "/rooms/1", url.Values{"purpose": {"x"}}, 404)
	bob.post("/events", "/rooms/1/delete", url.Values{}, 404)
	bob.post("/events", "/rooms/1/move", url.Values{"dir": {"up"}}, 404)
	bob.post("/events", "/events/1/rooms", url.Values{"place_name": {"x"}}, 404)
	if rooms, _ := st.ListRooms(1); len(rooms) != 1 || rooms[0].Purpose != "" {
		t.Errorf("bob changed something: %+v", rooms)
	}
}
