package store

import (
	"database/sql"
	"errors"
	"strings"
)

// Room is a place an event uses (an event_rooms row). PlaceName keeps the
// tracker's path so pages work when the tracker can't be reached.
type Room struct {
	ID        int64
	EventID   int64
	PlaceID   int64 // the tracker's place, 0 for a room typed in by hand
	PlaceName string
	Purpose   string
	Setup     string
	Notes     string
}

// FromTracker reports whether the room is a place in the tracker.
func (r Room) FromTracker() bool { return r.PlaceID != 0 }

// Label is the room as shown in a select: its place, and what it's for.
func (r Room) Label() string {
	if r.Purpose != "" {
		return r.PlaceName + " (" + r.Purpose + ")"
	}
	return r.PlaceName
}

// ErrRoomExists is returned when a tracker place is already one of the
// event's rooms.
var ErrRoomExists = errors.New("that room is already part of this event")

const roomCols = `id, event_id, COALESCE(place_id, 0), place_name, purpose, setup, notes`

func scanRoom(sc interface{ Scan(...any) error }) (Room, error) {
	var r Room
	err := sc.Scan(&r.ID, &r.EventID, &r.PlaceID, &r.PlaceName, &r.Purpose, &r.Setup, &r.Notes)
	return r, err
}

// ListRooms lists an event's rooms in order.
func (s *Store) ListRooms(eventID int64) ([]Room, error) {
	rows, err := s.DB.Query(`SELECT `+roomCols+` FROM event_rooms WHERE event_id = ? ORDER BY position, id`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Room
	for rows.Next() {
		r, err := scanRoom(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRoom loads one room; ErrNotFound if there isn't one.
func (s *Store) GetRoom(id int64) (*Room, error) {
	r, err := scanRoom(s.DB.QueryRow(`SELECT `+roomCols+` FROM event_rooms WHERE id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return &r, nil
}

// RoomInEvent reports whether roomID is one of the event's rooms.
func (s *Store) RoomInEvent(roomID, eventID int64) (bool, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM event_rooms WHERE id = ? AND event_id = ?`, roomID, eventID).Scan(&n)
	return n > 0, err
}

// AddRoom adds a room at the end of the event's list. A placeID of 0 is a
// room that isn't in the tracker. The same tracker place can't be added
// twice (ErrRoomExists).
func (s *Store) AddRoom(eventID, placeID int64, placeName string) (int64, error) {
	placeName = strings.TrimSpace(placeName)
	if placeName == "" {
		return 0, errors.New("a room needs a name")
	}
	if placeID != 0 {
		var n int
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM event_rooms WHERE event_id = ? AND place_id = ?`, eventID, placeID).Scan(&n); err != nil {
			return 0, err
		}
		if n > 0 {
			return 0, ErrRoomExists
		}
	}
	pos, err := s.nextPosition("event_rooms", eventID)
	if err != nil {
		return 0, err
	}
	res, err := s.DB.Exec(`INSERT INTO event_rooms (event_id, position, place_id, place_name) VALUES (?, ?, ?, ?)`,
		eventID, pos, nullInt(placeID), placeName)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateRoom saves purpose, setup and notes, and the name of a room that
// isn't in the tracker (a tracker room keeps its place's name).
func (s *Store) UpdateRoom(r *Room) error {
	_, err := s.DB.Exec(`UPDATE event_rooms SET purpose = ?, setup = ?, notes = ?,
		place_name = CASE WHEN place_id IS NULL AND ? <> '' THEN ? ELSE place_name END WHERE id = ?`,
		r.Purpose, r.Setup, r.Notes, strings.TrimSpace(r.PlaceName), strings.TrimSpace(r.PlaceName), r.ID)
	return err
}

// DeleteRoom removes a room. Tasks, needs and the rest that pointed at it
// stay, with no room.
func (s *Store) DeleteRoom(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM event_rooms WHERE id = ?`, id)
	return err
}

// What points at a room ------------------------------------------------------

// RoomUsage is what an event has pointed at one room.
type RoomUsage struct {
	Tasks    []string // "Title (status label)"
	Needs    []RoomNeed
	Schedule []string // "Sat 2 Nov 17:00 Set up"
	Helpers  []string // role names
}

// RoomNeed is a need going to a room.
type RoomNeed struct {
	Name     string
	Quantity int
}

// Empty reports whether nothing points at the room.
func (u RoomUsage) Empty() bool {
	return len(u.Tasks)+len(u.Needs)+len(u.Schedule)+len(u.Helpers) == 0
}

// RoomUsages maps each of the event's rooms to what points at it.
func (s *Store) RoomUsages(eventID int64) (map[int64]*RoomUsage, error) {
	out := map[int64]*RoomUsage{}
	get := func(id int64) *RoomUsage {
		if out[id] == nil {
			out[id] = &RoomUsage{}
		}
		return out[id]
	}
	each := func(q string, f func(sc *sql.Rows) error) error {
		rows, err := s.DB.Query(q, eventID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := f(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	err := each(`SELECT room_id, title, status FROM tasks WHERE event_id = ? AND room_id IS NOT NULL ORDER BY id`, func(r *sql.Rows) error {
		var id int64
		var title, status string
		if err := r.Scan(&id, &title, &status); err != nil {
			return err
		}
		u := get(id)
		u.Tasks = append(u.Tasks, title+" ("+TaskStatusLabel(status)+")")
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = each(`SELECT room_id, name, quantity FROM needs WHERE event_id = ? AND room_id IS NOT NULL ORDER BY id`, func(r *sql.Rows) error {
		var id int64
		var n RoomNeed
		if err := r.Scan(&id, &n.Name, &n.Quantity); err != nil {
			return err
		}
		u := get(id)
		u.Needs = append(u.Needs, n)
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = each(`SELECT room_id, date, time, label FROM schedule_items WHERE event_id = ? AND room_id IS NOT NULL ORDER BY date, time, id`, func(r *sql.Rows) error {
		var id int64
		var date, tm, label string
		if err := r.Scan(&id, &date, &tm, &label); err != nil {
			return err
		}
		u := get(id)
		u.Schedule = append(u.Schedule, strings.TrimSpace(date+" "+tm+" "+label))
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = each(`SELECT room_id, name FROM helper_roles WHERE event_id = ? AND room_id IS NOT NULL ORDER BY position, id`, func(r *sql.Rows) error {
		var id int64
		var name string
		if err := r.Scan(&id, &name); err != nil {
			return err
		}
		u := get(id)
		u.Helpers = append(u.Helpers, name)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Double-booking -------------------------------------------------------------

// EventRef is another event, as linked from a conflict warning.
type EventRef struct {
	ID    int64
	Title string
	When  string
}

// RoomConflicts maps each of the event's tracker rooms to the other live
// (not cancelled or done) events that use the same place on overlapping
// dates, counting only events viewer can see. Rooms typed in by hand never
// conflict, and neither do events not scheduled yet.
func (s *Store) RoomConflicts(eventID int64, viewer *User) (map[int64][]EventRef, error) {
	cond, args := visibleTo(viewer)
	q := `SELECT mine.id, e.id, e.title, e.kind, e.status, e.starts_on, e.start_time, e.ends_on, e.end_time, e.coordinator, COALESCE(e.expected_count, 0),
			e.description, e.notes, e.locked, COALESCE(e.copied_from, 0), e.created_at, e.updated_at
		FROM event_rooms mine
		JOIN events me ON me.id = mine.event_id
		JOIN event_rooms o ON o.place_id = mine.place_id AND o.event_id <> mine.event_id
		JOIN events e ON e.id = o.event_id
		WHERE mine.event_id = ? AND mine.place_id IS NOT NULL
		  AND e.status NOT IN ('done', 'cancelled')
		  AND e.starts_on <> '' AND me.starts_on <> ''
		  AND e.starts_on <= COALESCE(NULLIF(me.ends_on, ''), me.starts_on)
		  AND COALESCE(NULLIF(e.ends_on, ''), e.starts_on) >= me.starts_on
		  AND ` + cond + `
		ORDER BY e.starts_on, e.id`
	rows, err := s.DB.Query(q, append([]any{eventID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]EventRef{}
	for rows.Next() {
		var roomID int64
		var e Event
		err := rows.Scan(&roomID, &e.ID, &e.Title, &e.Kind, &e.Status, &e.StartsOn, &e.StartTime, &e.EndsOn, &e.EndTime, &e.Coordinator,
			&e.ExpectedCount, &e.Description, &e.Notes, &e.Locked, &e.CopiedFrom, &e.CreatedAt, &e.UpdatedAt)
		if err != nil {
			return nil, err
		}
		ref := EventRef{ID: e.ID, Title: e.Title, When: e.When()}
		dup := false
		for _, x := range out[roomID] {
			dup = dup || x.ID == ref.ID
		}
		if !dup {
			out[roomID] = append(out[roomID], ref)
		}
	}
	return out, rows.Err()
}
