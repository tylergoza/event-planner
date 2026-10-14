package server

import (
	"net/http"
	"strconv"

	"github.com/tylergoza/event-planner/internal/store"
)

// Tasks: to-do lists for the event ---------------------------------------

// taskFilters are the views of the task page, by the ?filter= value.
var taskFilters = []struct{ Key, Label string }{{"all", "All"}, {"mine", "Mine"}, {"open", "Open"}}

func taskFilter(r *http.Request) string {
	switch f := r.URL.Query().Get("filter"); f {
	case "mine", "open":
		return f
	}
	return "all"
}

func (s *Server) tasksData(r *http.Request, e *store.Event, data map[string]any) (map[string]any, error) {
	lists, err := s.store.ListTaskLists(e.ID)
	if err != nil {
		return nil, err
	}
	tasks, err := s.store.ListTasks(e.ID)
	if err != nil {
		return nil, err
	}
	total := store.Tally{Total: len(tasks)}
	overdue := 0
	for _, t := range tasks {
		if t.IsDone() {
			total.Done++
		}
		if t.Overdue(s.today()) {
			overdue++
		}
	}
	filter := taskFilter(r)
	var shown []store.Task
	for _, t := range tasks {
		switch {
		case filter == "mine" && t.AssigneeID != currentUser(r).ID:
		case filter == "open" && t.IsDone():
		default:
			shown = append(shown, t)
		}
	}
	data["Title"] = "Tasks · " + e.Title
	data["Groups"] = store.GroupTasks(lists, shown)
	data["Lists"] = lists
	data["Total"] = total
	data["Overdue"] = overdue
	data["Statuses"] = store.TaskStatuses
	data["Filter"] = filter
	data["Filters"] = taskFilters
	data["Today"] = s.today()
	return data, nil
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	data, err := s.tasksData(r, e, map[string]any{"Live": true})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	page := "tasks/index"
	if r.URL.Query().Get("print") == "1" {
		page = "tasks/print"
		delete(data, "Live")
	}
	s.eventPage(w, r, http.StatusOK, page, e, "tasks", data)
}

// taskListID checks that a list picked in a form is one of the event's
// (0 is no list).
func (s *Server) taskListID(eventID, listID int64) bool {
	if listID == 0 {
		return true
	}
	l, err := s.store.GetTaskList(listID)
	return err == nil && l.EventID == eventID
}

// readTask reads the edit form into t. A task has a person from the list
// or a name typed in, not both.
func (s *Server) readTask(r *http.Request, t *store.Task) []string {
	prevAssignee := t.AssigneeID
	t.Title, t.Notes, t.DueOn = formStr(r, "title"), formStr(r, "notes"), formStr(r, "due_on")
	t.ListID, t.RoomID = formInt(r, "list_id"), formInt(r, "room_id")
	t.AssigneeID, t.AssigneeName = formInt(r, "assignee_id"), formStr(r, "assignee_name")
	if v := formStr(r, "status"); v != "" {
		t.Status = v
	}
	t.UpdatedBy = currentUser(r).Name()
	var errs []string
	if t.Title == "" {
		errs = append(errs, "Say what has to be done.")
	}
	if !store.ValidTaskStatus(t.Status) {
		errs = append(errs, "Pick a status.")
	}
	if t.DueOn != "" && !validDate(t.DueOn) {
		errs = append(errs, "That due date isn't a date.")
	}
	if !s.taskListID(t.EventID, t.ListID) {
		errs = append(errs, "That list isn't part of this event.")
	}
	if t.RoomID != 0 {
		if ok, err := s.store.RoomInEvent(t.RoomID, t.EventID); err != nil || !ok {
			errs = append(errs, "That room isn't part of this event.")
		}
	}
	switch {
	case t.AssigneeID != 0 && t.AssigneeName != "":
		errs = append(errs, "Pick a person from the list or type a name, not both.")
	case t.AssigneeID != 0 && t.AssigneeID != prevAssignee:
		if u, err := s.store.GetUser(t.AssigneeID); err != nil || !u.Active {
			errs = append(errs, "That person can't be given tasks.")
		}
	}
	return errs
}

func (s *Server) handleTaskCreate(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	t := store.Task{EventID: e.ID, Status: "todo"}
	t.Title, t.ListID = formStr(r, "title"), formInt(r, "list_id")
	t.UpdatedBy = currentUser(r).Name()
	switch {
	case t.Title == "":
		s.setFlash(w, r, "error", "Say what has to be done.")
	case !s.taskListID(e.ID, t.ListID):
		s.setFlash(w, r, "error", "That list isn't part of this event.")
	default:
		if _, err := s.store.CreateTask(&t); err != nil {
			s.serverError(w, r, err)
			return
		}
		// A form on another page (a meeting's events list) can ask to come back to it.
		s.redirect(w, r, returnTo(r, eventURL(e.ID, "/tasks#"+listAnchor(t.ListID))), t.Title+" added.")
		return
	}
	http.Redirect(w, r, returnTo(r, eventURL(e.ID, "/tasks")), http.StatusSeeOther)
}

