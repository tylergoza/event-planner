package store

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// Event statuses, in the order an event goes through them.
var EventStatuses = []string{"planning", "ready", "done", "cancelled"}

var eventStatusLabels = map[string]string{
	"planning": "Planning", "ready": "Ready", "done": "Done", "cancelled": "Cancelled",
}

func EventStatusLabel(s string) string { return eventStatusLabels[s] }

// Event is a dinner, work day, fellowship, meeting... Dates are YYYY-MM-DD,
// times HH:MM ("" for all day). EndsOn "" means a one-day event. StartsOn
// "" means it isn't scheduled yet (BOOKING_PLAN.md): it has no date, and
// stays out of the calendar, feeds and room clash checks.
type Event struct {
	ID       int64
	Title    string
	Kind     string
	Status   string
	StartsOn string
	// DateTentative is set while the date is pencilled in: held, not
	// certain. The calendar gets it as "tentative".
	DateTentative bool
	StartTime     string
	EndsOn        string
	EndTime       string
	Coordinator   string
	ExpectedCount int // headcount; 0 when not known
	Description   string
	Notes         string
	// Visibility is "public" or "private": see what people outside see
	// in migration 002. PublicDescription is shown on the public calendar.
	Visibility        string
	PublicDescription string
	// Locked events are only for their members (and admins).
	Locked     bool
	CopiedFrom int64 // the event this was copied from, 0 if none
	// CalendarReplaces is the calendar booking this event takes over on its
	// first push; 0 once sent (or if none).
	CalendarReplaces int64
	// SeriesID is the repeating series this is an occurrence of, 0 if none.
	// OccurrenceDate is the date the series' rule gave it (it stays even if
	// the event is moved to another day); "" when not in a series.
	SeriesID       int64
	OccurrenceDate string
	CreatedAt      string
	UpdatedAt      string
}

// Scheduled reports whether the event has a date yet.
func (e Event) Scheduled() bool { return e.StartsOn != "" }

// Active reports whether the event is still being worked on.
func (e Event) Active() bool { return e.Status != "done" && e.Status != "cancelled" }

// LastDate is the day the event ends.
func (e Event) LastDate() string {
	if e.EndsOn != "" {
		return e.EndsOn
	}
	return e.StartsOn
}

func fmtClock(t string) string {
	if c, err := time.Parse("15:04", t); err == nil {
		return c.Format("3:04 PM")
	}
	return t
}

// When is the date and time for display, e.g. "Sat, Nov 1, 2026 · 6:00 PM – 8:00 PM".
func (e Event) When() string {
	if !e.Scheduled() {
		return "Not scheduled yet"
	}
	d, err := time.Parse(DateLayout, e.StartsOn)
	if err != nil {
		return e.StartsOn
	}
	out := d.Format("Mon, Jan 2, 2006")
	if e.EndsOn != "" && e.EndsOn != e.StartsOn {
		if end, err := time.Parse(DateLayout, e.EndsOn); err == nil {
			out += " " + fmtClock(e.StartTime)
			out = strings.TrimSpace(out) + " – " + end.Format("Mon, Jan 2, 2006")
			if e.EndTime != "" {
				out += " " + fmtClock(e.EndTime)
			}
			return out
		}
	}
	if e.StartTime != "" {
		out += " · " + fmtClock(e.StartTime)
		if e.EndTime != "" {
			out += " – " + fmtClock(e.EndTime)
		}
	}
	return out
}

const eventCols = `id, title, kind, status, starts_on, date_tentative, start_time, ends_on, end_time, coordinator, COALESCE(expected_count, 0),
	description, notes, locked, COALESCE(copied_from, 0), visibility, public_description, COALESCE(series_id, 0), occurrence_date, COALESCE(calendar_replaces, 0), created_at, updated_at`

func scanEvent(sc interface{ Scan(...any) error }) (Event, error) {
	var e Event
	err := sc.Scan(&e.ID, &e.Title, &e.Kind, &e.Status, &e.StartsOn, &e.DateTentative, &e.StartTime, &e.EndsOn, &e.EndTime, &e.Coordinator,
		&e.ExpectedCount, &e.Description, &e.Notes, &e.Locked, &e.CopiedFrom, &e.Visibility, &e.PublicDescription, &e.SeriesID, &e.OccurrenceDate, &e.CalendarReplaces, &e.CreatedAt, &e.UpdatedAt)
	return e, err
}

