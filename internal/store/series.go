package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tylergoza/event-planner/internal/recur"
)

// SeriesHorizonMonths is how far ahead of "today" occurrences are created.
const SeriesHorizonMonths = 6

var (
	// ErrNotInSeries is returned when an event isn't an occurrence of a series.
	ErrNotInSeries = errors.New("that event isn't part of a repeating series")
	// ErrAlreadyInSeries is returned when making a series from an occurrence.
	ErrAlreadyInSeries = errors.New("that event already repeats")
	// ErrNoTemplate is returned when a series has no event left to copy.
	ErrNoTemplate = errors.New("the series has no event left to copy from")
)

// Sections of an occurrence that "make later ones like this" can copy.
const (
	SectionRooms      = "rooms"
	SectionSchedule   = "schedule"
	SectionTasks      = "tasks" // task lists and tasks
	SectionHelperJobs = "helper_jobs"
	SectionNeeds      = "needs"
)

// Series is a repeating event's rule. Each occurrence is a real event.
type Series struct {
	ID               int64
	Rule             recur.Rule // Rule.Start is the series' starts_on
	TemplateEventID  int64      // the event new occurrences copy; 0 when it was deleted (the latest occurrence is used)
	GeneratedThrough string     // occurrences were created up to this date
}

// querier is what both *sql.DB and *sql.Tx offer.
type querier interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
	QueryRow(q string, args ...any) *sql.Row
}

const seriesCols = `id, freq, every, weekdays, month_day, month_week, month_weekday, starts_on, until, max_count, template_event_id, generated_through`

func loadSeries(q querier, id int64) (*Series, error) {
	var (
		sr                                Series
		freq, weekdays, starts, until     string
		every                             int
		mDay, mWeek, mWd, maxCount, tplID sql.NullInt64
	)
	err := q.QueryRow(`SELECT `+seriesCols+` FROM series WHERE id = ?`, id).Scan(
		&sr.ID, &freq, &every, &weekdays, &mDay, &mWeek, &mWd, &starts, &until, &maxCount, &tplID, &sr.GeneratedThrough)
	if err != nil {
		return nil, notFound(err)
	}
	r := recur.Rule{Freq: recur.Freq(freq), Every: every, MonthDay: int(mDay.Int64), MonthWeek: int(mWeek.Int64),
		MonthWeekday: time.Weekday(mWd.Int64), Count: int(maxCount.Int64)}
	for _, p := range strings.Split(weekdays, ",") {
		if p = strings.TrimSpace(p); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil {
				return nil, fmt.Errorf("bad weekdays %q", weekdays)
			}
			r.Weekdays = append(r.Weekdays, time.Weekday(n))
		}
	}
	if r.Start, err = recur.ParseDate(starts); err != nil {
		return nil, fmt.Errorf("bad series start %q", starts)
	}
	if until != "" {
		if r.Until, err = recur.ParseDate(until); err != nil {
			return nil, fmt.Errorf("bad series end %q", until)
		}
	}
	sr.Rule = r
	sr.TemplateEventID = tplID.Int64
	return &sr, nil
}

// GetSeries loads a series with its rule.
func (s *Store) GetSeries(id int64) (*Series, error) { return loadSeries(s.DB, id) }