// listAnchor is the id of a list's section on the task page.
func listAnchor(listID int64) string {
	if listID == 0 {
		return "list-other"
	}
	return "list-" + strconv.FormatInt(listID, 10)
}

func (s *Server) handleTaskPaste(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	listID := formInt(r, "list_id")
	if !s.taskListID(e.ID, listID) {
		s.setFlash(w, r, "error", "That list isn't part of this event.")
		http.Redirect(w, r, eventURL(e.ID, "/tasks"), http.StatusSeeOther)
		return
	}
	n, err := s.store.AddTasks(e.ID, listID, r.PostFormValue("text"), currentUser(r).Name())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	msg := pluralize(n, "task") + " added."
	if n == 0 {
		msg = "Nothing to add: put one task on each line."
	}
	s.redirect(w, r, eventURL(e.ID, "/tasks#"+listAnchor(listID)), msg)
}

func (s *Server) taskAndEvent(w http.ResponseWriter, r *http.Request) (*store.Task, *store.Event, bool) {
	t, err := s.store.GetTask(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, nil, false
	}
	e, ok := s.eventByID(w, r, t.EventID)
	return t, e, ok
}

func (s *Server) taskForm(w http.ResponseWriter, r *http.Request, status int, e *store.Event, t store.Task, errs []string) {
	lists, err := s.store.ListTaskLists(e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rooms, err := s.store.ListRooms(e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var keep []int64
	if t.AssigneeID != 0 {
		keep = append(keep, t.AssigneeID)
	}
	users, err := s.pickableUsers(r, keep)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.eventPage(w, r, status, "tasks/form", e, "tasks", map[string]any{
		"Form": t, "Errors": errs, "Lists": lists, "Rooms": rooms, "Users": users, "Statuses": store.TaskStatuses,
	})
}

func (s *Server) handleTaskEdit(w http.ResponseWriter, r *http.Request) {
	t, e, ok := s.taskAndEvent(w, r)
	if !ok {
		return
	}
	s.taskForm(w, r, http.StatusOK, e, *t, nil)
}

func (s *Server) handleTaskUpdate(w http.ResponseWriter, r *http.Request) {
	t, e, ok := s.taskAndEvent(w, r)
	if !ok {
		return
	}
	if errs := s.readTask(r, t); errs != nil {
		s.taskForm(w, r, http.StatusUnprocessableEntity, e, *t, errs)
		return
	}
	if err := s.store.UpdateTask(t); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/tasks#task-"+strconv.FormatInt(t.ID, 10)), "Saved.")
}

func (s *Server) handleTaskStatus(w http.ResponseWriter, r *http.Request) {
	t, e, ok := s.taskAndEvent(w, r)
	if !ok {
		return
	}
	status := formStr(r, "status")
	if !store.ValidTaskStatus(status) {
		s.setFlash(w, r, "error", "Pick a status.")
		http.Redirect(w, r, eventURL(e.ID, "/tasks"), http.StatusSeeOther)
		return
	}
	if err := s.store.SetTaskStatus(t.ID, status, currentUser(r).Name()); err != nil {
		s.serverError(w, r, err)
		return
	}
	next := r.PostFormValue("next")
	if next == "" {
		next = eventURL(e.ID, "/tasks")
	}
	s.redirect(w, r, safeRedirect(next)+"#task-"+strconv.FormatInt(t.ID, 10), t.Title+": "+store.TaskStatusLabel(status)+".")
}

func (s *Server) handleTaskDelete(w http.ResponseWriter, r *http.Request) {
	t, e, ok := s.taskAndEvent(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteTask(t.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/tasks#"+listAnchor(t.ListID)), t.Title+" removed.")
}

// Task lists ---------------------------------------------------------------

func (s *Server) handleTaskListCreate(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	name := formStr(r, "name")
	if name == "" {
		s.setFlash(w, r, "error", "Give the list a name.")
		http.Redirect(w, r, eventURL(e.ID, "/tasks"), http.StatusSeeOther)
		return
	}
	id, err := s.store.CreateTaskList(e.ID, name)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/tasks#"+listAnchor(id)), name+" list added.")
}

func (s *Server) listAndEvent(w http.ResponseWriter, r *http.Request) (*store.TaskList, *store.Event, bool) {
	l, err := s.store.GetTaskList(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, nil, false
	}
	e, ok := s.eventByID(w, r, l.EventID)
	return l, e, ok
}

func (s *Server) handleTaskListRename(w http.ResponseWriter, r *http.Request) {
	l, e, ok := s.listAndEvent(w, r)
	if !ok {
		return
	}
	name := formStr(r, "name")
	if name == "" {
		s.setFlash(w, r, "error", "Give the list a name.")
		http.Redirect(w, r, eventURL(e.ID, "/tasks"), http.StatusSeeOther)
		return
	}
	if err := s.store.RenameTaskList(l.ID, name); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/tasks#"+listAnchor(l.ID)), "Renamed to "+name+".")
}

func (s *Server) handleTaskListDelete(w http.ResponseWriter, r *http.Request) {
	l, e, ok := s.listAndEvent(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteTaskList(l.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/tasks"), l.Name+" list removed. Its tasks are under Other.")
}
