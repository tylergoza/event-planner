package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// CopyOptions says how to copy an event (for the next dinner or work day).
type CopyOptions struct {
	Title         string // empty: the same title
	StartsOn      string // the new first day, YYYY-MM-DD
	KeepAssignees bool   // tasks keep who they're given to
	KeepHelpers   bool   // people signed up for jobs stay signed up
	By            string // who copied it, recorded on the tasks and needs
}

// CopyEvent copies an event with its rooms, task lists, tasks, schedule,
// needs, agenda and helper jobs into a new planning event starting on
// opts.StartsOn. Dates in it move by the same number of days. Tasks start
// over as "to do" and needs as "needed". It all happens in one
// transaction, and returns the new event's ID. The copy is a one-off: it is
// never part of a repeating series, even when the source is.
func (s *Store) CopyEvent(srcID int64, opts CopyOptions) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	newID, err := copyEventTx(tx, srcID, opts)
	if err != nil {
		return 0, err
	}
	return newID, tx.Commit()
}

// copier copies the parts of one event into another inside a transaction.
type copier struct {
	tx            *sql.Tx
	src, dst      int64
	days          int // dates move by this many days
	by            string
	keepAssignees bool
	// room maps a source room ID (or nil) to the room in dst to use (nil
	// for none).
	room func(old any) (any, error)
}

// copyEventTx is CopyEvent inside a caller's transaction.
func copyEventTx(tx *sql.Tx, srcID int64, opts CopyOptions) (int64, error) {
	src, err := scanEvent(tx.QueryRow(`SELECT `+eventCols+` FROM events WHERE id = ?`, srcID))
	if err != nil {
		return 0, notFound(err)
	}
	to, err := time.Parse(DateLayout, opts.StartsOn)
	if err != nil {
		return 0, errors.New("pick the day the copy starts")
	}
	// An event not scheduled yet has nothing to move its dates from, so
	// they stay as they are.
	days := 0
	if src.Scheduled() {
		from, err := time.Parse(DateLayout, src.StartsOn)
		if err != nil {
			return 0, errors.New("the event's start date isn't a date")
		}
		days = int(to.Sub(from).Round(24*time.Hour).Hours() / 24)
	}
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = src.Title
	}

	res, err := tx.Exec(`INSERT INTO events (title, kind, status, starts_on, start_time, ends_on, end_time, coordinator, expected_count,
			description, notes, locked, copied_from, visibility, public_description, type)
		SELECT ?, kind, 'planning', ?, start_time,
			?, end_time, coordinator, expected_count, description, notes, locked, id, visibility, public_description, type
		FROM events WHERE id = ?`, title, opts.StartsOn, shiftDate(src.EndsOn, days), srcID)
	if err != nil {
		return 0, err
	}
	newID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO event_members (event_id, user_id) SELECT ?, user_id FROM event_members WHERE event_id = ?`, newID, srcID); err != nil {
		return 0, err
	}

	c := &copier{tx: tx, src: srcID, dst: newID, days: days, by: opts.By, keepAssignees: opts.KeepAssignees}
	rooms, err := c.rooms()
	if err != nil {
		return 0, err
	}
	c.room = func(old any) (any, error) { return mapID(rooms, old), nil }
	if err := c.tasks(); err != nil {
		return 0, err
	}
	if err := c.agenda(); err != nil {
		return 0, err
	}
	if err := c.schedule(); err != nil {
		return 0, err
	}
	if err := c.needs(); err != nil {
		return 0, err
	}
	if err := c.helperJobs(opts.KeepHelpers); err != nil {
		return 0, err
	}
	// The sign-up form comes along as a draft with no sign-ups.
	if err := copySignupFormTx(tx, srcID, newID); err != nil {
		return 0, err
	}
	return newID, nil
}

// mapID looks an old ID (an int64 or nil) up in m; nil when absent.
func mapID(m map[int64]int64, old any) any {
	if id, ok := old.(int64); ok {
		if n, ok := m[id]; ok {
			return n
		}
	}
	return nil
}

// ids runs a query returning one ID column.
func (c *copier) ids(q string, args ...any) ([]int64, error) {
	rows, err := c.tx.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// copyRows copies the rows of a table that has its own ID others point at,
// one by one, returning old ID -> new ID. insert is given the new event's
// ID and the source row's ID.
func (c *copier) copyRows(table, insert string) (map[int64]int64, error) {
	ids, err := c.ids(`SELECT id FROM `+table+` WHERE event_id = ? ORDER BY position, id`, c.src)
	if err != nil {
		return nil, err
	}
	m := map[int64]int64{}
	for _, id := range ids {
		r, err := c.tx.Exec(insert, c.dst, id)
		if err != nil {
			return nil, err
		}
		if m[id], err = r.LastInsertId(); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// rooms copies every room, returning old ID -> new ID.
func (c *copier) rooms() (map[int64]int64, error) {
	return c.copyRows("event_rooms", `INSERT INTO event_rooms (event_id, position, place_id, place_name, purpose, setup, notes)
		SELECT ?, position, place_id, place_name, purpose, setup, notes FROM event_rooms WHERE id = ?`)
}

// tasks copies the task lists and tasks. Tasks start as "to do".
func (c *copier) tasks() error {
	lists, err := c.copyRows("task_lists", `INSERT INTO task_lists (event_id, position, name) SELECT ?, position, name FROM task_lists WHERE id = ?`)
	if err != nil {
		return err
	}
	type taskRow struct {
		list, room, assignee    any
		position                int
		title, name, due, notes string
	}
	trows, err := c.tx.Query(`SELECT list_id, room_id, assignee_id, position, title, assignee_name, due_on, notes FROM tasks WHERE event_id = ? ORDER BY position, id`, c.src)
	if err != nil {
		return err
	}
	var tasks []taskRow
	for trows.Next() {
		var t taskRow
		if err := trows.Scan(&t.list, &t.room, &t.assignee, &t.position, &t.title, &t.name, &t.due, &t.notes); err != nil {
			trows.Close()
			return err
		}
		tasks = append(tasks, t)
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return err
	}
	for _, t := range tasks {
		assignee, name := t.assignee, t.name
		if !c.keepAssignees {
			assignee, name = nil, ""
		}
		room, err := c.room(t.room)
		if err != nil {
			return err
		}
		if _, err := c.tx.Exec(`INSERT INTO tasks (event_id, list_id, position, title, status, assignee_id, assignee_name, due_on, room_id, notes, done_at, updated_by)
			VALUES (?, ?, ?, ?, 'todo', ?, ?, ?, ?, ?, '', ?)`,
			c.dst, mapID(lists, t.list), t.position, t.title, assignee, name, shiftDate(t.due, c.days), room, t.notes, c.by); err != nil {
			return err
		}
	}
	return nil
}

// agenda copies the agenda items. Minutes and the events a meeting
// discussed are never copied.
func (c *copier) agenda() error {
	_, err := c.tx.Exec(`INSERT INTO agenda_items (event_id, position, title, details, carry_over)
		SELECT ?, position, title, details, carry_over FROM agenda_items WHERE event_id = ? ORDER BY position, id`, c.dst, c.src)
	return err
}

// schedule copies the timeline with its dates moved.
func (c *copier) schedule() error {
	type itemRow struct {
		date, time, end, label, notes string
		room                          any
	}
	irows, err := c.tx.Query(`SELECT date, time, end_time, label, notes, room_id FROM schedule_items WHERE event_id = ? ORDER BY date, time, id`, c.src)
	if err != nil {
		return err
	}
	var items []itemRow
	for irows.Next() {
		var i itemRow
		if err := irows.Scan(&i.date, &i.time, &i.end, &i.label, &i.notes, &i.room); err != nil {
			irows.Close()
			return err
		}
		items = append(items, i)
	}
	irows.Close()
	if err := irows.Err(); err != nil {
		return err
	}
	for _, i := range items {
		room, err := c.room(i.room)
		if err != nil {
			return err
		}
		if _, err := c.tx.Exec(`INSERT INTO schedule_items (event_id, date, time, end_time, label, room_id, notes) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			c.dst, shiftDate(i.date, c.days), i.time, i.end, i.label, room, i.notes); err != nil {
			return err
		}
	}
	return nil
}

