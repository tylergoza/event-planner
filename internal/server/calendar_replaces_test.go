package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tylergoza/event-planner/internal/store"
)

func TestMakeThisAnEventForm(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("ann", "", "a long password", store.RolePlanner)
	st.CreateUser("hal", "", "a long password", store.RoleHelper)
	ann := c.as("ann")

	body := ann.get("/events/new?from_calendar=305&title=Youth+Night&starts_on=2099-03-04&start_time=18:00&end_time=20:30&ends_on=2099-03-05&rooms=Lifecenter,+Kitchen", 200)
	for _, want := range []string{`value="Youth Night"`, `value="2099-03-04"`, `value="18:00"`, `value="20:30"`, `value="2099-03-05"`,
		`name="from_calendar" value="305"`, `name="calendar_rooms" value="Lifecenter,Kitchen"`, "Rooms from the calendar: Lifecenter, Kitchen", "booking #305"} {
		if !strings.Contains(body, want) {
			t.Errorf("form lacks %q", want)
		}
	}

	// Bad values are ignored.
	body = ann.get("/events/new?from_calendar=7&title=X&starts_on=nope&start_time=99:99&ends_on=2000-01-01", 200)
	if strings.Contains(body, "nope") || strings.Contains(body, "99:99") || strings.Contains(body, `value="2000-01-01"`) {
		t.Error("bad values kept")
	}
	// A bad from_calendar means no prefill at all.
	for _, q := range []string{"abc", "0", "-3"} {
		body = ann.get("/events/new?from_calendar="+q+"&title=Sneaky&rooms=Hall", 200)
		if strings.Contains(body, `name="from_calendar"`) || strings.Contains(body, "Sneaky") || strings.Contains(body, "calendar_rooms") {
			t.Errorf("from_calendar=%s used", q)
		}
	}

	hal := c.as("hal")
	hal.get("/events/new?from_calendar=305&title=Youth", 403)
}

func TestMakeThisAnEventCreate(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("ann", "", "a long password", store.RolePlanner)
	ann := c.as("ann")
	ann.post("/events/new", "/events", url.Values{"title": {"Youth Night"}, "status": {"planning"}, "starts_on": {"2099-03-04"},
		"from_calendar": {"305"}, "calendar_rooms": {"Lifecenter,Kitchen,lifecenter"}}, 200)
	e, err := st.GetEvent(1)
	if err != nil || e.CalendarReplaces != 305 {
		t.Fatalf("event %+v, %v", e, err)
	}
	rooms, _ := st.ListRooms(1)
	if len(rooms) != 2 || rooms[0].PlaceName != "Lifecenter" || rooms[1].PlaceName != "Kitchen" || rooms[0].PlaceID != 0 {
		t.Fatalf("rooms %+v", rooms)
	}
	// Without it, nothing is stored.
	ann.post("/events/new", "/events", url.Values{"title": {"Plain"}, "status": {"planning"}, "starts_on": {"2099-03-05"}, "from_calendar": {"x"}}, 200)
	if e, _ := st.GetEvent(2); e.CalendarReplaces != 0 {
		t.Fatalf("replaces = %d", e.CalendarReplaces)
	}
	// Copying doesn't carry it.
	if id, err := st.CopyEvent(1, store.CopyOptions{}); err == nil {
		if cp, _ := st.GetEvent(id); cp.CalendarReplaces != 0 {
			t.Fatalf("copy replaces = %d", cp.CalendarReplaces)
		}
	}
}

func TestCalendarWorkerSendsReplacesOnce(t *testing.T) {
	f, ts := newFakeCalendar(t)
	srv, st := newConnServer(t, map[string]string{"CALENDAR_URL": ts.URL, "CALENDAR_TOKEN": "cal_test"})
	ctx := context.Background()
	id, _ := st.CreateEvent(&store.Event{Title: "Youth Night", StartsOn: "2099-03-04", CalendarReplaces: 305})
	srv.sendCalendarJobs(ctx)
	calls := f.take()
	if len(calls) != 1 || calls[0].Method != http.MethodPut || calls[0].Body["replaces"] != float64(305) {
		t.Fatalf("first calls %+v", calls)
	}
	if e, _ := st.GetEvent(id); e.CalendarReplaces != 0 {
		t.Fatalf("not cleared: %d", e.CalendarReplaces)
	}
	// Clearing queued nothing.
	if s, _ := st.CalendarOutbox(); s.Waiting != 0 {
		t.Fatalf("waiting after clear: %+v", s)
	}
	srv.sendCalendarJobs(ctx)
	if calls := f.take(); len(calls) != 0 {
		t.Fatalf("resent: %+v", calls)
	}
	// An edit sends again, without replaces.
	e, _ := st.GetEvent(id)
	e.Title = "Youth Night 2"
	st.UpdateEvent(e)
	srv.sendCalendarJobs(ctx)
	calls = f.take()
	if len(calls) != 1 {
		t.Fatalf("second calls %+v", calls)
	}
	if _, ok := calls[0].Body["replaces"]; ok {
		t.Fatalf("replaces sent again: %v", calls[0].Body)
	}
}
