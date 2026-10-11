package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func newEvent(t *testing.T, st *Store, title string) int64 {
	t.Helper()
	id, err := st.CreateEvent(&Event{Title: title, StartsOn: "2026-11-01"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func listNames(lists []TaskList) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l.Name)
	}
	return out
}

func TestTaskListOrdering(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Dinner")
	other := newEvent(t, st, "Work day")
	a, _ := st.CreateTaskList(ev, "Before")
	b, _ := st.CreateTaskList(ev, "Kitchen")
	c, _ := st.CreateTaskList(ev, "Clean-up")
	st.CreateTaskList(other, "Elsewhere")

	check := func(want ...string) {
		t.Helper()
		lists, err := st.ListTaskLists(ev)
		if err != nil {
			t.Fatal(err)
		}
		got := listNames(lists)
		if len(got) != len(want) {
			t.Fatalf("lists = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("lists = %v, want %v", got, want)
			}
		}
	}
	check("Before", "Kitchen", "Clean-up")
	st.Move("task_lists", c, true)
	check("Before", "Clean-up", "Kitchen")
	st.Move("task_lists", a, true) // already first: nothing happens
	check("Before", "Clean-up", "Kitchen")
	st.Move("task_lists", a, false)
	check("Clean-up", "Before", "Kitchen")
	st.Move("task_lists", b, false) // already last
	check("Clean-up", "Before", "Kitchen")
	if got, err := st.EventOf("task_lists", b); err != nil || got != ev {
		t.Errorf("EventOf = %d, %v", got, err)
	}
	if err := st.Move("users", 1, true); err == nil {
		t.Error("moving rows of another table should fail")
	}
	if _, err := st.EventOf("task_lists", 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("EventOf missing = %v", err)
	}

	// Deleting a list keeps its tasks, under "Other".
	st.RenameTaskList(a, "Early")
	task, _ := st.CreateTask(&Task{EventID: ev, ListID: a, Title: "Buy plates"})
	if err := st.DeleteTaskList(a); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetTask(task)
	if err != nil || got.ListID != 0 {
		t.Fatalf("task after list delete: %+v %v", got, err)
	}
}

func TestGroupTasks(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Dinner")
	l1, _ := st.CreateTaskList(ev, "Before")
	st.CreateTaskList(ev, "Empty")
	st.CreateTask(&Task{EventID: ev, ListID: l1, Title: "A", Status: "done"})
	st.CreateTask(&Task{EventID: ev, ListID: l1, Title: "B"})
	st.CreateTask(&Task{EventID: ev, Title: "Loose"})
	lists, _ := st.ListTaskLists(ev)
	tasks, _ := st.ListTasks(ev)
	groups := GroupTasks(lists, tasks)
	if len(groups) != 3 || groups[0].Name() != "Before" || groups[1].Name() != "Empty" || groups[2].Name() != "Other" {
		t.Fatalf("groups = %+v", groups)
	}
	if groups[0].Tally != (Tally{1, 2}) || len(groups[2].Tasks) != 1 {
		t.Errorf("tallies: %+v / %+v", groups[0].Tally, groups[2])
	}
	if g := GroupTasks(nil, nil); len(g) != 1 || g[0].Name() != "Other" {
		t.Errorf("no lists should still offer Other: %+v", g)
	}
}

