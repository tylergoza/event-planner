package server

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/tylergoza/event-planner/internal/calendar"
	"github.com/tylergoza/event-planner/internal/store"
)

// Room warnings across apps (CALENDAR_PLAN.md step 4). Warnings never block
// a save.

// span is a stretch of dates, with optional times, as events and calendar
// blocks both have them.
type span struct{ StartsOn, StartTime, EndsOn, EndTime string }

// bounds turns a span into a half-open interval. A missing start time is
// the start of the day and a missing end time the end of the last day, so an
// all-day span overlaps any time on its dates. ok is false for dates that
// can't be read.
func (p span) bounds() (start, end time.Time, ok bool) {
	day, err := time.Parse(store.DateLayout, p.StartsOn)
	if err != nil {
		return start, end, false
	}
	last := day
	if p.EndsOn != "" {
		if last, err = time.Parse(store.DateLayout, p.EndsOn); err != nil {
			return start, end, false
		}
	}
	at := func(d time.Time, clock string) (time.Time, bool) {
		c, err := time.Parse("15:04", clock)
		if err != nil {
			return d, false
		}
		return d.Add(time.Duration(c.Hour())*time.Hour + time.Duration(c.Minute())*time.Minute), true
	}
	start, _ = at(day, p.StartTime)
	end, has := at(last, p.EndTime)
	if !has {
		end = last.AddDate(0, 0, 1)
	}
	if !end.After(start) {
		end = start.Add(time.Minute)
	}
	return start, end, true
}

// spansOverlap reports whether two spans share any time. Spans that can't
// be read count as overlapping, so a warning is never lost to bad data.
func spansOverlap(a, b span) bool {
	as, ae, ok1 := a.bounds()
	bs, be, ok2 := b.bounds()
	if !ok1 || !ok2 {
		return true
	}
	return as.Before(be) && bs.Before(ae)
}

func eventSpan(e *store.Event) span {
	return span{e.StartsOn, e.StartTime, e.EndsOn, e.EndTime}
}

func blockSpan(b calendar.Block) span {
	return span{b.StartsOn, b.StartTime, b.EndsOn, b.EndTime}
}

// clash is one calendar booking shown as a warning.
type clash struct {
	Busy    bool   // someone else's private booking: no title
	Mark    string // "Pencilled in" or "Pending"; "" when confirmed
	Title   string
	Source  string // "Production planner", "Calendar"; empty for our own
	EventID int64  // one of our own events, linked
	When    string
	Rooms   string
}

func clockShort(t string) (string, string) {
	c, err := time.Parse("15:04", t)
	if err != nil {
		return "", ""
	}
	return c.Format("3:04"), c.Format("pm")
}

// clockRange is "2:00–4:00 pm", "11:00 am–1:00 pm", "from 2:00 pm" or "all day".
func clockRange(start, end string) string {
	s, sp := clockShort(start)
	e, ep := clockShort(end)
	switch {
	case s == "":
		return "all day"
	case e == "":
		return "from " + s + " " + sp
	case sp == ep:
		return s + "–" + e + " " + ep
	}
	return s + " " + sp + "–" + e + " " + ep
}

// blockWhen is when a block happens, with its date if showDate or if it
// runs over several days.
func blockWhen(b calendar.Block, showDate bool) string {
	day := func(d string) string {
		if t, err := time.Parse(store.DateLayout, d); err == nil {
			return t.Format("Jan 2")
		}
		return d
	}
	if b.EndsOn != "" && b.EndsOn != b.StartsOn {
		out := day(b.StartsOn)
		if s, sp := clockShort(b.StartTime); s != "" {
			out += " " + s + " " + sp
		}
		out += " – " + day(b.EndsOn)
		if e, ep := clockShort(b.EndTime); e != "" {
			out += " " + e + " " + ep
		}
		return out
	}
	out := clockRange(b.StartTime, b.EndTime)
	if showDate {
		out = day(b.StartsOn) + ", " + out
	}
	return out
}

func sourceLabel(src string) string {
	switch src {
	case "production", "planner":
		return "Production planner"
	}
	return "Calendar"
}

// statusMark is the label for a booking that holds its rooms but isn't
// confirmed. Both count as busy all the same.
func statusMark(status string) string {
	switch status {
	case "tentative":
		return "Pencilled in"
	case "pending":
		return "Pending"
	}
	return ""
}

func newClash(b calendar.Block, showDate bool) clash {
	c := clash{Busy: b.Busy, Mark: statusMark(b.Status), When: blockWhen(b, showDate), Rooms: strings.Join(b.Rooms, ", ")}
	if b.Busy {
		return c
	}
	c.Title = b.Title
	if b.Ours() {
		c.EventID, _ = strconv.ParseInt(b.SourceID, 10, 64)
	} else {
		c.Source = sourceLabel(b.Source)
	}
	return c
}

// isOwn is whether a block is this event's own booking.
func isOwn(b calendar.Block, eventID int64) bool {
	return b.Ours() && b.SourceID == strconv.FormatInt(eventID, 10)
}

// roomClashes asks the calendar who else has the event's rooms during the
// event, keyed by lower-cased room name. Blocks outside the event's times and
// the event's own booking are left out.
func (s *Server) roomClashes(ctx context.Context, e *store.Event, rooms []store.Room) (map[string][]clash, error) {
	names := make([]string, len(rooms))
	for i, r := range rooms {
		names[i] = r.PlaceName
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	blocks, err := s.Calendar().Availability(ctx, names, e.StartsOn, e.LastDate())
	if err != nil {
		return nil, err
	}
	multi := e.LastDate() != e.StartsOn
	out := map[string][]clash{}
	for _, b := range blocks {
		if isOwn(b, e.ID) || !spansOverlap(eventSpan(e), blockSpan(b)) {
			continue
		}
		c := newClash(b, multi)
		for _, n := range names {
			for _, br := range b.Rooms {
				if strings.EqualFold(n, br) {
					k := strings.ToLower(n)
					out[k] = append(out[k], c)
					break
				}
			}
		}
	}
	return out, nil
}

// storedClashes decodes the conflicts the worker kept for an event.
func (s *Server) storedClashes(e *store.Event) []clash {
	raw, err := s.store.CalendarConflicts(e.ID)
	if err != nil || raw == "" {
		return nil
	}
	var blocks []calendar.Block
	if json.Unmarshal([]byte(raw), &blocks) != nil {
		return nil
	}
	var out []clash
	for _, b := range blocks {
		if !isOwn(b, e.ID) {
			out = append(out, newClash(b, true))
		}
	}
	return out
}