func insertSeries(q querier, r recur.Rule) (int64, error) {
	wds := make([]int, len(r.Weekdays))
	for i, w := range r.Weekdays {
		wds[i] = int(w)
	}
	sort.Ints(wds)
	parts := make([]string, len(wds))
	for i, w := range wds {
		parts[i] = strconv.Itoa(w)
	}
	var mDay, mWeek, mWd, maxCount, until any
	if r.Freq == recur.Monthly {
		if r.MonthDay != 0 {
			mDay = r.MonthDay
		} else {
			mWeek, mWd = r.MonthWeek, int(r.MonthWeekday)
		}
	}
	if r.Count > 0 {
		maxCount = r.Count
	}
	until = ""
	if !r.Until.IsZero() {
		until = recur.FormatDate(r.Until)
	}
	res, err := q.Exec(`INSERT INTO series (freq, every, weekdays, month_day, month_week, month_weekday, starts_on, until, max_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(r.Freq), r.Every, strings.Join(parts, ","), mDay, mWeek, mWd, recur.FormatDate(r.Start), until, maxCount)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func addDays(d string, n int) string { return shiftDate(d, n) }

// horizon is the last date occurrences are created for, given today.
func horizon(today string) (string, error) {
	t, err := time.Parse(DateLayout, today)
	if err != nil {
		return "", errors.New("today isn't a date")
	}
	return t.AddDate(0, SeriesHorizonMonths, 0).Format(DateLayout), nil
}

// templateOf is the event to copy for a series: its template, else the
// latest remaining occurrence; 0 when none is left.
func templateOf(q querier, sr *Series) (int64, error) {
	if sr.TemplateEventID != 0 {
		return sr.TemplateEventID, nil
	}
	var id int64
	err := q.QueryRow(`SELECT id FROM events WHERE series_id = ? ORDER BY occurrence_date DESC LIMIT 1`, sr.ID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// makeOccurrence copies the template to date as an occurrence of the series.
func makeOccurrence(tx *sql.Tx, seriesID, tpl int64, date string) (int64, error) {
	id, err := copyEventTx(tx, tpl, CopyOptions{StartsOn: date, KeepAssignees: true})
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(`UPDATE events SET series_id = ?, occurrence_date = ?, copied_from = NULL WHERE id = ?`, seriesID, date, id)
	return id, err
}

func stringSet(q querier, query string, args ...any) (map[string]bool, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out[d] = true
	}
	return out, rows.Err()
}

// generate creates the occurrences the rule gives from the later of floor and
// the day after generated_through, up to the horizon. It skips dates that
// already have an occurrence (moved ones count, by occurrence date) and
// skipped dates. With no template and no occurrence left it does nothing.
func generate(tx *sql.Tx, seriesID int64, today, floor string) (int, error) {
	sr, err := loadSeries(tx, seriesID)
	if err != nil {
		return 0, err
	}
	hz, err := horizon(today)
	if err != nil {
		return 0, err
	}
	tpl, err := templateOf(tx, sr)
	if err != nil || tpl == 0 {
		return 0, err
	}
	have, err := stringSet(tx, `SELECT occurrence_date FROM events WHERE series_id = ?`, seriesID)
	if err != nil {
		return 0, err
	}
	skipped, err := stringSet(tx, `SELECT date FROM series_skips WHERE series_id = ?`, seriesID)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, d := range sr.Rule.Between(sr.Rule.Start, mustDate(hz)) {
		ds := recur.FormatDate(d)
		if ds < floor || ds <= sr.GeneratedThrough || have[ds] || skipped[ds] {
			continue
		}
		if _, err := makeOccurrence(tx, seriesID, tpl, ds); err != nil {
			return created, err
		}
		created++
	}
	if hz > sr.GeneratedThrough {
		if _, err := tx.Exec(`UPDATE series SET generated_through = ? WHERE id = ?`, hz, seriesID); err != nil {
			return created, err
		}
	}
	return created, nil
}

func mustDate(s string) time.Time {
	t, _ := time.Parse(DateLayout, s)
	return t
}

// MakeSeries turns a one-off event into a repeating series. The event
// becomes the first occurrence and the template, whatever its date, even if
// the rule wouldn't fall on it; r.Start is replaced by the event's starts_on
// (a Count counts the rule's dates from there). Occurrences from today on
// are then created up to the horizon.
func (s *Store) MakeSeries(eventID int64, r recur.Rule, today string) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ev, err := scanEvent(tx.QueryRow(`SELECT `+eventCols+` FROM events WHERE id = ?`, eventID))
	if err != nil {
		return 0, notFound(err)
	}
	if ev.SeriesID != 0 {
		return 0, ErrAlreadyInSeries
	}
	if r.Start, err = recur.ParseDate(ev.StartsOn); err != nil {
		return 0, errors.New("the event's start date isn't a date")
	}
	if err := r.Validate(); err != nil {
		return 0, err
	}
	id, err := insertSeries(tx, r)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE events SET series_id = ?, occurrence_date = starts_on WHERE id = ?`, id, eventID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE series SET template_event_id = ? WHERE id = ?`, eventID, id); err != nil {
		return 0, err
	}
	if _, err := generate(tx, id, today, today); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// ExtendSeries tops up every series to the horizon from today, returning
// how many occurrences it created. Safe to run as often as you like.
func (s *Store) ExtendSeries(today string) (int, error) {
	rows, err := s.DB.Query(`SELECT id FROM series ORDER BY id`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	total := 0
	for _, id := range ids {
		n, err := s.ExtendOneSeries(id, today)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// ExtendOneSeries tops up one series. Dates before today aren't created.
func (s *Store) ExtendOneSeries(id int64, today string) (int, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n, err := generate(tx, id, today, today)
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// Occurrences lists a series' events ordered by occurrence date.
func (s *Store) Occurrences(seriesID int64) ([]Event, error) {
	rows, err := s.DB.Query(`SELECT `+eventCols+` FROM events WHERE series_id = ? ORDER BY occurrence_date`, seriesID)
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

// SkippedDates lists the dates deleted from a series, ascending.
func (s *Store) SkippedDates(seriesID int64) ([]string, error) {
	rows, err := s.DB.Query(`SELECT date FROM series_skips WHERE series_id = ? ORDER BY date`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SkipOccurrence deletes one occurrence and remembers its date so the series
// doesn't bring it back; no other occurrence changes. If it was the template,
// the next later occurrence becomes it (else the latest earlier one). An
// event that isn't in a series is simply deleted.
func (s *Store) SkipOccurrence(eventID int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var seriesID sql.NullInt64
	var date string
	if err := tx.QueryRow(`SELECT series_id, occurrence_date FROM events WHERE id = ?`, eventID).Scan(&seriesID, &date); err != nil {
		return notFound(err)
	}
	if seriesID.Valid {
		var tpl sql.NullInt64
		if err := tx.QueryRow(`SELECT template_event_id FROM series WHERE id = ?`, seriesID.Int64).Scan(&tpl); err != nil {
			return err
		}
		if tpl.Valid && tpl.Int64 == eventID {
			var next sql.NullInt64
			err := tx.QueryRow(`SELECT COALESCE(
				(SELECT id FROM events WHERE series_id = ?1 AND occurrence_date > ?2 ORDER BY occurrence_date LIMIT 1),
				(SELECT id FROM events WHERE series_id = ?1 AND occurrence_date < ?2 ORDER BY occurrence_date DESC LIMIT 1))`,
				seriesID.Int64, date).Scan(&next)
			if err != nil {
				return err
			}
			var v any
			if next.Valid {
				v = next.Int64
			}
			if _, err := tx.Exec(`UPDATE series SET template_event_id = ? WHERE id = ?`, v, seriesID.Int64); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO series_skips (series_id, date) VALUES (?, ?)`, seriesID.Int64, date); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM events WHERE id = ?`, eventID); err != nil {
		return err
	}
	return tx.Commit()
}

// RestoreSkip brings back a skipped date: it stops being skipped and an
// occurrence is created for it from the template, even beyond the horizon.
// The date must be one the rule gives. (today is unused: nothing about
// restoring depends on it.)
func (s *Store) RestoreSkip(seriesID int64, date string, today string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	sr, err := loadSeries(tx, seriesID)
	if err != nil {
		return err
	}
	d, err := recur.ParseDate(date)
	if err != nil {
		return errors.New("that isn't a date")
	}
	if len(sr.Rule.Between(d, d)) == 0 {
		return errors.New("that date isn't in the repeat pattern")
	}
	tpl, err := templateOf(tx, sr)
	if err != nil {
		return err
	}
	if tpl == 0 {
		return ErrNoTemplate
	}
	if _, err := tx.Exec(`DELETE FROM series_skips WHERE series_id = ? AND date = ?`, seriesID, date); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM events WHERE series_id = ? AND occurrence_date = ?`, seriesID, date).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := makeOccurrence(tx, seriesID, tpl, date); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// OccurrenceImpact is what would be lost by deleting or overwriting an
