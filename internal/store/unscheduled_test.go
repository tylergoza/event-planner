package store

import (
	"reflect"
	"testing"
)

func setTentative(t *testing.T, st *Store, id int64, v bool) {
	t.Helper()
	if _, err := st.DB.Exec(`UPDATE events SET date_tentative = ? WHERE id = ?`, v, id); err != nil {
		t.Fatal(err)
	}
}

func TestUnscheduledRoundTrip(t *testing.T) {
	st := openTest(t)
	id, err := st.CreateEvent(&Event{Title: "Someday"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := st.GetEvent(id)
	if err != nil {
		t.Fatal(err)
	}
	if e.Scheduled() || e.StartsOn != "" || e.When() != "Not scheduled yet" || e.DateTentative {
		t.Fatalf("undated %+v when=%q", e, e.When())
	}
	e.Notes = "still no date"
	if err := st.UpdateEvent(e); err != nil {
		t.Fatal(err)
	}
	if e, _ = st.GetEvent(id); e.Scheduled() || e.Notes != "still no date" {
		t.Fatalf("after update %+v", e)
	}
	// Give it a date, then take it away again.
	e.StartsOn = "2026-12-01"
	st.UpdateEvent(e)
	if e, _ = st.GetEvent(id); !e.Scheduled() || e.When() == "Not scheduled yet" {
		t.Fatalf("dated %+v", e)
	}
	e.StartsOn = ""
	st.UpdateEvent(e)
	if e, _ = st.GetEvent(id); e.Scheduled() {
		t.Fatalf("cleared %+v", e)
	}
}

func TestTentativeRoundTrip(t *testing.T) {
	st := openTest(t)
	id, err := st.CreateEvent(&Event{Title: "Maybe", StartsOn: "2026-12-01", DateTentative: true})
	if err != nil {
		t.Fatal(err)
	}
	e, _ := st.GetEvent(id)
	if !e.DateTentative || !e.Scheduled() {
		t.Fatalf("created %+v", e)
	}
	e.DateTentative = false
	if err := st.UpdateEvent(e); err != nil {
		t.Fatal(err)
	}
	if e, _ = st.GetEvent(id); e.DateTentative {
		t.Fatal("still tentative")
	}
	e.DateTentative = true
	st.UpdateEvent(e)
	if e, _ = st.GetEvent(id); !e.DateTentative {
		t.Fatal("not tentative again")
	}
}

func TestUnscheduledListOrder(t *testing.T) {
	st := openTest(t)
	admin := calUser(t, st, "root", RoleAdmin)
	mk := func(title, status, starts string) int64 {
		id, err := st.CreateEvent(&Event{Title: title, Status: status, StartsOn: starts})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	late := mk("late", "planning", "2026-12-01")
	soon := mk("soon", "planning", "2026-11-01")
	und1 := mk("und1", "planning", "")
	und2 := mk("und2", "planning", "")
	done := mk("done", "done", "2026-10-01")
	canc := mk("cancelled undated", "cancelled", "")

	list, err := st.ListEvents(admin, false, "2026-10-13")
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for _, s := range list {
		got = append(got, s.ID)
	}
	// Active undated (newest first), active dated (soonest first), then finished (newest first).
	// The order among the finished two is not asserted (undated sorts first
	// among them today).
	want := []int64{und2, und1, soon, late}
	if len(got) != 6 || !reflect.DeepEqual(got[:4], want) {
		t.Fatalf("order %v, want %v then the finished ones", got, want)
	}
	if rest := map[int64]bool{got[4]: true, got[5]: true}; !rest[done] || !rest[canc] {
		t.Fatalf("finished ones should be last: %v", got)
	}

	undated, dated := Unscheduled(list)
	ids := func(l []EventSummary) []int64 {
		out := []int64{}
		for _, s := range l {
			out = append(out, s.ID)
		}
		return out
	}
	if !reflect.DeepEqual(ids(undated), []int64{und2, und1}) {
		t.Errorf("undated %v", ids(undated))
	}
	if d := ids(dated); len(d) != 4 || d[0] != soon || d[1] != late {
		t.Errorf("dated %v", d)
	}
	hasCanc := false
	for _, s := range dated {
		hasCanc = hasCanc || s.ID == canc
	}
	if !hasCanc {
		t.Error("cancelled undated event should be in the dated half")
	}

	active, _ := st.ListEvents(admin, true, "2026-10-13")
	if len(active) != 4 || active[0].ID != und2 {
		t.Errorf("active list %d first %d", len(active), active[0].ID)
	}
}

func TestUnscheduledLeftOffCalendar(t *testing.T) {
	st := openTest(t)
	dated := calEvent(t, st, "Dated", "public", false, "planning", "2026-11-05", "", "")
	undated := calEvent(t, st, "Undated", "public", false, "planning", "", "", "")

	list, err := st.Calendar("2000-01-01", "2100-01-01", CalendarPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].EventID != dated {
		t.Fatalf("calendar %v", titles(list))
	}
	if c, err := st.CalendarEvent(undated, CalendarPrivate); err == nil && c != nil {
		t.Fatalf("undated event on calendar: %+v", c)
	}
	if c, err := st.CalendarEvent(dated, CalendarPrivate); err != nil || c == nil {
		t.Fatalf("dated event missing: %v", err)
	}
}

func TestTentativeCalendarEntries(t *testing.T) {
	st := openTest(t)
	pub := calEvent(t, st, "Pencil", "public", false, "planning", "2026-11-05", "", "")
	sure := calEvent(t, st, "Sure", "public", false, "planning", "2026-11-06", "", "")
	priv := calEvent(t, st, "Secret pencil", "private", false, "planning", "2026-11-07", "", "", "Hall")
	setTentative(t, st, pub, true)
	setTentative(t, st, priv, true)

	list, err := st.Calendar("2026-11-01", "2026-11-30", CalendarPublic)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("entries %v", titles(list))
	}
	for _, c := range list {
		switch {
		case c.Busy:
			if !c.Tentative || c.Title != "" || c.EventID != 0 {
				t.Errorf("busy entry %+v", c)
			}
		case c.EventID == pub:
			if !c.Tentative {
				t.Errorf("pencilled entry not tentative %+v", c)
			}
		case c.EventID == sure:
			if c.Tentative {
				t.Errorf("confirmed entry tentative %+v", c)
			}
		default:
			t.Errorf("unexpected entry %+v", c)
		}
	}
	if c, err := st.CalendarEvent(pub, CalendarPublic); err != nil || c == nil || !c.Tentative {
		t.Errorf("CalendarEvent tentative: %+v %v", c, err)
	}
	if c, err := st.CalendarEvent(sure, CalendarPublic); err != nil || c == nil || c.Tentative {
		t.Errorf("CalendarEvent confirmed: %+v %v", c, err)
	}
}

func TestUnscheduledRoomConflicts(t *testing.T) {
	st := openTest(t)
	admin := calUser(t, st, "root", RoleAdmin)
	mk := func(title, starts, ends string) int64 {
		id, err := st.CreateEvent(&Event{Title: title, StartsOn: starts, EndsOn: ends})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.AddRoom(id, 7, "Main › Hall"); err != nil {
			t.Fatal(err)
		}
		return id
	}
	u1 := mk("u1", "", "")
	u2 := mk("u2", "", "")
	d1 := mk("d1", "2026-11-01", "2026-11-02")
	d2 := mk("d2", "2026-11-02", "")

	for _, id := range []int64{u1, u2} {
		got, err := st.RoomConflicts(id, admin)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("undated %d conflicts: %v", id, got)
		}
	}
	// A dated event doesn't conflict with undated ones either.
	for _, id := range []int64{d1, d2} {
		got, _ := st.RoomConflicts(id, admin)
		n := 0
		for _, refs := range got {
			for _, r := range refs {
				n++
				if r.ID == u1 || r.ID == u2 {
					t.Errorf("event %d conflicts with undated %d", id, r.ID)
				}
			}
		}
		if n != 1 {
			t.Errorf("event %d has %d conflicts, want 1: %v", id, n, got)
		}
	}
}

func TestConfirmDateQueuesOutbox(t *testing.T) {
	st := openTest(t)
	id, _ := st.CreateEvent(&Event{Title: "Maybe", StartsOn: "2026-12-01", DateTentative: true})

	// Only date_tentative changes: still queued.
	clearOutbox(t, st)
	setTentative(t, st, id, false)
	if got := outbox(t, st); got[id] != "put" || len(got) != 1 {
		t.Fatalf("tentative flip: %v", got)
	}
	setTentative(t, st, id, true)

	clearOutbox(t, st)
	if err := st.ConfirmDate(id); err != nil {
		t.Fatal(err)
	}
	if e, _ := st.GetEvent(id); e.DateTentative {
		t.Fatal("still tentative")
	}
	if got := outbox(t, st); got[id] != "put" || len(got) != 1 {
		t.Fatalf("confirm: %v", got)
	}

	// Already confirmed: nothing to send.
	clearOutbox(t, st)
	if err := st.ConfirmDate(id); err != nil {
		t.Fatal(err)
	}
	if got := outbox(t, st); len(got) != 0 {
		t.Fatalf("second confirm queued %v", got)
	}
}

func TestUnscheduledCopyEvent(t *testing.T) {
	st := openTest(t)
	src, _ := st.CreateEvent(&Event{Title: "Someday", Kind: "Fellowship dinner"})
	st.CreateTask(&Task{EventID: src, Title: "Book", DueOn: "2026-10-25"})
	st.CreateTask(&Task{EventID: src, Title: "No date"})

	id, err := st.CopyEvent(src, CopyOptions{StartsOn: "2026-11-08", By: "Pat"})
	if err != nil {
		t.Fatal(err)
	}
	e, _ := st.GetEvent(id)
	if e.StartsOn != "2026-11-08" || !e.Scheduled() || e.CopiedFrom != src {
		t.Fatalf("copy %+v", e)
	}
	tasks, _ := st.ListTasks(id)
	if len(tasks) != 2 {
		t.Fatalf("tasks %+v", tasks)
	}
	for _, tk := range tasks {
		if tk.Title == "Book" && tk.DueOn != "2026-10-25" {
			t.Errorf("due date shifted to %q", tk.DueOn)
		}
		if tk.Title == "No date" && tk.DueOn != "" {
			t.Errorf("due date appeared: %q", tk.DueOn)
		}
	}
}

func TestTentativeApplyDetailsForward(t *testing.T) {
	st := openTest(t)
	_, sid, _ := seriesFixture(t, st)
	occ := mustOcc(t, st, sid)
	yes := true
	n, err := st.ApplyDetailsForward(occ[4].ID, DetailsChange{DateTentative: &yes}, "x")
	if err != nil || n == 0 {
		t.Fatalf("applied %d %v", n, err)
	}
	for i, o := range occ {
		e, _ := st.GetEvent(o.ID)
		if want := i >= 4; e.DateTentative != want {
			t.Errorf("occurrence %d tentative = %v, want %v", i, e.DateTentative, want)
		}
	}
	no := false
	if _, err := st.ApplyDetailsForward(occ[6].ID, DetailsChange{DateTentative: &no}, "x"); err != nil {
		t.Fatal(err)
	}
	if e, _ := st.GetEvent(occ[5].ID); !e.DateTentative {
		t.Error("occurrence 5 should stay tentative")
	}
	if e, _ := st.GetEvent(occ[6].ID); e.DateTentative {
		t.Error("occurrence 6 should be confirmed")
	}
}

func TestUnscheduledCalendarBackfill(t *testing.T) {
	st := openTest(t)
	a, _ := st.CreateEvent(&Event{Title: "A", StartsOn: "2026-01-01"})
	st.CreateEvent(&Event{Title: "Undated"})
	st.CreateEvent(&Event{Title: "Undated cancelled", Status: "cancelled"})
	clearOutbox(t, st)

	if n, _ := st.CountCalendarBackfill("2025-10-12"); n != 1 {
		t.Fatalf("count = %d", n)
	}
	if n, err := st.QueueCalendarBackfill("2025-10-12"); err != nil || n != 1 {
		t.Fatalf("queued %d %v", n, err)
	}
	if got := outbox(t, st); len(got) != 1 || got[a] != "put" {
		t.Fatalf("outbox %v", got)
	}
}
