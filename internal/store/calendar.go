package store

import (
	"strings"
)

// CalendarAccess says how much of the calendar a reader may see. It is
// built with CalendarPublic, CalendarPrivate or CalendarSignedIn.
type CalendarAccess struct {
	private bool
	user    *User
}

var (
	// CalendarPublic is what an anonymous visitor, or an API token without
	// private access, sees: public events in full, everything else as busy.
	CalendarPublic = CalendarAccess{}
	// CalendarPrivate is a token allowed private events: every unlocked
	// event in full. Locked events are still only busy.
	CalendarPrivate = CalendarAccess{private: true}
)

// CalendarSignedIn is a signed-in user: full details of every event
// visibleTo lets them see (so locked events they aren't on are busy).
func CalendarSignedIn(u *User) CalendarAccess { return CalendarAccess{user: u} }

// CalendarEntry is one event on the calendar. When Busy is true the reader
// may not see the event itself, only that its rooms are taken at that time:
// EventID is 0, and Title, Kind, Status and PublicDescription are empty.
// Notes, tasks, helpers, needs, coordinator, headcount and members are never
// part of an entry.
type CalendarEntry struct {
	EventID           int64
	Busy              bool
	Title             string
	Kind              string
	Status            string
	StartsOn          string // YYYY-MM-DD
	StartTime         string // HH:MM, "" for all day
	EndsOn            string // "" for a one-day event
	EndTime           string
	Rooms             []string // room names, in the event's order
	PublicDescription string   // only on full entries
	Public            bool     // the event's visibility is public (full entries only)
}

// LastDate is the day the entry ends.
func (c CalendarEntry) LastDate() string {
	if c.EndsOn != "" {
		return c.EndsOn
	}
	return c.StartsOn
}

const calendarSelect = `
	SELECT e.id, e.title, e.kind, e.status, e.starts_on, e.start_time, e.ends_on, e.end_time,
	       e.visibility, e.public_description, e.locked, %s
	FROM events e
	WHERE e.status <> 'cancelled' AND %s
	ORDER BY e.starts_on, e.start_time, e.id`

// Calendar lists the non-cancelled events that fall on any day from..to
// (YYYY-MM-DD, both inclusive), ordered by date then time. A multi-day event
// is included if any of its days is in the range. What each entry shows
// depends on access:
//
//   - full details when the event is public and not locked; or access is
//     CalendarPrivate and the event isn't locked; or access is
//     CalendarSignedIn and the user can see the event;
//   - otherwise a busy entry: rooms and times only. A busy entry may have no
//     rooms; the caller shows it as "Private event".
func (s *Store) Calendar(from, to string, access CalendarAccess) ([]CalendarEntry, error) {
	return s.calendar(`e.starts_on <= ? AND COALESCE(NULLIF(e.ends_on, ''), e.starts_on) >= ?`, []any{to, from}, access)
}

// CalendarEvent returns one event the access may see in full, or
// ErrNotFound if it doesn't exist, is cancelled, or would only show as busy.
func (s *Store) CalendarEvent(id int64, access CalendarAccess) (*CalendarEntry, error) {
	list, err := s.calendar(`e.id = ?`, []any{id}, access)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 || list[0].Busy {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

func (s *Store) calendar(where string, args []any, access CalendarAccess) ([]CalendarEntry, error) {
	// seen is 1 when a signed-in user may see the event; for other access
	// levels it is not used.
	seenCond, seenArgs := "0", []any(nil)
	if access.user != nil {
		seenCond, seenArgs = visibleTo(access.user)
	}
	q := strings.Replace(calendarSelect, "%s", "CASE WHEN "+seenCond+" THEN 1 ELSE 0 END", 1)
	q = strings.Replace(q, "%s", where, 1)
	rows, err := s.DB.Query(q, append(seenArgs, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CalendarEntry
	var ids []any
	for rows.Next() {
		var (
			c                       CalendarEntry
			id                      int64
			visibility, description string
			locked, seen            bool
			title, kind, status     string
		)
		if err := rows.Scan(&id, &title, &kind, &status, &c.StartsOn, &c.StartTime, &c.EndsOn, &c.EndTime,
			&visibility, &description, &locked, &seen); err != nil {
			return nil, err
		}
		full := false
		switch {
		case access.user != nil:
			full = seen
		case access.private:
			full = !locked
		default:
			full = visibility == "public" && !locked
		}
		if full {
			c.EventID, c.Title, c.Kind, c.Status = id, title, kind, status
			c.PublicDescription = description
			c.Public = visibility == "public"
		} else {
			c.Busy = true
		}
		out = append(out, c)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(ids) == 0 {
		return out, nil
	}

	rooms := map[int64][]string{}
	rr, err := s.DB.Query(`SELECT event_id, place_name FROM event_rooms WHERE event_id IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`) ORDER BY event_id, position, id`, ids...)
	if err != nil {
		return nil, err
	}
	defer rr.Close()
	for rr.Next() {
		var id int64
		var name string
		if err := rr.Scan(&id, &name); err != nil {
			return nil, err
		}
		rooms[id] = append(rooms[id], name)
	}
	if err := rr.Err(); err != nil {
		return nil, err
	}
	// out and ids are in the same order.
	for i := range out {
		out[i].Rooms = rooms[ids[i].(int64)]
	}
	return out, nil
}