// occurrence, for warning the user first.
type OccurrenceImpact struct {
	EventID         int64
	Title           string
	StartsOn        string
	OccurrenceDate  string
	TasksDone       int // tasks marked done
	TasksInProgress int // tasks in progress or blocked
	Helpers         int // people signed up for jobs
	NeedsGathered   int // needs gathered or returned
}

// HasWork reports whether anything would be lost.
func (i OccurrenceImpact) HasWork() bool {
	return i.TasksDone+i.TasksInProgress+i.Helpers+i.NeedsGathered > 0
}

// impactOf counts the work on an event; the flags choose what is counted.
func impactOf(q querier, id int64, tasks, helpers, needs bool) (OccurrenceImpact, error) {
	var im OccurrenceImpact
	err := q.QueryRow(`SELECT id, title, starts_on, occurrence_date FROM events WHERE id = ?`, id).Scan(&im.EventID, &im.Title, &im.StartsOn, &im.OccurrenceDate)
	if err != nil {
		return im, notFound(err)
	}
	if tasks {
		err = q.QueryRow(`SELECT COALESCE(SUM(status = 'done'), 0), COALESCE(SUM(status IN ('in_progress', 'blocked')), 0) FROM tasks WHERE event_id = ?`, id).
			Scan(&im.TasksDone, &im.TasksInProgress)
		if err != nil {
			return im, err
		}
	}
	if helpers {
		err = q.QueryRow(`SELECT COUNT(*) FROM helpers h JOIN helper_roles r ON r.id = h.role_id WHERE r.event_id = ?`, id).Scan(&im.Helpers)
		if err != nil {
			return im, err
		}
	}
	if needs {
		err = q.QueryRow(`SELECT COUNT(*) FROM needs WHERE event_id = ? AND status <> 'needed'`, id).Scan(&im.NeedsGathered)
	}
	return im, err
}

