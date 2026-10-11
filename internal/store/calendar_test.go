package store

import (
	"errors"
	"reflect"
	"testing"
)

// calEvent inserts an event with plain SQL, so these tests don't depend on
// how the rest of the store creates events.
func calEvent(t *testing.T, st *Store, title, visibility string, locked bool, status, startsOn, startTime, endsOn string, rooms ...string) int64 {
	t.Helper()
	res, err := st.DB.Exec(`INSERT INTO events (title, kind, status, starts_on, start_time, ends_on, end_time, visibility, public_description, locked, notes, coordinator)
		VALUES (?, 'Dinner', ?, ?, ?, ?, '', ?, 'Come hungry', ?, 'SECRET NOTE', 'SECRET COORD')`,
		title, status, startsOn, startTime, endsOn, visibility, locked)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	for i, r := range rooms {
		if _, err := st.DB.Exec(`INSERT INTO event_rooms (event_id, position, place_name) VALUES (?, ?, ?)`, id, i+1, r); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func calUser(t *testing.T, st *Store, name, role string) *User {
	t.Helper()
	res, err := st.DB.Exec(`INSERT INTO users (username, display_name, role) VALUES (?, ?, ?)`, name, name, role)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return &User{ID: id, Username: name, Role: role}
}

func titles(list []CalendarEntry) []string {
	out := []string{}
	for _, c := range list {
		if c.Busy {
			out = append(out, "(busy)")
		} else {
			out = append(out, c.Title)
		}
	}
	return out
}

func TestCalendarAccessLevels(t *testing.T) {
	st := openTest(t)
	calEvent(t, st, "Pub", "public", false, "planning", "2026-11-01", "18:00", "", "Lifecenter", "Kitchen")
	calEvent(t, st, "Priv", "private", false, "planning", "2026-11-02", "09:00", "", "Library")
	calEvent(t, st, "PubLocked", "public", true, "planning", "2026-11-03", "09:00", "", "Office")
	calEvent(t, st, "PrivLocked", "private", true, "planning", "2026-11-04", "", "")
	calEvent(t, st, "Cancelled", "public", false, "cancelled", "2026-11-05", "", "")

	all := func(a CalendarAccess) []CalendarEntry {
		t.Helper()
		l, err := st.Calendar("2026-10-01", "2026-12-31", a)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}

	pub := all(CalendarPublic)
	if got, want := titles(pub), []string{"Pub", "(busy)", "(busy)", "(busy)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("public: %v, want %v", got, want)
	}
	if pub[0].EventID == 0 || !pub[0].Public || pub[0].PublicDescription != "Come hungry" || !reflect.DeepEqual(pub[0].Rooms, []string{"Lifecenter", "Kitchen"}) {
		t.Errorf("public full entry: %+v", pub[0])
	}

	priv := all(CalendarPrivate)
	if got, want := titles(priv), []string{"Pub", "Priv", "(busy)", "(busy)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("private: %v, want %v", got, want)
	}
	if priv[1].Public {
		t.Error("a private event is not Public")
	}

	// Signed-in: a helper not on the locked events sees them as busy; an
	// admin sees everything but cancelled events.
	helper := calUser(t, st, "helper", "helper")
	if got, want := titles(all(CalendarSignedIn(helper))), []string{"Pub", "Priv", "(busy)", "(busy)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("helper: %v, want %v", got, want)
	}
	admin := calUser(t, st, "boss", "admin")
	if got, want := titles(all(CalendarSignedIn(admin))), []string{"Pub", "Priv", "PubLocked", "PrivLocked"}; !reflect.DeepEqual(got, want) {
		t.Errorf("admin: %v, want %v", got, want)
	}
	// A member of a locked event sees it.
	if _, err := st.DB.Exec(`INSERT INTO event_members (event_id, user_id) VALUES (4, ?)`, helper.ID); err != nil {
		t.Fatal(err)
	}
	if got, want := titles(all(CalendarSignedIn(helper))), []string{"Pub", "Priv", "(busy)", "PrivLocked"}; !reflect.DeepEqual(got, want) {
		t.Errorf("member: %v, want %v", got, want)
	}
}

func TestCalendarBusyEntriesHideDetails(t *testing.T) {
	st := openTest(t)
	calEvent(t, st, "Elders", "private", false, "ready", "2026-11-02", "14:00", "", "Lifecenter", "Library")
	calEvent(t, st, "Nowhere", "private", false, "ready", "2026-11-02", "", "")
	list, err := st.Calendar("2026-11-02", "2026-11-02", CalendarPublic)
	if err != nil || len(list) != 2 {
		t.Fatalf("%v %+v", err, list)
	}
	want := CalendarEntry{Busy: true, StartsOn: "2026-11-02", StartTime: "14:00", Rooms: []string{"Lifecenter", "Library"}}
	if !reflect.DeepEqual(list[1], want) {
		t.Errorf("busy entry: %+v", list)
	}
	for _, c := range list {
		if c.Title != "" || c.EventID != 0 || c.PublicDescription != "" || c.Kind != "" || c.Status != "" || c.Public {
			t.Errorf("busy entry leaks: %+v", c)
		}
	}
	if list[0].Rooms != nil {
		t.Errorf("an event with no rooms should have none: %+v", list[1])
	}
}

func TestCalendarRange(t *testing.T) {
	st := openTest(t)
	calEvent(t, st, "Retreat", "public", false, "planning", "2026-11-06", "", "2026-11-08")
	calEvent(t, st, "Before", "public", false, "planning", "2026-11-01", "", "")
	calEvent(t, st, "After", "public", false, "planning", "2026-11-20", "", "")
	calEvent(t, st, "Late", "public", false, "planning", "2026-11-07", "20:00", "")
	calEvent(t, st, "Early", "public", false, "planning", "2026-11-07", "08:00", "")

	for _, c := range []struct {
		from, to string
		want     []string
	}{
		{"2026-11-07", "2026-11-07", []string{"Retreat", "Early", "Late"}}, // starts earlier, overlaps
		{"2026-11-08", "2026-11-09", []string{"Retreat"}},                  // last day
		{"2026-11-09", "2026-11-19", []string{}},
		{"2026-11-01", "2026-11-06", []string{"Before", "Retreat"}}, // first day
		{"2026-11-05", "2026-11-05", []string{}},
	} {
		got, err := st.Calendar(c.from, c.to, CalendarPublic)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(titles(got), c.want) {
			t.Errorf("%s..%s: %v, want %v", c.from, c.to, titles(got), c.want)
		}
	}
}

func TestCalendarEvent(t *testing.T) {
	st := openTest(t)
	pub := calEvent(t, st, "Pub", "public", false, "planning", "2026-11-01", "", "")
	priv := calEvent(t, st, "Priv", "private", false, "planning", "2026-11-01", "", "")
	locked := calEvent(t, st, "Locked", "private", true, "planning", "2026-11-01", "", "")
	gone := calEvent(t, st, "Gone", "public", false, "cancelled", "2026-11-01", "", "")
	for _, c := range []struct {
		id     int64
		access CalendarAccess
		ok     bool
	}{
		{pub, CalendarPublic, true}, {priv, CalendarPublic, false},
		{priv, CalendarPrivate, true}, {locked, CalendarPrivate, false},
		{gone, CalendarPrivate, false}, {999, CalendarPrivate, false},
	} {
		_, err := st.CalendarEvent(c.id, c.access)
		if c.ok && err != nil || !c.ok && !errors.Is(err, ErrNotFound) {
			t.Errorf("event %d: err %v, want ok=%v", c.id, err, c.ok)
		}
	}
}
