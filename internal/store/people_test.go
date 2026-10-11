package store

import "testing"

func TestEventPeople(t *testing.T) {
	st := openTest(t)
	ann, _ := st.CreateUser("ann", "Ann Lee", "a long password", RoleHelper)
	ev := newEvent(t, st, "Dinner")
	other := newEvent(t, st, "Other")
	lst, _ := st.CreateTaskList(ev, "Kitchen")
	room, _ := st.AddRoom(ev, 0, "Hall")
	st.CreateTask(&Task{EventID: ev, ListID: lst, Title: "Cook", AssigneeID: ann, DueOn: "2026-10-01", RoomID: room})
	st.CreateTask(&Task{EventID: ev, Title: "Plates", AssigneeName: "dana "})
	st.CreateTask(&Task{EventID: ev, Title: "Trash", AssigneeName: "Zed", Status: "done"})
	st.CreateTask(&Task{EventID: ev, Title: "Nobody open"})
	st.CreateTask(&Task{EventID: ev, Title: "Nobody done", Status: "done"})
	st.CreateTask(&Task{EventID: other, Title: "Elsewhere", AssigneeName: "Qux"})

	greet, _ := st.CreateHelperRole(&HelperRole{EventID: ev, Name: "Greeters", Slots: 3, WhenText: "5:00", RoomID: room})
	dish, _ := st.CreateHelperRole(&HelperRole{EventID: ev, Name: "Dishes", Slots: 1})
	st.SignUpUser(greet, ann)
	st.SignUpName(greet, "Dana", "")
	st.SignUpName(dish, "ZED", "")

	p, err := st.EventPeople(ev, "2026-10-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.People) != 3 {
		t.Fatalf("people %+v", p.People)
	}
	a, d, z := p.People[0], p.People[1], p.People[2]
	if a.Name != "Ann Lee" || a.UserID != ann || len(a.Tasks) != 1 || a.Tasks[0].ListName != "Kitchen" || !a.Tasks[0].Late || a.Tasks[0].RoomName != "Hall" ||
		len(a.Jobs) != 1 || a.Jobs[0].RoleName != "Greeters" || a.Jobs[0].WhenText != "5:00" || a.Jobs[0].RoomName != "Hall" {
		t.Errorf("ann %+v", a)
	}
	// "dana " on a task and "Dana" on a job are one person.
	if d.Name != "dana" || d.UserID != 0 || len(d.Tasks) != 1 || len(d.Jobs) != 1 || d.Tasks[0].Late {
		t.Errorf("dana %+v", d)
	}
	if z.Name != "Zed" || len(z.Tasks) != 1 || len(z.Jobs) != 1 || z.OpenTasks() != 0 {
		t.Errorf("zed %+v", z)
	}
	if len(p.Unassigned) != 1 || p.Unassigned[0].Title != "Nobody open" {
		t.Errorf("unassigned %+v", p.Unassigned)
	}
	if len(p.ShortJobs) != 1 || p.ShortJobs[0].Name != "Greeters" || p.ShortJobs[0].Short() != 1 {
		t.Errorf("short %+v", p.ShortJobs)
	}

	empty, err := st.EventPeople(newEvent(t, st, "Empty"), "2026-10-10")
	if err != nil || len(empty.People)+len(empty.Unassigned)+len(empty.ShortJobs) != 0 {
		t.Errorf("empty %+v %v", empty, err)
	}
}