func TestTaskStatusDoneAt(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Dinner")
	id, err := st.CreateTask(&Task{EventID: ev, Title: "Set tables"})
	if err != nil {
		t.Fatal(err)
	}
	get := func() *Task {
		t.Helper()
		task, err := st.GetTask(id)
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	if task := get(); task.Status != "todo" || task.DoneAt != "" {
		t.Fatalf("new task: %+v", task)
	}
	st.SetTaskStatus(id, "in_progress", "Pat")
	if task := get(); task.Status != "in_progress" || task.DoneAt != "" || task.UpdatedBy != "Pat" {
		t.Errorf("doing: %+v", task)
	}
	st.SetTaskStatus(id, "done", "Pat")
	task := get()
	if task.Status != "done" || task.DoneAt == "" {
		t.Fatalf("done: %+v", task)
	}
	// Saving it again as done keeps the original time.
	st.DB.Exec(`UPDATE tasks SET done_at = '2026-01-01 10:00:00' WHERE id = ?`, id)
	task.UpdatedBy = "Sam"
	if err := st.UpdateTask(task); err != nil {
		t.Fatal(err)
	}
	if got := get(); got.DoneAt != "2026-01-01 10:00:00" {
		t.Errorf("done_at changed on re-save: %q", got.DoneAt)
	}
	st.SetTaskStatus(id, "blocked", "Sam")
	if got := get(); got.DoneAt != "" {
		t.Errorf("done_at should clear when reopened: %q", got.DoneAt)
	}
	// Created as done.
	id2, _ := st.CreateTask(&Task{EventID: ev, Title: "Already", Status: "done"})
	if got, _ := st.GetTask(id2); got.DoneAt == "" {
		t.Error("a task created as done should have done_at")
	}
}

func TestTaskOverdue(t *testing.T) {
	task := Task{Status: "todo", DueOn: "2026-10-01"}
	if !task.Overdue("2026-10-02") || task.Overdue("2026-10-01") || task.Overdue("2026-09-30") {
		t.Error("overdue is strictly before today")
	}
	task.Status = "done"
	if task.Overdue("2026-10-02") {
		t.Error("done tasks are never overdue")
	}
	if (Task{Status: "todo"}).Overdue("2026-10-02") {
		t.Error("no due date, not overdue")
	}
}

func TestTaskAssigneeExclusive(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Dinner")
	uid, _ := st.CreateUser("ann", "Ann A", "a long password", RoleHelper)
	id, err := st.CreateTask(&Task{EventID: ev, Title: "Both", AssigneeID: uid, AssigneeName: "Youth group"})
	if err != nil {
		t.Fatal(err)
	}
	task, _ := st.GetTask(id)
	if task.AssigneeID != uid || task.AssigneeName != "" || task.Assignee() != "Ann A" {
		t.Errorf("user should win: %+v", task)
	}
	task.AssigneeID, task.AssigneeName = 0, "Youth group"
	if err := st.UpdateTask(task); err != nil {
		t.Fatal(err)
	}
	if task, _ = st.GetTask(id); task.AssigneeID != 0 || task.Assignee() != "Youth group" {
		t.Errorf("name only: %+v", task)
	}
	// The schema itself refuses both.
	if _, err := st.DB.Exec(`UPDATE tasks SET assignee_id = ?, assignee_name = 'x' WHERE id = ?`, uid, id); err == nil {
		t.Error("expected the CHECK constraint to refuse both")
	}
	// Deleting the user frees the task rather than deleting it.
	st.UpdateTask(&Task{ID: id, Title: "Both", AssigneeID: uid})
	st.DeleteUser(uid)
	if task, err = st.GetTask(id); err != nil || task.Assignee() != "" {
		t.Errorf("after user delete: %+v %v", task, err)
	}
}

func TestAddTasks(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Dinner")
	l, _ := st.CreateTaskList(ev, "Kitchen")
	n, err := st.AddTasks(ev, l, "Chop onions\n\n  Boil water \r\nSet out spoons\n", "Pat")
	if err != nil || n != 3 {
		t.Fatalf("AddTasks = %d, %v", n, err)
	}
	tasks, _ := st.ListTasks(ev)
	if len(tasks) != 3 || tasks[1].Title != "Boil water" || tasks[2].ListID != l || tasks[0].UpdatedBy != "Pat" {
		t.Errorf("tasks = %+v", tasks)
	}
}

func TestMyTasks(t *testing.T) {
	st := openTest(t)
	ann, _ := st.CreateUser("ann", "Ann", "a long password", RoleHelper)
	bob, _ := st.CreateUser("bob", "Bob", "a long password", RoleHelper)
	annU, _ := st.GetUser(ann)
	bobU, _ := st.GetUser(bob)

	open := newEvent(t, st, "Open event")
	done, _ := st.CreateEvent(&Event{Title: "Past", StartsOn: "2026-01-01", Status: "done"})
	secret, _ := st.CreateEvent(&Event{Title: "Secret", StartsOn: "2026-11-01", Locked: true})

	st.CreateTask(&Task{EventID: open, Title: "Later", AssigneeID: ann, DueOn: "2026-11-05"})
	st.CreateTask(&Task{EventID: open, Title: "No date", AssigneeID: ann})
	st.CreateTask(&Task{EventID: open, Title: "Sooner", AssigneeID: ann, DueOn: "2026-10-01"})
	st.CreateTask(&Task{EventID: open, Title: "Finished", AssigneeID: ann, Status: "done"})
	st.CreateTask(&Task{EventID: open, Title: "Bob's", AssigneeID: bob})
	st.CreateTask(&Task{EventID: open, Title: "Named", AssigneeName: "Ann"})
	st.CreateTask(&Task{EventID: done, Title: "In a finished event", AssigneeID: ann})
	st.CreateTask(&Task{EventID: secret, Title: "Hidden", AssigneeID: ann})

	mine, err := st.MyTasks(annU)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range mine {
		got = append(got, m.Title)
	}
	want := []string{"Sooner", "Later", "No date"}
	if len(got) != 3 {
		t.Fatalf("my tasks = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("my tasks = %v, want %v", got, want)
		}
	}
	if mine[0].EventTitle != "Open event" {
		t.Errorf("event title = %q", mine[0].EventTitle)
	}
	// Bob has one visible; the locked event's task is only Ann's and she is not a member.
	if theirs, _ := st.MyTasks(bobU); len(theirs) != 1 {
		t.Errorf("bob's tasks: %+v", theirs)
	}
	// Once Ann is a member the locked event's task shows.
	st.SetMembers(secret, []int64{ann})
	if mine, _ = st.MyTasks(annU); len(mine) != 4 {
		t.Errorf("member's tasks: %d", len(mine))
	}
}

func TestSummary(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Dinner")
	st.CreateTask(&Task{EventID: ev, Title: "a", Status: "done"})
	st.CreateTask(&Task{EventID: ev, Title: "b", DueOn: "2026-10-01"})
	st.CreateTask(&Task{EventID: ev, Title: "c", Status: "blocked"})
	st.CreateNeed(&Need{EventID: ev, Name: "Plates", Quantity: 50, Status: "gathered"})
	st.CreateNeed(&Need{EventID: ev, Name: "Forks", Quantity: 50})
	e, _ := st.GetEvent(ev)
	sum, err := st.Summary(e, "2026-10-10")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Tasks != (Tally{1, 3}) || sum.Overdue != 1 || sum.NeedsHelp != 1 || sum.Needs != (Tally{1, 2}) {
		t.Errorf("summary = %+v", sum)
	}
}

func TestEventAccessAndOrder(t *testing.T) {
	st := openTest(t)
	adminID, _ := st.CreateUser("admin", "", "a long password", RoleAdmin)
	annID, _ := st.CreateUser("ann", "", "a long password", RoleHelper)
	admin, _ := st.GetUser(adminID)
	ann, _ := st.GetUser(annID)

	st.CreateEvent(&Event{Title: "Later", StartsOn: "2026-12-01"})
	st.CreateEvent(&Event{Title: "Sooner", StartsOn: "2026-11-01"})
	st.CreateEvent(&Event{Title: "Old", StartsOn: "2026-01-01", Status: "done"})
	locked, _ := st.CreateEvent(&Event{Title: "Locked", StartsOn: "2026-11-15", Locked: true})

	titles := func(u *User, activeOnly bool) []string {
		list, err := st.ListEvents(u, activeOnly, "2026-10-10")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range list {
			out = append(out, e.Title)
		}
		return out
	}
	if got := titles(admin, false); len(got) != 4 || got[0] != "Sooner" || got[1] != "Locked" || got[2] != "Later" || got[3] != "Old" {
		t.Errorf("admin list = %v", got)
	}
	if got := titles(ann, true); len(got) != 2 || got[0] != "Sooner" {
		t.Errorf("ann list = %v", got)
	}
	e, _ := st.GetEvent(locked)
	if ok, _ := st.CanSee(e, ann); ok {
		t.Error("ann shouldn't see the locked event")
	}
	st.SetMembers(locked, []int64{annID})
	if ok, _ := st.CanSee(e, ann); !ok {
		t.Error("members see the locked event")
	}
	if got := titles(ann, true); len(got) != 3 {
		t.Errorf("ann list as member = %v", got)
	}
}

func TestEventWhen(t *testing.T) {
	cases := []struct {
		e    Event
		want string
	}{
		{Event{StartsOn: "2026-11-01"}, "Sun, Nov 1, 2026"},
		{Event{StartsOn: "2026-11-01", StartTime: "18:00", EndTime: "20:30"}, "Sun, Nov 1, 2026 · 6:00 PM – 8:30 PM"},
		{Event{StartsOn: "2026-11-01", EndsOn: "2026-11-03"}, "Sun, Nov 1, 2026 – Tue, Nov 3, 2026"},
	}
	for _, c := range cases {
		if got := c.e.When(); got != c.want {
			t.Errorf("When() = %q, want %q", got, c.want)
		}
	}
}

func TestScheduleAndRooms(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Dinner")
	res, err := st.DB.Exec(`INSERT INTO event_rooms (event_id, position, place_name, purpose) VALUES (?, 1, 'Main › Hall', 'Dinner')`, ev)
	if err != nil {
		t.Fatal(err)
	}
	room, _ := res.LastInsertId()
	rooms, err := st.ListRooms(ev)
	if err != nil || len(rooms) != 1 || rooms[0].Label() != "Main › Hall (Dinner)" {
		t.Fatalf("rooms = %+v %v", rooms, err)
	}
	if ok, _ := st.RoomInEvent(room, ev); !ok {
		t.Error("room is in the event")
	}
	if ok, _ := st.RoomInEvent(room, ev+1); ok {
		t.Error("room is not in another event")
	}
	st.CreateScheduleItem(&ScheduleItem{EventID: ev, Date: "2026-11-01", Time: "17:00", Label: "Doors"})
	st.CreateScheduleItem(&ScheduleItem{EventID: ev, Date: "2026-11-01", Time: "15:00", Label: "Set up", RoomID: room})
	items, _ := st.ListSchedule(ev)
	if len(items) != 2 || items[0].Label != "Set up" || items[0].RoomName != "Main › Hall" || items[1].RoomID != 0 {
		t.Errorf("items = %+v", items)
	}
	// Removing the room leaves the item, with no room.
	st.DB.Exec(`DELETE FROM event_rooms WHERE id = ?`, room)
	if it, _ := st.GetScheduleItem(items[0].ID); it.RoomID != 0 {
		t.Errorf("room should clear: %+v", it)
	}
}

func TestNeeds(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Dinner")
	id, err := st.CreateNeed(&Need{EventID: ev, Name: " Chairs ", Quantity: 0, TrackerKind: "product", TrackerID: 9})
	if err != nil {
		t.Fatal(err)
	}
	n, _ := st.GetNeed(id)
	if n.Name != "Chairs" || n.Quantity != 1 || !n.FromTracker() || n.Status != "needed" {
		t.Errorf("need = %+v", n)
	}
	st.SetNeedStatus(id, "gathered", "Pat")
	if n, _ = st.GetNeed(id); n.Status != "gathered" || n.UpdatedBy != "Pat" {
		t.Errorf("need after status = %+v", n)
	}
}
