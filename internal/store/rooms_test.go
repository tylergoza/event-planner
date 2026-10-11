package store

import (
	"errors"
	"testing"
)

func mkEvent(t *testing.T, st *Store, title, start, end, status string, locked bool) int64 {
	t.Helper()
	id, err := st.CreateEvent(&Event{Title: title, StartsOn: start, EndsOn: end, Status: status, Locked: locked})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRoomsAddListOrder(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Dinner")
	a, _ := st.AddRoom(ev, 10, "Main › Hall")
	b, err := st.AddRoom(ev, 0, "Park shelter")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddRoom(ev, 10, "Main › Hall"); !errors.Is(err, ErrRoomExists) {
		t.Fatalf("duplicate place: %v", err)
	}
	if _, err := st.AddRoom(ev, 0, "Park shelter"); err != nil {
		t.Errorf("free-text duplicates are fine: %v", err)
	}
	rooms, _ := st.ListRooms(ev)
	if len(rooms) != 3 || rooms[0].ID != a || rooms[1].ID != b || rooms[0].PlaceID != 10 || rooms[1].PlaceID != 0 {
		t.Fatalf("rooms %+v", rooms)
	}
	if err := st.Move("event_rooms", b, true); err != nil {
		t.Fatal(err)
	}
	rooms, _ = st.ListRooms(ev)
	if rooms[0].ID != b || rooms[1].ID != a {
		t.Errorf("after move up: %+v", rooms)
	}
	// Tracker rooms keep their name; free-text ones can be renamed.
	st.UpdateRoom(&Room{ID: a, PlaceName: "X", Purpose: "Seating", Setup: "8 tables"})
	st.UpdateRoom(&Room{ID: b, PlaceName: "Pavilion"})
	ra, _ := st.GetRoom(a)
	rb, _ := st.GetRoom(b)
	if ra.PlaceName != "Main › Hall" || ra.Purpose != "Seating" || ra.Setup != "8 tables" || rb.PlaceName != "Pavilion" {
		t.Errorf("update: %+v %+v", ra, rb)
	}
	if _, err := st.GetRoom(9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing room: %v", err)
	}
}

func TestDeleteRoomClearsTask(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Dinner")
	room, _ := st.AddRoom(ev, 0, "Hall")
	task := &Task{EventID: ev, Title: "Sweep", Status: "todo", RoomID: room}
	id, err := st.CreateTask(task)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := st.RoomUsages(ev)
	if len(u[room].Tasks) != 1 {
		t.Fatalf("usage %+v", u[room])
	}
	if err := st.DeleteRoom(room); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetTask(id)
	if got.RoomID != 0 {
		t.Errorf("task room_id = %d, want cleared", got.RoomID)
	}
}

func TestRoomConflicts(t *testing.T) {
	st := openTest(t)
	boss := &User{ID: 1, Role: RoleAdmin}
	bobID, _ := st.CreateUser("bob", "", "a long password", RoleHelper)
	bob := &User{ID: bobID}

	mine := mkEvent(t, st, "Mine", "2026-11-10", "2026-11-12", "planning", false)
	overlap := mkEvent(t, st, "Overlap", "2026-11-12", "", "planning", false)
	before := mkEvent(t, st, "Before", "2026-11-01", "2026-11-09", "planning", false)
	cancelled := mkEvent(t, st, "Cancelled", "2026-11-11", "", "cancelled", false)
	locked := mkEvent(t, st, "Secret", "2026-11-11", "", "planning", true)
	free := mkEvent(t, st, "Free text", "2026-11-11", "", "planning", false)

	m1, _ := st.AddRoom(mine, 5, "Hall")
	m2, _ := st.AddRoom(mine, 0, "Shelter")
	for _, ev := range []int64{overlap, before, cancelled, locked} {
		st.AddRoom(ev, 5, "Hall")
	}
	st.AddRoom(free, 0, "Shelter")
	st.AddRoom(overlap, 6, "Other")

	got, err := st.RoomConflicts(mine, boss)
	if err != nil {
		t.Fatal(err)
	}
	if len(got[m1]) != 2 || got[m1][0].ID != locked || got[m1][1].ID != overlap {
		t.Errorf("admin conflicts: %+v", got[m1])
	}
	if len(got[m2]) != 0 {
		t.Errorf("free-text room conflicted: %+v", got[m2])
	}
	got, _ = st.RoomConflicts(mine, bob)
	if len(got[m1]) != 1 || got[m1][0].ID != overlap {
		t.Errorf("non-member should not see the locked event: %+v", got[m1])
	}
	st.SetMembers(locked, []int64{bobID})
	got, _ = st.RoomConflicts(mine, bob)
	if len(got[m1]) != 2 {
		t.Errorf("member should see it: %+v", got[m1])
	}
}
