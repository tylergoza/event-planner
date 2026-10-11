package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/tylergoza/event-planner/internal/sso"
	"github.com/tylergoza/event-planner/internal/store"
)

func TestHelperCannotPlan(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	annID, _ := st.CreateUser("ann", "Ann", "a long password", store.RoleHelper)
	ev, _ := st.CreateEvent(&store.Event{Title: "Dinner", StartsOn: "2099-01-01"})
	role, _ := st.CreateHelperRole(&store.HelperRole{EventID: ev, Name: "Cook", Slots: 2})
	st.AddRoom(ev, 0, "Hall")
	ann := c.as("ann")
	e := "/events/" + itoa(ev)

	ann.get("/events/new", 403)
	ann.post("/events", "/events", url.Values{"title": {"X"}, "status": {"planning"}, "starts_on": {"2099-01-01"}}, 403)
	ann.get(e+"/edit", 403)
	ann.post(e, e, url.Values{"title": {"X"}}, 403)
	ann.post(e, e+"/delete", url.Values{}, 403)
	ann.get(e+"/copy", 403)
	ann.post(e, e+"/copy", url.Values{"starts_on": {"2099-02-01"}}, 403)
	ann.post(e, e+"/schedule", url.Values{"label": {"Doors"}, "date": {"2099-01-01"}}, 403)
	ann.post(e, e+"/rooms", url.Values{"place_name": {"Hall 2"}}, 403)
	ann.post(e, "/rooms/1/delete", url.Values{}, 403)
	ann.post(e, e+"/helper-roles", url.Values{"name": {"Greeter"}, "slots": {"1"}}, 403)
	ann.post(e, e+"/helper-roles/paste", url.Values{"text": {"Greeter"}}, 403)
	ann.post(e, "/helper-roles/"+itoa(role)+"/delete", url.Values{}, 403)
	if evs, _ := st.ListEvents(&store.User{Role: store.RoleAdmin}, false, "2099-01-01"); len(evs) != 1 {
		t.Errorf("%d events, want 1", len(evs))
	}

	// Buttons are gone, but the pages still show the content.
	body := ann.get(e, 200)
	for _, no := range []string{e + "/edit", e + "/copy"} {
		if strings.Contains(body, no) {
			t.Errorf("event page shows %s to a helper", no)
		}
	}
	if !strings.Contains(body, "Private") {
		t.Error("event page should show the Private badge")
	}
	if strings.Contains(ann.get("/events", 200), "/events/new") {
		t.Error("events list shows New event to a helper")
	}
	sched := ann.get(e+"/schedule", 200)
	if strings.Contains(sched, "Add to the schedule") {
		t.Error("schedule form shown to a helper")
	}
	rooms := ann.get(e+"/rooms", 200)
	if !strings.Contains(rooms, "Hall") || strings.Contains(rooms, "/rooms/1/edit") || strings.Contains(rooms, "Somewhere else") {
		t.Error("rooms page should be read-only for a helper")
	}
	jobs := ann.get(e+"/helpers", 200)
	if !strings.Contains(jobs, "Cook") || strings.Contains(jobs, "Add a job") || strings.Contains(jobs, "/helper-roles/"+itoa(role)+"/edit") || !strings.Contains(jobs, "Sign me up") {
		t.Error("helper jobs should be read-only but sign-up should work")
	}

	// What a helper can do.
	ann.post(e+"/tasks", e+"/tasks", url.Values{"title": {"Buy rice"}}, 200)
	tasks, _ := st.ListTasks(ev)
	if len(tasks) != 1 {
		t.Fatalf("%d tasks, want 1", len(tasks))
	}
	ann.post(e+"/tasks", "/tasks/"+itoa(tasks[0].ID)+"/status", url.Values{"status": {"done"}, "next": {e + "/tasks"}}, 200)
	if ts, _ := st.ListTasks(ev); ts[0].Status != "done" {
		t.Errorf("task status %q", ts[0].Status)
	}
	ann.post(e+"/helpers", "/helper-roles/"+itoa(role)+"/me", url.Values{}, 200)
	if roles, _ := st.ListHelperRoles(ev); len(roles[0].Helpers) != 1 || roles[0].Helpers[0].UserID != annID {
		t.Errorf("sign-me-up didn't work: %+v", roles)
	}
	ann.post(e+"/helpers", "/helper-roles/"+itoa(role)+"/signup", url.Values{"name": {"Pat"}}, 200)
	ann.post(e+"/needs", e+"/needs", url.Values{"name": {"Paper plates"}, "quantity": {"3"}}, 200)
	needs, _ := st.ListNeeds(ev)
	if len(needs) != 1 {
		t.Fatalf("%d needs, want 1", len(needs))
	}
	ann.post(e+"/needs", "/needs/"+itoa(needs[0].ID)+"/status", url.Values{"status": {"gathered"}}, 200)
}

