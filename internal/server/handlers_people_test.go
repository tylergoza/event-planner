package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/tylergoza/event-planner/internal/store"
)

func TestPeoplePage(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	annID, _ := st.CreateUser("ann", "Ann Lee", "a long password", store.RolePlanner)
	ann := c.as("ann")
	ev, _ := st.CreateEvent(&store.Event{Title: "Harvest Dinner", StartsOn: "2099-01-01"})
	page := "/events/" + itoa(ev) + "/people"

	if body := ann.get(page, 200); !strings.Contains(body, "Nobody has a task or a job yet") || !strings.Contains(body, `data-controller="live"`) || !strings.Contains(body, ">People<") {
		t.Error("empty people page")
	}
	ann.get(page+"?print=1", 200)

	st.CreateTask(&store.Task{EventID: ev, Title: "Cook the ham", AssigneeID: annID})
	st.CreateTask(&store.Task{EventID: ev, Title: "Find a pianist"})
	role, _ := st.CreateHelperRole(&store.HelperRole{EventID: ev, Name: "Servers", Slots: 3})
	st.SignUpName(role, "Vic", "")
	for _, p := range []string{page, page + "?print=1"} {
		body := ann.get(p, 200)
		for _, want := range []string{"Ann Lee", "Cook the ham", "Vic", "Servers", "Find a pianist", "needs 2 more"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", p, want)
			}
		}
	}
	if body := ann.get(page+"?print=1", 200); !strings.Contains(body, `data-controller="print"`) || strings.Contains(body, `data-controller="live"`) {
		t.Error("print view should print and not update live")
	}
}

func TestCopyEventPages(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	annID, _ := st.CreateUser("ann", "Ann Lee", "a long password", store.RolePlanner)
	st.CreateUser("bob", "", "a long password", store.RolePlanner)
	boss, ann, bob := c.as("boss"), c.as("ann"), c.as("bob")

	ev, _ := st.CreateEvent(&store.Event{Title: "Harvest Dinner", StartsOn: "2099-01-01"})
	st.CreateTask(&store.Task{EventID: ev, Title: "Cook", AssigneeID: annID, DueOn: "2098-12-30"})
	role, _ := st.CreateHelperRole(&store.HelperRole{EventID: ev, Name: "Servers", Slots: 2})
	st.SignUpName(role, "Vic", "")
	src := "/events/" + itoa(ev)

	body := ann.get(src+"/copy", 200)
	for _, want := range []string{"New start date", "Keep who's on each task", "Keep helpers signed up", `value="Harvest Dinner"`} {
		if !strings.Contains(body, want) {
			t.Errorf("copy form missing %q", want)
		}
	}
	if !strings.Contains(ann.get(src, 200), src+"/copy") {
		t.Error("event page should link to copy")
	}

	// No date: sent back.
	if body := ann.post(src+"/copy", src+"/copy", url.Values{"title": {"Next"}}, 422); !strings.Contains(body, "Pick the day the copy starts") {
		t.Error("expected a date error")
	}
	body = ann.post(src+"/copy", src+"/copy", url.Values{"title": {"Spring Dinner"}, "starts_on": {"2099-04-01"}, "assignees": {"1"}}, 200)
	if !strings.Contains(body, "Spring Dinner") || !strings.Contains(body, "Copied from") || !strings.Contains(body, "Harvest Dinner") {
		t.Error("expected to land on the new event, saying where it came from")
	}
	events, _ := st.ListEvents(&store.User{Role: store.RoleAdmin}, false, "2099-01-01")
	var newID int64
	for _, e := range events {
		if e.Title == "Spring Dinner" {
			newID = e.ID
		}
	}
	if newID == 0 {
		t.Fatal("copy not created")
	}
	tasks, _ := st.ListTasks(newID)
	roles, _ := st.ListHelperRoles(newID)
	if len(tasks) != 1 || tasks[0].AssigneeID != annID || tasks[0].DueOn != "2099-03-30" || len(roles) != 1 || len(roles[0].Helpers) != 0 {
		t.Errorf("copy: %+v %+v", tasks, roles)
	}

	// Locked: non-members get 404 for the form and the post; members and admins can copy.
	st.DB.Exec(`UPDATE events SET locked = 1 WHERE id = ?`, ev)
	st.SetMembers(ev, []int64{annID})
	bob.get(src+"/copy", 404)
	bob.post("/events", src+"/copy", url.Values{"starts_on": {"2099-05-01"}}, 404)
	ann.get(src+"/copy", 200)
	boss.post(src+"/copy", src+"/copy", url.Values{"starts_on": {"2099-05-01"}}, 200)
	if e, _ := st.GetEvent(ev); e == nil {
		t.Fatal("source gone")
	}
	all, _ := st.ListEvents(&store.User{Role: store.RoleAdmin}, false, "2099-01-01")
	if len(all) != 3 {
		t.Errorf("%d events, want 3 (bob's copy must not exist)", len(all))
	}
	// Bob can't see a copy of a locked event it came from.
	for _, e := range all {
		if e.CopiedFrom == ev && e.Title == "Harvest Dinner" {
			bob.get("/events/"+itoa(e.ID), 404)
		}
	}
}

func TestDashboardHelperJobs(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	annID, _ := st.CreateUser("ann", "Ann Lee", "a long password", store.RolePlanner)
	ann := c.as("ann")

	open, _ := st.CreateEvent(&store.Event{Title: "Open Supper", StartsOn: "2099-01-01"})
	secret, _ := st.CreateEvent(&store.Event{Title: "Secret Retreat", StartsOn: "2099-02-01", Locked: true})
	for _, ev := range []int64{open, secret} {
		r, _ := st.CreateHelperRole(&store.HelperRole{EventID: ev, Name: map[int64]string{open: "Greeters", secret: "Cooks"}[ev], Slots: 1})
		st.SignUpUser(r, annID)
	}
	body := ann.get("/", 200)
	if !strings.Contains(body, "My helper jobs") || !strings.Contains(body, "Greeters") {
		t.Error("expected the open event's job")
	}
	if strings.Contains(body, "Secret Retreat") || strings.Contains(body, "Cooks") {
		t.Error("a locked event she isn't a member of must not show")
	}
	st.SetMembers(secret, []int64{annID})
	if body := ann.get("/", 200); !strings.Contains(body, "Cooks") {
		t.Error("members see their jobs in a locked event")
	}
}

func TestOverviewRoomsAndHelpers(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("ann", "Ann Lee", "a long password", store.RoleAdmin)
	ann := c.as("ann")
	a, _ := st.CreateEvent(&store.Event{Title: "Dinner A", StartsOn: "2099-01-01"})
	b, _ := st.CreateEvent(&store.Event{Title: "Dinner B", StartsOn: "2099-01-01"})
	st.AddRoom(a, 5, "Main › Hall")
	st.AddRoom(b, 5, "Main › Hall")
	r, _ := st.CreateHelperRole(&store.HelperRole{EventID: a, Name: "Cooks", Slots: 3})
	st.SignUpName(r, "Vic", "")
	body := ann.get("/events/"+itoa(a), 200)
	for _, want := range []string{"Rooms", "1 room double-booked", "1 <small>of 3 spots", "1 job short"} {
		if !strings.Contains(body, want) {
			t.Errorf("overview missing %q", want)
		}
	}
}
