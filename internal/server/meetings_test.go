package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/tylergoza/event-planner/internal/store"
)

func TestCreateAndEditMeeting(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("pat", "", "a long password", store.RolePlanner)
	pat := c.as("pat")

	if body := pat.get("/events/new?type=meeting", 200); !strings.Contains(body, `value="meeting" checked`) {
		t.Error("the form should pre-select Meeting")
	}
	if body := pat.get("/events/new", 200); !strings.Contains(body, `value="event" checked`) {
		t.Error("the form should pre-select Event")
	}
	pat.post("/events/new", "/events", newEventForm("Elders", "2099-03-01", url.Values{"type": {"meeting"}}), 200)
	m := byTitle(t, st, "Elders")
	if !m.IsMeeting() {
		t.Fatalf("type %q, want meeting", m.Type)
	}
	// No type at all is a plain event.
	pat.post("/events/new", "/events", newEventForm("Picnic", "2099-03-02", nil), 200)
	if byTitle(t, st, "Picnic").IsMeeting() {
		t.Error("a form without a type should make an event")
	}
	if body := pat.post("/events/new", "/events", newEventForm("X", "2099-03-02", url.Values{"type": {"party"}}), 422); !strings.Contains(body, "Pick Event or Meeting") {
		t.Error("expected a type error")
	}
	if body := pat.post("/events/new", "/events", newEventForm("", "2099-03-02", url.Values{"type": {"meeting"}}), 422); !strings.Contains(body, "Give the meeting a title") {
		t.Error("expected a meeting title error")
	}

	// Editing keeps the type, even if the form leaves it out.
	e := "/events/" + itoa(m.ID)
	edit := pat.get(e+"/edit", 200)
	if !strings.Contains(edit, `name="type" value="meeting"`) {
		t.Error("the edit form should carry the type")
	}
	pat.post(e+"/edit", e, editFormFor(m, url.Values{"title": {"Elders board"}}), 200)
	if got := mustEvent(t, st, m.ID); got.Title != "Elders board" || !got.IsMeeting() {
		t.Errorf("after edit: %q %q", got.Title, got.Type)
	}
}

func TestEventsTypeFilter(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("pat", "", "a long password", store.RolePlanner)
	pat := c.as("pat")
	st.CreateEvent(&store.Event{Title: "Fall Dinner", StartsOn: "2099-03-01"})
	st.CreateEvent(&store.Event{Title: "Elders Meeting", Type: "meeting", StartsOn: "2099-03-02"})

	both := pat.get("/events", 200)
	if !strings.Contains(both, "Fall Dinner") || !strings.Contains(both, "Elders Meeting") || !strings.Contains(both, "badge--meeting") {
		t.Error("All should list both, with a Meeting badge")
	}
	meetings := pat.get("/events?type=meeting", 200)
	if strings.Contains(meetings, "Fall Dinner") || !strings.Contains(meetings, "Elders Meeting") {
		t.Error("Meetings should list only meetings")
	}
	events := pat.get("/events?type=event", 200)
	if !strings.Contains(events, "Fall Dinner") || strings.Contains(events, "Elders Meeting") {
		t.Error("Events should list only events")
	}
	if !strings.Contains(pat.get("/events?type=bogus", 200), "Elders Meeting") {
		t.Error("an unknown type should show everything")
	}
	dash := pat.get("/", 200)
	if !strings.Contains(dash, "/events/new?type=meeting") || !strings.Contains(dash, "badge--meeting") {
		t.Error("the dashboard should offer New meeting and label meetings")
	}
}

