package store

import (
	"sort"
	"strconv"
	"strings"
)

// PersonTask is a task on someone's plate, with its list's name.
type PersonTask struct {
	Task
	ListName string // "" when it's on no list
	Late     bool   // open and past its due date
}

// PersonJob is a helper job someone is signed up for.
type PersonJob struct {
	RoleID   int64
	RoleName string
	WhenText string
	RoomName string
}

// Person is one person's share of an event: a user (UserID set) or
// someone known only by a typed name.
type Person struct {
	Name   string
	UserID int64
	Tasks  []PersonTask
	Jobs   []PersonJob
}

// OpenTasks is how many of their tasks aren't done.
func (p Person) OpenTasks() int {
	n := 0
	for _, t := range p.Tasks {
		if !t.IsDone() {
			n++
		}
	}
	return n
}

// EventPeople is who's doing what in an event, and what nobody has yet.
type EventPeople struct {
	People     []Person     // by name
	Unassigned []PersonTask // open tasks with no one on them
	ShortJobs  []HelperRole // jobs with fewer people than slots
}

// EventPeople loads the event's tasks and jobs and groups them by person.
// today (YYYY-MM-DD) decides which tasks are overdue.
func (s *Store) EventPeople(eventID int64, today string) (*EventPeople, error) {
	tasks, err := s.ListTasks(eventID)
	if err != nil {
		return nil, err
	}
	lists, err := s.ListTaskLists(eventID)
	if err != nil {
		return nil, err
	}
	roles, err := s.ListHelperRoles(eventID)
	if err != nil {
		return nil, err
	}
	return GroupPeople(tasks, lists, roles, today), nil
}

// GroupPeople groups tasks and helper jobs by person. Users are told
// apart by ID; typed names are matched ignoring case and surrounding
// space, so "dana" on a task and "Dana " on a job are one person. The
// first spelling seen is the one shown.
func GroupPeople(tasks []Task, lists []TaskList, roles []HelperRole, today string) *EventPeople {
	listName := map[int64]string{}
	for _, l := range lists {
		listName[l.ID] = l.Name
	}
	out := &EventPeople{}
	index := map[string]*Person{}
	var order []*Person
	person := func(userID int64, name string) *Person {
		name = strings.TrimSpace(name)
		key := "n:" + strings.ToLower(name)
		if userID != 0 {
			key = "u:" + strconv.FormatInt(userID, 10)
		}
		p := index[key]
		if p == nil {
			p = &Person{Name: name, UserID: userID}
			index[key] = p
			order = append(order, p)
		}
		return p
	}

	for _, t := range tasks {
		pt := PersonTask{Task: t, ListName: listName[t.ListID], Late: t.Overdue(today)}
		name := strings.TrimSpace(t.Assignee())
		switch {
		case t.AssigneeID != 0:
			p := person(t.AssigneeID, name)
			p.Tasks = append(p.Tasks, pt)
		case name != "":
			p := person(0, name)
			p.Tasks = append(p.Tasks, pt)
		case !t.IsDone():
			out.Unassigned = append(out.Unassigned, pt)
		}
	}
	for _, r := range roles {
		for _, h := range r.Helpers {
			if strings.TrimSpace(h.Name) == "" {
				continue
			}
			p := person(h.UserID, h.Name)
			p.Jobs = append(p.Jobs, PersonJob{RoleID: r.ID, RoleName: r.Name, WhenText: r.WhenText, RoomName: r.RoomName})
		}
		if r.Short() > 0 {
			out.ShortJobs = append(out.ShortJobs, r)
		}
	}
	for _, p := range order {
		if p.Name == "" {
			p.Name = "(unnamed)"
		}
		out.People = append(out.People, *p)
	}
	sort.SliceStable(out.People, func(i, j int) bool {
		a, b := strings.ToLower(out.People[i].Name), strings.ToLower(out.People[j].Name)
		if a != b {
			return a < b
		}
		return out.People[i].UserID < out.People[j].UserID
	})
	return out
}
