package server

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/tylergoza/event-planner/internal/calendar"
	"github.com/tylergoza/event-planner/internal/store"
)

// Sending events to the church calendar (CALENDAR_PLAN.md). Changes queue
// in the calendar_outbox table in the same transaction as the change; the
// worker here sends them every 30 seconds and straight after a save.
// Saving an event never waits on the calendar.

// Calendar is the church calendar client; check Configured before use.
func (s *Server) Calendar() *calendar.Client { return s.calendar.Load() }

// KickCalendar asks the worker to send what's waiting now rather than at
// its next tick. It never blocks.
func (s *Server) KickCalendar() {
	select {
	case s.calendarKick <- struct{}{}:
	default:
	}
}

// RunCalendarWorker sends waiting changes until ctx is done.
func (s *Server) RunCalendarWorker(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		s.sendCalendarJobs(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.calendarKick:
		}
	}
}

// calendarRetry is how long to wait after the nth failure in a row.
func calendarRetry(attempts int) time.Duration {
	switch attempts {
	case 1:
		return time.Minute
	case 2:
		return 5 * time.Minute
	case 3:
		return 30 * time.Minute
	}
	return time.Hour
}

// sendCalendarJobs sends everything that's due. If the calendar can't be
// reached it stops for now; the rest would fail the same way.
func (s *Server) sendCalendarJobs(ctx context.Context) {
	c := s.Calendar()
	if !c.Configured() {
		return
	}
	for ctx.Err() == nil {
		jobs, err := s.store.DueCalendarJobs(50)
		if err != nil {
			s.log.Error("calendar outbox", "err", err)
			return
		}
		if len(jobs) == 0 {
			return
		}
		for _, j := range jobs {
			err := s.sendCalendarJob(ctx, c, j)
			if ctx.Err() != nil {
				return // shutting down: the row stays for next time
			}
			if err == nil {
				err = s.store.CalendarSent(j)
				if err != nil {
					s.log.Error("calendar outbox", "err", err)
					return
				}
				continue
			}
			s.log.Warn("calendar send failed", "event", j.EventID, "op", j.Op, "attempt", j.Attempts+1, "err", err)
			if err := s.store.CalendarFailed(j, err.Error(), calendarRetry(j.Attempts+1)); err != nil {
				s.log.Error("calendar outbox", "err", err)
				return
			}
			var answered *calendar.Error
			if !errors.As(err, &answered) {
				return
			}
		}
	}
}

func (s *Server) sendCalendarJob(ctx context.Context, c *calendar.Client, j store.CalendarJob) error {
	if j.Op == "delete" {
		return c.DeleteBooking(ctx, j.EventID)
	}
	e, rooms, err := s.store.OutboxEvent(j.EventID)
	if errors.Is(err, store.ErrNotFound) {
		return c.DeleteBooking(ctx, j.EventID) // deleted since it was queued
	}
	if err != nil {
		return err
	}
	if !e.Scheduled() {
		// No date yet: nothing to hold, and it may have had one before.
		// A booking it would take over stays until it has a date.
		if err := c.DeleteBooking(ctx, e.ID); err != nil {
			return err
		}
		return s.store.SetCalendarConflicts(e.ID, "")
	}
	b := s.calendarBooking(e, rooms)
	b.Replaces = e.CalendarReplaces
	res, err := c.PutBooking(ctx, e.ID, b)
	if err != nil {
		return err
	}
	if e.CalendarReplaces != 0 {
		// The calendar has taken over that booking; don't ask again.
		if err := s.store.ClearCalendarReplaces(e.ID); err != nil {
			s.log.Warn("calendar replaces not cleared", "event", e.ID, "err", err)
		}
	}
	// Keep the answer for the event's page; a failure here never fails the send.
	kept := ""
	if len(res.Conflicts) > 0 {
		s.log.Info("calendar reports room clashes", "event", e.ID, "conflicts", len(res.Conflicts))
		if b, err := json.Marshal(res.Conflicts); err == nil {
			kept = string(b)
		}
	}
	if err := s.store.SetCalendarConflicts(e.ID, kept); err != nil {
		s.log.Warn("calendar conflicts not kept", "event", e.ID, "err", err)
	}
	return nil
}

// calendarBooking is what the calendar is sent for an event. Never notes,
// tasks, helpers or needs; for a locked event not even its title, kind or
// description, only rooms and times. Status is "cancelled" for a cancelled
// event, "tentative" while its date is pencilled in, else "confirmed".
// Events not scheduled yet aren't sent (sendCalendarJob deletes them).
func (s *Server) calendarBooking(e *store.Event, rooms []store.Room) calendar.Booking {
	b := calendar.Booking{
		StartsOn: e.StartsOn, StartTime: e.StartTime, EndsOn: e.EndsOn, EndTime: e.EndTime,
		Visibility: e.Visibility, Status: "confirmed", Rooms: []calendar.Room{},
	}
	if e.Locked {
		b.Visibility = "locked"
	} else {
		b.Title, b.Kind, b.Description = e.Title, e.Kind, e.PublicDescription
	}
	switch {
	case e.Status == "cancelled":
		b.Status = "cancelled"
	case e.DateTentative:
		b.Status = "tentative"
	}
	if base := s.configuredBaseURL(); base != "" {
		b.URL = base + "/events/" + strconv.FormatInt(e.ID, 10)
	}
	// The calendar keeps one row per room name.
	seen := map[string]bool{}
	for _, r := range rooms {
		if k := strings.ToLower(r.PlaceName); !seen[k] {
			seen[k] = true
			b.Rooms = append(b.Rooms, calendar.Room{PlaceID: r.PlaceID, Name: r.PlaceName})
		}
	}
	return b
}