func TestMeetingPage(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("pat", "", "a long password", store.RolePlanner)
	pat := c.as("pat")
	m, _ := st.CreateEvent(&store.Event{Title: "Elders", Type: "meeting", StartsOn: "2099-03-02"})
	ev, _ := st.CreateEvent(&store.Event{Title: "Fall Dinner", StartsOn: "2099-03-02"})
	st.AddAgendaItem(m, "Opening prayer", "", false)
	e := "/events/" + itoa(m)

	body := pat.get(e, 200)
	for _, want := range []string{"Agenda", "Opening prayer", "Action items", "Rooms", "More", "/events/" + itoa(m) + "/helpers"} {
		if !strings.Contains(body, want) {
			t.Errorf("meeting page is missing %q", want)
		}
	}
	for _, no := range []string{`>Helpers</a>`, `>Needs</a>`, `>Timeline</a>`, `>Schedule</a>`, `>Tasks</a>`} {
		if strings.Contains(body, no) {
			t.Errorf("empty meeting shows tab %s", no)
		}
	}
	// Rows make the tab appear and drop it from the menu.
	st.CreateHelperRole(&store.HelperRole{EventID: m, Name: "Note taker", Slots: 1})
	body = pat.get(e, 200)
	if !strings.Contains(body, `>Helpers</a>`) || strings.Contains(body, "helpers list") {
		t.Error("a meeting with helper jobs should show the Helpers tab")
	}
	// The plain event page keeps its tabs and sections.
	plain := pat.get("/events/"+itoa(ev), 200)
	for _, want := range []string{`>Schedule</a>`, `>Tasks</a>`, `>Helpers</a>`, `>Needs</a>`, "Tasks done", "Needs gathered"} {
		if !strings.Contains(plain, want) {
			t.Errorf("event page is missing %q", want)
		}
	}
	if strings.Contains(plain, "Agenda") || strings.Contains(plain, "badge--meeting") {
		t.Error("an event page shouldn't look like a meeting")
	}
}

func TestMeetingCopyKeepsAgenda(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("pat", "", "a long password", store.RolePlanner)
	pat := c.as("pat")
	m, _ := st.CreateEvent(&store.Event{Title: "Elders", Type: "meeting", StartsOn: "2099-03-02"})
	st.AddAgendaItem(m, "Budget", "", false)
	st.SetMinutes(m, "We met.")
	e := "/events/" + itoa(m)
	pat.post(e+"/copy", e+"/copy", url.Values{"starts_on": {"2099-03-09"}}, 200)
	var cp *store.Event
	list, _ := st.ListEventsOfType(&store.User{Role: store.RoleAdmin}, false, "2099-01-01", "meeting")
	for _, s := range list {
		if s.ID != m {
			cp = mustEvent(t, st, s.ID)
		}
	}
	if cp == nil || !cp.IsMeeting() || cp.Minutes != "" {
		t.Fatalf("copy: %+v", cp)
	}
	if items, _ := st.AgendaItems(cp.ID); len(items) != 1 || items[0].Title != "Budget" {
		t.Errorf("copy agenda: %+v", items)
	}
}

func TestAgendaCRUDAndMove(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("pat", "", "a long password", store.RolePlanner)
	pat := c.as("pat")
	m, _ := st.CreateEvent(&store.Event{Title: "Elders", Type: "meeting", StartsOn: "2099-03-02"})
	plain, _ := st.CreateEvent(&store.Event{Title: "Dinner", StartsOn: "2099-03-02"})
	e := "/events/" + itoa(m)

	pat.post(e, e+"/agenda", url.Values{"title": {"Opening"}, "details": {"Prayer"}, "carry_over": {"1"}}, 200)
	pat.post(e, e+"/agenda", url.Values{"title": {"Budget"}}, 200)
	pat.post(e, e+"/agenda", url.Values{"title": {""}}, 200) // flash error, nothing added
	items, _ := st.AgendaItems(m)
	if len(items) != 2 || items[0].Title != "Opening" || !items[0].CarryOver || items[0].Details != "Prayer" || items[1].CarryOver {
		t.Fatalf("items: %+v", items)
	}
	pat.post(e, e+"/agenda", url.Values{"title": {"x"}}, 200)
	pat.post("/events/"+itoa(plain), "/events/"+itoa(plain)+"/agenda", url.Values{"title": {"x"}}, 404)

	a, b := items[0].ID, items[1].ID
	pat.post(e, "/agenda/"+itoa(b)+"/move", url.Values{"dir": {"up"}, "next": {e}}, 200)
	if got, _ := st.AgendaItems(m); got[0].ID != b {
		t.Errorf("Budget should be first: %+v", got)
	}
	if body := pat.get("/agenda/"+itoa(a)+"/edit", 200); !strings.Contains(body, "Prayer") {
		t.Error("edit page should show the details")
	}
	pat.post("/agenda/"+itoa(a)+"/edit", "/agenda/"+itoa(a), url.Values{"title": {"Welcome"}}, 200)
	if got, _ := st.GetAgendaItem(a); got.Title != "Welcome" || got.CarryOver || got.Details != "" {
		t.Errorf("after edit: %+v", got)
	}
	if body := pat.post("/agenda/"+itoa(a)+"/edit", "/agenda/"+itoa(a), url.Values{"title": {""}}, 422); !strings.Contains(body, "Give the agenda item a title") {
		t.Error("expected a title error")
	}
	pat.post(e, "/agenda/"+itoa(a)+"/delete", url.Values{}, 200)
	if _, err := st.GetAgendaItem(a); err == nil {
		t.Error("item should be deleted")
	}
}

