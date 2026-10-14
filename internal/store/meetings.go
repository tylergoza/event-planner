package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Meetings (MEETINGS_PLAN.md): an event with type "meeting" has an agenda,
// minutes and a list of the events it discussed. All of it is internal,
// like notes: none of it reaches the calendar, API or public pages.

// Agenda -------------------------------------------------------------------

// AgendaItem is one thing a meeting will cover. CarryOver asks for it to be
// copied to the next meeting by BringOver.
type AgendaItem struct {
	ID        int64
	EventID   int64
	Position  int
	Title     string
	Details   string
	CarryOver bool
}

// AgendaItems lists a meeting's agenda in order.
func (s *Store) AgendaItems(eventID int64) ([]AgendaItem, error) {
	rows, err := s.DB.Query(`SELECT id, event_id, position, title, details, carry_over FROM agenda_items WHERE event_id = ? ORDER BY position, id`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgendaItem
	for rows.Next() {
		var a AgendaItem
		if err := rows.Scan(&a.ID, &a.EventID, &a.Position, &a.Title, &a.Details, &a.CarryOver); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAgendaItem is one agenda item.
func (s *Store) GetAgendaItem(id int64) (*AgendaItem, error) {
	var a AgendaItem
	err := s.DB.QueryRow(`SELECT id, event_id, position, title, details, carry_over FROM agenda_items WHERE id = ?`, id).
		Scan(&a.ID, &a.EventID, &a.Position, &a.Title, &a.Details, &a.CarryOver)
	if err != nil {
		return nil, notFound(err)
	}
	return &a, nil
}

// AddAgendaItem appends an item to the end of an event's agenda.
func (s *Store) AddAgendaItem(eventID int64, title, details string, carryOver bool) (int64, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return 0, errors.New("give the agenda item a title")
	}
	pos, err := s.nextPosition("agenda_items", eventID)
	if err != nil {
		return 0, err
	}
	res, err := s.DB.Exec(`INSERT INTO agenda_items (event_id, position, title, details, carry_over) VALUES (?, ?, ?, ?, ?)`,
		eventID, pos, title, strings.TrimSpace(details), carryOver)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateAgendaItem changes an item's title, details and carry-over mark.
func (s *Store) UpdateAgendaItem(id int64, title, details string, carryOver bool) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("give the agenda item a title")
	}
	res, err := s.DB.Exec(`UPDATE agenda_items SET title = ?, details = ?, carry_over = ? WHERE id = ?`,
		title, strings.TrimSpace(details), carryOver, id)
	return affected(res, err)
}

// DeleteAgendaItem removes an item. Move reorders (table "agenda_items").
func (s *Store) DeleteAgendaItem(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM agenda_items WHERE id = ?`, id)
	return err
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Minutes ------------------------------------------------------------------

// SetMinutes saves a meeting's minutes.
func (s *Store) SetMinutes(eventID int64, text string) error {
	res, err := s.DB.Exec(`UPDATE events SET minutes = ? WHERE id = ?`, strings.TrimSpace(text), eventID)
	return affected(res, err)
}

// Events discussed ---------------------------------------------------------

// MeetingEvent is an event a meeting discussed, with what the meeting page
// shows about it. When the viewer can't see the event (it's locked),
// Visible is false and everything but Position is blank: show it as
// "Private event" with no title or link.
type MeetingEvent struct {
	Position      int
	Visible       bool
	EventID       int64
	Title         string
	StartsOn      string
	DateTentative bool
	Status        string
	Coordinator   string
	Locked        bool
	OpenTasks     int
	Note          string
}

// Scheduled reports whether the event has a date yet.
func (m MeetingEvent) Scheduled() bool { return m.StartsOn != "" }

// MeetingEvents lists the events a meeting discussed, in order, as u sees
// them.
func (s *Store) MeetingEvents(meetingID int64, u *User) ([]MeetingEvent, error) {
	rows, err := s.DB.Query(`SELECT me.position, e.id, e.title, e.starts_on, e.date_tentative, e.status, e.coordinator, e.locked,
			(SELECT COUNT(*) FROM tasks t WHERE t.event_id = e.id AND t.status <> 'done'), me.note,
			(NOT e.locked OR EXISTS (SELECT 1 FROM event_members m WHERE m.event_id = e.id AND m.user_id = ?))
		FROM meeting_events me JOIN events e ON e.id = me.event_id
		WHERE me.meeting_id = ? ORDER BY me.position, e.id`, userID(u), meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MeetingEvent
	for rows.Next() {
		var m MeetingEvent
		if err := rows.Scan(&m.Position, &m.EventID, &m.Title, &m.StartsOn, &m.DateTentative, &m.Status, &m.Coordinator, &m.Locked,
			&m.OpenTasks, &m.Note, &m.Visible); err != nil {
			return nil, err
		}
		if u != nil && u.IsAdmin() {
			m.Visible = true
		}
		if !m.Visible {
			m = MeetingEvent{Position: m.Position}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func userID(u *User) int64 {
	if u == nil {
		return 0
	}
	return u.ID
}

// LinkEvent adds an event to the list a meeting discussed, at the end. It
// does nothing if already linked. A meeting can't link itself or another
// meeting.
func (s *Store) LinkEvent(meetingID, eventID int64) error {
	if meetingID == eventID {
		return errors.New("a meeting can't discuss itself")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireType(tx, meetingID, "meeting"); err != nil {
		return err
	}
	if err := requireType(tx, eventID, "event"); err != nil {
		return err
	}
	if err := linkEvent(tx, meetingID, eventID, ""); err != nil {
		return err
	}
	return tx.Commit()
}

// requireType checks an event exists and has the type.
func requireType(tx *sql.Tx, id int64, typ string) error {
	var got string
	if err := tx.QueryRow(`SELECT type FROM events WHERE id = ?`, id).Scan(&got); err != nil {
		return notFound(err)
	}
	if got != typ {
		if typ == "meeting" {
			return errors.New("that isn't a meeting")
		}
		return errors.New("a meeting can't discuss another meeting")
	}
	return nil
}

// linkEvent appends a link unless there is one already.
func linkEvent(tx *sql.Tx, meetingID, eventID int64, note string) error {
	var pos int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(position), 0) + 1 FROM meeting_events WHERE meeting_id = ?`, meetingID).Scan(&pos); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT OR IGNORE INTO meeting_events (meeting_id, event_id, position, note) VALUES (?, ?, ?, ?)`, meetingID, eventID, pos, note)
	return err
}

// UnlinkEvent removes an event from a meeting's list; the event stays.
func (s *Store) UnlinkEvent(meetingID, eventID int64) error {
	_, err := s.DB.Exec(`DELETE FROM meeting_events WHERE meeting_id = ? AND event_id = ?`, meetingID, eventID)
	return err
}

// SetDiscussionNote saves what a meeting said about an event it discussed.
func (s *Store) SetDiscussionNote(meetingID, eventID int64, note string) error {
	res, err := s.DB.Exec(`UPDATE meeting_events SET note = ? WHERE meeting_id = ? AND event_id = ?`, strings.TrimSpace(note), meetingID, eventID)
	return affected(res, err)
}

// MoveMeetingEvent swaps a linked event with its neighbour above (up) or
// below in the meeting's list. At either end it does nothing.
func (s *Store) MoveMeetingEvent(meetingID, eventID int64, up bool) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var pos int
	if err := tx.QueryRow(`SELECT position FROM meeting_events WHERE meeting_id = ? AND event_id = ?`, meetingID, eventID).Scan(&pos); err != nil {
		return notFound(err)
	}
	q := `SELECT event_id, position FROM meeting_events WHERE meeting_id = ? AND position > ? ORDER BY position LIMIT 1`
	if up {
		q = `SELECT event_id, position FROM meeting_events WHERE meeting_id = ? AND position < ? ORDER BY position DESC LIMIT 1`
	}
	var otherID int64
	var otherPos int
	if err := tx.QueryRow(q, meetingID, pos).Scan(&otherID, &otherPos); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE meeting_events SET position = ? WHERE meeting_id = ? AND event_id = ?`, otherPos, meetingID, eventID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE meeting_events SET position = ? WHERE meeting_id = ? AND event_id = ?`, pos, meetingID, otherID); err != nil {
		return err
	}
	return tx.Commit()
}

// DiscussedAt is a meeting that discussed an event.
type DiscussedAt struct {
	MeetingID int64
	Title     string
	StartsOn  string
	Note      string
}

// DiscussedAtList lists the meetings that discussed an event, newest
// first (not scheduled ones first), leaving out locked meetings u can't see.
func (s *Store) DiscussedAtList(eventID int64, u *User) ([]DiscussedAt, error) {
	cond, args := visibleTo(u)
	rows, err := s.DB.Query(`SELECT e.id, e.title, e.starts_on, me.note
		FROM meeting_events me JOIN events e ON e.id = me.meeting_id
		WHERE me.event_id = ? AND `+cond+` ORDER BY (e.starts_on = '') DESC, e.starts_on DESC, e.id DESC`,
		append([]any{eventID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiscussedAt
	for rows.Next() {
		var d DiscussedAt
		if err := rows.Scan(&d.MeetingID, &d.Title, &d.StartsOn, &d.Note); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PickLimit is how many candidates PickEvents returns at most.
const PickLimit = 50

// PickEvents lists the events a meeting could add: not meetings, not
// already linked, visible to u, whose title contains search (if any). The
// ones not scheduled yet and the ones still planning come first, then
// newest first.
func (s *Store) PickEvents(meetingID int64, u *User, search string) ([]Event, error) {
	cond, args := visibleTo(u)
	q := `SELECT ` + eventCols + ` FROM events e WHERE type = 'event' AND ` + cond + `
		AND NOT EXISTS (SELECT 1 FROM meeting_events me WHERE me.meeting_id = ? AND me.event_id = e.id)`
	args = append(args, meetingID)
	if search = strings.TrimSpace(search); search != "" {
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(search)
		q += ` AND title LIKE ? ESCAPE '\'`
		args = append(args, "%"+esc+"%")
	}
	q += ` ORDER BY (starts_on <> '' AND status <> 'planning'), (starts_on = '') DESC, starts_on DESC, id DESC LIMIT ?`
	args = append(args, PickLimit)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// NewIdea records an idea that came up at a meeting: a planning event with
