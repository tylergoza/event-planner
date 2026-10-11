package server

import (
	"net/http"

	"github.com/tylergoza/event-planner/internal/store"
)

// People: who's doing what ---------------------------------------------------

func (s *Server) handlePeople(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	people, err := s.store.EventPeople(e.ID, s.today())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data := map[string]any{"Title": "People · " + e.Title, "Live": true, "P": people}
	page := "people/index"
	if r.URL.Query().Get("print") == "1" {
		page = "people/print"
		delete(data, "Live")
	}
	s.eventPage(w, r, http.StatusOK, page, e, "people", data)
}

// Copying an event -----------------------------------------------------------

func (s *Server) copyForm(w http.ResponseWriter, r *http.Request, status int, e *store.Event, form map[string]string, errs []string) {
	s.eventPage(w, r, status, "events/copy", e, "", map[string]any{"Title": "Copy " + e.Title, "Form": form, "Errors": errs})
}

func (s *Server) handleEventCopyForm(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	s.copyForm(w, r, http.StatusOK, e, map[string]string{"title": e.Title, "starts_on": "", "assignees": "1"}, nil)
}

func (s *Server) handleEventCopy(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	opts := store.CopyOptions{
		Title: formStr(r, "title"), StartsOn: formStr(r, "starts_on"),
		KeepAssignees: formStr(r, "assignees") != "", KeepHelpers: formStr(r, "helpers") != "",
		By: currentUser(r).Name(),
	}
	if opts.Title == "" {
		opts.Title = e.Title
	}
	var errs []string
	if !validDate(opts.StartsOn) {
		errs = append(errs, "Pick the day the copy starts.")
	}
	if errs != nil {
		form := map[string]string{"title": opts.Title, "starts_on": opts.StartsOn}
		if opts.KeepAssignees {
			form["assignees"] = "1"
		}
		if opts.KeepHelpers {
			form["helpers"] = "1"
		}
		s.copyForm(w, r, http.StatusUnprocessableEntity, e, form, errs)
		return
	}
	id, err := s.store.CopyEvent(e.ID, opts)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(id, ""), "Copied from "+e.Title+". Check the dates and who's doing what.")
}
