package store

import (
	"strings"
	"time"
)

// ScheduleItem is one line of an event's timeline: set-up, doors open,
// the meal, clean-up.
type ScheduleItem struct {
	ID       int64
	EventID  int64
	Date     string
	Time     string
	EndTime  string
	Label    string
	RoomID   int64 // 0: no particular room
	RoomName string
	Notes    string
}

// When is the date and time for display, e.g. "Sat, Nov 1, 2026 · 4:00 PM – 5:00 PM".
func (i ScheduleItem) When() string {
	d, err := time.Parse(DateLayout, i.Date)
	if err != nil {
		return i.Date
	}
	out := d.Format("Mon, Jan 2, 2006")
	if i.Time != "" {
		out += " · " + fmtClock(i.Time)
		if i.EndTime != "" {
			out += " – " + fmtClock(i.EndTime)
		}
	}
	return out
}

const scheduleCols = `i.id, i.event_id, i.date, i.time, i.end_time, i.label, COALESCE(i.room_id, 0), COALESCE(r.place_name, ''), i.notes`

const scheduleFrom = ` FROM schedule_items i LEFT JOIN event_rooms r ON r.id = i.room_id`

func scanSchedule(sc interface{ Scan(...any) error }) (ScheduleItem, error) {
	var i ScheduleItem
	err := sc.Scan(&i.ID, &i.EventID, &i.Date, &i.Time, &i.EndTime, &i.Label, &i.RoomID, &i.RoomName, &i.Notes)
	return i, err
}

func (s *Store) ListSchedule(eventID int64) ([]ScheduleItem, error) {
	rows, err := s.DB.Query(`SELECT `+scheduleCols+scheduleFrom+` WHERE i.event_id = ? ORDER BY i.date, i.time, i.id`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduleItem
	for rows.Next() {
		i, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *Store) GetScheduleItem(id int64) (*ScheduleItem, error) {
	i, err := scanSchedule(s.DB.QueryRow(`SELECT `+scheduleCols+scheduleFrom+` WHERE i.id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return &i, nil
}

func (s *Store) CreateScheduleItem(i *ScheduleItem) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO schedule_items (event_id, date, time, end_time, label, room_id, notes) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		i.EventID, i.Date, i.Time, i.EndTime, strings.TrimSpace(i.Label), nullInt(i.RoomID), i.Notes)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateScheduleItem(i *ScheduleItem) error {
	_, err := s.DB.Exec(`UPDATE schedule_items SET date = ?, time = ?, end_time = ?, label = ?, room_id = ?, notes = ? WHERE id = ?`,
		i.Date, i.Time, i.EndTime, strings.TrimSpace(i.Label), nullInt(i.RoomID), i.Notes, i.ID)
	return err
}

func (s *Store) DeleteScheduleItem(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM schedule_items WHERE id = ?`, id)
	return err
}
