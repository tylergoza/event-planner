package server

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tylergoza/event-planner/internal/store"
)

func TestDiscussedLinkIdeaAndTasks(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("pat", "", "a long password", store.RolePlanner)
	pat := c.as("pat")
	m, _ := st.CreateEvent(&store.Event{Title: "Elders", Type: "meeting", StartsOn: "2099-03-02"})
	dinner, _ := st.CreateEvent(&store.Event{Title: "Fall Dinner", StartsOn: "2099-10-02"})
	e := "/events/" + itoa(m)

	// The picker lists events not yet linked; Add links one.
	if body := pat.get(e+"?pick=dinner", 200); !strings.Contains(body, "Fall Dinner") || !strings.Contains(body, `name="event_id" value="`+itoa(dinner)+`"`) {
		t.Error("picker should list Fall Dinner with an Add button")
	}
	pat.post(e, e+"/discussed", url.Values{"event_id": {itoa(dinner)}}, 200)
	body := pat.get(e, 200)
	if !strings.Contains(body, "Fall Dinner") || !strings.Contains(body, "/events/"+itoa(dinner)+"/tasks") {
		t.Error("linked event should be listed with an add-task form")
	}
	if strings.Contains(pat.get(e+"?pick=dinner", 200), `name="event_id"`) {
		t.Error("an already linked event shouldn't be offered")
	}

	// Discussion note.
	pat.post(e, e+"/discussed/"+itoa(dinner)+"/note", url.Values{"note": {"Need a caterer"}}, 200)
	if !strings.Contains(pat.get(e, 200), "Need a caterer") {
		t.Error("note should show")
	}
	// Back-link on the event page.
	back := pat.get("/events/"+itoa(dinner), 200)
	if !strings.Contains(back, "Discussed at") || !strings.Contains(back, "Elders") || !strings.Contains(back, "Need a caterer") {
		t.Error("event page should list the meeting and its note")
	}

	// A task added from the row lands on the linked event, and comes back.
	pat.post(e, "/events/"+itoa(dinner)+"/tasks", url.Values{"title": {"Book caterer"}, "return": {e + "#discussed-" + itoa(dinner)}}, 200)
	if ts, _ := st.ListTasks(dinner); len(ts) != 1 || ts[0].Title != "Book caterer" {
		t.Errorf("tasks on the event: %+v", ts)
	}
	if ts, _ := st.ListTasks(m); len(ts) != 0 {
		t.Errorf("task landed on the meeting: %+v", ts)
	}
	// Return only to local /events/ pages.
	if got := returnToFor("//evil.example/x"); got != "FALLBACK" {
		t.Errorf("return %q", got)
	}
	if got := returnToFor("https://evil.example/x"); got != "FALLBACK" {
		t.Errorf("return %q", got)
	}

	// New idea: undated, planning, linked, inherits lock and members.
	pat.post(e, e+"/discussed/idea", url.Values{"title": {"Summer picnic"}}, 200)
	idea := byTitle(t, st, "Summer picnic")
	if idea.Scheduled() || idea.Status != "planning" {
		t.Errorf("idea %+v", idea)
	}
	if !strings.Contains(pat.get(e, 200), "Summer picnic") {
		t.Error("idea should be on the meeting page")
	}
	if p := pat.get("/events/"+itoa(idea.ID), 200); !strings.Contains(p, "Came up at") || !strings.Contains(p, "Elders") {
		t.Error("idea page should say where it came up")
	}
	pat.post(e, e+"/discussed/idea", url.Values{"title": {" "}}, 200) // flash, no new event

	// Order and removal.
	pat.post(e, e+"/discussed/"+itoa(idea.ID)+"/move", url.Values{"dir": {"up"}, "next": {e}}, 200)
	if list, _ := st.MeetingEvents(m, &store.User{Role: store.RoleAdmin}); len(list) != 2 || list[0].EventID != idea.ID {
		t.Errorf("order after move: %+v", list)
	}
	pat.post(e, e+"/discussed/"+itoa(dinner)+"/delete", url.Values{}, 200)
	if list, _ := st.MeetingEvents(m, &store.User{Role: store.RoleAdmin}); len(list) != 1 {
		t.Errorf("after unlink: %+v", list)
	}
	if _, err := st.GetEvent(dinner); err != nil {
		t.Error("unlinking must keep the event")
	}
}

