package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tylergoza/event-planner/internal/calendar"
	"github.com/tylergoza/event-planner/internal/store"
)

// unschedEnv has an admin, a planner (pat) and a helper (ann).
func unschedEnv(t *testing.T) (pat, ann *client, st *store.Store) {
	t.Helper()
	c, st := newTestServer(t)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	st.CreateUser("pat", "Pat", "a long password", store.RolePlanner)
	st.CreateUser("ann", "Ann", "a long password", store.RoleHelper)
	return c.as("pat"), c.as("ann"), st
}

func unschedForm(title, status string) url.Values {
	return url.Values{"title": {title}, "status": {status}, "scheduling": {"unscheduled"},
		"starts_on": {"2099-03-01"}, "ends_on": {"2099-03-02"}, "date_tentative": {"1"}}
}

func findEvent(t *testing.T, st *store.Store, title string) *store.Event {
	t.Helper()
	var id int64
	if err := st.DB.QueryRow(`SELECT id FROM events WHERE title = ?`, title).Scan(&id); err != nil {
		t.Fatalf("event %q: %v", title, err)
	}
	e, err := st.GetEvent(id)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestUnscheduledCreate(t *testing.T) {
	pat, _, st := unschedEnv(t)
	pat.post("/events/new", "/events", unschedForm("Someday Picnic", "planning"), 200)
	e := findEvent(t, st, "Someday Picnic")
	if e.StartsOn != "" || e.EndsOn != "" || e.DateTentative || e.Scheduled() {
		t.Fatalf("saved %+v", e)
	}
}

func TestUnscheduledEditDatedToUndated(t *testing.T) {
	pat, _, st := unschedEnv(t)
	id, _ := st.CreateEvent(&store.Event{Title: "Dinner", StartsOn: "2099-05-01", DateTentative: true, Status: "planning"})
	e := "/events/" + itoa(id)
	pat.post(e+"/edit", e, unschedForm("Dinner", "planning"), 200)
	got, _ := st.GetEvent(id)
	if got.StartsOn != "" || got.DateTentative {
		t.Fatalf("after edit %+v", got)
	}
}

func TestUnscheduledReadyRefused(t *testing.T) {
	pat, _, st := unschedEnv(t)
	body := pat.post("/events/new", "/events", unschedForm("Ready One", "ready"), 422)
	if !strings.Contains(body, "needs a date") {
		t.Error("expected needs-a-date error")
	}
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM events WHERE title = 'Ready One'`).Scan(&n)
	if n != 0 {
		t.Error("event was saved")
	}
	// Editing a dated event to ready + undated is refused too.
	id, _ := st.CreateEvent(&store.Event{Title: "Dated", StartsOn: "2099-05-01", Status: "planning"})
	e := "/events/" + itoa(id)
	pat.post(e+"/edit", e, unschedForm("Dated", "ready"), 422)
	if got, _ := st.GetEvent(id); got.StartsOn != "2099-05-01" {
		t.Errorf("date lost: %+v", got)
	}
}

func TestUnscheduledSeriesOccurrenceRefused(t *testing.T) {
	env := newSeriesEnv(t)
	occ := env.occ[2]
	e := "/events/" + itoa(occ.ID)
	body := env.pat.post(e+"/edit", e, unschedForm("Supper", "planning"), 422)
	if !strings.Contains(body, "repeating series") {
		t.Error("expected series error")
	}
	if got := env.get(t, occ.ID); got.StartsOn != occ.StartsOn || got.StartsOn == "" {
		t.Errorf("occurrence changed: %q -> %q", occ.StartsOn, got.StartsOn)
	}
}

func TestUnscheduledRepeatRefused(t *testing.T) {
	pat, _, st := unschedEnv(t)
	f := unschedForm("Weekly Maybe", "planning")
	f.Set("repeat_freq", "weekly")
	f.Set("repeat_every", "1")
	f.Set("repeat_weekday", "3")
	f.Set("repeat_end", "never")
	pat.post("/events/new", "/events", f, 422)
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
	if n != 0 {
		t.Errorf("%d events saved", n)
	}
}

func TestUnscheduledListsGrouped(t *testing.T) {
	pat, _, st := unschedEnv(t)
	st.CreateEvent(&store.Event{Title: "Undated Retreat", Status: "planning"})
	st.CreateEvent(&store.Event{Title: "Dated Supper", StartsOn: day(soon(1)), Status: "planning"})
	for _, p := range []string{"/", "/events"} {
		body := pat.get(p, 200)
		i := strings.Index(body, "Not scheduled yet")
		if i < 0 || !strings.Contains(body, "Undated Retreat") {
			t.Fatalf("%s: no unscheduled section", p)
		}
		if strings.Index(body, "Undated Retreat") < i {
			t.Errorf("%s: undated event before its heading", p)
		}
	}
}

func TestUnscheduledNoSectionWhenNone(t *testing.T) {
	pat, _, st := unschedEnv(t)
	st.CreateEvent(&store.Event{Title: "Dated Supper", StartsOn: day(soon(1)), Status: "planning"})
	for _, p := range []string{"/", "/events"} {
		if strings.Contains(pat.get(p, 200), "Not scheduled yet") {
			t.Errorf("%s shows the section with no undated events", p)
		}
	}
}

func TestTentativeDashboardNote(t *testing.T) {
	pat, _, st := unschedEnv(t)
	st.CreateEvent(&store.Event{Title: "Maybe Supper", StartsOn: day(soon(1)), Status: "ready", DateTentative: true})
	if !strings.Contains(pat.get("/", 200), "Date not confirmed") {
		t.Error("ready + pencilled-in card should say Date not confirmed")
	}
	st.CreateEvent(&store.Event{Title: "Sure Supper", StartsOn: day(soon(2)), Status: "ready"})
	st.DB.Exec(`UPDATE events SET date_tentative = 0 WHERE title = 'Maybe Supper'`)
	if strings.Contains(pat.get("/", 200), "Date not confirmed") {
		t.Error("confirmed events shouldn't say Date not confirmed")
	}
}

func TestConfirmDate(t *testing.T) {
	pat, ann, st := unschedEnv(t)
	id, _ := st.CreateEvent(&store.Event{Title: "Maybe", StartsOn: "2099-05-01", DateTentative: true, Status: "planning"})
	e := "/events/" + itoa(id)
	ann.post(e, e+"/confirm-date", url.Values{}, 403)
	if got, _ := st.GetEvent(id); !got.DateTentative {
		t.Fatal("helper confirmed the date")
	}
	pat.post(e, e+"/confirm-date", url.Values{}, 200)
	if got, _ := st.GetEvent(id); got.DateTentative || got.StartsOn != "2099-05-01" {
		t.Fatalf("after confirm %+v", got)
	}
}

func TestConfirmDateUndatedAsksForADate(t *testing.T) {
	pat, _, st := unschedEnv(t)
	id, _ := st.CreateEvent(&store.Event{Title: "Nodate", Status: "planning"})
	e := "/events/" + itoa(id)
	body := pat.post(e, e+"/confirm-date", url.Values{}, 200)
	if !strings.Contains(body, "Pick a date first") {
		t.Error("expected the pick-a-date notice")
	}
}

func TestTentativeCalendarBookingStatus(t *testing.T) {
	srv, _ := newConnServer(t, nil)
	for _, c := range []struct {
		name      string
		status    string
		tentative bool
		want      string
	}{
		{"plain", "planning", false, "confirmed"},
		{"tentative", "planning", true, "tentative"},
		{"cancelled", "cancelled", false, "cancelled"},
		{"cancelled and tentative", "cancelled", true, "cancelled"},
	} {
		b := srv.calendarBooking(&store.Event{ID: 1, Title: "X", StartsOn: "2099-01-01", Status: c.status, DateTentative: c.tentative}, nil)
		if b.Status != c.want {
			t.Errorf("%s: status %q, want %q", c.name, b.Status, c.want)
		}
	}
}

func TestUnscheduledWorkerSendsDelete(t *testing.T) {
	f, ts := newFakeCalendar(t)
	srv, st := newConnServer(t, map[string]string{"CALENDAR_URL": ts.URL, "CALENDAR_TOKEN": "cal_test"})
	ctx := context.Background()

	id, _ := st.CreateEvent(&store.Event{Title: "Fall Dinner", StartsOn: "2099-11-07", DateTentative: true, Visibility: "public"})
	srv.sendCalendarJobs(ctx)
	calls := f.take()
	if len(calls) != 1 || calls[0].Method != http.MethodPut || calls[0].Body["status"] != "tentative" {
		t.Fatalf("pencilled-in calls %+v", calls)
	}

	e, _ := st.GetEvent(id)
	e.StartsOn, e.EndsOn, e.DateTentative = "", "", false
	if err := st.UpdateEvent(e); err != nil {
		t.Fatal(err)
	}
	srv.sendCalendarJobs(ctx)
	calls = f.take()
	if len(calls) != 1 || calls[0].Method != http.MethodDelete || calls[0].Path != "/api/v1/bookings/events/"+itoa(id) {
		t.Fatalf("undated calls %+v", calls)
	}
	if s, _ := st.CalendarOutbox(); s.Waiting != 0 {
		t.Errorf("still waiting: %+v", s)
	}
}

func TestUnscheduledAPIAndICS(t *testing.T) {
	c, st := newTestServer(t)
	tok, _ := st.CreateToken("site", false, 0)
	pen := apiEvent(t, st, "Pencilled Dinner", "public", false, "planning", "2099-11-01", "18:00")
	sure := apiEvent(t, st, "Sure Dinner", "public", false, "planning", "2099-11-02", "18:00")
	st.DB.Exec(`UPDATE events SET date_tentative = 1 WHERE id = ?`, pen)
	und := apiEvent(t, st, "Undated Dinner", "public", false, "planning", "", "")

	list := apiEvents(t, c, "/api/v1/events"+apiRangeQ, tok)
	if len(list) != 2 {
		t.Fatalf("events %v", list)
	}
	for _, ev := range list {
		wantTent := ev["title"] == "Pencilled Dinner"
		if ev["tentative"] != wantTent {
			t.Errorf("%v: tentative = %v", ev["title"], ev["tentative"])
		}
		if ev["title"] == "Undated Dinner" {
			t.Error("undated event in the API list")
		}
	}
	apiGet(t, c, "/api/v1/events/"+itoa(und), tok, 404)
	apiGet(t, c, "/api/v1/events/"+itoa(sure), tok, 200)

	ics, _ := apiGet(t, c, "/calendar.ics"+apiRangeQ, "", 200)
	if strings.Count(ics, "STATUS:TENTATIVE") != 1 {
		t.Errorf("ics TENTATIVE count:\n%s", ics)
	}
	if strings.Contains(ics, "Undated Dinner") || !strings.Contains(ics, "Pencilled Dinner") {
		t.Errorf("ics contents:\n%s", ics)
	}
}

func TestTentativeCalendarMonthView(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("coordinator", "", "a long password", store.RoleAdmin)
	pen := apiEvent(t, st, "Pencilled Dinner", "public", false, "planning", "2099-11-05", "18:00", "Hall")
	apiEvent(t, st, "Sure Dinner", "public", false, "planning", "2099-11-06", "18:00", "Hall")
	apiEvent(t, st, "Undated Dinner", "public", false, "planning", "", "")
	st.DB.Exec(`UPDATE events SET date_tentative = 1 WHERE id = ?`, pen)

	body := anonClient(c).get("/?month=2099-11", 200)
	if !strings.Contains(body, "cal-item--tentative") || !strings.Contains(body, "Pencilled in") {
		t.Error("pencilled-in item not marked")
	}
	if n := strings.Count(body, "cal-item--tentative"); n != 2 { // grid + agenda
		t.Errorf("cal-item--tentative appears %d times, want 2", n)
	}
	if strings.Contains(body, "Undated Dinner") {
		t.Error("undated event on the calendar")
	}
}

func TestPencilStatusMark(t *testing.T) {
	for status, want := range map[string]string{"tentative": "Pencilled in", "pending": "Pending", "confirmed": "", "": ""} {
		if got := statusMark(status); got != want {
			t.Errorf("statusMark(%q) = %q, want %q", status, got, want)
		}
	}
	b := calendar.Block{Source: "events", SourceID: "4", Title: "Dinner", StartsOn: "2099-01-01", Status: "tentative", Rooms: []string{"Hall"}}
	if c := newClash(b, false); c.Mark != "Pencilled in" {
		t.Errorf("clash mark %q", c.Mark)
	}
	b.Status = "pending"
	if c := newClash(b, false); c.Mark != "Pending" {
		t.Errorf("clash mark %q", c.Mark)
	}
	b.Status = "confirmed"
	if c := newClash(b, false); c.Mark != "" {
		t.Errorf("clash mark %q", c.Mark)
	}
}