func eventIDs(q querier, query string, args ...any) ([]int64, error) {
	rows, err := q.Query(query, args...)
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

// EndSeriesPreview lists the occurrences EndSeriesFrom(seriesID, from) would
// delete (occurrence_date >= from) with the work on each.
func (s *Store) EndSeriesPreview(seriesID int64, from string) ([]OccurrenceImpact, error) {
	ids, err := eventIDs(s.DB, `SELECT id FROM events WHERE series_id = ? AND occurrence_date >= ? ORDER BY occurrence_date`, seriesID, from)
	if err != nil {
		return nil, err
	}
	var out []OccurrenceImpact
	for _, id := range ids {
		im, err := impactOf(s.DB, id, true, true, true)
		if err != nil {
			return nil, err
		}
		out = append(out, im)
	}
	return out, nil
}

// endSeries ends the series the day before from and deletes the occurrences
// whose occurrence date is from or later (a moved occurrence is judged by
// the date the rule gave it, not where it was moved to). A series left with
// no occurrences is deleted.
func endSeries(tx *sql.Tx, seriesID int64, from string) (int, error) {
	sr, err := loadSeries(tx, seriesID)
	if err != nil {
		return 0, err
	}
	until := addDays(from, -1)
	if !sr.Rule.Until.IsZero() && recur.FormatDate(sr.Rule.Until) < until {
		until = recur.FormatDate(sr.Rule.Until) // the earlier end wins
	}
	if _, err := tx.Exec(`UPDATE series SET until = ? WHERE id = ?`, until, seriesID); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`DELETE FROM events WHERE series_id = ? AND occurrence_date >= ?`, seriesID, from)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := tx.Exec(`DELETE FROM series_skips WHERE series_id = ? AND date >= ?`, seriesID, from); err != nil {
		return 0, err
	}
	var left int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM events WHERE series_id = ?`, seriesID).Scan(&left); err != nil {
		return 0, err
	}
	if left == 0 {
		_, err = tx.Exec(`DELETE FROM series WHERE id = ?`, seriesID)
	}
	return int(n), err
}

// EndSeriesFrom ends the series on the day before from and deletes every
// occurrence with occurrence_date >= from; earlier ones are untouched.
// Returns how many were deleted.
func (s *Store) EndSeriesFrom(seriesID int64, from string) (int, error) {
	if _, err := time.Parse(DateLayout, from); err != nil {
		return 0, errors.New("pick a date")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n, err := endSeries(tx, seriesID, from)
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// DetailsChange holds the fields the user changed; nil fields are left
// alone. Status, locked and dates are never applied to later occurrences.
type DetailsChange struct {
	Title, Kind, StartTime, EndTime, Coordinator *string
	Description, PublicDescription, Visibility   *string
	Notes                                        *string
	ExpectedCount                                *int
}

// ApplyDetailsForward applies the changed fields to this occurrence and every
// later one (occurrence_date >= this one's) and makes this occurrence the
// series' template. Returns how many events were updated.
func (s *Store) ApplyDetailsForward(eventID int64, c DetailsChange, by string) (int, error) {
	var sets []string
	var args []any
	add := func(col string, p *string) {
		if p != nil {
			sets = append(sets, col+" = ?")
			args = append(args, *p)
		}
	}
	if c.Title != nil && strings.TrimSpace(*c.Title) == "" {
		return 0, errors.New("an event needs a title")
	}
	if c.Visibility != nil {
		v := NormalizeVisibility(*c.Visibility)
		c.Visibility = &v
	}
	add("title", c.Title)
	add("kind", c.Kind)
	add("start_time", c.StartTime)
	add("end_time", c.EndTime)
	add("coordinator", c.Coordinator)
	add("description", c.Description)
	add("public_description", c.PublicDescription)
	add("visibility", c.Visibility)
	add("notes", c.Notes)
	if c.ExpectedCount != nil {
		sets = append(sets, "expected_count = ?")
		args = append(args, nullInt(int64(*c.ExpectedCount)))
	}

	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var seriesID sql.NullInt64
	var date string
	if err := tx.QueryRow(`SELECT series_id, occurrence_date FROM events WHERE id = ?`, eventID).Scan(&seriesID, &date); err != nil {
		return 0, notFound(err)
	}
	if !seriesID.Valid {
		return 0, ErrNotInSeries
	}
	n := 0
	if len(sets) > 0 {
		args = append(args, seriesID.Int64, date)
		res, err := tx.Exec(`UPDATE events SET `+strings.Join(sets, ", ")+`, updated_at = datetime('now') WHERE series_id = ? AND occurrence_date >= ?`, args...)
		if err != nil {
			return 0, err
		}
		c, _ := res.RowsAffected()
		n = int(c)
	}
	if _, err := tx.Exec(`UPDATE series SET template_event_id = ? WHERE id = ?`, eventID, seriesID.Int64); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// validSections checks the section names.
func validSections(sections []string) (map[string]bool, error) {
	set := map[string]bool{}
	for _, sec := range sections {
		switch sec {
		case SectionRooms, SectionSchedule, SectionTasks, SectionHelperJobs, SectionNeeds:
			set[sec] = true
		default:
			return nil, fmt.Errorf("unknown section %q", sec)
		}
	}
	if len(set) == 0 {
		return nil, errors.New("choose what to copy")
	}
	return set, nil
}

// laterOccurrences returns the IDs of the occurrences after eventID's, and
// the event itself.
func laterOccurrences(q querier, eventID int64) (Event, []int64, error) {
	ev, err := scanEvent(q.QueryRow(`SELECT `+eventCols+` FROM events WHERE id = ?`, eventID))
	if err != nil {
		return ev, nil, notFound(err)
	}
	if ev.SeriesID == 0 {
		return ev, nil, ErrNotInSeries
	}
	ids, err := eventIDs(q, `SELECT id FROM events WHERE series_id = ? AND occurrence_date > ? ORDER BY occurrence_date`, ev.SeriesID, ev.OccurrenceDate)
	return ev, ids, err
}

// MatchLaterPreview lists the later occurrences MatchLater would overwrite
// with the work in the chosen sections that would be lost: done / in-progress
// tasks (tasks), helpers signed up (helper_jobs), needs gathered (needs).
func (s *Store) MatchLaterPreview(eventID int64, sections []string) ([]OccurrenceImpact, error) {
	set, err := validSections(sections)
	if err != nil {
		return nil, err
	}
	_, ids, err := laterOccurrences(s.DB, eventID)
	if err != nil {
		return nil, err
	}
	var out []OccurrenceImpact
	for _, id := range ids {
		im, err := impactOf(s.DB, id, set[SectionTasks], set[SectionHelperJobs], set[SectionNeeds])
		if err != nil {
			return nil, err
		}
		out = append(out, im)
	}
	return out, nil
}

// MatchLater replaces the chosen sections of every later occurrence with
// copies from this one, and makes this occurrence the template. Dates move
// by the difference between the two events' starts_on. Tasks start as "to
// do" and keep their assignees, needs start as "needed", helper jobs come
// without helpers. Rooms are matched by place ID (or name); a room the
// target lacks is added when something copied uses it. With "rooms" the
// target's rooms are replaced by this one's, and what is kept elsewhere
// (e.g. tasks not being replaced) is pointed at the matching new room.
// Returns the number of occurrences updated.
func (s *Store) MatchLater(eventID int64, sections []string, by string) (int, error) {
	set, err := validSections(sections)
	if err != nil {
		return 0, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	src, ids, err := laterOccurrences(tx, eventID)
	if err != nil {
		return 0, err
	}
	from, err := time.Parse(DateLayout, src.StartsOn)
	if err != nil {
		return 0, errors.New("the event's start date isn't a date")
	}
	for _, id := range ids {
		var starts string
		if err := tx.QueryRow(`SELECT starts_on FROM events WHERE id = ?`, id).Scan(&starts); err != nil {
			return 0, err
		}
		to, err := time.Parse(DateLayout, starts)
		if err != nil {
			return 0, errors.New("an occurrence's start date isn't a date")
		}
		days := int(to.Sub(from).Round(24*time.Hour).Hours() / 24)
		c := &copier{tx: tx, src: eventID, dst: id, days: days, by: by, keepAssignees: true}
		if err := matchOne(c, set); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(`UPDATE series SET template_event_id = ? WHERE id = ?`, eventID, src.SeriesID); err != nil {
		return 0, err
	}
	return len(ids), tx.Commit()
}

type roomKey struct {
	placeID int64
	name    string // only when placeID is 0
}

func keyOf(placeID sql.NullInt64, name string) roomKey {
	if placeID.Valid {
		return roomKey{placeID: placeID.Int64}
	}
	return roomKey{name: name}
}

// roomsOf returns an event's rooms: ID -> key, plus the IDs in order.
func roomsOf(q querier, eventID int64) (map[int64]roomKey, error) {
	rows, err := q.Query(`SELECT id, place_id, place_name FROM event_rooms WHERE event_id = ? ORDER BY position, id`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]roomKey{}
	for rows.Next() {
		var id int64
		var pid sql.NullInt64
		var name string
		if err := rows.Scan(&id, &pid, &name); err != nil {
			return nil, err
		}
		out[id] = keyOf(pid, name)
	}
	return out, rows.Err()
}

// matchOne makes the target event c.dst's chosen sections like c.src's.
func matchOne(c *copier, set map[string]bool) error {
	tx := c.tx
	if set[SectionRooms] {
		old, err := roomsOf(tx, c.dst)
		if err != nil {
			return err
		}
		fresh, err := c.rooms()
		if err != nil {
			return err
		}
		newRooms, err := roomsOf(tx, c.dst)
		if err != nil {
			return err
		}
		byKey := map[roomKey]int64{}
		for id, k := range newRooms {
			if _, wasOld := old[id]; !wasOld {
				byKey[k] = id
			}
		}
		for oldID, k := range old {
			if newID, ok := byKey[k]; ok {
				for _, t := range []string{"tasks", "schedule_items", "needs", "helper_roles"} {
					if _, err := tx.Exec(`UPDATE `+t+` SET room_id = ? WHERE event_id = ? AND room_id = ?`, newID, c.dst, oldID); err != nil {
						return err
					}
				}
			}
			if _, err := tx.Exec(`DELETE FROM event_rooms WHERE id = ?`, oldID); err != nil {
				return err
			}
		}
		c.room = func(o any) (any, error) { return mapID(fresh, o), nil }
	} else {
		cache := map[int64]int64{}
		c.room = func(o any) (any, error) {
			srcRoom, ok := o.(int64)
			if !ok {
				return nil, nil
			}
			if id, ok := cache[srcRoom]; ok {
				return id, nil
			}
			var pid sql.NullInt64
			var name, purpose, setup, notes string
			if err := tx.QueryRow(`SELECT place_id, place_name, purpose, setup, notes FROM event_rooms WHERE id = ?`, srcRoom).
				Scan(&pid, &name, &purpose, &setup, &notes); err != nil {
				return nil, err
			}
			have, err := roomsOf(tx, c.dst)
			if err != nil {
				return nil, err
			}
			want := keyOf(pid, name)
			var found int64
			for id, k := range have {
				if k == want && (found == 0 || id < found) {
					found = id
				}
			}
			if found == 0 {
				var pos int
				if err := tx.QueryRow(`SELECT COALESCE(MAX(position), 0) + 1 FROM event_rooms WHERE event_id = ?`, c.dst).Scan(&pos); err != nil {
					return nil, err
				}
				var pv any
				if pid.Valid {
					pv = pid.Int64
				}
				res, err := tx.Exec(`INSERT INTO event_rooms (event_id, position, place_id, place_name, purpose, setup, notes) VALUES (?, ?, ?, ?, ?, ?, ?)`,
					c.dst, pos, pv, name, purpose, setup, notes)
				if err != nil {
					return nil, err
				}
				if found, err = res.LastInsertId(); err != nil {
					return nil, err
				}
			}
			cache[srcRoom] = found
			return found, nil
		}
	}
	if set[SectionTasks] {
		for _, q := range []string{`DELETE FROM tasks WHERE event_id = ?`, `DELETE FROM task_lists WHERE event_id = ?`} {
			if _, err := tx.Exec(q, c.dst); err != nil {
				return err
			}
		}
		if err := c.tasks(); err != nil {
			return err
		}
	}
	if set[SectionSchedule] {
		if _, err := tx.Exec(`DELETE FROM schedule_items WHERE event_id = ?`, c.dst); err != nil {
			return err
		}
		if err := c.schedule(); err != nil {
			return err
		}
	}
	if set[SectionNeeds] {
		if _, err := tx.Exec(`DELETE FROM needs WHERE event_id = ?`, c.dst); err != nil {
			return err
		}
		if err := c.needs(); err != nil {
			return err
		}
	}
	if set[SectionHelperJobs] {
		if _, err := tx.Exec(`DELETE FROM helper_roles WHERE event_id = ?`, c.dst); err != nil {
			return err
		}
		if err := c.helperJobs(false); err != nil {
			return err
		}
	}
	return nil
}

// ChangePatternFrom splits a series: the old one ends the day before r.Start
// (its occurrences with occurrence_date >= r.Start are deleted, like
// EndSeriesFrom) and a new series with rule r takes over. The new series'
// template is a copy of the given occurrence's content, made before the
// deletes, placed on the first date the rule gives on or after r.Start (so
// the copy is the new series' first occurrence); r.Start must be a real
// date. Then occurrences are created to the horizon. Returns the new series ID.
func (s *Store) ChangePatternFrom(eventID int64, r recur.Rule, today string) (int64, error) {
	if err := r.Validate(); err != nil {
		return 0, err
	}
	first, ok := r.Next(r.Start.AddDate(0, 0, -1))
	if !ok {
		return 0, errors.New("the new pattern has no dates")
	}
	startStr := recur.FormatDate(r.Start)
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var oldID sql.NullInt64
	if err := tx.QueryRow(`SELECT series_id FROM events WHERE id = ?`, eventID).Scan(&oldID); err != nil {
		return 0, notFound(err)
	}
	if !oldID.Valid {
		return 0, ErrNotInSeries
	}
	newID, err := insertSeries(tx, r)
	if err != nil {
		return 0, err
	}
	tpl, err := makeOccurrence(tx, newID, eventID, recur.FormatDate(first))
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE series SET template_event_id = ? WHERE id = ?`, tpl, newID); err != nil {
		return 0, err
	}
	if _, err := endSeries(tx, oldID.Int64, startStr); err != nil {
		return 0, err
	}
	if _, err := generate(tx, newID, today, startStr); err != nil {
		return 0, err
	}
	return newID, tx.Commit()
}
