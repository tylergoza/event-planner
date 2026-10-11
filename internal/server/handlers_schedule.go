package server

import (
	"net/http"

	"github.com/tylergoza/event-planner/internal/store"
)

// scheduleData is the schedule page: the timeline and the add form.
func (s *Server) scheduleData(e *store.Event, form store.ScheduleItem, errs []string) (map[string]any, error) {
	items, err := s.store.ListSchedule(e.ID)
	if err != nil {
		return nil, err
	}
	rooms, err := s.store.ListRooms(e.ID)
	if err != nil {
		return nil, err
	}
	if form.Date == "" {
		form.Date = e.StartsOn
	}
	return map[string]any{"Items": items, "Form": form, "Errors": errs, "Rooms": rooms}, nil
}

func (s *Server) handleSchedule(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	data, err := s.scheduleData(e, store.ScheduleItem{}, nil)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data["Live"] = true
	data["Today"] = s.today()
	data["Title"] = "Schedule · " + e.Title
	s.eventPage(w, r, http.StatusOK, "schedule/index", e, "schedule", data)
}

func (s *Server) readScheduleItem(r *http.Request, i *store.ScheduleItem) []string {
	i.Date, i.Time, i.EndTime = formStr(r, "date"), formStr(r, "time"), formStr(r, "end_time")
	i.Label, i.Notes, i.RoomID = formStr(r, "label"), formStr(r, "notes"), formInt(r, "room_id")
	var errs []string
	if i.Label == "" {
		errs = append(errs, "Say what happens.")
	}
	if !validDate(i.Date) {
		errs = append(errs, "Pick a date.")
	}
	if (i.Time != "" && !validTime(i.Time)) || (i.EndTime != "" && !validTime(i.EndTime)) {
		errs = append(errs, "Times look like 18:30.")
	}
	if i.RoomID != 0 {
		if ok, err := s.store.RoomInEvent(i.RoomID, i.EventID); err != nil || !ok {
			errs = append(errs, "That room isn't part of this event.")
		}
	}
	return errs
}

func (s *Server) handleScheduleCreate(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	i := store.ScheduleItem{EventID: e.ID}
	if errs := s.readScheduleItem(r, &i); errs != nil {
		data, err := s.scheduleData(e, i, errs)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		data["Today"] = s.today()
		s.eventPage(w, r, http.StatusUnprocessableEntity, "schedule/index", e, "schedule", data)
		return
	}
	if _, err := s.store.CreateScheduleItem(&i); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/schedule"), i.Label+" on "+fmtDate(i.Date)+" added.")
}

func (s *Server) itemAndEvent(w http.ResponseWriter, r *http.Request) (*store.ScheduleItem, *store.Event, bool) {
	i, err := s.store.GetScheduleItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, nil, false
	}
	e, ok := s.eventByID(w, r, i.EventID)
	return i, e, ok
}

func (s *Server) scheduleForm(w http.ResponseWriter, r *http.Request, status int, e *store.Event, i store.ScheduleItem, errs []string) {
	rooms, err := s.store.ListRooms(e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.eventPage(w, r, status, "schedule/form", e, "schedule", map[string]any{"Form": i, "Errors": errs, "Rooms": rooms})
}

func (s *Server) handleScheduleEdit(w http.ResponseWriter, r *http.Request) {
	i, e, ok := s.itemAndEvent(w, r)
	if !ok {
		return
	}
	s.scheduleForm(w, r, http.StatusOK, e, *i, nil)
}

func (s *Server) handleScheduleUpdate(w http.ResponseWriter, r *http.Request) {
	i, e, ok := s.itemAndEvent(w, r)
	if !ok {
		return
	}
	if errs := s.readScheduleItem(r, i); errs != nil {
		s.scheduleForm(w, r, http.StatusUnprocessableEntity, e, *i, errs)
		return
	}
	if err := s.store.UpdateScheduleItem(i); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/schedule"), "Saved.")
}

func (s *Server) handleScheduleDelete(w http.ResponseWriter, r *http.Request) {
	i, e, ok := s.itemAndEvent(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteScheduleItem(i.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/schedule"), "Removed from the schedule.")
}