func TestPlannerPermissions(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("pat", "", "a long password", store.RolePlanner)
	pat := c.as("pat")

	body := pat.get("/events", 200)
	if !strings.Contains(body, "/events/new") || strings.Contains(body, "/admin/users") || strings.Contains(body, "/admin/settings") {
		t.Error("planner nav/list is wrong")
	}
	pat.post("/events/new", "/events", url.Values{"title": {"Fall Dinner"}, "status": {"planning"}, "starts_on": {"2099-01-01"},
		"visibility": {"public"}, "public_description": {"Come eat"}}, 200)
	e, err := st.GetEvent(1)
	if err != nil || e.Visibility != "public" || e.PublicDescription != "Come eat" {
		t.Fatalf("event %+v, %v", e, err)
	}
	if body := pat.get("/events/1", 200); !strings.Contains(body, "Public") || !strings.Contains(body, "/events/1/edit") {
		t.Error("planner event page should show the Public badge and Edit")
	}
	form := pat.get("/events/1/edit", 200)
	for _, want := range []string{"Who can see the details", "Public description", "Leave internal notes in Notes."} {
		if !strings.Contains(form, want) {
			t.Errorf("event form missing %q", want)
		}
	}
	if strings.Contains(form, `name="access"`) {
		t.Error("planner shouldn't get the lock controls")
	}
	pat.post("/events/1/edit", "/events/1", url.Values{"title": {"Fall Dinner"}, "status": {"planning"}, "starts_on": {"2099-01-01"},
		"visibility": {"private"}, "public_description": {"Changed"}}, 200)
	if e, _ := st.GetEvent(1); e.Visibility != "private" || e.PublicDescription != "Changed" {
		t.Errorf("update: %+v", e)
	}
	pat.post("/events/1/edit", "/events/1", url.Values{"title": {"Fall Dinner"}, "status": {"planning"}, "starts_on": {"2099-01-01"},
		"visibility": {"public"}, "public_description": {"Come eat"}}, 200)

	pat.post("/events/1/copy", "/events/1/copy", url.Values{"starts_on": {"2099-02-01"}}, 200)
	cp, err := st.GetEvent(2)
	if err != nil || cp.Visibility != "public" || cp.PublicDescription != "Come eat" {
		t.Fatalf("copy %+v, %v", cp, err)
	}
	pat.post("/events/1/schedule", "/events/1/schedule", url.Values{"label": {"Doors"}, "date": {"2099-01-01"}}, 200)
	pat.post("/events/1/rooms", "/events/1/rooms", url.Values{"place_name": {"Hall"}}, 200)
	pat.post("/events/1/helpers", "/events/1/helper-roles", url.Values{"name": {"Greeter"}, "slots": {"1"}}, 200)

	// Admin-only.
	pat.get("/admin/users", 403)
	pat.get("/admin/settings", 403)
	pat.get("/admin/backup", 403)
	pat.post("/events/1/edit", "/events/1", url.Values{"title": {"Fall Dinner"}, "status": {"planning"}, "starts_on": {"2099-01-01"}, "access": {"locked"}}, 403)
	pat.post("/events/new", "/events", url.Values{"title": {"Secret"}, "status": {"planning"}, "starts_on": {"2099-01-01"}, "access": {"locked"}}, 403)
	if e, _ := st.GetEvent(1); e.Locked {
		t.Error("planner locked an event")
	}

	pat.post("/events/2/edit", "/events/2/delete", url.Values{}, 200)
	if _, err := st.GetEvent(2); err == nil {
		t.Error("planner couldn't delete an event")
	}
}

func TestAdminUsersFormRole(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	boss := c.as("boss")
	form := boss.get("/admin/users/new", 200)
	if !strings.Contains(form, `name="role"`) || strings.Contains(form, "is_admin") {
		t.Error("user form should have a role select")
	}
	boss.post("/admin/users/new", "/admin/users", url.Values{"username": {"pat"}, "role": {"planner"}, "password": {"a long password"}, "password_confirm": {"a long password"}}, 200)
	u, _ := st.GetUser(2)
	if u == nil || u.Role != store.RolePlanner {
		t.Fatalf("pat: %+v", u)
	}
	// The only admin can't be demoted.
	body := boss.post("/admin/users/1/edit", "/admin/users/1", url.Values{"role": {"planner"}}, 422)
	if !strings.Contains(body, "At least one administrator") {
		t.Error("expected last-admin protection")
	}
	boss.post("/admin/users/2/edit", "/admin/users/2", url.Values{"role": {"admin"}}, 200)
	boss.post("/admin/users/1/edit", "/admin/users/1", url.Values{"role": {"helper"}}, 403) // redirected on to the Users page, which they can no longer open
}

func TestSSORoleMapping(t *testing.T) {
	for role, want := range map[string]string{"admin": "admin", "planner": "planner", "helper": "helper", "user": "helper", "": "helper", "weird": "helper"} {
		got := ssoUserOf(sso.AppUser{User: sso.User{Sub: "1", Username: "x", Role: role}, Active: true})
		if got.Role != want {
			t.Errorf("role %q mapped to %q, want %q", role, got.Role, want)
		}
	}
}
