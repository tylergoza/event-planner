package server

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tylergoza/event-planner/internal/store"
)

func backfillServer(t *testing.T, env map[string]string) (*client, *store.Store) {
	t.Helper()
	c, st := newTestServerWith(t, Config{Env: env})
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	st.CreateUser("ann", "", "a long password", store.RolePlanner)
	return c.as("boss"), st
}

func seedBackfillEvents(t *testing.T, st *store.Store) {
	t.Helper()
	now := time.Now()
	for _, e := range []store.Event{
		{Title: "Old", StartsOn: now.AddDate(-2, 0, 0).Format(store.DateLayout)},
		{Title: "Recent", StartsOn: now.AddDate(0, -3, 0).Format(store.DateLayout)},
		{Title: "Cancelled", Status: "cancelled", StartsOn: now.AddDate(0, -1, 0).Format(store.DateLayout)},
		{Title: "Soon", StartsOn: now.AddDate(0, 1, 0).Format(store.DateLayout)},
	} {
		if _, err := st.CreateEvent(&e); err != nil {
			t.Fatal(err)
		}
	}
	// Saving queues rows by itself; start from an empty outbox.
	if _, err := st.DB.Exec(`DELETE FROM calendar_outbox`); err != nil {
		t.Fatal(err)
	}
}

func outboxCount(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM calendar_outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCalendarBackfillButton(t *testing.T) {
	admin, st := backfillServer(t, map[string]string{"CALENDAR_URL": "http://127.0.0.1:1", "CALENDAR_TOKEN": "cal_test"})
	seedBackfillEvents(t, st)

	page := admin.get("/admin/connections", 200)
	if !strings.Contains(page, "Send 3 events from") || !strings.Contains(page, "Waiting to send") {
		t.Fatalf("expected the button and status on the page:\n%s", page)
	}
	admin.post("/admin/connections", "/admin/connections/calendar/backfill", url.Values{}, 200)
	want := 3
	if got := outboxCount(t, st); got != want {
		t.Fatalf("outbox has %d rows, want %d", got, want)
	}
	var old int
	st.DB.QueryRow(`SELECT COUNT(*) FROM calendar_outbox WHERE event_id = 1`).Scan(&old)
	if old != 0 {
		t.Error("an event from two years ago was queued")
	}
	// Again: still one row per event.
	if body := admin.post("/admin/connections", "/admin/connections/calendar/backfill", url.Values{}, 200); !strings.Contains(body, "Queued 3 events") {
		t.Errorf("expected the flash, got:\n%s", body)
	}
	if got := outboxCount(t, st); got != want {
		t.Fatalf("after pressing twice: %d rows, want %d", got, want)
	}

	var status struct {
		Waiting  int    `json:"waiting"`
		LastSent string `json:"last_sent"`
	}
	if err := json.Unmarshal([]byte(admin.get("/admin/connections/calendar/status", 200)), &status); err != nil {
		t.Fatal(err)
	}
	if status.Waiting != want || status.LastSent != "never" {
		t.Errorf("status = %+v", status)
	}
}

func TestCalendarBackfillAdminsOnly(t *testing.T) {
	admin, st := backfillServer(t, map[string]string{"CALENDAR_URL": "http://127.0.0.1:1", "CALENDAR_TOKEN": "cal_test"})
	seedBackfillEvents(t, st)
	ann := admin.as("ann")
	ann.get("/admin/connections/calendar/status", 403)
	ann.post("/account", "/admin/connections/calendar/backfill", url.Values{}, 403)
	if outboxCount(t, st) != 0 {
		t.Error("a non-admin queued events")
	}
}

func TestCalendarBackfillNotConnected(t *testing.T) {
	admin, st := backfillServer(t, map[string]string{"CALENDAR_URL": ""})
	seedBackfillEvents(t, st)
	if page := admin.get("/admin/connections", 200); strings.Contains(page, "Send all events") {
		t.Error("button shown without a calendar")
	}
	if body := admin.post("/admin/connections", "/admin/connections/calendar/backfill", url.Values{}, 200); !strings.Contains(body, "connected, so there") {
		t.Errorf("expected an error flash:\n%s", body)
	}
	if outboxCount(t, st) != 0 {
		t.Error("events queued with no calendar")
	}
}