func TestAgendaPermissions(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	memberID, _ := st.CreateUser("mel", "", "a long password", store.RoleHelper)
	st.CreateUser("ann", "", "a long password", store.RoleHelper)
	m, _ := st.CreateEvent(&store.Event{Title: "Elders", Type: "meeting", StartsOn: "2099-03-02"})
	st.SetMembers(m, []int64{memberID})
	item, _ := st.AddAgendaItem(m, "Opening", "", false)
	e := "/events/" + itoa(m)
	ann, mel := c.as("ann"), c.as("mel")

	// A helper who isn't a member reads, but can't change anything.
	body := ann.get(e, 200)
	if !strings.Contains(body, "Opening") || strings.Contains(body, "Add to agenda") || strings.Contains(body, "/agenda/"+itoa(item)+"/edit") {
		t.Error("a non-member should see a read-only agenda")
	}
	ann.post(e, e+"/agenda", url.Values{"title": {"Sneaky"}}, 403)
	ann.get("/agenda/"+itoa(item)+"/edit", 403)
	ann.post(e, "/agenda/"+itoa(item), url.Values{"title": {"Sneaky"}}, 403)
	ann.post(e, "/agenda/"+itoa(item)+"/move", url.Values{"dir": {"down"}}, 403)
	ann.post(e, "/agenda/"+itoa(item)+"/delete", url.Values{}, 403)
	if items, _ := st.AgendaItems(m); len(items) != 1 || items[0].Title != "Opening" {
		t.Fatalf("a non-member changed the agenda: %+v", items)
	}

	// A member can.
	if !strings.Contains(mel.get(e, 200), "Add to agenda") {
		t.Error("a member should see the add form")
	}
	mel.post(e, e+"/agenda", url.Values{"title": {"Closing"}}, 200)
	mel.post(e, "/agenda/"+itoa(item), url.Values{"title": {"Welcome"}}, 200)
	if items, _ := st.AgendaItems(m); len(items) != 2 || items[0].Title != "Welcome" {
		t.Errorf("a member's changes: %+v", items)
	}
	mel.post(e, "/agenda/"+itoa(item)+"/delete", url.Values{}, 200)
	if items, _ := st.AgendaItems(m); len(items) != 1 {
		t.Errorf("a member should be able to delete: %+v", items)
	}

	// Locked meetings stay hidden from people who can't see them.
	st.SetMembers(m, []int64{memberID})
	locked := mustEvent(t, st, m)
	locked.Locked = true
	st.UpdateEvent(locked)
	ann.get("/agenda/"+itoa(item)+"/edit", 404)
	ann.get(e+"/agenda", 404)
}

func TestAgendaPrint(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	st.CreateUser("ann", "", "a long password", store.RoleHelper)
	m, _ := st.CreateEvent(&store.Event{Title: "Elders", Type: "meeting", StartsOn: "2099-03-02"})
	st.AddRoom(m, 0, "Library")
	st.AddAgendaItem(m, "Opening prayer", "Open with a hymn", false)
	open, _ := st.CreateEvent(&store.Event{Title: "Fall Dinner", StartsOn: "2099-10-02"})
	secret, _ := st.CreateEvent(&store.Event{Title: "Staff Retreat", StartsOn: "2099-10-03", Locked: true})
	st.LinkEvent(m, open)
	st.LinkEvent(m, secret)
	e := "/events/" + itoa(m)

	body := c.as("ann").get(e+"/agenda", 200)
	for _, want := range []string{"Elders", "Library", "Opening prayer", "Open with a hymn", "Events to discuss", "Fall Dinner", "Private event", "Print"} {
		if !strings.Contains(body, want) {
			t.Errorf("print view is missing %q", want)
		}
	}
	if strings.Contains(body, "Staff Retreat") {
		t.Error("print view leaks a private event's title")
	}
	if !strings.Contains(c.as("boss").get(e+"/agenda", 200), "Staff Retreat") {
		t.Error("an admin should see the event's title")
	}
	// Not a meeting: no agenda view.
	c.as("boss").get("/events/"+itoa(open)+"/agenda", 404)
}
