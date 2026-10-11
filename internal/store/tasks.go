package store

import (
	"strings"
	"time"
)

// Task statuses, from not started to done.
var TaskStatuses = []string{"todo", "in_progress", "blocked", "done"}

var taskStatusLabels = map[string]string{"todo": "To do", "in_progress": "Doing", "blocked": "Needs help", "done": "Done"}

func TaskStatusLabel(s string) string { return taskStatusLabels[s] }

func ValidTaskStatus(s string) bool { _, ok := taskStatusLabels[s]; return ok }

// Task lists ---------------------------------------------------------------

// TaskList is a named to-do list in an event ("Before", "Kitchen").
type TaskList struct {
	ID       int64
	EventID  int64
	Position int
	Name     string
}

func (s *Store) ListTaskLists(eventID int64) ([]TaskList, error) {
	rows, err := s.DB.Query(`SELECT id, event_id, position, name FROM task_lists WHERE event_id = ? ORDER BY position, id`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskList
	for rows.Next() {
		var l TaskList
		if err := rows.Scan(&l.ID, &l.EventID, &l.Position, &l.Name); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) GetTaskList(id int64) (*TaskList, error) {
	var l TaskList
	err := s.DB.QueryRow(`SELECT id, event_id, position, name FROM task_lists WHERE id = ?`, id).Scan(&l.ID, &l.EventID, &l.Position, &l.Name)
	if err != nil {
		return nil, notFound(err)
	}
	return &l, nil
}

func (s *Store) CreateTaskList(eventID int64, name string) (int64, error) {
	pos, err := s.nextPosition("task_lists", eventID)
	if err != nil {
		return 0, err
	}
	res, err := s.DB.Exec(`INSERT INTO task_lists (event_id, position, name) VALUES (?, ?, ?)`, eventID, pos, strings.TrimSpace(name))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) RenameTaskList(id int64, name string) error {
	_, err := s.DB.Exec(`UPDATE task_lists SET name = ? WHERE id = ?`, strings.TrimSpace(name), id)
	return err
}

// DeleteTaskList removes a list. Its tasks stay, under "Other".
func (s *Store) DeleteTaskList(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM task_lists WHERE id = ?`, id)
	return err
}

// Tasks --------------------------------------------------------------------

// Task is one thing to do. It belongs to a user (AssigneeID) or to anyone
// else by name (AssigneeName), never both.
type Task struct {
	ID           int64
	EventID      int64
	ListID       int64 // 0: not on a list
	Position     int
	Title        string
	Status       string
	AssigneeID   int64
	AssigneeName string // free text; empty when AssigneeID is set
	UserName     string // the assignee user's display name
	DueOn        string
	RoomID       int64
	RoomName     string
	Notes        string
	DoneAt       string
	UpdatedBy    string
	UpdatedAt    string
}

// Assignee is who has the task, as shown.
func (t Task) Assignee() string {
	if t.AssigneeID != 0 {
		return t.UserName
	}
	return t.AssigneeName
}

func (t Task) IsDone() bool { return t.Status == "done" }

// Overdue reports whether the task is open and past its due date.
func (t Task) Overdue(today string) bool {
	return t.Status != "done" && t.DueOn != "" && t.DueOn < today
}

const taskCols = `t.id, t.event_id, COALESCE(t.list_id, 0), t.position, t.title, t.status, COALESCE(t.assignee_id, 0), t.assignee_name,
	COALESCE(NULLIF(u.display_name, ''), u.username, ''), t.due_on, COALESCE(t.room_id, 0), COALESCE(r.place_name, ''),
	t.notes, t.done_at, t.updated_by, t.updated_at`

const taskFrom = ` FROM tasks t LEFT JOIN users u ON u.id = t.assignee_id LEFT JOIN event_rooms r ON r.id = t.room_id`

func scanTask(sc interface{ Scan(...any) error }) (Task, error) {
	var t Task
	err := sc.Scan(&t.ID, &t.EventID, &t.ListID, &t.Position, &t.Title, &t.Status, &t.AssigneeID, &t.AssigneeName,
		&t.UserName, &t.DueOn, &t.RoomID, &t.RoomName, &t.Notes, &t.DoneAt, &t.UpdatedBy, &t.UpdatedAt)
	return t, err
}

func (s *Store) queryTasks(q string, args ...any) ([]Task, error) {
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListTasks lists an event's tasks, in the order they were added.
func (s *Store) ListTasks(eventID int64) ([]Task, error) {
	return s.queryTasks(`SELECT `+taskCols+taskFrom+` WHERE t.event_id = ? ORDER BY t.position, t.id`, eventID)
}

func (s *Store) GetTask(id int64) (*Task, error) {
	t, err := scanTask(s.DB.QueryRow(`SELECT `+taskCols+taskFrom+` WHERE t.id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return &t, nil
}

// normalize keeps a task to one kind of assignee: a user wins over a name.
func (t *Task) normalize() {
	t.Title = strings.TrimSpace(t.Title)
	if t.Status == "" {
		t.Status = "todo"
	}
	if t.AssigneeID != 0 {
		t.AssigneeName = ""
	}
}

func (s *Store) CreateTask(t *Task) (int64, error) {
	t.normalize()
	pos, err := s.nextPosition("tasks", t.EventID)
	if err != nil {
		return 0, err
	}
	doneAt := ""
	if t.Status == "done" {
		doneAt = time.Now().UTC().Format("2006-01-02 15:04:05")
	}
	res, err := s.DB.Exec(`INSERT INTO tasks (event_id, list_id, position, title, status, assignee_id, assignee_name, due_on, room_id, notes, done_at, updated_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.EventID, nullInt(t.ListID), pos, t.Title, t.Status, nullInt(t.AssigneeID), t.AssigneeName, t.DueOn, nullInt(t.RoomID), t.Notes, doneAt, t.UpdatedBy)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// doneAtSQL sets done_at for a change of status to ?: the time it was
// marked done (kept if it already was), or empty. It reads the old status.
const doneAtSQL = `done_at = CASE WHEN ? = 'done' THEN CASE WHEN status = 'done' THEN done_at ELSE datetime('now') END ELSE '' END`

func (s *Store) UpdateTask(t *Task) error {
	t.normalize()
	_, err := s.DB.Exec(`UPDATE tasks SET `+doneAtSQL+`, list_id = ?, title = ?, status = ?, assignee_id = ?, assignee_name = ?, due_on = ?,
		room_id = ?, notes = ?, updated_by = ?, updated_at = datetime('now') WHERE id = ?`,
		t.Status, nullInt(t.ListID), t.Title, t.Status, nullInt(t.AssigneeID), t.AssigneeName, t.DueOn, nullInt(t.RoomID), t.Notes, t.UpdatedBy, t.ID)
	return err
}

// SetTaskStatus changes a task's status, stamping done_at when it's done
// and clearing it otherwise.
func (s *Store) SetTaskStatus(id int64, status, by string) error {
	_, err := s.DB.Exec(`UPDATE tasks SET `+doneAtSQL+`, status = ?, updated_by = ?, updated_at = datetime('now') WHERE id = ?`, status, status, by, id)
	return err
}

func (s *Store) DeleteTask(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM tasks WHERE id = ?`, id)
	return err
}

// AddTasks adds one task per non-blank line of text to a list (0 for
// none), returning how many it added.
func (s *Store) AddTasks(eventID, listID int64, text, by string) (int, error) {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, err := s.CreateTask(&Task{EventID: eventID, ListID: listID, Title: line, UpdatedBy: by}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// MyTask is a task with the event it's in.
type MyTask struct {
	Task
	EventTitle string
}

// MyTasks lists the open tasks assigned to u in the active events u can
// see, soonest due first, those with no due date last.
func (s *Store) MyTasks(u *User) ([]MyTask, error) {
	cond, args := visibleTo(u)
	rows, err := s.DB.Query(`SELECT `+taskCols+`, e.title`+taskFrom+` JOIN events e ON e.id = t.event_id
		WHERE t.assignee_id = ? AND t.status <> 'done' AND e.status NOT IN ('done', 'cancelled') AND `+cond+`
		ORDER BY t.due_on = '', t.due_on, e.starts_on, t.id`, append([]any{u.ID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MyTask
	for rows.Next() {
		var m MyTask
		if err := rows.Scan(&m.ID, &m.EventID, &m.ListID, &m.Position, &m.Title, &m.Status, &m.AssigneeID, &m.AssigneeName,
			&m.UserName, &m.DueOn, &m.RoomID, &m.RoomName, &m.Notes, &m.DoneAt, &m.UpdatedBy, &m.UpdatedAt, &m.EventTitle); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// TaskGroup is one list's tasks and how many are done. List is nil for
// the tasks on no list ("Other").
type TaskGroup struct {
	List  *TaskList
	Tasks []Task
	Tally Tally
}

// Name is the list's name, or "Other".
func (g TaskGroup) Name() string {
	if g.List == nil {
		return "Other"
	}
	return g.List.Name
}

// GroupTasks puts tasks under their lists, in list order. Every list
// shows, empty or not; "Other" comes last, and only when it has tasks or
// there are no lists at all.
func GroupTasks(lists []TaskList, tasks []Task) []TaskGroup {
	idx := map[int64]int{}
	groups := make([]TaskGroup, 0, len(lists)+1)
	for i := range lists {
		idx[lists[i].ID] = len(groups)
		groups = append(groups, TaskGroup{List: &lists[i]})
	}
	other := TaskGroup{}
	for _, t := range tasks {
		g := &other
		if i, ok := idx[t.ListID]; ok && t.ListID != 0 {
			g = &groups[i]
		}
		g.Tasks = append(g.Tasks, t)
		g.Tally.Total++
		if t.IsDone() {
			g.Tally.Done++
		}
	}
	if len(other.Tasks) > 0 || len(lists) == 0 {
		groups = append(groups, other)
	}
	return groups
}
