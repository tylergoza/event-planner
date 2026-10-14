package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/tylergoza/event-planner/internal/store"
)

type calCall struct {
	Method, Path string
	Body         map[string]any
}

// fakeCalendar records what it's sent; fail makes it answer 500.
type fakeCalendar struct {
	mu    sync.Mutex
	calls []calCall
	fail  bool
}

func (f *fakeCalendar) take() []calCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

func newFakeCalendar(t *testing.T) (*fakeCalendar, *httptest.Server) {
	f := &fakeCalendar{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := calCall{Method: r.Method, Path: r.URL.Path}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			json.Unmarshal(b, &c.Body)
		}
		f.mu.Lock()
		f.calls = append(f.calls, c)
		fail := f.fail
		f.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"database is locked"}`))
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Write([]byte(`{"id": 1, "conflicts": []}`))
	}))
	t.Cleanup(ts.Close)
	return f, ts
}

func TestCalendarWorker(t *testing.T) {
	f, ts := newFakeCalendar(t)
	srv, st := newConnServer(t, map[string]string{"CALENDAR_URL": ts.URL, "CALENDAR_TOKEN": "cal_test"})
	st.SetSetting("base_url", "https://events.example.org")
	srv.reloadSettings()
	ctx := context.Background()

	id, _ := st.CreateEvent(&store.Event{Title: "Fall Dinner", Kind: "Fellowship dinner", StartsOn: "2026-11-07", StartTime: "17:30",
		Visibility: "public", PublicDescription: "Potluck", Notes: "secret notes"})
	st.AddRoom(id, 41, "Lifecenter")
	st.AddRoom(id, 0, "lifecenter")
	srv.sendCalendarJobs(ctx)
	calls := f.take()
	if len(calls) != 1 || calls[0].Method != http.MethodPut || calls[0].Path != "/api/v1/bookings/events/1" {
		t.Fatalf("calls %+v", calls)
	}
	b := calls[0].Body
	if b["title"] != "Fall Dinner" || b["description"] != "Potluck" || b["visibility"] != "public" || b["status"] != "confirmed" ||
		b["url"] != "https://events.example.org/events/1" || len(b["rooms"].([]any)) != 1 {
		t.Fatalf("sent %v", b)
	}
	if _, ok := b["notes"]; ok {
		t.Fatal("notes sent")
	}
	if s, _ := st.CalendarOutbox(); s.Waiting != 0 {
		t.Fatalf("still waiting: %+v", s)
	}

	// Locked: no title, kind or description. Cancelled shows as cancelled.
	e, _ := st.GetEvent(id)
	e.Locked, e.Status = true, "cancelled"
	st.UpdateEvent(e)
	srv.sendCalendarJobs(ctx)
	b = f.take()[0].Body
	if b["title"] != "" || b["kind"] != "" || b["description"] != "" || b["visibility"] != "locked" || b["status"] != "cancelled" {
		t.Fatalf("locked sent %v", b)
	}

	// Deleted: a DELETE.
	st.DeleteEvent(id)
	srv.sendCalendarJobs(ctx)
	if calls := f.take(); len(calls) != 1 || calls[0].Method != http.MethodDelete {
		t.Fatalf("delete calls %+v", calls)
	}
}

func TestCalendarWorkerBacksOff(t *testing.T) {
	f, ts := newFakeCalendar(t)
	srv, st := newConnServer(t, map[string]string{"CALENDAR_URL": ts.URL, "CALENDAR_TOKEN": "cal_test"})
	f.fail = true
	st.CreateEvent(&store.Event{Title: "A", StartsOn: "2026-11-07"})
	st.CreateEvent(&store.Event{Title: "B", StartsOn: "2026-11-08"})
	srv.sendCalendarJobs(context.Background())
	// The calendar answered, so both were tried; neither is due again yet.
	if calls := f.take(); len(calls) != 2 {
		t.Fatalf("calls %+v", calls)
	}
	srv.sendCalendarJobs(context.Background())
	if calls := f.take(); len(calls) != 0 {
		t.Fatalf("retried straight away: %+v", calls)
	}
	s, _ := st.CalendarOutbox()
	if s.Waiting != 2 || s.LastError == "" {
		t.Fatalf("status %+v", s)
	}
	if calendarRetry(1) != time.Minute || calendarRetry(3) != 30*time.Minute || calendarRetry(9) != time.Hour {
		t.Fatal("back-off steps")
	}
}

func TestCalendarWorkerUnreachableStops(t *testing.T) {
	srv, st := newConnServer(t, map[string]string{"CALENDAR_URL": "http://127.0.0.1:1", "CALENDAR_TOKEN": "cal_test"})
	st.CreateEvent(&store.Event{Title: "A", StartsOn: "2026-11-07"})
	st.CreateEvent(&store.Event{Title: "B", StartsOn: "2026-11-08"})
	srv.sendCalendarJobs(context.Background())
	var tried int
	st.DB.QueryRow(`SELECT COUNT(*) FROM calendar_outbox WHERE attempts > 0`).Scan(&tried)
	if tried != 1 {
		t.Fatalf("%d tried after the first couldn't connect, want 1", tried)
	}
}

func TestCalendarWorkerOffWithoutConnection(t *testing.T) {
	srv, st := newConnServer(t, nil)
	st.CreateEvent(&store.Event{Title: "A", StartsOn: "2026-11-07"})
	srv.sendCalendarJobs(context.Background())
	if s, _ := st.CalendarOutbox(); s.Waiting != 1 || s.LastError != "" {
		t.Fatalf("status %+v", s)
	}
}

func TestCalendarWorkerStops(t *testing.T) {
	srv, _ := newConnServer(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.RunCalendarWorker(ctx); close(done) }()
	srv.KickCalendar()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker didn't stop")
	}
}

func TestBookingSignupOnlyForPublicEvents(t *testing.T) {
	srv, st := newConnServer(t, map[string]string{})
	st.SetSetting("base_url", "https://events.example.org")
	srv.reloadSettings()
	id, _ := st.CreateEvent(&store.Event{Title: "Fall Dinner", StartsOn: "2099-11-07", Visibility: "public"})
	f, _ := st.CreateSignupForm(id)
	f.Status = "open"
	if err := st.UpdateSignupForm(f); err != nil {
		t.Fatal(err)
	}
	e, _ := st.GetEvent(id)
	if srv.bookingSignup(e) == nil {
		t.Fatal("public event should carry the sign-up link")
	}
	e.Visibility = "private"
	if srv.bookingSignup(e) != nil {
		t.Fatal("private event must not carry the sign-up link")
	}
}
