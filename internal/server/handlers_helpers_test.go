package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/tylergoza/event-planner/internal/store"
)

func TestHelpersTab(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	annID, _ := st.CreateUser("ann", "Ann Lee", "a long password", store.RolePlanner)
	bobID, _ := st.CreateUser("bob", "", "a long password", store.RolePlanner)
	ann := c.as("ann")
	bob := c.as("bob")
	evID, err := st.CreateEvent(&store.Event{Title: "Harvest Dinner", StartsOn: "2099-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	page := "/events/" + itoa(evID) + "/helpers"

	if body := ann.get(page, 200); !strings.Contains(body, "No jobs yet") || !strings.Contains(body, "0 of 0") {
		t.Error("empty helpers page should say there are no jobs")
	}

	// Add a job, then paste two.
	ann.post(page, "/events/"+itoa(evID)+"/helper-roles", url.Values{"name": {"Greeters"}, "slots": {"2"}, "when_text": {"5:00"}}, 200)
	ann.post(page, "/events/"+itoa(evID)+"/helper-roles", url.Values{"name": {""}, "slots": {"2"}}, 200) // rejected, flash only
	ann.post(page, "/events/"+itoa(evID)+"/helper-roles/paste", url.Values{"text": {"Dishes x3\nParking"}}, 200)
	roles, _ := st.ListHelperRoles(evID)
	if len(roles) != 3 || roles[0].Slots != 2 || roles[1].Slots != 3 || roles[2].Name != "Parking" {
		t.Fatalf("roles %+v", roles)
	}
	g := itoa(roles[0].ID)
	signup := "/helper-roles/" + g + "/signup"

	// Sign up a user, a typed name; both together or neither is rejected.
	ann.post(page, signup, url.Values{"user_id": {itoa(annID)}}, 200)
	ann.post(page, signup, url.Values{"name": {"Visitor Vic"}, "phone": {"555-0100"}}, 200)
	ann.post(page, signup, url.Values{"user_id": {itoa(annID)}, "name": {"Both"}}, 200)
	ann.post(page, signup, url.Values{}, 200)
	ann.post(page, signup, url.Values{"user_id": {itoa(annID)}}, 200) // duplicate
	r, _ := st.GetHelperRole(roles[0].ID)
	if len(r.Helpers) != 2 || r.Helpers[0].Name != "Ann Lee" || r.Helpers[0].UserID != annID || r.Helpers[1].Name != "Visitor Vic" || r.Helpers[1].Phone != "555-0100" {
		t.Fatalf("helpers %+v", r.Helpers)
	}

	body := ann.get(page, 200)
	for _, want := range []string{"Greeters", "2 of 2", "full", "Visitor Vic", "555-0100", "Take me off", "Sign me up"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}

	// Over by one.
	ann.post(page, signup, url.Values{"name": {"Extra"}}, 200)
	if body := ann.get(page, 200); !strings.Contains(body, "over by 1") {
		t.Error("page should say the job is over by 1")
	}

	// Sign me up / take me off.
	d := itoa(roles[1].ID)
	bob.post(page, "/helper-roles/"+d+"/me", url.Values{}, 200)
	if r, _ := st.GetHelperRole(roles[1].ID); len(r.Helpers) != 1 || r.Helpers[0].UserID != bobID {
		t.Fatalf("bob not signed up: %+v", r.Helpers)
	}
	bob.post(page, "/helper-roles/"+d+"/me", url.Values{}, 200) // already on: flash only
	bob.post(page, "/helper-roles/"+d+"/me/delete", url.Values{}, 200)
	if r, _ := st.GetHelperRole(roles[1].ID); len(r.Helpers) != 0 {
		t.Fatalf("bob still on: %+v", r.Helpers)
	}

	// Edit and remove a helper; user rows keep their name.
	vic := r.Helpers[1]
	ann.get("/helpers/"+itoa(vic.ID)+"/edit", 200)
	ann.post(page, "/helpers/"+itoa(vic.ID)+"/edit", url.Values{"name": {"Victor"}, "phone": {"555"}}, 200)
	ann.post(page, "/helpers/"+itoa(r.Helpers[0].ID)+"/edit", url.Values{"name": {"Hacked"}}, 200)
	r, _ = st.GetHelperRole(roles[0].ID)
	if r.Helpers[1].Name != "Victor" || r.Helpers[0].Name != "Ann Lee" {
		t.Errorf("after edit %+v", r.Helpers)
	}
	ann.post(page, "/helpers/"+itoa(vic.ID)+"/delete", url.Values{}, 200)
	if r, _ = st.GetHelperRole(roles[0].ID); len(r.Helpers) != 2 {
		t.Errorf("%d helpers after delete, want 2", len(r.Helpers))
	}

	// Edit, move and delete a job.
	ann.get("/helper-roles/"+g+"/edit", 200)
	ann.post(page, "/helper-roles/"+g, url.Values{"name": {"Door team"}, "slots": {"4"}}, 200)
	ann.post(page, "/helper-roles/"+g, url.Values{"name": {""}, "slots": {"4"}}, 422)
	ann.post(page, "/helper-roles/"+g, url.Values{"name": {"x"}, "slots": {"1"}, "room_id": {"999"}}, 422)
	ann.post(page, "/helper-roles/"+g+"/move", url.Values{"dir": {"down"}, "next": {page}}, 200)
	roles, _ = st.ListHelperRoles(evID)
	if roles[0].Name != "Dishes" || roles[1].Name != "Door team" || roles[1].Slots != 4 {
		t.Errorf("roles after edit/move %+v", roles)
	}
	ann.post(page, "/helper-roles/"+g+"/delete", url.Values{}, 200)
	if roles, _ = st.ListHelperRoles(evID); len(roles) != 2 {
		t.Errorf("%d roles after delete, want 2", len(roles))
	}
}

func TestHelpersLockedEvent(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	st.CreateUser("bob", "", "a long password", store.RolePlanner)
	boss, bob := c.as("boss"), c.as("bob")
	evID, _ := st.CreateEvent(&store.Event{Title: "Staff Retreat", StartsOn: "2099-01-01", Locked: true})
	role, _ := st.CreateHelperRole(&store.HelperRole{EventID: evID, Name: "Cook", Slots: 1})
	hid, _ := st.SignUpName(role, "Pat", "")
	if e, _ := st.GetEvent(evID); !e.Locked {
		t.Fatal("event should be locked")
	}

	boss.get("/helper-roles/"+itoa(role)+"/edit", 200)
	bob.get("/events/"+itoa(evID)+"/helpers", 404)
	bob.get("/helper-roles/"+itoa(role)+"/edit", 404)
	bob.get("/helpers/"+itoa(hid)+"/edit", 404)
	bob.post("/events", "/helper-roles/"+itoa(role), url.Values{"name": {"x"}, "slots": {"1"}}, 404)
	bob.post("/events", "/helper-roles/"+itoa(role)+"/me", url.Values{}, 404)
	bob.post("/events", "/helper-roles/"+itoa(role)+"/signup", url.Values{"name": {"x"}}, 404)
	bob.post("/events", "/helper-roles/"+itoa(role)+"/move", url.Values{"dir": {"up"}}, 404)
	bob.post("/events", "/helpers/"+itoa(hid)+"/delete", url.Values{}, 404)
	bob.get("/helper-roles/9999/edit", 404)
	if r, _ := st.GetHelperRole(role); len(r.Helpers) != 1 || r.Name != "Cook" {
		t.Errorf("locked event changed: %+v", r)
	}
}