func returnToFor(to string) string {
	r := httptest.NewRequest("POST", "/", strings.NewReader(url.Values{"return": {to}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return returnTo(r, "FALLBACK")
}

func TestIdeaInheritsLockAndMembers(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	memberID, _ := st.CreateUser("mel", "", "a long password", store.RoleHelper)
	st.CreateUser("ann", "", "a long password", store.RoleHelper)
	m, _ := st.CreateEvent(&store.Event{Title: "Elders", Type: "meeting", StartsOn: "2099-03-02", Locked: true})
	st.SetMembers(m, []int64{memberID})
	e := "/events/" + itoa(m)
	c.as("mel").post(e, e+"/discussed/idea", url.Values{"title": {"Secret plan"}}, 200)
	idea := byTitle(t, st, "Secret plan")
	if !idea.Locked {
		t.Error("idea should inherit the lock")
	}
	if ids, _ := st.Members(idea.ID); len(ids) != 1 || ids[0] != memberID {
		t.Errorf("members %v", ids)
	}
	c.as("ann").get("/events/"+itoa(idea.ID), 404)
}

func TestDiscussedPermissionsAndPrivateRows(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	memberID, _ := st.CreateUser("mel", "", "a long password", store.RoleHelper)
	st.CreateUser("ann", "", "a long password", store.RoleHelper)
	m, _ := st.CreateEvent(&store.Event{Title: "Elders", Type: "meeting", StartsOn: "2099-03-02"})
	st.SetMembers(m, []int64{memberID})
	open, _ := st.CreateEvent(&store.Event{Title: "Fall Dinner", StartsOn: "2099-10-02"})
	secret, _ := st.CreateEvent(&store.Event{Title: "Staff Retreat", StartsOn: "2099-10-03", Locked: true})
	st.LinkEvent(m, open)
	st.LinkEvent(m, secret)
	e := "/events/" + itoa(m)
	ann, mel := c.as("ann"), c.as("mel")

	body := ann.get(e, 200)
	if !strings.Contains(body, "Fall Dinner") || !strings.Contains(body, "Private event") || strings.Contains(body, "Staff Retreat") {
		t.Error("locked rows should read Private event with no title")
	}
	if strings.Contains(body, "Add idea") || strings.Contains(body, "Bring over") {
		t.Error("a non-member shouldn't see the edit controls")
	}
	if !strings.Contains(c.as("boss").get(e, 200), "Staff Retreat") {
		t.Error("an admin should see the title")
	}
	ann.post(e, e+"/discussed", url.Values{"event_id": {itoa(open)}}, 403)
	ann.post(e, e+"/discussed/idea", url.Values{"title": {"x"}}, 403)
	ann.post(e, e+"/discussed/"+itoa(open)+"/note", url.Values{"note": {"x"}}, 403)
	ann.post(e, e+"/discussed/"+itoa(open)+"/move", url.Values{"dir": {"up"}}, 403)
	ann.post(e, e+"/discussed/"+itoa(open)+"/delete", url.Values{}, 403)
	ann.post(e, e+"/bring-over", url.Values{}, 403)
	ann.post(e, e+"/minutes", url.Values{"minutes": {"x"}}, 403)
	if list, _ := st.MeetingEvents(m, &store.User{Role: store.RoleAdmin}); len(list) != 2 {
		t.Fatalf("a non-member changed the list: %+v", list)
	}

	// A member can't link a locked event they can't see.
	mel.post(e, e+"/discussed", url.Values{"event_id": {itoa(secret)}}, 200)
	// Back-links hide a locked meeting from people who can't see it.
	st.UnlinkEvent(m, secret)
	st.LinkEvent(m, open)
	locked := mustEvent(t, st, m)
	locked.Locked = true
	st.UpdateEvent(locked)
	if strings.Contains(ann.get("/events/"+itoa(open), 200), "Elders") {
		t.Error("a locked meeting leaks into Discussed at")
	}
}

func TestBringOverButton(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("pat", "", "a long password", store.RolePlanner)
	pat := c.as("pat")
	first, _ := st.CreateEvent(&store.Event{Title: "Elders Oct", Type: "meeting", Kind: "Elders", StartsOn: "2099-10-06"})
	next, _ := st.CreateEvent(&store.Event{Title: "Elders Nov", Type: "meeting", Kind: "Elders", StartsOn: "2099-11-03"})
	lone, _ := st.CreateEvent(&store.Event{Title: "Lone", Type: "meeting", Kind: "Other", StartsOn: "2099-11-04"})
	ev, _ := st.CreateEvent(&store.Event{Title: "Fall Dinner", StartsOn: "2099-12-02"})
	st.LinkEvent(first, ev)
	st.AddAgendaItem(first, "Budget", "", true)

	body := pat.post("/events/"+itoa(lone), "/events/"+itoa(lone)+"/bring-over", url.Values{}, 200)
	if !strings.Contains(body, "No earlier meeting found.") {
		t.Error("expected the no earlier meeting flash")
	}
	body = pat.post("/events/"+itoa(next), "/events/"+itoa(next)+"/bring-over", url.Values{}, 200)
	if !strings.Contains(body, "Brought over 1 event and 1 agenda item from Oct 6, 2099.") {
		t.Errorf("flash missing:\n%s", body)
	}
	if !strings.Contains(body, "Fall Dinner") {
		t.Error("the event should be listed")
	}
}

func TestMinutesAndCloseOut(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("pat", "", "a long password", store.RolePlanner)
	st.CreateUser("ann", "", "a long password", store.RoleHelper)
	pat, ann := c.as("pat"), c.as("ann")
	past := time.Now().AddDate(0, 0, -3).Format(store.DateLayout)
	m, _ := st.CreateEvent(&store.Event{Title: "Elders", Type: "meeting", StartsOn: past, Status: "planning"})
	future, _ := st.CreateEvent(&store.Event{Title: "Later", Type: "meeting", StartsOn: "2099-03-02", Status: "planning"})
	ev, _ := st.CreateEvent(&store.Event{Title: "Work day", StartsOn: past, Status: "ready"})
	e := "/events/" + itoa(m)

	// Minutes: open once started; collapsed before.
	body := pat.get(e, 200)
	if !strings.Contains(body, "<details open>") || !strings.Contains(body, "Save minutes") {
		t.Error("minutes should be open after the meeting starts")
	}
	if fb := pat.get("/events/"+itoa(future), 200); !strings.Contains(fb, "Minutes (after the meeting)") || strings.Contains(fb, "<details open>") {
		t.Error("minutes should be collapsed before the meeting")
	}
	pat.post(e, e+"/minutes", url.Values{"minutes": {"We prayed.\nWe voted."}}, 200)
	if got := mustEvent(t, st, m).Minutes; got != "We prayed.\nWe voted." {
		t.Errorf("minutes %q", got)
	}
	if body := ann.get(e, 200); !strings.Contains(body, "<p>We voted.</p>") || strings.Contains(body, "Save minutes") {
		t.Error("others should read the minutes")
	}

	// Close-out prompt: planners only, only after the end.
	if !strings.Contains(pat.get("/events/"+itoa(ev), 200), "Mark as done") {
		t.Error("a past event should offer Mark as done")
	}
	if strings.Contains(ann.get("/events/"+itoa(ev), 200), "Mark as done") {
		t.Error("helpers shouldn't be offered Mark as done")
	}
	if strings.Contains(pat.get("/events/"+itoa(future), 200), "Mark as done") {
		t.Error("a future event shouldn't be offered Mark as done")
	}
	st.DB.Exec(`UPDATE events SET starts_on = ?, end_time = '23:59' WHERE id = ?`, time.Now().Format(store.DateLayout), ev)
	if strings.Contains(pat.get("/events/"+itoa(ev), 200), "Mark as done") {
		t.Error("an event ending later today shouldn't be offered Mark as done yet")
	}
	st.DB.Exec(`UPDATE events SET starts_on = ?, end_time = '' WHERE id = ?`, past, ev)

	// Dashboard group.
	dash := pat.get("/", 200)
	if !strings.Contains(dash, "Needs closing out") || !strings.Contains(dash, "Work day") {
		t.Error("the dashboard should list events needing closing out")
	}
	if strings.Contains(ann.get("/", 200), "Needs closing out") {
		t.Error("helpers shouldn't see the group")
	}

	// Actions.
	ann.post("/", "/events/"+itoa(ev)+"/status", url.Values{"status": {"done"}}, 403)
	pat.post("/events/"+itoa(ev), "/events/"+itoa(ev)+"/status", url.Values{"status": {"planning"}}, 200) // ignored
	if mustEvent(t, st, ev).Status != "ready" {
		t.Error("only done or cancelled are allowed")
	}
	pat.post("/events/"+itoa(ev), "/events/"+itoa(ev)+"/status", url.Values{"status": {"cancelled"}}, 200)
	if mustEvent(t, st, ev).Status != "cancelled" {
		t.Error("should be cancelled")
	}
	// A meeting with no minutes is not marked done: it goes to the minutes.
	st.SetMinutes(m, "")
	body = pat.post(e, e+"/status", url.Values{"status": {"done"}}, 200)
	if mustEvent(t, st, m).Status != "planning" || !strings.Contains(body, "Add the minutes, then mark it done.") {
		t.Error("done without minutes should ask for them")
	}
	st.SetMinutes(m, "Met.")
	pat.post(e, e+"/status", url.Values{"status": {"done"}}, 200)
	if mustEvent(t, st, m).Status != "done" {
		t.Error("with minutes the meeting should be done")
	}
	if strings.Contains(pat.get("/", 200), "Needs closing out") {
		t.Error("nothing left to close out")
	}
}

// Minutes, agenda and discussion notes stay inside the app.
func TestMeetingInternalsNeverLeak(t *testing.T) {
	c, st := newTestServer(t)
	id := apiEvent(t, st, "Elders", "public", false, "planning", "2099-11-01", "18:00", "Library")
	other := apiEvent(t, st, "Dinner", "public", false, "planning", "2099-11-02", "18:00")
	const minutes, agenda, note = "MINUTES-ZEBRA", "AGENDA-ZEBRA", "NOTE-ZEBRA"
	st.DB.Exec(`UPDATE events SET type = 'meeting', minutes = ? WHERE id = ?`, minutes, id)
	st.AddAgendaItem(id, agenda, agenda, true)
	st.LinkEvent(id, other)
	st.SetDiscussionNote(id, other, note)
	tok, _ := st.CreateToken("cal", true, 0)
	secrets := []string{minutes, agenda, note}
	check := func(what, body string) {
		t.Helper()
		for _, s := range secrets {
			if strings.Contains(body, s) {
				t.Errorf("%s leaks %q", what, s)
			}
		}
	}

	for _, p := range []string{"/api/v1/events" + apiRangeQ, "/api/v1/events/" + itoa(id), "/api/v1/events/" + itoa(other)} {
		body, _ := apiGet(t, c, p, tok, 200)
		check(p, body)
	}
	body, _ := apiGet(t, c, "/calendar.ics"+apiRangeQ, "", 200)
	check("public .ics", body)
	body, _ = apiGet(t, c, "/api/v1/events.ics"+apiRangeQ, tok, 200)
	check("token .ics", body)
	check("/calendar", c.get("/calendar", 200))
	check("/calendar/{id}", c.get("/calendar/"+itoa(id), 200))

	srv, _ := newConnServer(t, nil)
	ev := mustEvent(t, st, id)
	ev.Locked = false
	raw, _ := json.Marshal(srv.calendarBooking(ev, nil))
	check("calendar push payload", string(raw))
}
