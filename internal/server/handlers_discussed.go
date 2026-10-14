package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tylergoza/event-planner/internal/store"
)

// Events discussed, minutes and closing out (MEETINGS_PLAN.md steps 4-5).
// Planners and the meeting's members change the list and the minutes (the
// same check as the agenda); closing out is a planner action.

// discussedEditor is agendaEditor for the routes under /events/{id}/discussed.
func (s *Server) discussedEditor(w http.ResponseWriter, r *http.Request) (*store.Event, bool) {
	return s.agendaEditor(w, r, pathID(r))
}

func discussedURL(meetingID int64) string { return eventURL(meetingID, "#discussed") }

func (s *Server) discussedError(w http.ResponseWriter, r *http.Request, e *store.Event, msg string) {
	s.setFlash(w, r, "error", msg)
	http.Redirect(w, r, discussedURL(e.ID), http.StatusSeeOther)
}

func (s *Server) handleDiscussedLink(w http.ResponseWriter, r *http.Request) {
	e, ok := s.discussedEditor(w, r)
	if !ok {
		return
	}
	target, err := s.store.GetEvent(formInt(r, "event_id"))
	if err == nil {
		var can bool
		if can, err = s.store.CanSee(target, currentUser(r)); err == nil && !can {
			err = store.ErrNotFound
		}
	}
	if err != nil {
		s.discussedError(w, r, e, "We couldn't find that event.")
		return
	}
	if err := s.store.LinkEvent(e.ID, target.ID); err != nil {
		s.discussedError(w, r, e, "That event can't be added: "+err.Error()+".")
		return
	}
	s.redirect(w, r, eventURL(e.ID, "#discussed-"+strconv.FormatInt(target.ID, 10)), target.Title+" added to the meeting.")
}

func (s *Server) handleDiscussedIdea(w http.ResponseWriter, r *http.Request) {
	e, ok := s.discussedEditor(w, r)
	if !ok {
		return
	}
	title := formStr(r, "title")
	if title == "" {
		s.discussedError(w, r, e, "Give the idea a title.")
		return
	}
	id, err := s.store.NewIdea(e.ID, title)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "#discussed-"+strconv.FormatInt(id, 10)), title+" added as an idea.")
}

// discussedLinked is the event named by {eid}, which must be linked to the meeting.
func (s *Server) discussedLinked(w http.ResponseWriter, r *http.Request) (*store.Event, int64, bool) {
	e, ok := s.discussedEditor(w, r)
	if !ok {
		return nil, 0, false
	}
	eid, _ := strconv.ParseInt(r.PathValue("eid"), 10, 64)
	return e, eid, true
}

func (s *Server) handleDiscussedNote(w http.ResponseWriter, r *http.Request) {
	e, eid, ok := s.discussedLinked(w, r)
	if !ok {
		return
	}
	if err := s.store.SetDiscussionNote(e.ID, eid, r.PostFormValue("note")); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "#discussed-"+strconv.FormatInt(eid, 10)), "Note saved.")
}

func (s *Server) handleDiscussedMove(w http.ResponseWriter, r *http.Request) {
	e, eid, ok := s.discussedLinked(w, r)
	if !ok {
		return
	}
	if err := s.store.MoveMeetingEvent(e.ID, eid, r.PostFormValue("dir") == "up"); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, safeRedirect(r.PostFormValue("next")), http.StatusSeeOther)
}

func (s *Server) handleDiscussedDelete(w http.ResponseWriter, r *http.Request) {
	e, eid, ok := s.discussedLinked(w, r)
	if !ok {
		return
	}
	if err := s.store.UnlinkEvent(e.ID, eid); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, discussedURL(e.ID), "Removed from this meeting. The event itself is untouched.")
}

func (s *Server) handleBringOver(w http.ResponseWriter, r *http.Request) {
	e, ok := s.discussedEditor(w, r)
	if !ok {
		return
	}
	prev, err := s.store.PreviousMeeting(e.ID, currentUser(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if prev == nil {
		s.setFlash(w, r, "error", "No earlier meeting found.")
		http.Redirect(w, r, discussedURL(e.ID), http.StatusSeeOther)
		return
	}
	res, err := s.store.BringOver(e.ID, currentUser(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, discussedURL(e.ID), fmt.Sprintf("Brought over %s and %s from %s.",
		pluralize(res.Events, "event"), pluralize(res.Agenda, "agenda item"), fmtDate(prev.StartsOn)))
}

// Minutes -----------------------------------------------------------------

func (s *Server) handleMinutes(w http.ResponseWriter, r *http.Request) {
	e, ok := s.discussedEditor(w, r)
	if !ok {
		return
	}
	if err := s.store.SetMinutes(e.ID, r.PostFormValue("minutes")); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "#minutes"), "Minutes saved.")
}

// meetingStarted reports whether a scheduled meeting's start has passed.
func meetingStarted(e *store.Event, now time.Time) bool {
	if !e.Scheduled() {
		return false
	}
	layout, val := store.DateLayout, e.StartsOn
	if e.StartTime != "" {
		layout, val = layout+" 15:04", val+" "+e.StartTime
	}
	t, err := time.ParseInLocation(layout, val, time.Local)
	return err == nil && !now.Before(t)
}

// Closing out -------------------------------------------------------------

// handleCloseOut is the "Mark as done / Mark as cancelled" prompt on an
// event that has ended. A meeting with no minutes isn't marked done; it
// goes to the minutes with a reminder instead, and the prompt stays until
// they're written and "Mark as done" is pressed again.
func (s *Server) handleCloseOut(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	status := formStr(r, "status")
	if status != "done" && status != "cancelled" {
		s.setFlash(w, r, "error", "Choose done or cancelled.")
		http.Redirect(w, r, eventURL(e.ID, ""), http.StatusSeeOther)
		return
	}
	if e.Status != "planning" && e.Status != "ready" {
		http.Redirect(w, r, eventURL(e.ID, ""), http.StatusSeeOther)
		return
	}
	if status == "done" && e.IsMeeting() && strings.TrimSpace(e.Minutes) == "" {
		s.setFlash(w, r, "error", "Add the minutes, then mark it done.")
		http.Redirect(w, r, eventURL(e.ID, "#minutes"), http.StatusSeeOther)
		return
	}
	e.Status = status
	if err := s.store.UpdateEvent(e); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, safeRedirect(returnTo(r, eventURL(e.ID, ""))), e.Title+" marked as "+status+".")
}

// returnTo is the local page a form asked to come back to (its "return"
// field), or fallback. Only paths under /events/ are accepted.
func returnTo(r *http.Request, fallback string) string {
	to := r.PostFormValue("return")
	if strings.HasPrefix(to, "/events/") && !strings.ContainsAny(to, "\\\r\n") {
		return to
	}
	return fallback
}
