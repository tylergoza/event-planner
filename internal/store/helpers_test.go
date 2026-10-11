package store

import (
	"errors"
	"reflect"
	"testing"
)

func helperRoleNames(roles []HelperRole) []string {
	var out []string
	for _, r := range roles {
		out = append(out, r.Name)
	}
	return out
}

func TestHelperRoleOrderAndMath(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Dinner")
	a, _ := st.CreateHelperRole(&HelperRole{EventID: ev, Name: "Greeters", Slots: 2})
	st.CreateHelperRole(&HelperRole{EventID: ev, Name: "Dishes", Slots: 0}) // clamps to 1
	c, _ := st.CreateHelperRole(&HelperRole{EventID: ev, Name: "Parking", Slots: 1})

	if err := st.Move("helper_roles", c, true); err != nil {
		t.Fatal(err)
	}
	roles, _ := st.ListHelperRoles(ev)
	if got, want := helperRoleNames(roles), []string{"Greeters", "Parking", "Dishes"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	if roles[2].Slots != 1 {
		t.Errorf("slots %d, want 1", roles[2].Slots)
	}

	st.SignUpName(a, "Walk-in", "555-1234")
	roles, _ = st.ListHelperRoles(ev)
	if r := roles[0]; r.Filled() != 1 || r.Short() != 1 || r.Over() != 0 || len(r.Helpers) != 1 {
		t.Errorf("one of two: filled %d short %d over %d", r.Filled(), r.Short(), r.Over())
	}
	st.SignUpName(a, "Second", "")
	st.SignUpName(a, "Third", "")
	roles, _ = st.ListHelperRoles(ev)
	if r := roles[0]; r.Filled() != 2 || r.Short() != 0 || r.Over() != 1 {
		t.Errorf("three of two: filled %d short %d over %d", r.Filled(), r.Short(), r.Over())
	}
	if tl := TallyHelperRoles(roles); tl != (HelperTally{Slots: 4, Filled: 2, ShortJobs: 2}) {
		t.Errorf("tally %+v", tl)
	}
	if _, err := st.SignUpName(a, "  ", ""); err == nil {
		t.Error("blank name should be rejected")
	}
}

func TestHelperUserSignUp(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Dinner")
	role, _ := st.CreateHelperRole(&HelperRole{EventID: ev, Name: "Greeters", Slots: 2})
	ann, _ := st.CreateUser("ann", "Ann Lee", "a long password", RoleHelper)
	bob, _ := st.CreateUser("bob", "", "a long password", RoleHelper)

	hid, err := st.SignUpUser(role, ann)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := st.GetHelper(hid); h.Name != "Ann Lee" || h.UserID != ann {
		t.Errorf("helper %+v, want Ann Lee linked", h)
	}
	if _, err := st.SignUpUser(role, ann); !errors.Is(err, ErrAlreadySignedUp) {
		t.Errorf("duplicate: %v, want ErrAlreadySignedUp", err)
	}
	bid, _ := st.SignUpUser(role, bob)
	if h, _ := st.GetHelper(bid); h.Name != "bob" {
		t.Errorf("name %q, want username fallback", h.Name)
	}

	// Name is read-only for a user row; phone and notes save.
	st.UpdateHelper(&Helper{ID: hid, UserID: ann, Name: "Hacked", Phone: "555", Notes: "early"})
	if h, _ := st.GetHelper(hid); h.Name != "Ann Lee" || h.Phone != "555" || h.Notes != "early" {
		t.Errorf("after update %+v", h)
	}

	// Deleting the user keeps the name.
	if err := st.DeleteUser(ann); err != nil {
		t.Fatal(err)
	}
	if h, _ := st.GetHelper(hid); h.UserID != 0 || h.Name != "Ann Lee" {
		t.Errorf("after user delete %+v, want name kept, no user", h)
	}

	if err := st.RemoveUserFromRole(role, bob); err != nil {
		t.Fatal(err)
	}
	if r, _ := st.GetHelperRole(role); len(r.Helpers) != 1 {
		t.Errorf("%d helpers, want 1", len(r.Helpers))
	}
}

func TestHelperRoleRoomDelete(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Dinner")
	res, err := st.DB.Exec(`INSERT INTO event_rooms (event_id, position, place_name) VALUES (?, 1, 'Fellowship Hall')`, ev)
	if err != nil {
		t.Fatal(err)
	}
	room, _ := res.LastInsertId()
	id, _ := st.CreateHelperRole(&HelperRole{EventID: ev, Name: "Tables", Slots: 1, RoomID: room})
	if r, _ := st.GetHelperRole(id); r.RoomID != room || r.RoomName != "Fellowship Hall" {
		t.Fatalf("role %+v", r)
	}
	if _, err := st.DB.Exec(`DELETE FROM event_rooms WHERE id = ?`, room); err != nil {
		t.Fatal(err)
	}
	if r, _ := st.GetHelperRole(id); r.RoomID != 0 || r.RoomName != "" {
		t.Errorf("room %d %q, want none", r.RoomID, r.RoomName)
	}
}

func TestHelperAssignmentsForUser(t *testing.T) {
	st := openTest(t)
	u, _ := st.CreateUser("ann", "Ann", "a long password", RoleHelper)
	mk := func(title, starts, status string) int64 {
		id, err := st.CreateEvent(&Event{Title: title, StartsOn: starts, Status: status})
		if err != nil {
			t.Fatal(err)
		}
		r, _ := st.CreateHelperRole(&HelperRole{EventID: id, Name: title + " job", WhenText: "5pm"})
		if _, err := st.SignUpUser(r, u); err != nil {
			t.Fatal(err)
		}
		return id
	}
	mk("Later", "2026-12-01", "planning")
	mk("Sooner", "2026-11-01", "ready")
	mk("Over", "2026-10-01", "done")
	mk("Off", "2026-10-02", "cancelled")

	got, err := st.HelperAssignmentsForUser(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].EventTitle != "Sooner" || got[1].EventTitle != "Later" {
		t.Fatalf("assignments %+v", got)
	}
	if got[0].RoleName != "Sooner job" || got[0].WhenText != "5pm" || got[0].StartsOn != "2026-11-01" {
		t.Errorf("first %+v", got[0])
	}
}

func TestParseHelperRoles(t *testing.T) {
	got := ParseHelperRoles("Greeters x3\n\n  Dishes - 2 \nParking\nFloor X 4\nCall 911 - 0\nBox x3 extra\nRoom 5-3")
	want := []HelperRole{
		{Name: "Greeters", Slots: 3}, {Name: "Dishes", Slots: 2}, {Name: "Parking", Slots: 1},
		{Name: "Floor", Slots: 4}, {Name: "Call 911 - 0", Slots: 1}, {Name: "Box x3 extra", Slots: 1}, {Name: "Room 5-3", Slots: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	st := openTest(t)
	ev := newEvent(t, st, "Dinner")
	if n, err := st.AddHelperRoles(ev, "A x2\nB"); err != nil || n != 2 {
		t.Errorf("added %d, %v", n, err)
	}
}
