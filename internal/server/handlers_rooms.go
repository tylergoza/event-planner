package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/tylergoza/event-planner/internal/store"
	"github.com/tylergoza/event-planner/internal/tracker"
)

// Rooms: the places an event uses and how each is set up ---------------------

// roomRow is a room with what points at it and who else wants it.
type roomRow struct {
	store.Room
	Link      string // the place's page in the tracker
	Usage     store.RoomUsage
	Conflicts []store.EventRef
}

func (s *Server) roomRows(e *store.Event, viewer *store.User) ([]roomRow, error) {
	rooms, err := s.store.ListRooms(e.ID)
	if err != nil {
		return nil, err
	}
	usage, err := s.store.RoomUsages(e.ID)
	if err != nil {
		return nil, err
	}
	conflicts, err := s.store.RoomConflicts(e.ID, viewer)
	if err != nil {
		return nil, err
	}
	t := s.Tracker()
	rows := make([]roomRow, len(rooms))
	for i, rm := range rooms {
		rows[i] = roomRow{Room: rm, Conflicts: conflicts[rm.ID]}
		if u := usage[rm.ID]; u != nil {
			rows[i].Usage = *u
		}
		if rm.FromTracker() && t.Configured() {
			rows[i].Link = t.PageURL("place", rm.PlaceID)
		}
	}
	return rows, nil
}

func (s *Server) roomsData(r *http.Request, e *store.Event, data map[string]any) (map[string]any, error) {
	rows, err := s.roomRows(e, currentUser(r))
	if err != nil {
		return nil, err
	}
	t := s.Tracker()
	data["Title"] = "Rooms · " + e.Title
	data["Rooms"] = rows
	data["TrackerOn"] = t.Configured()
	if _, ok := data["Form"]; !ok {
		data["Form"] = store.Room{}
	}
	if q := r.URL.Query().Get("q"); q != "" && t.Configured() {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		data["Q"] = q
		places, err := t.Rooms(ctx, q)
		if err != nil {
			data["SearchError"] = trackerErr(err)
		}
		have := map[int64]bool{}
		for _, rm := range rows {
			have[rm.PlaceID] = true
		}
		var found []tracker.Place
		for _, p := range places {
			if !have[p.ID] {
				found = append(found, p)
			}
		}
		data["Found"] = found
		data["FoundAll"] = len(places)
	}
	return data, nil
}

func (s *Server) renderRooms(w http.ResponseWriter, r *http.Request, status int, e *store.Event, data map[string]any) {
	data, err := s.roomsData(r, e, data)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	page := "rooms/index"
	if status == http.StatusOK && r.URL.Query().Get("print") == "1" {
		page = "rooms/print"
		delete(data, "Live")
	}
	s.eventPage(w, r, status, page, e, "rooms", data)
}

func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	s.renderRooms(w, r, http.StatusOK, e, map[string]any{"Live": true})
}

func (s *Server) handleRoomCreate(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	placeID, name := formInt(r, "place_id"), formStr(r, "place_name")
	fail := func(msg string) {
		s.renderRooms(w, r, http.StatusUnprocessableEntity, e, map[string]any{
			"Form": store.Room{PlaceName: name}, "Errors": []string{msg},
		})
	}
	if placeID != 0 {
		t := s.Tracker()
		if !t.Configured() {
			fail("Connect the maintenance tracker in Settings to add its rooms.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		p, err := t.Place(ctx, placeID)
		switch {
		case errors.Is(err, tracker.ErrNotFound):
			fail("That place isn't in the maintenance tracker.")
			return
		case err != nil:
			fail(trackerErr(err))
			return
		}
		name = p.Path
		if name == "" {
			name = p.Name
		}
	}
	if name == "" {
		fail("Say which room.")
		return
	}
	if _, err := s.store.AddRoom(e.ID, placeID, name); errors.Is(err, store.ErrRoomExists) {
		fail(name + " is already one of this event's rooms.")
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/rooms"), name+" added.")
}

func (s *Server) roomAndEvent(w http.ResponseWriter, r *http.Request) (*store.Room, *store.Event, bool) {
	rm, err := s.store.GetRoom(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, nil, false
	}
	e, ok := s.eventByID(w, r, rm.EventID)
	return rm, e, ok
}

func (s *Server) roomForm(w http.ResponseWriter, r *http.Request, status int, e *store.Event, rm store.Room, errs []string) {
	link := ""
	if t := s.Tracker(); rm.FromTracker() && t.Configured() {
		link = t.PageURL("place", rm.PlaceID)
	}
	s.eventPage(w, r, status, "rooms/form", e, "rooms", map[string]any{
		"Title": "Edit " + rm.PlaceName + " · " + e.Title, "Form": rm, "Errors": errs, "TrackerLink": link,
	})
}

func (s *Server) handleRoomEdit(w http.ResponseWriter, r *http.Request) {
	rm, e, ok := s.roomAndEvent(w, r)
	if !ok {
		return
	}
	s.roomForm(w, r, http.StatusOK, e, *rm, nil)
}

func (s *Server) handleRoomUpdate(w http.ResponseWriter, r *http.Request) {
	rm, e, ok := s.roomAndEvent(w, r)
	if !ok {
		return
	}
	rm.Purpose, rm.Setup, rm.Notes = formStr(r, "purpose"), formStr(r, "setup"), formStr(r, "notes")
	if !rm.FromTracker() {
		if rm.PlaceName = formStr(r, "place_name"); rm.PlaceName == "" {
			s.roomForm(w, r, http.StatusUnprocessableEntity, e, *rm, []string{"Say which room."})
			return
		}
	} else {
		rm.PlaceName = "" // kept as it is
	}
	if err := s.store.UpdateRoom(rm); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/rooms"), "Saved.")
}

func (s *Server) handleRoomDelete(w http.ResponseWriter, r *http.Request) {
	rm, e, ok := s.roomAndEvent(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteRoom(rm.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/rooms"), rm.PlaceName+" removed.")
}