// needs copies the needs, all starting as "needed".
func (c *copier) needs() error {
	type needRow struct {
		name, kind, notes string
		qty               int
		room, trackerID   any
	}
	nrows, err := c.tx.Query(`SELECT name, quantity, room_id, tracker_kind, tracker_id, notes FROM needs WHERE event_id = ? ORDER BY id`, c.src)
	if err != nil {
		return err
	}
	var needs []needRow
	for nrows.Next() {
		var n needRow
		if err := nrows.Scan(&n.name, &n.qty, &n.room, &n.kind, &n.trackerID, &n.notes); err != nil {
			nrows.Close()
			return err
		}
		needs = append(needs, n)
	}
	nrows.Close()
	if err := nrows.Err(); err != nil {
		return err
	}
	for _, n := range needs {
		room, err := c.room(n.room)
		if err != nil {
			return err
		}
		if _, err := c.tx.Exec(`INSERT INTO needs (event_id, name, quantity, room_id, tracker_kind, tracker_id, status, notes, updated_by)
			VALUES (?, ?, ?, ?, ?, ?, 'needed', ?, ?)`, c.dst, n.name, n.qty, room, n.kind, n.trackerID, n.notes, c.by); err != nil {
			return err
		}
	}
	return nil
}

// helperJobs copies the helper jobs, and optionally the people on them.
func (c *copier) helperJobs(keepHelpers bool) error {
	type roleRow struct {
		id, position, slots int64
		name, when, notes   string
		room                any
	}
	rrows, err := c.tx.Query(`SELECT id, position, slots, name, when_text, notes, room_id FROM helper_roles WHERE event_id = ? ORDER BY position, id`, c.src)
	if err != nil {
		return err
	}
	var roles []roleRow
	for rrows.Next() {
		var r roleRow
		if err := rrows.Scan(&r.id, &r.position, &r.slots, &r.name, &r.when, &r.notes, &r.room); err != nil {
			rrows.Close()
			return err
		}
		roles = append(roles, r)
	}
	rrows.Close()
	if err := rrows.Err(); err != nil {
		return err
	}
	for _, r := range roles {
		room, err := c.room(r.room)
		if err != nil {
			return err
		}
		res, err := c.tx.Exec(`INSERT INTO helper_roles (event_id, position, name, slots, when_text, room_id, notes) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			c.dst, r.position, r.name, r.slots, r.when, room, r.notes)
		if err != nil {
			return err
		}
		if !keepHelpers {
			continue
		}
		roleID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := c.tx.Exec(`INSERT INTO helpers (role_id, user_id, name, phone, notes) SELECT ?, user_id, name, phone, notes FROM helpers WHERE role_id = ? ORDER BY id`, roleID, r.id); err != nil {
			return err
		}
	}
	return nil
}

// shiftDate moves a YYYY-MM-DD date by days; empty and unreadable dates
// stay as they are.
func shiftDate(d string, days int) string {
	t, err := time.Parse(DateLayout, d)
	if err != nil {
		return d
	}
	return t.AddDate(0, 0, days).Format(DateLayout)
}
