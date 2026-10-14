package store

import (
	"reflect"
	"testing"
	"time"
)

func newMeeting(t *testing.T, st *Store, title, kind, date string) int64 {
	t.Helper()
	id, err := st.CreateEvent(&Event{Title: title, Kind: kind, Type: "meeting", StartsOn: date})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func agendaTitles(t *testing.T, st *Store, id int64) []string {
	t.Helper()
	items, err := st.AgendaItems(id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, a := range items {
		out = append(out, a.Title)
	}
	return out
}

func linkedIDs(t *testing.T, st *Store, meeting int64) []int64 {
	t.Helper()
	list, err := st.MeetingEvents(meeting, &User{Role: RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	var out []int64
	for _, m := range list {
		out = append(out, m.EventID)
	}
	return out
}

func TestEventTypeRoundTrip(t *testing.T) {
	st := openTest(t)
	m := newMeeting(t, st, "Elders", "Elders meeting", "2026-11-03")
	ev := newEvent(t, st, "Dinner")
	if e, _ := st.GetEvent(m); !e.IsMeeting() || e.Type != "meeting" {
		t.Fatalf("meeting %+v", e)
	}
	if e, _ := st.GetEvent(ev); e.IsMeeting() || e.Type != "event" {
		t.Fatalf("event %+v", e)
	}
	e, _ := st.GetEvent(ev)
	e.Type = "bogus"
	st.UpdateEvent(e)
	if e, _ = st.GetEvent(ev); e.Type != "event" {
		t.Fatalf("bogus type kept: %q", e.Type)
	}
	e.Type = "meeting"
	st.UpdateEvent(e)
	if e, _ = st.GetEvent(ev); !e.IsMeeting() {
		t.Fatal("type not updated")
	}
	// Editing the event leaves the minutes alone.
	st.SetMinutes(m, "  We met.  ")
	e, _ = st.GetEvent(m)
	e.Notes = "x"
	st.UpdateEvent(e)
	if e, _ = st.GetEvent(m); e.Minutes != "We met." {
		t.Fatalf("minutes %q", e.Minutes)
	}
	if err := st.SetMinutes(9999, "x"); err != ErrNotFound {
		t.Fatalf("missing event: %v", err)
	}
}

func TestListEventsTypeFilter(t *testing.T) {
	st := openTest(t)
	newMeeting(t, st, "Elders", "", "2026-11-03")
	newEvent(t, st, "Dinner")
	admin := &User{Role: RoleAdmin}
	count := func(typ string) int {
		l, err := st.ListEventsOfType(admin, false, "2026-10-01", typ)
		if err != nil {
			t.Fatal(err)
		}
		return len(l)
	}
	if count("") != 2 || count("event") != 1 || count("meeting") != 1 {
		t.Fatalf("filter: all=%d event=%d meeting=%d", count(""), count("event"), count("meeting"))
	}
	if l, _ := st.ListEvents(admin, true, "2026-10-01"); len(l) != 2 {
		t.Fatalf("ListEvents %d", len(l))
	}
}

func TestAgendaCRUDAndMove(t *testing.T) {
	st := openTest(t)
	m := newMeeting(t, st, "Elders", "", "2026-11-03")
	if _, err := st.AddAgendaItem(m, "  ", "", false); err == nil {
		t.Fatal("blank title accepted")
	}
	a, _ := st.AddAgendaItem(m, "Opening", "Prayer", false)
	b, _ := st.AddAgendaItem(m, "Budget", "", true)
	c, _ := st.AddAgendaItem(m, "Closing", "", false)
	if got := agendaTitles(t, st, m); !reflect.DeepEqual(got, []string{"Opening", "Budget", "Closing"}) {
		t.Fatalf("order %v", got)
	}
	if err := st.Move("agenda_items", c, true); err != nil {
		t.Fatal(err)
	}
	if got := agendaTitles(t, st, m); !reflect.DeepEqual(got, []string{"Opening", "Closing", "Budget"}) {
		t.Fatalf("after move %v", got)
	}
	if ev, err := st.EventOf("agenda_items", b); err != nil || ev != m {
		t.Fatalf("EventOf %d %v", ev, err)
	}
	if err := st.UpdateAgendaItem(a, "Welcome", "Hymn", true); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateAgendaItem(a, "", "", false); err == nil {
		t.Fatal("blank title accepted on update")
	}
	got, _ := st.GetAgendaItem(a)
	if got.Title != "Welcome" || got.Details != "Hymn" || !got.CarryOver {
		t.Fatalf("updated %+v", got)
	}
	if err := st.DeleteAgendaItem(b); err != nil {
		t.Fatal(err)
	}
	if got := agendaTitles(t, st, m); !reflect.DeepEqual(got, []string{"Welcome", "Closing"}) {
		t.Fatalf("after delete %v", got)
	}
}

func TestLinkUnlinkAndMove(t *testing.T) {
	st := openTest(t)
	m := newMeeting(t, st, "Elders", "", "2026-11-03")
	other := newMeeting(t, st, "Deacons", "", "2026-11-04")
	e1, e2, e3 := newEvent(t, st, "One"), newEvent(t, st, "Two"), newEvent(t, st, "Three")
	if err := st.LinkEvent(m, m); err == nil {
		t.Fatal("self-link allowed")
	}
	if err := st.LinkEvent(m, other); err == nil {
		t.Fatal("linking a meeting allowed")
	}
	if err := st.LinkEvent(e1, e2); err == nil {
		t.Fatal("linking from a non-meeting allowed")
	}
	for _, id := range []int64{e1, e2, e3, e2} { // e2 twice: no duplicate
		if err := st.LinkEvent(m, id); err != nil {
			t.Fatal(err)
		}
	}
	if got := linkedIDs(t, st, m); !reflect.DeepEqual(got, []int64{e1, e2, e3}) {
		t.Fatalf("linked %v", got)
	}
	if err := st.MoveMeetingEvent(m, e3, true); err != nil {
		t.Fatal(err)
	}
	st.MoveMeetingEvent(m, e1, true) // already first: no-op
	if got := linkedIDs(t, st, m); !reflect.DeepEqual(got, []int64{e1, e3, e2}) {
		t.Fatalf("after move %v", got)
	}
	st.MoveMeetingEvent(m, e1, false)
	if got := linkedIDs(t, st, m); !reflect.DeepEqual(got, []int64{e3, e1, e2}) {
		t.Fatalf("after move down %v", got)
	}
	if err := st.SetDiscussionNote(m, e1, " Agreed "); err != nil {
		t.Fatal(err)
	}
	if err := st.SetDiscussionNote(m, other, "x"); err != ErrNotFound {
		t.Fatalf("note on unlinked: %v", err)
	}
	list, _ := st.MeetingEvents(m, &User{Role: RoleAdmin})
	if list[1].Note != "Agreed" || list[1].Title != "One" || !list[1].Visible {
		t.Fatalf("row %+v", list[1])
	}
	if err := st.UnlinkEvent(m, e1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetEvent(e1); err != nil {
		t.Fatal("unlink deleted the event")
	}
	if got := linkedIDs(t, st, m); !reflect.DeepEqual(got, []int64{e3, e2}) {
		t.Fatalf("after unlink %v", got)
	}
}

func TestMeetingEventsRowDetailsAndLocked(t *testing.T) {
	st := openTest(t)
	ann, _ := st.CreateUser("ann", "Ann", "a long password", RoleHelper)
	bob, _ := st.CreateUser("bob", "Bob", "a long password", RoleHelper)
	m := newMeeting(t, st, "Elders", "", "2026-11-03")
	open, _ := st.CreateEvent(&Event{Title: "Open", Coordinator: "Pat", DateTentative: true, StartsOn: "2026-12-01"})
	secret, _ := st.CreateEvent(&Event{Title: "Secret", Locked: true})
	st.SetMembers(secret, []int64{ann})
	st.CreateTask(&Task{EventID: open, Title: "a", Status: "todo"})
	st.CreateTask(&Task{EventID: open, Title: "b", Status: "done"})
	st.LinkEvent(m, open)
	st.LinkEvent(m, secret)

	list, _ := st.MeetingEvents(m, &User{ID: ann, Role: RoleHelper})
	if len(list) != 2 || list[0].OpenTasks != 1 || list[0].Coordinator != "Pat" || !list[0].DateTentative || !list[1].Visible || list[1].Title != "Secret" {
		t.Fatalf("ann %+v", list)
	}
	list, _ = st.MeetingEvents(m, &User{ID: bob, Role: RoleHelper})
	if list[1].Visible || list[1].Title != "" || list[1].EventID != 0 || list[1].Position == 0 {
		t.Fatalf("bob sees %+v", list[1])
	}
}

func TestPickEvents(t *testing.T) {
	st := openTest(t)
	admin := &User{Role: RoleAdmin}
	m := newMeeting(t, st, "Elders", "", "2026-11-03")
	newMeeting(t, st, "Other meeting", "", "2026-11-04")
	linked := newEvent(t, st, "Linked dinner")
	st.LinkEvent(m, linked)
	old, _ := st.CreateEvent(&Event{Title: "Old ready", Status: "ready", StartsOn: "2026-06-01"})
	newer, _ := st.CreateEvent(&Event{Title: "Newer ready", Status: "ready", StartsOn: "2026-09-01"})
	planning, _ := st.CreateEvent(&Event{Title: "Planning", StartsOn: "2026-08-01"})
	idea, _ := st.CreateEvent(&Event{Title: "Idea_100%"})
	got, err := st.PickEvents(m, admin, "")
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	if want := []int64{idea, planning, newer, old}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("pick order %v want %v", ids, want)
	}
	got, _ = st.PickEvents(m, admin, "ready")
	if len(got) != 2 {
		t.Fatalf("search ready: %d", len(got))
	}
	if got, _ = st.PickEvents(m, admin, "_100%"); len(got) != 1 || got[0].ID != idea {
		t.Fatalf("wildcards not escaped: %+v", got)
	}
}

func TestNewIdea(t *testing.T) {
	st := openTest(t)
	ann, _ := st.CreateUser("ann", "Ann", "a long password", RoleHelper)
	m, _ := st.CreateEvent(&Event{Title: "Committee", Type: "meeting", StartsOn: "2026-11-03", Locked: true, Visibility: "public"})
	st.SetMembers(m, []int64{ann})
	if _, err := st.NewIdea(m, " "); err == nil {
		t.Fatal("blank idea accepted")
	}
	id, err := st.NewIdea(m, " Hymn sing ")
	if err != nil {
		t.Fatal(err)
	}
	e, _ := st.GetEvent(id)
	if e.Title != "Hymn sing" || e.Scheduled() || e.Status != "planning" || e.IsMeeting() || !e.Locked || e.Visibility != "public" {
		t.Fatalf("idea %+v", e)
	}
	if mem, _ := st.Members(id); !reflect.DeepEqual(mem, []int64{ann}) {
		t.Fatalf("members %v", mem)
	}
	if got := linkedIDs(t, st, m); !reflect.DeepEqual(got, []int64{id}) {
		t.Fatalf("linked %v", got)
	}
	if _, err := st.NewIdea(id, "x"); err == nil {
		t.Fatal("idea on a non-meeting allowed")
	}
}

func TestDiscussedAtBackLinks(t *testing.T) {
	st := openTest(t)
	ann, _ := st.CreateUser("ann", "Ann", "a long password", RoleHelper)
	ev := newEvent(t, st, "Dinner")
	m1 := newMeeting(t, st, "March", "", "2026-03-01")
	m2 := newMeeting(t, st, "May", "", "2026-05-01")
	locked, _ := st.CreateEvent(&Event{Title: "Private mtg", Type: "meeting", StartsOn: "2026-04-01", Locked: true})
	for _, m := range []int64{m1, m2, locked} {
		st.LinkEvent(m, ev)
	}
	st.SetDiscussionNote(m1, ev, "Maybe")
	got, err := st.DiscussedAtList(ev, &User{ID: ann, Role: RoleHelper})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].MeetingID != m2 || got[1].MeetingID != m1 || got[1].Note != "Maybe" {
		t.Fatalf("helper sees %+v", got)
	}
	if got, _ = st.DiscussedAtList(ev, &User{Role: RoleAdmin}); len(got) != 3 {
		t.Fatalf("admin sees %d", len(got))
	}
}

var adm = &User{ID: 1, Role: RoleAdmin}

func TestPreviousMeeting(t *testing.T) {
	st := openTest(t)
	other := newMeeting(t, st, "Other kind", "Deacons", "2026-10-20")
	byKind := newMeeting(t, st, "Elders Oct", "Elders", "2026-10-06")
	_ = other
	m := newMeeting(t, st, "Elders Nov", "Elders", "2026-11-03")
	prev, err := st.PreviousMeeting(m, adm)
	if err != nil || prev == nil || prev.ID != byKind {
		t.Fatalf("by kind: %+v %v", prev, err)
	}
	// A series wins over a later same-kind meeting outside it.
	sid, _ := st.MakeSeries(m, weekly(4), "2026-11-01")
	occ := mustOcc(t, st, sid)
	if len(occ) < 2 {
		t.Fatalf("occurrences %d", len(occ))
	}
	if prev, _ = st.PreviousMeeting(occ[1].ID, adm); prev == nil || prev.ID != occ[0].ID {
		t.Fatalf("series previous %+v want %d", prev, occ[0].ID)
	}
	// The first of the series falls back to the kind.
	if prev, _ = st.PreviousMeeting(m, adm); prev == nil || prev.ID != byKind {
		t.Fatalf("first in series %+v", prev)
	}
	undated := newMeeting(t, st, "Someday", "Elders", "")
	if prev, _ = st.PreviousMeeting(undated, adm); prev != nil {
		t.Fatalf("undated got %+v", prev)
	}
	noKind := newMeeting(t, st, "No kind", "", "2026-12-24")
	if prev, _ = st.PreviousMeeting(noKind, adm); prev != nil {
		t.Fatalf("no kind got %+v", prev)
	}
	plain := newEvent(t, st, "Dinner")
	if prev, _ = st.PreviousMeeting(plain, adm); prev != nil {
		t.Fatalf("event got %+v", prev)
	}
}

func TestBringOver(t *testing.T) {
	st := openTest(t)
	last := newMeeting(t, st, "Elders Oct", "Elders", "2026-10-06")
	this := newMeeting(t, st, "Elders Nov", "Elders", "2026-11-03")
	open1 := newEvent(t, st, "Open one")
	open2 := newEvent(t, st, "Open two")
	done, _ := st.CreateEvent(&Event{Title: "Done", Status: "done", StartsOn: "2026-09-01"})
	cancelled, _ := st.CreateEvent(&Event{Title: "Cancelled", Status: "cancelled", StartsOn: "2026-09-02"})
	already := newEvent(t, st, "Already")
	for _, id := range []int64{open1, open2, done, cancelled, already} {
		st.LinkEvent(last, id)
	}
	st.SetDiscussionNote(last, open1, "Need a budget")
	st.SetDiscussionNote(last, already, "old")
	st.LinkEvent(this, already)
	st.SetDiscussionNote(this, already, "new")
	st.AddAgendaItem(last, "Standing", "d", true)
	st.AddAgendaItem(last, "One-off", "", false)
	st.AddAgendaItem(last, "Standing 2", "", true)
	st.AddAgendaItem(this, "Opening", "", false)

	res, err := st.BringOver(this, adm)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Found || res.Events != 2 || res.Agenda != 2 {
		t.Fatalf("result %+v", res)
	}
	if got := linkedIDs(t, st, this); !reflect.DeepEqual(got, []int64{already, open1, open2}) {
		t.Fatalf("linked %v", got)
	}
	list, _ := st.MeetingEvents(this, &User{Role: RoleAdmin})
	if list[0].Note != "new" || list[1].Note != "Last time: Need a budget" || list[2].Note != "" {
		t.Fatalf("notes %q %q %q", list[0].Note, list[1].Note, list[2].Note)
	}
	items, _ := st.AgendaItems(this)
	if len(items) != 3 || items[0].Title != "Opening" || items[1].Title != "Standing" || items[2].Title != "Standing 2" || !items[1].CarryOver || items[1].Details != "d" {
		t.Fatalf("agenda %+v", items)
	}
	// Doing it twice adds nothing.
	if res, _ = st.BringOver(this, adm); res.Events != 0 || res.Agenda != 0 || !res.Found {
		t.Fatalf("second run %+v", res)
	}
	// An earlier note that already says "Last time:" isn't stacked.
	next := newMeeting(t, st, "Elders Dec", "Elders", "2026-12-01")
	st.BringOver(next, adm)
	list, _ = st.MeetingEvents(next, &User{Role: RoleAdmin})
	for _, m := range list {
		if m.EventID == open1 && m.Note != "Last time: Need a budget" {
			t.Fatalf("stacked note %q", m.Note)
		}
	}
	// No previous meeting.
	if res, _ = st.BringOver(last, adm); res.Found {
		t.Fatalf("first meeting found one: %+v", res)
	}
}

func TestCopyKeepsTypeAndAgendaDropsMinutes(t *testing.T) {
	st := openTest(t)
	m := newMeeting(t, st, "Elders", "Elders", "2026-11-03")
	st.AddAgendaItem(m, "Opening", "Prayer", false)
	st.AddAgendaItem(m, "Budget", "", true)
	st.SetMinutes(m, "We met")
	ev := newEvent(t, st, "Dinner")
	st.LinkEvent(m, ev)
	cp, err := st.CopyEvent(m, CopyOptions{StartsOn: "2026-12-01"})
	if err != nil {
		t.Fatal(err)
	}
	e, _ := st.GetEvent(cp)
	if !e.IsMeeting() || e.Minutes != "" {
		t.Fatalf("copy %+v", e)
	}
	items, _ := st.AgendaItems(cp)
	if len(items) != 2 || items[0].Title != "Opening" || items[0].Details != "Prayer" || !items[1].CarryOver {
		t.Fatalf("agenda %+v", items)
	}
	if got := linkedIDs(t, st, cp); len(got) != 0 {
		t.Fatalf("copied links %v", got)
	}
	if got := linkedIDs(t, st, m); len(got) != 1 {
		t.Fatalf("source links %v", got)
	}
	// The source's agenda is untouched.
	if got := agendaTitles(t, st, m); len(got) != 2 {
		t.Fatalf("source agenda %v", got)
	}
}

func TestSeriesOccurrenceGetsStandingAgenda(t *testing.T) {
	st := openTest(t)
	m := newMeeting(t, st, "Weekly", "Staff", "2026-11-01")
	st.AddAgendaItem(m, "Prayer", "", false)
	st.AddAgendaItem(m, "Updates", "", true)
	st.SetMinutes(m, "template minutes")
	sid, err := st.MakeSeries(m, weekly(1), testToday)
	if err != nil {
		t.Fatal(err)
	}
	occ := mustOcc(t, st, sid)
	if len(occ) < 2 {
		t.Fatalf("occurrences %d", len(occ))
	}
	for _, o := range occ {
		if o.ID == m {
			continue
		}
		if !o.IsMeeting() || o.Minutes != "" {
			t.Fatalf("occurrence %+v", o)
		}
		if got := agendaTitles(t, st, o.ID); !reflect.DeepEqual(got, []string{"Prayer", "Updates"}) {
			t.Fatalf("occurrence %s agenda %v", o.OccurrenceDate, got)
		}
	}
}

func TestNeedsClosingOut(t *testing.T) {
	st := openTest(t)
	mk := func(e Event) int64 {
		id, err := st.CreateEvent(&e)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	timed := mk(Event{Title: "Timed", StartsOn: "2026-10-10", StartTime: "18:00", EndTime: "20:00"})
	allDay := mk(Event{Title: "All day", StartsOn: "2026-10-10"})
	multi := mk(Event{Title: "Multi", StartsOn: "2026-10-08", EndsOn: "2026-10-10", EndTime: "12:00"})
	mk(Event{Title: "Done", StartsOn: "2026-10-01", Status: "done"})
	mk(Event{Title: "Cancelled", StartsOn: "2026-10-01", Status: "cancelled"})
	mk(Event{Title: "Unscheduled"})
	ready := mk(Event{Title: "Ready", StartsOn: "2026-10-02", Status: "ready"})
	meeting := mk(Event{Title: "Mtg", Type: "meeting", StartsOn: "2026-10-09", StartTime: "19:00"})
	locked := mk(Event{Title: "Locked", StartsOn: "2026-10-01", Locked: true})

	at := func(s string) time.Time {
		tm, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	ids := func(now time.Time) []int64 {
		list, err := st.NeedsClosingOut(now)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, e := range list {
			out = append(out, e.ID)
		}
		return out
	}
	// Before the timed event ends, one minute short.
	if got := ids(at("2026-10-10 19:59")); !reflect.DeepEqual(got, []int64{locked, ready, multi, meeting}) {
		t.Fatalf("19:59 %v", got)
	}
	// At the end time it counts; the all-day one waits for midnight.
	if got := ids(at("2026-10-10 20:00")); !reflect.DeepEqual(got, []int64{locked, ready, multi, meeting, timed}) {
		t.Fatalf("20:00 %v", got)
	}
	if got := ids(at("2026-10-10 23:59")); len(got) != 5 {
		t.Fatalf("23:59 %v", got)
	}
	if got := ids(at("2026-10-11 00:00")); !reflect.DeepEqual(got, []int64{locked, ready, multi, meeting, timed, allDay}) {
		t.Fatalf("midnight %v", got)
	}
	// The multi-day one ended on its last day at 12:00.
	if got := ids(at("2026-10-10 11:59")); len(got) == 0 || contains(got, multi) {
		t.Fatalf("11:59 %v", got)
	}
	e, _ := st.GetEvent(timed)
	if e.NeedsClosingOut(at("2026-10-10 19:00")) || !e.NeedsClosingOut(at("2026-10-10 20:00")) {
		t.Fatal("Event.NeedsClosingOut boundary")
	}
	// Visibility: a helper outside the locked event doesn't get it.
	helper := &User{ID: 99, Role: RoleHelper}
	list, _ := st.NeedsClosingOutFor(helper, at("2026-10-12 09:00"))
	for _, e := range list {
		if e.ID == locked {
			t.Fatal("locked event shown to a non-member")
		}
	}
	if len(list) != 5 {
		t.Fatalf("helper sees %d", len(list))
	}
}

func contains(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func TestBringOverSkipsHiddenMeetings(t *testing.T) {
	st := openTest(t)
	ann, _ := st.CreateUser("ann", "Ann", "a long password", RolePlanner)
	annU, _ := st.GetUser(ann)
	old := newMeeting(t, st, "Elders Sep", "Elders", "2026-09-01")
	hidden, _ := st.CreateEvent(&Event{Title: "Elders Oct", Kind: "Elders", Type: "meeting", StartsOn: "2026-10-06", Locked: true})
	this := newMeeting(t, st, "Elders Nov", "Elders", "2026-11-03")
	visible := newEvent(t, st, "Visible")
	secret, _ := st.CreateEvent(&Event{Title: "Secret", Locked: true})
	st.LinkEvent(old, visible)
	st.LinkEvent(old, secret)
	st.LinkEvent(hidden, visible)
	st.AddAgendaItem(hidden, "Hidden item", "", true)
	st.AddAgendaItem(old, "Old item", "", true)

	prev, err := st.PreviousMeeting(this, annU)
	if err != nil || prev == nil || prev.ID != old {
		t.Fatalf("want older visible meeting, got %+v %v", prev, err)
	}
	res, err := st.BringOver(this, annU)
	if err != nil || !res.Found || res.Events != 1 || res.Agenda != 1 {
		t.Fatalf("result %+v %v", res, err)
	}
	if got := linkedIDs(t, st, this); !reflect.DeepEqual(got, []int64{visible}) {
		t.Fatalf("linked %v", got)
	}
	if got := agendaTitles(t, st, this); !reflect.DeepEqual(got, []string{"Old item"}) {
		t.Fatalf("agenda %v", got)
	}
	// With only the hidden one before it, nothing is found.
	st.DeleteEvent(old)
	if prev, _ = st.PreviousMeeting(this, annU); prev != nil {
		t.Fatalf("hidden meeting leaked: %+v", prev)
	}
	if res, _ = st.BringOver(this, annU); res.Found {
		t.Fatalf("found hidden: %+v", res)
	}
	// A member sees it.
	st.SetMembers(hidden, []int64{ann})
	if prev, _ = st.PreviousMeeting(this, annU); prev == nil || prev.ID != hidden {
		t.Fatalf("member should see it: %+v", prev)
	}
}
