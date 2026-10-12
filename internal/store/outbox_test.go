package store

import (
	"testing"
	"time"
)

// outbox is the waiting rows as event ID → op.
func outbox(t *testing.T, st *Store) map[int64]string {
	t.Helper()
	rows, err := st.DB.Query(`SELECT event_id, op FROM calendar_outbox`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var op string
		rows.Scan(&id, &op)
		out[id] = op
	}
	return out
}

func clearOutbox(t *testing.T, st *Store) {
	t.Helper()
	if _, err := st.DB.Exec(`DELETE FROM calendar_outbox`); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxQueuesChanges(t *testing.T) {
	st := openTest(t)
	id := newEvent(t, st, "Dinner")
	if got := outbox(t, st); got[id] != "put" || len(got) != 1 {
		t.Fatalf("after create: %v", got)
	}

	// Notes and headcount don't show on the calendar.
	clearOutbox(t, st)
	e, _ := st.GetEvent(id)
	e.Notes, e.ExpectedCount, e.Coordinator = "bring pies", 40, "Pat"
	st.UpdateEvent(e)
	if got := outbox(t, st); len(got) != 0 {
		t.Fatalf("notes edit queued: %v", got)
	}

	// Ten quick edits: one row.
	for i := 0; i < 10; i++ {
		e.Title = "Dinner " + string(rune('A'+i))
		if err := st.UpdateEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM calendar_outbox`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d rows after ten edits", n)
	}

	// Locking and rooms queue too.
	for _, change := range []func(){
		func() { e.Locked = true; st.UpdateEvent(e) },
		func() { st.AddRoom(id, 41, "Lifecenter") },
	} {
		clearOutbox(t, st)
		change()
		if got := outbox(t, st); got[id] != "put" {
			t.Fatalf("change not queued: %v", got)
		}
	}
	rooms, _ := st.ListRooms(id)
	clearOutbox(t, st)
	st.UpdateRoom(&Room{ID: rooms[0].ID, Purpose: "Seating"}) // purpose isn't sent
	if got := outbox(t, st); len(got) != 0 {
		t.Fatalf("purpose edit queued: %v", got)
	}
	st.DeleteRoom(rooms[0].ID)
	if got := outbox(t, st); got[id] != "put" {
		t.Fatalf("room delete: %v", got)
	}

	// Deleting the event (and so its rooms) leaves a delete.
	st.AddRoom(id, 0, "Kitchen")
	if err := st.DeleteEvent(id); err != nil {
		t.Fatal(err)
	}
	if got := outbox(t, st); got[id] != "delete" || len(got) != 1 {
		t.Fatalf("after delete: %v", got)
	}
}

func TestOutboxSeries(t *testing.T) {
	st := openTest(t)
	tpl, sid, _ := seriesFixture(t, st)
	occ, _ := st.Occurrences(sid)
	got := outbox(t, st)
	for _, o := range occ {
		if got[o.ID] != "put" {
			t.Fatalf("occurrence %d (%s) not queued: %v", o.ID, o.StartsOn, got)
		}
	}
	clearOutbox(t, st)
	if err := st.SkipOccurrence(occ[2].ID); err != nil {
		t.Fatal(err)
	}
	if got := outbox(t, st); got[occ[2].ID] != "delete" || len(got) != 1 {
		t.Fatalf("after skip: %v", got)
	}
	clearOutbox(t, st)
	title := "Sunday supper"
	if _, err := st.ApplyDetailsForward(tpl, DetailsChange{Title: &title}, "pat"); err != nil {
		t.Fatal(err)
	}
	if got := outbox(t, st); len(got) != len(occ)-1 { // all but the skipped one
		t.Fatalf("details forward queued %d of %d: %v", len(got), len(occ)-1, got)
	}
}

func TestOutboxSendAndRetry(t *testing.T) {
	st := openTest(t)
	id := newEvent(t, st, "Dinner")
	jobs, err := st.DueCalendarJobs(10)
	if err != nil || len(jobs) != 1 || jobs[0].EventID != id {
		t.Fatalf("due %+v, %v", jobs, err)
	}
	j := jobs[0]

	// A failure waits.
	if err := st.CalendarFailed(j, "couldn't reach the calendar", time.Minute); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := st.DueCalendarJobs(10); len(jobs) != 0 {
		t.Fatalf("failed job due again straight away: %+v", jobs)
	}
	status, _ := st.CalendarOutbox()
	if status.Waiting != 1 || status.LastError != "couldn't reach the calendar" || status.LastSent != "" {
		t.Fatalf("status %+v", status)
	}

	// Changed while sending: clearing the old row leaves the new change.
	e, _ := st.GetEvent(id)
	e.Title = "Supper"
	st.UpdateEvent(e)
	st.CalendarSent(j)
	jobs, _ = st.DueCalendarJobs(10)
	if len(jobs) != 1 || jobs[0].Seq == j.Seq || jobs[0].Attempts != 0 {
		t.Fatalf("newer change lost: %+v", jobs)
	}
	st.CalendarSent(jobs[0])
	if status, _ := st.CalendarOutbox(); status.Waiting != 0 || status.LastSent == "" {
		t.Fatalf("status after send %+v", status)
	}
}

func TestOutboxBackfill(t *testing.T) {
	st := openTest(t)
	old, _ := st.CreateEvent(&Event{Title: "Old", StartsOn: "2024-01-01"})
	a, _ := st.CreateEvent(&Event{Title: "A", StartsOn: "2026-01-01"})
	b, _ := st.CreateEvent(&Event{Title: "B", StartsOn: "2025-09-30", EndsOn: "2025-10-12", Status: "cancelled"})
	clearOutbox(t, st)
	if n, _ := st.CountCalendarBackfill("2025-10-12"); n != 2 {
		t.Fatalf("count = %d", n)
	}
	for i := 0; i < 2; i++ {
		if _, err := st.QueueCalendarBackfill("2025-10-12"); err != nil {
			t.Fatal(err)
		}
	}
	got := outbox(t, st)
	if len(got) != 2 || got[a] != "put" || got[b] != "put" || got[old] != "" {
		t.Fatalf("backfill queued %v", got)
	}
}
