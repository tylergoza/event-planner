package store

import "testing"

func TestCopyEvent(t *testing.T) {
	st := openTest(t)
	ann, _ := st.CreateUser("ann", "Ann", "a long password", RoleHelper)
	src, err := st.CreateEvent(&Event{Title: "Dinner", Kind: "Fellowship dinner", Status: "done", StartsOn: "2026-11-01", EndsOn: "2026-11-03",
		StartTime: "17:00", Coordinator: "Pat", ExpectedCount: 40, Description: "d", Notes: "n", Locked: true})
	if err != nil {
		t.Fatal(err)
	}
	st.SetMembers(src, []int64{ann})
	hall, _ := st.AddRoom(src, 7, "Main › Hall")
	yard, _ := st.AddRoom(src, 0, "Yard")
	st.UpdateRoom(&Room{ID: hall, Purpose: "Seating", Setup: "8 tables", Notes: "rn"})
	lst, _ := st.CreateTaskList(src, "Before")
	st.CreateTaskList(src, "After")

	t1, _ := st.CreateTask(&Task{EventID: src, ListID: lst, Title: "Book", Status: "done", AssigneeID: ann, DueOn: "2026-10-25", RoomID: hall, UpdatedBy: "x"})
	t2, _ := st.CreateTask(&Task{EventID: src, Title: "Sweep", Status: "blocked", AssigneeName: "Youth", RoomID: yard})
	st.CreateTask(&Task{EventID: src, Title: "No date", Status: "in_progress"})
	st.CreateScheduleItem(&ScheduleItem{EventID: src, Date: "2026-11-01", Time: "16:00", Label: "Set up", RoomID: hall})
	st.CreateScheduleItem(&ScheduleItem{EventID: src, Date: "2026-11-03", Label: "Clean", RoomID: yard})
	st.CreateNeed(&Need{EventID: src, Name: "Tables", Quantity: 4, Status: "gathered", RoomID: hall})
	st.CreateNeed(&Need{EventID: src, Name: "Chairs", Quantity: 9, TrackerKind: "product", TrackerID: 3, Status: "returned"})
	role, _ := st.CreateHelperRole(&HelperRole{EventID: src, Name: "Greeters", Slots: 2, RoomID: hall})
	st.SignUpUser(role, ann)
	st.SignUpName(role, "Vic", "555")

	// A week later (same day offset applies everywhere), keeping assignees only.
	id, err := st.CopyEvent(src, CopyOptions{Title: "Dinner 2", StartsOn: "2026-11-08", KeepAssignees: true, By: "Pat"})
	if err != nil {
		t.Fatal(err)
	}
	e, _ := st.GetEvent(id)
	if e.Title != "Dinner 2" || e.Status != "planning" || e.StartsOn != "2026-11-08" || e.EndsOn != "2026-11-10" || e.CopiedFrom != src ||
		!e.Locked || e.Kind != "Fellowship dinner" || e.StartTime != "17:00" || e.Coordinator != "Pat" || e.ExpectedCount != 40 || e.Description != "d" || e.Notes != "n" {
		t.Fatalf("event %+v", e)
	}
	if m, _ := st.Members(id); len(m) != 1 || m[0] != ann {
		t.Errorf("members %v", m)
	}

	rooms, _ := st.ListRooms(id)
	if len(rooms) != 2 || rooms[0].PlaceID != 7 || rooms[0].Setup != "8 tables" || rooms[0].Purpose != "Seating" || rooms[0].Notes != "rn" || rooms[1].PlaceName != "Yard" {
		t.Fatalf("rooms %+v", rooms)
	}
	newHall, newYard := rooms[0].ID, rooms[1].ID
	if newHall == hall || newYard == yard {
		t.Fatal("rooms should be new rows")
	}

	lists, _ := st.ListTaskLists(id)
	if len(lists) != 2 || lists[0].Name != "Before" || lists[0].ID == lst {
		t.Fatalf("lists %+v", lists)
	}
	tasks, _ := st.ListTasks(id)
	if len(tasks) != 3 {
		t.Fatalf("tasks %+v", tasks)
	}
	a, b, c := tasks[0], tasks[1], tasks[2]
	if a.Title != "Book" || a.Status != "todo" || a.DoneAt != "" || a.DueOn != "2026-11-01" || a.AssigneeID != ann || a.RoomID != newHall || a.ListID != lists[0].ID || a.UpdatedBy != "Pat" {
		t.Errorf("task a %+v", a)
	}
	if b.Status != "todo" || b.AssigneeName != "Youth" || b.RoomID != newYard || b.ListID != 0 {
		t.Errorf("task b %+v", b)
	}
	if c.DueOn != "" || c.Status != "todo" || c.RoomID != 0 {
		t.Errorf("task c %+v", c)
	}

	sched, _ := st.ListSchedule(id)
	if len(sched) != 2 || sched[0].Date != "2026-11-08" || sched[0].RoomID != newHall || sched[1].Date != "2026-11-10" || sched[1].RoomID != newYard || sched[0].EventID != id {
		t.Errorf("schedule %+v", sched)
	}
	needs, _ := st.ListNeeds(id)
	if len(needs) != 2 {
		t.Fatalf("needs %+v", needs)
	}
	for _, n := range needs {
		if n.Status != "needed" {
			t.Errorf("need %+v", n)
		}
		if n.Name == "Tables" && (n.RoomID != newHall || n.Quantity != 4) {
			t.Errorf("need %+v", n)
		}
		if n.Name == "Chairs" && (n.TrackerKind != "product" || n.TrackerID != 3) {
			t.Errorf("need %+v", n)
		}
	}
	roles, _ := st.ListHelperRoles(id)
	if len(roles) != 1 || roles[0].Name != "Greeters" || roles[0].Slots != 2 || roles[0].RoomID != newHall || len(roles[0].Helpers) != 0 {
		t.Errorf("roles %+v", roles)
	}

	// Source untouched.
	se, _ := st.GetEvent(src)
	if se.Status != "done" || se.StartsOn != "2026-11-01" || se.CopiedFrom != 0 {
		t.Errorf("source event %+v", se)
	}
	if st1, _ := st.GetTask(t1); st1.Status != "done" || st1.RoomID != hall || st1.DueOn != "2026-10-25" {
		t.Errorf("source task %+v", st1)
	}
	if st2, _ := st.GetTask(t2); st2.RoomID != yard || st2.Status != "blocked" {
		t.Errorf("source task %+v", st2)
	}
	sroles, _ := st.ListHelperRoles(src)
	if len(sroles) != 1 || len(sroles[0].Helpers) != 2 || sroles[0].RoomID != hall {
		t.Errorf("source roles %+v", sroles)
	}
	if r, _ := st.ListRooms(src); len(r) != 2 || r[0].ID != hall {
		t.Errorf("source rooms %+v", r)
	}

	// Other way round: drop assignees, keep helpers, and go back in time.
	id2, err := st.CopyEvent(src, CopyOptions{StartsOn: "2026-10-30", KeepHelpers: true})
	if err != nil {
		t.Fatal(err)
	}
	e2, _ := st.GetEvent(id2)
	if e2.Title != "Dinner" || e2.EndsOn != "2026-11-01" {
		t.Errorf("event 2 %+v", e2)
	}
	tasks2, _ := st.ListTasks(id2)
	if tasks2[0].AssigneeID != 0 || tasks2[1].AssigneeName != "" || tasks2[0].DueOn != "2026-10-23" {
		t.Errorf("tasks without assignees %+v", tasks2)
	}
	roles2, _ := st.ListHelperRoles(id2)
	if len(roles2[0].Helpers) != 2 || roles2[0].Helpers[0].UserID != ann || roles2[0].Helpers[1].Phone != "555" {
		t.Errorf("helpers %+v", roles2[0].Helpers)
	}

	if _, err := st.CopyEvent(src, CopyOptions{StartsOn: "nope"}); err == nil {
		t.Error("a bad date should fail")
	}
	if _, err := st.CopyEvent(999, CopyOptions{StartsOn: "2026-11-08"}); err == nil {
		t.Error("a missing event should fail")
	}
	// A one-day event stays one day.
	one := newEvent(t, st, "One")
	oc, _ := st.CopyEvent(one, CopyOptions{StartsOn: "2027-01-01"})
	if e, _ := st.GetEvent(oc); e.EndsOn != "" {
		t.Errorf("ends_on %q", e.EndsOn)
	}
}
