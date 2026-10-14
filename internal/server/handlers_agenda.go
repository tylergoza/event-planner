package server

import (
	"net/http"
	"slices"

	"github.com/tylergoza/event-planner/internal/store"
)

// Meeting agenda (MEETINGS_PLAN.md). Planners and the meeting's members
// change it; everyone else who can see the meeting reads it.

// canEditAgenda reports whether u may change e's agenda and minutes: a
// planner (or admin), or someone on the event's member list.
func (s *Server) canEditAgenda(e *store.Event, u *store.User) (bool, error) {
	if u.CanPlan() {
		return true, nil
	}
	ids, err := s.store.Members(e.ID)
	return slices.Contains(ids, u.ID), err
}

// agendaEditor loads the meeting for id and checks the signed-in user may
// change its agenda. Anything else answers "not found" or 403.
func (s *Server) agendaEditor(w http.ResponseWriter, r *http.Request, id int64) (*store.Event, bool) {
	e, ok := s.eventByID(w, r, id)
	if !ok {
		return nil, false
	}
	if !e.IsMeeting() {
		s.notFound(w, r)
		return nil, false
	}
	can, err := s.canEditAgenda(e, currentUser(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	if !can {
		s.render(w, r, http.StatusForbidden, "error", map[string]any{
			"Title": "Members only", "Message": "Only planners and the people on this meeting can change its agenda.",
		})
		return nil, false
	}
	return e, true
}

// agendaItem loads the item named in the path and its meeting, checking
// the user may change it.
func (s *Server) agendaItem(w http.ResponseWriter, r *http.Request) (*store.AgendaItem, *store.Event, bool) {
	a, err := s.store.GetAgendaItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, nil, false
	}
	e, ok := s.agendaEditor(w, r, a.EventID)
	return a, e, ok
}

func readAgendaItem(r *http.Request, a *store.AgendaItem) {
	a.Title, a.Details = formStr(r, "title"), formStr(r, "details")
	a.CarryOver = formStr(r, "carry_over") == "1"
}

func (s *Server) handleAgendaCreate(w http.ResponseWriter, r *http.Request) {
	e, ok := s.agendaEditor(w, r, pathID(r))
	if !ok {
		return
	}
	var a store.AgendaItem
	readAgendaItem(r, &a)
	if a.Title == "" {
		s.setFlash(w, r, "error", "Give the agenda item a title.")
		http.Redirect(w, r, eventURL(e.ID, "#agenda"), http.StatusSeeOther)
		return
	}
	if _, err := s.store.AddAgendaItem(e.ID, a.Title, a.Details, a.CarryOver); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "#agenda"), a.Title+" added to the agenda.")
}

func (s *Server) agendaForm(w http.ResponseWriter, r *http.Request, status int, e *store.Event, a store.AgendaItem, errs []string) {
	s.eventPage(w, r, status, "events/agenda_form", e, "overview", map[string]any{"Form": a, "Errors": errs, "Title": "Edit agenda item"})
}

func (s *Server) handleAgendaEdit(w http.ResponseWriter, r *http.Request) {
	a, e, ok := s.agendaItem(w, r)
	if !ok {
		return
	}
	s.agendaForm(w, r, http.StatusOK, e, *a, nil)
}

func (s *Server) handleAgendaUpdate(w http.ResponseWriter, r *http.Request) {
	a, e, ok := s.agendaItem(w, r)
	if !ok {
		return
	}
	readAgendaItem(r, a)
	if a.Title == "" {
		s.agendaForm(w, r, http.StatusUnprocessableEntity, e, *a, []string{"Give the agenda item a title."})
		return
	}
	if err := s.store.UpdateAgendaItem(a.ID, a.Title, a.Details, a.CarryOver); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "#agenda"), "Saved.")
}

func (s *Server) handleAgendaDelete(w http.ResponseWriter, r *http.Request) {
	a, e, ok := s.agendaItem(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteAgendaItem(a.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "#agenda"), a.Title+" removed from the agenda.")
}

func (s *Server) handleAgendaMove(w http.ResponseWriter, r *http.Request) {
	a, _, ok := s.agendaItem(w, r)
	if !ok {
		return
	}
	if err := s.store.Move("agenda_items", a.ID, r.PostFormValue("dir") == "up"); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, safeRedirect(r.PostFormValue("next")), http.StatusSeeOther)
}

// handleAgendaPrint is the print-friendly agenda: title, date, rooms,
// agenda and the events to discuss.
func (s *Server) handleAgendaPrint(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	if !e.IsMeeting() {
		s.notFound(w, r)
		return
	}
	items, err := s.store.AgendaItems(e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rooms, err := s.store.ListRooms(e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	discuss, err := s.store.MeetingEvents(e.ID, currentUser(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.eventPage(w, r, http.StatusOK, "events/agenda", e, "overview", map[string]any{
		"Title": "Agenda · " + e.Title, "Agenda": items, "Rooms": rooms, "Discuss": discuss,
	})
}
