package server

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tylergoza/event-planner/internal/calendar"
	"github.com/tylergoza/event-planner/internal/store"
)

func TestSpansOverlap(t *testing.T) {
	for _, c := range []struct {
		name string
		a, b span
		want bool
	}{
		{"same hours", span{"2026-11-07", "14:00", "", "16:00"}, span{"2026-11-07", "15:00", "", "17:00"}, true},
		{"back to back", span{"2026-11-07", "14:00", "", "16:00"}, span{"2026-11-07", "16:00", "", "18:00"}, false},
		{"other day", span{"2026-11-07", "14:00", "", "16:00"}, span{"2026-11-08", "14:00", "", "16:00"}, false},
		{"all day vs timed", span{"2026-11-07", "14:00", "", "16:00"}, span{"2026-11-07", "", "", ""}, true},
		{"timed vs all day other date", span{"2026-11-07", "14:00", "", "16:00"}, span{"2026-11-08", "", "", ""}, false},
		{"multi-day covers middle", span{"2026-11-08", "09:00", "", "10:00"}, span{"2026-11-07", "18:00", "2026-11-09", "08:00"}, true},
		{"multi-day ends before", span{"2026-11-09", "09:00", "", "10:00"}, span{"2026-11-07", "18:00", "2026-11-09", "08:00"}, false},
		{"start only runs to end of day", span{"2026-11-07", "20:00", "", ""}, span{"2026-11-07", "18:00", "", ""}, true},
		{"bad date keeps warning", span{"nope", "", "", ""}, span{"2026-11-07", "", "", ""}, true},
	} {
		if got := spansOverlap(c.a, c.b); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
		if got := spansOverlap(c.b, c.a); got != c.want {
			t.Errorf("%s reversed: got %v", c.name, got)
		}
	}
}

func TestBlockWhen(t *testing.T) {
	b := calendar.Block{StartsOn: "2026-11-07", StartTime: "14:00", EndTime: "16:00"}
	if got := blockWhen(b, false); got != "2:00–4:00 pm" {
		t.Error(got)
	}
	b.StartTime = "11:00"
	if got := blockWhen(b, true); got != "Nov 7, 11:00 am–4:00 pm" {
		t.Error(got)
	}
	if got := blockWhen(calendar.Block{StartsOn: "2026-11-07"}, false); got != "all day" {
		t.Error(got)
	}
}

// calendarWithBlocks answers availability with the given JSON blocks, or 500.
func calendarWithBlocks(t *testing.T, blocks string, fail bool) *httptest.Server {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, `{"busy": %s}`, blocks)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func roomsCalendarSetup(t *testing.T, ts *httptest.Server) (*client, *Server, *store.Store) {
	srv, st := newConnServer(t, map[string]string{"CALENDAR_URL": ts.URL, "CALENDAR_TOKEN": "cal_test"})
	web := httptest.NewServer(srv)
	t.Cleanup(web.Close)
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: web.URL, http: &http.Client{Jar: jar}}
	st.CreateUser("ann", "", "a long password", store.RolePlanner)
	id, _ := st.CreateEvent(&store.Event{Title: "Dinner", StartsOn: "2026-11-07", StartTime: "15:00", EndTime: "17:00"})
	st.AddRoom(id, 0, "Lifecenter")
	return c.as("ann"), srv, st
}

func TestRoomsPageCalendarClashes(t *testing.T) {
	ts := calendarWithBlocks(t, `[
		{"source":"other","source_id":"9","busy":true,"starts_on":"2026-11-07","start_time":"14:00","end_time":"16:00","rooms":["Lifecenter"]},
		{"source":"production","source_id":"3","title":"Choir","starts_on":"2026-11-07","start_time":"16:30","end_time":"18:00","rooms":["lifecenter"]},
		{"source":"events","source_id":"1","title":"Dinner","starts_on":"2026-11-07","start_time":"15:00","end_time":"17:00","rooms":["Lifecenter"]},
		{"source":"events","source_id":"2","title":"Cleanup","starts_on":"2026-11-07","start_time":"09:00","end_time":"10:00","rooms":["Lifecenter"]},
		{"source":"events","source_id":"5","title":"Youth","starts_on":"2026-11-07","start_time":"15:30","end_time":"16:00","rooms":["Lifecenter"]}]`, false)
	c, _, _ := roomsCalendarSetup(t, ts)
	body := c.get("/events/1/rooms", 200)
	for _, want := range []string{"Booked · 2:00–4:00 pm", "Choir", "Production planner", `<a href="/events/5">Youth</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}
	if strings.Contains(body, "Cleanup") || strings.Contains(body, "Couldn't reach") {
		t.Error("showed a block outside the event's time or the unreachable hint")
	}
	if strings.Contains(body, ">Dinner</a>") {
		t.Error("showed the event's own booking")
	}
}

func TestRoomsPageCalendarDownFallsBack(t *testing.T) {
	ts := calendarWithBlocks(t, `[]`, true)
	c, _, st := roomsCalendarSetup(t, ts)
	other, _ := st.CreateEvent(&store.Event{Title: "Wedding", StartsOn: "2026-11-07"})
	st.DB.Exec(`UPDATE event_rooms SET place_id = 7`)
	st.AddRoom(other, 7, "Lifecenter")
	body := c.get("/events/1/rooms", 200)
	if !strings.Contains(body, "Couldn't reach the calendar, so only this app's events were checked.") {
		t.Error("no hint")
	}
	if !strings.Contains(body, "Double-booked") || !strings.Contains(body, "Wedding") {
		t.Errorf("same-app check missing:\n%s", body)
	}
}

func TestRoomsPageNoCalendar(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("ann", "", "a long password", store.RolePlanner)
	id, _ := st.CreateEvent(&store.Event{Title: "Dinner", StartsOn: "2026-11-07"})
	st.AddRoom(id, 0, "Lifecenter")
	body := c.as("ann").get("/events/1/rooms", 200)
	if strings.Contains(body, "Couldn't reach") || strings.Contains(body, "Also booked on the calendar") {
		t.Error("calendar mentioned without one")
	}
}

func TestWorkerKeepsConflictsForEventPage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":1,"conflicts":[{"source":"other","busy":true,"starts_on":"2026-11-07","start_time":"14:00","end_time":"16:00","rooms":["Lifecenter"]}]}`)
	}))
	t.Cleanup(ts.Close)
	c, srv, st := roomsCalendarSetup(t, ts)
	if got := c.get("/events/1", 200); strings.Contains(got, "The calendar shows these") {
		t.Fatal("warning before any send")
	}
	// The test server's worker isn't running; send by hand.
	srv.sendCalendarJobs(t.Context())
	raw, _ := st.CalendarConflicts(1)
	if raw == "" {
		t.Fatal("conflicts not kept")
	}
	body := c.get("/events/1", 200)
	if !strings.Contains(body, "The calendar shows these at the same time in the same rooms") || !strings.Contains(body, "Booked · Nov 7, 2:00–4:00 pm") {
		t.Errorf("event page:\n%s", body)
	}
}