// no date yet, the meeting's lock, visibility and members, linked to the
// meeting. Returns the new event's ID.
func (s *Store) NewIdea(meetingID int64, title string) (int64, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return 0, errors.New("give the idea a title")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := requireType(tx, meetingID, "meeting"); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`INSERT INTO events (title, status, starts_on, locked, visibility)
		SELECT ?, 'planning', '', locked, visibility FROM events WHERE id = ?`, title, meetingID)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO event_members (event_id, user_id) SELECT ?, user_id FROM event_members WHERE event_id = ?`, id, meetingID); err != nil {
		return 0, err
	}
	if err := linkEvent(tx, meetingID, id, ""); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// Carrying things over -----------------------------------------------------

// PreviousMeeting is the meeting before this one: the nearest earlier
// meeting in the same series, else the latest earlier scheduled meeting
// with the same (non-empty) kind. Nil when there isn't one, or when this
// meeting has no date. Meetings u can't see (locked) are skipped.
func (s *Store) PreviousMeeting(meetingID int64, u *User) (*Event, error) {
	return previousMeeting(s.DB, meetingID, u)
}

func previousMeeting(q querier, meetingID int64, u *User) (*Event, error) {
	m, err := scanEvent(q.QueryRow(`SELECT `+eventCols+` FROM events WHERE id = ?`, meetingID))
	if err != nil {
		return nil, notFound(err)
	}
	if !m.IsMeeting() || !m.Scheduled() {
		return nil, nil
	}
	cond, vargs := visibleTo(u)
	find := func(where string, args ...any) (*Event, error) {
		all := append([]any{m.StartsOn, m.ID}, args...)
		all = append(all, vargs...)
		e, err := scanEvent(q.QueryRow(`SELECT `+eventCols+` FROM events e WHERE type = 'meeting' AND starts_on <> '' AND starts_on < ? AND id <> ? AND `+where+
			` AND `+cond+` ORDER BY starts_on DESC, start_time DESC, id DESC LIMIT 1`, all...))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return &e, nil
	}
	if m.SeriesID != 0 {
		if e, err := find(`series_id = ?`, m.SeriesID); e != nil || err != nil {
			return e, err
		}
	}
	if m.Kind == "" {
		return nil, nil
	}
	return find(`kind = ?`, m.Kind)
}

// BroughtOver says what BringOver did.
type BroughtOver struct {
	Found  bool // there was a previous meeting
	Events int  // events linked
	Agenda int  // agenda items copied
}

const lastTime = "Last time: "

// BringOver copies from the previous meeting into this one: the events it
// discussed that are still open (not done or cancelled) and not already
// linked, with the old note as "Last time: ..." when there was one, and the
// agenda items marked carry over (unless this meeting has an item with the
// same title), still marked.
func (s *Store) BringOver(meetingID int64, u *User) (BroughtOver, error) {
	var out BroughtOver
	tx, err := s.DB.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	prev, err := previousMeeting(tx, meetingID, u)
	if err != nil || prev == nil {
		return out, err
	}
	out.Found = true

	type link struct {
		id   int64
		note string
	}
	cond, vargs := visibleTo(u)
	rows, err := tx.Query(`SELECT me.event_id, me.note FROM meeting_events me JOIN events e ON e.id = me.event_id
		WHERE me.meeting_id = ? AND e.status NOT IN ('done', 'cancelled') AND `+cond+`
		AND NOT EXISTS (SELECT 1 FROM meeting_events x WHERE x.meeting_id = ? AND x.event_id = me.event_id)
		ORDER BY me.position, me.event_id`, append([]any{prev.ID}, append(vargs, meetingID)...)...)
	if err != nil {
		return out, err
	}
	var links []link
	for rows.Next() {
		var l link
		if err := rows.Scan(&l.id, &l.note); err != nil {
			rows.Close()
			return out, err
		}
		links = append(links, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	for _, l := range links {
		note := ""
		if old := strings.TrimSpace(strings.TrimPrefix(l.note, lastTime)); old != "" {
			note = lastTime + old
		}
		if err := linkEvent(tx, meetingID, l.id, note); err != nil {
			return out, err
		}
		out.Events++
	}

	res, err := tx.Exec(`INSERT INTO agenda_items (event_id, position, title, details, carry_over)
		SELECT ?, (SELECT COALESCE(MAX(position), 0) FROM agenda_items WHERE event_id = ?) + ROW_NUMBER() OVER (ORDER BY a.position, a.id),
			a.title, a.details, 1
		FROM agenda_items a WHERE a.event_id = ? AND a.carry_over = 1
		AND NOT EXISTS (SELECT 1 FROM agenda_items x WHERE x.event_id = ? AND x.title = a.title)`, meetingID, meetingID, prev.ID, meetingID)
	if err != nil {
		return out, err
	}
	n, _ := res.RowsAffected()
	out.Agenda = int(n)
	return out, tx.Commit()
}

// Closing out --------------------------------------------------------------

// endsAt is when the event ends in local time: its last day and end time,
// or the end of that day when it has no end time. False when it isn't
// scheduled.
func (e Event) endsAt() (time.Time, bool) {
	if !e.Scheduled() {
		return time.Time{}, false
	}
	day, err := time.ParseInLocation(DateLayout, e.LastDate(), time.Local)
	if err != nil {
		return time.Time{}, false
	}
	if e.EndTime != "" {
		if t, err := time.ParseInLocation(DateLayout+" 15:04", e.LastDate()+" "+e.EndTime, time.Local); err == nil {
			return t, true
		}
	}
	return day.AddDate(0, 0, 1), true
}

// NeedsClosingOut reports whether the event's end has passed (now is at or
// after it) while it is still planning or ready. Events not scheduled yet
// never do.
func (e Event) NeedsClosingOut(now time.Time) bool {
	if e.Status != "planning" && e.Status != "ready" {
		return false
	}
	end, ok := e.endsAt()
	return ok && !now.Before(end)
}

// NeedsClosingOut lists every event, meetings too, that has ended but is
// still planning or ready, oldest first.
func (s *Store) NeedsClosingOut(now time.Time) ([]Event, error) {
	return s.needsClosingOut(`1`, nil, now)
}

// NeedsClosingOutFor is NeedsClosingOut limited to the events u can see.
func (s *Store) NeedsClosingOutFor(u *User, now time.Time) ([]Event, error) {
	cond, args := visibleTo(u)
	return s.needsClosingOut(cond, args, now)
}

func (s *Store) needsClosingOut(cond string, args []any, now time.Time) ([]Event, error) {
	// A day's margin keeps the SQL filter loose; the exact end is checked below.
	rows, err := s.DB.Query(`SELECT `+eventCols+` FROM events e WHERE status IN ('planning', 'ready') AND starts_on <> ''
		AND COALESCE(NULLIF(ends_on, ''), starts_on) <= ? AND `+cond+` ORDER BY starts_on, id`,
		append([]any{now.AddDate(0, 0, 1).Format(DateLayout)}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		if e.NeedsClosingOut(now) {
			out = append(out, e)
		}
	}
	return out, rows.Err()
}
