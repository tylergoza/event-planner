package server

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tylergoza/event-planner/internal/store"
)

func anonClient(c *client) *client {
	return &client{t: c.t, base: c.base, http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
}

func TestIsPublicPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/": true, "/calendar": true, "/calendar/3": true, "/calendar.ics": true, "/api/v1/events": true,
		"/events": false, "/events/1": false, "/admin/users": false, "/calendars": false, "/live": false,
	} {
		if isPublicPath(p) != want {
			t.Errorf("isPublicPath(%q) = %v", p, !want)
		}
	}
}

func TestPublicCalendar(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("coordinator", "", "a long password", store.RoleAdmin)
	pub := apiEvent(t, st, "Harvest Dinner", "public", false, "planning", "2099-11-05", "18:00", "Lifecenter")
	priv := apiEvent(t, st, "Elders Meeting", "private", false, "planning", "2099-11-06", "14:00", "Lifecenter", "Kitchen")
	locked := apiEvent(t, st, "Locked Retreat", "public", true, "planning", "2099-11-07", "10:00", "Chapel")
	canc := apiEvent(t, st, "Cancelled Party", "public", false, "cancelled", "2099-11-08", "10:00")
	apiEvent(t, st, "Secret Chat", "private", false, "planning", "2099-11-09", "")
	st.DB.Exec(`UPDATE events SET end_time = '16:00' WHERE id = ?`, priv)
	// A multi-day public event, 11-10 to 11-12.
	multi := apiEvent(t, st, "Youth Camp", "public", false, "planning", "2099-11-10", "")
	st.DB.Exec(`UPDATE events SET ends_on = '2099-11-12' WHERE id = ?`, multi)

	anon := anonClient(c)
	body := anon.get("/?month=2099-11", 200)
	for _, want := range []string{"Harvest Dinner", "Lifecenter, Kitchen unavailable · 2:00–4:00 pm", "Chapel unavailable · 10:00 am", "Private event · all day", "/calendar/" + itoa(pub), "Sign in"} {
		if !strings.Contains(body, want) {
			t.Errorf("anonymous calendar missing %q", want)
		}
	}
	for _, bad := range []string{"Elders", "Locked Retreat", "Cancelled Party", "Secret Chat", "/events/", `href="/events"`, "SECRET"} {
		if strings.Contains(body, bad) {
			t.Errorf("anonymous calendar leaks %q", bad)
		}
	}
	// Multi-day: the camp is listed on each of its three days (grid + agenda).
	if n := strings.Count(body, "Youth Camp"); n != 6 {
		t.Errorf("Youth Camp shown %d times, want 6 (3 days x grid and agenda)", n)
	}
	if !strings.Contains(body, "All day") {
		t.Error("an all-day event should say so")
	}

	// Navigation and /calendar are the same page.
	if cal := anon.get("/calendar?month=2099-11", 200); cal != body {
		// CSRF tokens differ between requests only when cookies differ; no jar here, so compare titles.
		if !strings.Contains(cal, "November 2099") {
			t.Error("/calendar?month= should show that month")
		}
	}
	if next := anon.get("/calendar?month=2099-11", 200); !strings.Contains(next, `href="/calendar?month=2099-12"`) || !strings.Contains(next, `href="/calendar?month=2099-10"`) {
		t.Error("prev/next links missing")
	}
	if other := anon.get("/calendar?month=2099-12", 200); strings.Contains(other, "Harvest Dinner") {
		t.Error("December shouldn't show November's events")
	}

	// Room filter.
	filtered := anon.get("/calendar?month=2099-11&room=Chapel", 200)
	if strings.Contains(filtered, "Harvest Dinner") || !strings.Contains(filtered, "Chapel unavailable") {
		t.Error("room filter should keep only entries using the room")
	}
	if !strings.Contains(filtered, `<option value="Lifecenter"`) {
		t.Error("room select should list every room used this month")
	}
	// Only rooms with events on the page are offered: not a cancelled
	// event's room, not a room booked in another month.
	apiEvent(t, st, "Cancelled Gym Night", "public", false, "cancelled", "2099-11-20", "19:00", "Gym")
	apiEvent(t, st, "January Brunch", "public", false, "planning", "2100-01-15", "10:00", "Library")
	nov := anon.get("/calendar?month=2099-11", 200)
	for _, unused := range []string{"Gym", "Library"} {
		if strings.Contains(nov, `<option value="`+unused+`"`) {
			t.Errorf("room select offers %q, which has no events this month", unused)
		}
	}
	if dec := anon.get("/calendar?month=2099-12", 200); strings.Contains(dec, `id="cal-room"`) {
		t.Error("a month with no events shouldn't show the room filter")
	}

	// Event pages.
	page := anon.get("/calendar/"+itoa(pub), 200)
	if !strings.Contains(page, "Harvest Dinner") || !strings.Contains(page, "Come, hungry; bring a dish") || strings.Contains(page, "/events/") || strings.Contains(page, "SECRET") {
		t.Error("public event page content")
	}
	for _, id := range []int64{priv, locked, canc, 999} {
		anon.get("/calendar/"+itoa(id), 404)
	}

	// Still private elsewhere.
	if resp := anon.getRaw(anon.http, "/events"); resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Errorf("/events should redirect to login, got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// No cookies on the API and feed; and no session lookup is needed.
	for _, p := range []string{"/api/v1/events" + apiRangeQ, "/calendar.ics", "/api/v1/nothing"} {
		resp, err := http.Get(c.base + p)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if len(resp.Header.Values("Set-Cookie")) != 0 {
			t.Errorf("%s set a cookie: %v", p, resp.Header["Set-Cookie"])
		}
	}
}

func TestSignedInCalendar(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	st.CreateUser("bob", "", "a long password", store.RoleHelper)
	pub := apiEvent(t, st, "Harvest Dinner", "public", false, "planning", "2099-11-05", "18:00", "Lifecenter")
	priv := apiEvent(t, st, "Elders Meeting", "private", false, "planning", "2099-11-06", "14:00", "Library")
	locked := apiEvent(t, st, "Locked Retreat", "private", true, "planning", "2099-11-07", "10:00", "Chapel")
	_ = pub

	bob := c.as("bob")
	if home := bob.get("/", 200); !strings.Contains(home, "Dashboard") || !strings.Contains(home, `href="/calendar"`) {
		t.Error("signed in, / should be the dashboard with a Calendar link")
	}
	body := bob.get("/calendar?month=2099-11", 200)
	if !strings.Contains(body, "Elders Meeting") || !strings.Contains(body, "/events/"+itoa(priv)) {
		t.Error("signed-in people should see private events with a link to the planning page")
	}
	if strings.Contains(body, "Locked Retreat") || !strings.Contains(body, "Chapel unavailable · 10:00 am") {
		t.Error("a locked event bob isn't on should be busy")
	}
	if !strings.Contains(body, "Dashboard") {
		t.Error("signed-in calendar should have the app nav")
	}
	bob.get("/calendar/"+itoa(priv), 200)
	bob.get("/calendar/"+itoa(locked), 404)

	boss := c.as("boss")
	if !strings.Contains(boss.get("/calendar?month=2099-11", 200), "Locked Retreat") {
		t.Error("admins see locked events")
	}
}

func TestCalendarText(t *testing.T) {
	for _, tc := range []struct{ s, e, want string }{
		{"14:00", "16:00", "2:00–4:00 pm"}, {"11:00", "13:00", "11:00 am–1:00 pm"}, {"17:30", "", "5:30 pm"}, {"", "", ""},
	} {
		if got := timeRange(tc.s, tc.e); got != tc.want {
			t.Errorf("timeRange(%q,%q) = %q, want %q", tc.s, tc.e, got, tc.want)
		}
	}
	allDay := newCalItem(store.CalendarEntry{Busy: true, Rooms: []string{"Lifecenter", "Kitchen"}, StartsOn: "2099-01-01"}, "2099-01-01")
	if allDay.BusyText != "Lifecenter, Kitchen unavailable all day" {
		t.Error(allDay.BusyText)
	}
}