func (s *Store) GetEvent(id int64) (*Event, error) {
	e, err := scanEvent(s.DB.QueryRow(`SELECT `+eventCols+` FROM events WHERE id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return &e, nil
}

func (s *Store) CreateEvent(e *Event) (int64, error) {
	if e.Status == "" {
		e.Status = "planning"
	}
	e.Visibility = NormalizeVisibility(e.Visibility)
	res, err := s.DB.Exec(`INSERT INTO events (title, kind, status, starts_on, date_tentative, start_time, ends_on, end_time, coordinator, expected_count,
		description, notes, locked, copied_from, visibility, public_description, calendar_replaces) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Title, e.Kind, e.Status, e.StartsOn, e.DateTentative, e.StartTime, e.EndsOn, e.EndTime, e.Coordinator, nullInt(int64(e.ExpectedCount)),
		e.Description, e.Notes, e.Locked, nullInt(e.CopiedFrom), e.Visibility, e.PublicDescription, nullInt(e.CalendarReplaces))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Visibilities are the choices for who can see an event's details.
var Visibilities = []string{"private", "public"}

// NormalizeVisibility maps anything but "public" to "private".
func NormalizeVisibility(v string) string {
	if v == "public" {
		return v
	}
	return "private"
}

func (s *Store) UpdateEvent(e *Event) error {
	e.Visibility = NormalizeVisibility(e.Visibility)
	_, err := s.DB.Exec(`UPDATE events SET title = ?, kind = ?, status = ?, starts_on = ?, date_tentative = ?, start_time = ?, ends_on = ?, end_time = ?,
		coordinator = ?, expected_count = ?, description = ?, notes = ?, locked = ?, visibility = ?, public_description = ?, updated_at = datetime('now') WHERE id = ?`,
		e.Title, e.Kind, e.Status, e.StartsOn, e.DateTentative, e.StartTime, e.EndsOn, e.EndTime, e.Coordinator, nullInt(int64(e.ExpectedCount)),
		e.Description, e.Notes, e.Locked, e.Visibility, e.PublicDescription, e.ID)
	return err
}

// ClearCalendarReplaces forgets the booking an event took over, once the
// calendar has it. Queues nothing: the column isn't one the 004 trigger watches.
func (s *Store) ClearCalendarReplaces(id int64) error {
	_, err := s.DB.Exec(`UPDATE events SET calendar_replaces = NULL WHERE id = ?`, id)
	return err
}

// ConfirmDate clears an event's pencilled-in mark. The 007 trigger queues
// the change for the calendar.
func (s *Store) ConfirmDate(id int64) error {
	_, err := s.DB.Exec(`UPDATE events SET date_tentative = 0, updated_at = datetime('now') WHERE id = ? AND date_tentative = 1`, id)
	return err
}

func (s *Store) DeleteEvent(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM events WHERE id = ?`, id)
	return err
}

// Who can see an event -----------------------------------------------------

// visibleTo is a condition on events e that's true for the ones u can
// see: all of them for an admin, otherwise the unlocked ones and the
// locked ones u is a member of.
func visibleTo(u *User) (string, []any) {
	if u.IsAdmin() {
		return `1`, nil
	}
	return `(NOT e.locked OR EXISTS (SELECT 1 FROM event_members m WHERE m.event_id = e.id AND m.user_id = ?))`, []any{u.ID}
}

// CanSee reports whether u may see and work on e.
func (s *Store) CanSee(e *Event, u *User) (bool, error) {
	if !e.Locked || u.IsAdmin() {
		return true, nil
	}
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM event_members WHERE event_id = ? AND user_id = ?`, e.ID, u.ID).Scan(&n)
	return n > 0, err
}

// Members lists the IDs of the users on an event's member list.
func (s *Store) Members(eventID int64) ([]int64, error) {
	rows, err := s.DB.Query(`SELECT user_id FROM event_members WHERE event_id = ? ORDER BY user_id`, eventID)
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

// SetMembers replaces an event's member list. Inactive users (access
// removed) stay on it if they were already, but can't be newly added.
func (s *Store) SetMembers(eventID int64, userIDs []int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	del := `DELETE FROM event_members WHERE event_id = ?`
	if len(userIDs) > 0 {
		ids := make([]string, len(userIDs))
		for i, id := range userIDs {
			ids[i] = strconv.FormatInt(id, 10)
		}
		del += ` AND user_id NOT IN (` + strings.Join(ids, ",") + `)`
	}
	if _, err := tx.Exec(del, eventID); err != nil {
		return err
	}
	for _, id := range userIDs {
		// Skips IDs that aren't (active) users rather than failing;
		// current members are kept by the IGNORE.
		if _, err := tx.Exec(`INSERT OR IGNORE INTO event_members (event_id, user_id) SELECT ?, id FROM users WHERE id = ? AND active = 1`, eventID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Summaries ----------------------------------------------------------------

// EventSummary is an event with what the dashboard shows about it.
type EventSummary struct {
	Event
	Tasks     Tally
	Overdue   int   // open tasks past their due date
	NeedsHelp int   // tasks marked "needs help"
	Needs     Tally // gathered (or returned) out of all
}

// Tally counts how many of something are done.
type Tally struct{ Done, Total int }

func (t Tally) Percent() int {
	if t.Total == 0 {
		return 0
	}
	return t.Done * 100 / t.Total
}

// ListEvents returns the events u can see with their summaries: active
// ones first by start date, then the rest newest first.
func (s *Store) ListEvents(u *User, activeOnly bool, today string) ([]EventSummary, error) {
	cond, args := visibleTo(u)
	q := `SELECT ` + eventCols + ` FROM events e WHERE ` + cond
	if activeOnly {
		q += ` AND status NOT IN ('done', 'cancelled')`
	}
	rows, err := s.DB.Query(q+` ORDER BY starts_on, id`, args...)
	if err != nil {
		return nil, err
	}
	var out []EventSummary
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, EventSummary{Event: e})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.summarize(&out[i], today); err != nil {
			return nil, err
		}
	}
	sortSummaries(out)
	return out, nil
}

func (s *Store) Summary(e *Event, today string) (*EventSummary, error) {
	sum := &EventSummary{Event: *e}
	return sum, s.summarize(sum, today)
}

func (s *Store) summarize(sum *EventSummary, today string) error {
	return s.DB.QueryRow(`SELECT
		(SELECT COUNT(*) FROM tasks WHERE event_id = ?1 AND status = 'done'),
		(SELECT COUNT(*) FROM tasks WHERE event_id = ?1),
		(SELECT COUNT(*) FROM tasks WHERE event_id = ?1 AND status <> 'done' AND due_on <> '' AND due_on < ?2),
		(SELECT COUNT(*) FROM tasks WHERE event_id = ?1 AND status = 'blocked'),
		(SELECT COUNT(*) FROM needs WHERE event_id = ?1 AND status <> 'needed'),
		(SELECT COUNT(*) FROM needs WHERE event_id = ?1)`, sum.ID, today).
		Scan(&sum.Tasks.Done, &sum.Tasks.Total, &sum.Overdue, &sum.NeedsHelp, &sum.Needs.Done, &sum.Needs.Total)
}

// sortSummaries puts active events first: the ones not scheduled yet
// (newest first), then soonest first. Then the finished ones, newest first
// (any with no date last).
func sortSummaries(list []EventSummary) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Active() != b.Active() {
			return a.Active()
		}
		if !a.Active() {
			return a.StartsOn > b.StartsOn
		}
		if a.Scheduled() != b.Scheduled() {
			return !a.Scheduled()
		}
		if !a.Scheduled() {
			return a.ID > b.ID
		}
		return a.StartsOn < b.StartsOn
	})
}

// Unscheduled is the active events in list with no date yet, and the
// rest, each in list's order.
func Unscheduled(list []EventSummary) (undated, dated []EventSummary) {
	for _, e := range list {
		if e.Scheduled() || !e.Active() {
			dated = append(dated, e)
		} else {
			undated = append(undated, e)
		}
	}
	return undated, dated
}
