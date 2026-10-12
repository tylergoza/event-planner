package store

import (
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// The calendar outbox: events waiting to be sent to the church calendar.
// Rows are queued by triggers (migration 004); the server's worker sends
// them and clears or reschedules them here.

// CalendarJob is one waiting row. Op is "put" or "delete".
type CalendarJob struct {
	EventID  int64
	Op       string
	Seq      int64
	Attempts int
}

// DueCalendarJobs is up to limit rows whose time to try has come, oldest first.
func (s *Store) DueCalendarJobs(limit int) ([]CalendarJob, error) {
	rows, err := s.DB.Query(`SELECT event_id, op, seq, attempts FROM calendar_outbox
		WHERE next_try_at <= datetime('now') ORDER BY next_try_at, seq LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CalendarJob
	for rows.Next() {
		var j CalendarJob
		if err := rows.Scan(&j.EventID, &j.Op, &j.Seq, &j.Attempts); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// CalendarSent clears a row that was sent, unless it changed meanwhile, and
// notes the time for the admin page.
func (s *Store) CalendarSent(j CalendarJob) error {
	if _, err := s.DB.Exec(`DELETE FROM calendar_outbox WHERE event_id = ? AND seq = ?`, j.EventID, j.Seq); err != nil {
		return err
	}
	return s.SetSetting("calendar_last_sent", time.Now().UTC().Format("2006-01-02 15:04:05"))
}

// CalendarFailed keeps a row for another try after retryIn, unless it
// changed meanwhile (then the new change goes straight away).
func (s *Store) CalendarFailed(j CalendarJob, msg string, retryIn time.Duration) error {
	_, err := s.DB.Exec(`UPDATE calendar_outbox SET attempts = attempts + 1, last_error = ?,
		next_try_at = datetime('now', ?) WHERE event_id = ? AND seq = ?`,
		msg, "+"+strconv.FormatInt(int64(retryIn/time.Second), 10)+" seconds", j.EventID, j.Seq)
	return err
}

// CalendarOutboxStatus is for the admin page: how many rows are waiting,
// the latest error still waiting (if any), and when something was last sent
// ("" if never).
type CalendarOutboxStatus struct {
	Waiting   int
	LastError string
	LastSent  string
}

func (s *Store) CalendarOutbox() (CalendarOutboxStatus, error) {
	var st CalendarOutboxStatus
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM calendar_outbox`).Scan(&st.Waiting); err != nil {
		return st, err
	}
	err := s.DB.QueryRow(`SELECT last_error FROM calendar_outbox WHERE last_error <> '' ORDER BY next_try_at DESC LIMIT 1`).Scan(&st.LastError)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return st, err
	}
	st.LastSent = s.Setting("calendar_last_sent", "")
	return st, nil
}

// OutboxEvent is what the worker needs to send one event: the event and
// its rooms. ErrNotFound when the event is gone (send a delete).
func (s *Store) OutboxEvent(id int64) (*Event, []Room, error) {
	e, err := s.GetEvent(id)
	if err != nil {
		return nil, nil, err
	}
	rooms, err := s.ListRooms(id)
	if err != nil {
		return nil, nil, err
	}
	return e, rooms, nil
}

// calendarFrom is the backfill's condition on events: those that end on
// or after from.
const calendarFrom = `(CASE WHEN ends_on <> '' THEN ends_on ELSE starts_on END) >= ?`

// CountCalendarBackfill is how many events QueueCalendarBackfill would queue.
func (s *Store) CountCalendarBackfill(from string) (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM events WHERE `+calendarFrom, from).Scan(&n)
	return n, err
}

// QueueCalendarBackfill queues every event ending on or after from to be
// sent. Pressing it twice still leaves one row per event.
func (s *Store) QueueCalendarBackfill(from string) (int, error) {
	res, err := s.DB.Exec(`INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
		SELECT id, 'put', (SELECT COALESCE(MAX(seq), 0) FROM calendar_outbox) + ROW_NUMBER() OVER (ORDER BY id)
		FROM events WHERE `+calendarFrom, from)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
