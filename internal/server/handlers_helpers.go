package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/tylergoza/event-planner/internal/store"
)

// Helpers: volunteer jobs and who is signed up for them -------------------

func roleAnchor(id int64) string { return "#role-" + strconv.FormatInt(id, 10) }

// helperUsers are the active people who can be signed up from a select.
func (s *Server) helperUsers(r *http.Request) ([]store.User, error) {
	all, err := s.pickableUsers(r, nil)
	if err != nil {
		return nil, err
	}
	out := all[:0:0]
	for _, u := range all {
		if u.Active {
			out = append(out, u)
		}
	}
	return out, nil
}

func (s *Server) handleHelpers(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	roles, err := s.store.ListHelperRoles(e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rooms, err := s.store.ListRooms(e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	users, err := s.helperUsers(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	tally := store.TallyHelperRoles(roles)
	s.eventPage(w, r, http.StatusOK, "helpers/index", e, "helpers", map[string]any{
		"Title": "Helpers · " + e.Title, "Live": true,
		"Roles": roles, "Tally": tally, "Progress": store.Tally{Total: tally.Slots, Done: tally.Filled}, "Rooms": rooms, "Users": users,
		"Me": currentUser(r).ID,
	})
}

// readHelperRole reads the job form into h and returns what's wrong.
func (s *Server) readHelperRole(r *http.Request, h *store.HelperRole) []string {
	h.Name, h.WhenText, h.Notes = formStr(r, "name"), formStr(r, "when_text"), formStr(r, "notes")
	h.RoomID = formInt(r, "room_id")
	slots, err := strconv.Atoi(formStr(r, "slots"))
	h.Slots = slots
	var errs []string
	if h.Name == "" {
		errs = append(errs, "Give the job a name.")
	}
	if err != nil || slots < 1 || slots > 999 {
		errs = append(errs, "Say how many people it needs (1 or more).")
		h.Slots = 1
	}
	if h.RoomID != 0 {
		if ok, err := s.store.RoomInEvent(h.RoomID, h.EventID); err != nil || !ok {
			errs = append(errs, "That room isn't part of this event.")
		}
	}
	return errs
}

func (s *Server) handleHelperRoleCreate(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	h := store.HelperRole{EventID: e.ID}
	if errs := s.readHelperRole(r, &h); errs != nil {
		s.setFlash(w, r, "error", errs[0])
		http.Redirect(w, r, eventURL(e.ID, "/helpers"), http.StatusSeeOther)
		return
	}
	id, err := s.store.CreateHelperRole(&h)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/helpers"+roleAnchor(id)), h.Name+" added.")
}

func (s *Server) handleHelperRolePaste(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	n, err := s.store.AddHelperRoles(e.ID, r.PostFormValue("text"))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	msg := pluralize(n, "job") + " added."
	if n == 0 {
		msg = "Nothing to add: put one job on each line."
	}
	s.redirect(w, r, eventURL(e.ID, "/helpers"), msg)
}

func (s *Server) roleAndEvent(w http.ResponseWriter, r *http.Request) (*store.HelperRole, *store.Event, bool) {
	h, err := s.store.GetHelperRole(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, nil, false
	}
	e, ok := s.eventByID(w, r, h.EventID)
	return h, e, ok
}

func (s *Server) helperRoleForm(w http.ResponseWriter, r *http.Request, status int, e *store.Event, h store.HelperRole, errs []string) {
	rooms, err := s.store.ListRooms(e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.eventPage(w, r, status, "helpers/role_form", e, "helpers", map[string]any{
		"Title": "Edit job · " + e.Title, "Form": h, "Errors": errs, "Rooms": rooms,
	})
}

func (s *Server) handleHelperRoleEdit(w http.ResponseWriter, r *http.Request) {
	h, e, ok := s.roleAndEvent(w, r)
	if !ok {
		return
	}
	s.helperRoleForm(w, r, http.StatusOK, e, *h, nil)
}

func (s *Server) handleHelperRoleUpdate(w http.ResponseWriter, r *http.Request) {
	h, e, ok := s.roleAndEvent(w, r)
	if !ok {
		return
	}
	if errs := s.readHelperRole(r, h); errs != nil {
		s.helperRoleForm(w, r, http.StatusUnprocessableEntity, e, *h, errs)
		return
	}
	if err := s.store.UpdateHelperRole(h); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/helpers"+roleAnchor(h.ID)), "Saved.")
}

func (s *Server) handleHelperRoleDelete(w http.ResponseWriter, r *http.Request) {
	h, e, ok := s.roleAndEvent(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteHelperRole(h.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/helpers"), h.Name+" removed.")
}

// overNote is added to a flash when a job has more people than slots.
func overNote(role *store.HelperRole, added int) string {
	if over := len(role.Helpers) + added - role.Slots; over > 0 {
		return " That's " + strconv.Itoa(over) + " more than the job needs."
	}
	return ""
}

func (s *Server) handleHelperSignUp(w http.ResponseWriter, r *http.Request) {
	role, e, ok := s.roleAndEvent(w, r)
	if !ok {
		return
	}
	back := eventURL(e.ID, "/helpers"+roleAnchor(role.ID))
	userID, name := formInt(r, "user_id"), formStr(r, "name")
	switch {
	case userID != 0 && name != "":
		s.setFlash(w, r, "error", "Pick a person from the list or type a name, not both.")
	case userID == 0 && name == "":
		s.setFlash(w, r, "error", "Pick a person or type a name.")
	case userID != 0:
		if u, err := s.store.GetUser(userID); err != nil || !u.Active {
			s.setFlash(w, r, "error", "That person can't be signed up.")
			break
		}
		if _, err := s.store.SignUpUser(role.ID, userID); errors.Is(err, store.ErrAlreadySignedUp) {
			s.setFlash(w, r, "error", "They're already on "+role.Name+".")
		} else if err != nil {
			s.serverError(w, r, err)
			return
		} else {
			s.redirect(w, r, back, "Signed up for "+role.Name+"."+overNote(role, 1))
			return
		}
	default:
		if _, err := s.store.SignUpName(role.ID, name, formStr(r, "phone")); err != nil {
			s.serverError(w, r, err)
			return
		}
		s.redirect(w, r, back, name+" signed up for "+role.Name+"."+overNote(role, 1))
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) handleHelperMe(w http.ResponseWriter, r *http.Request) {
	role, e, ok := s.roleAndEvent(w, r)
	if !ok {
		return
	}
	back := eventURL(e.ID, "/helpers"+roleAnchor(role.ID))
	_, err := s.store.SignUpUser(role.ID, currentUser(r).ID)
	switch {
	case errors.Is(err, store.ErrAlreadySignedUp):
		s.setFlash(w, r, "error", "You're already on "+role.Name+".")
		http.Redirect(w, r, back, http.StatusSeeOther)
	case err != nil:
		s.serverError(w, r, err)
	default:
		s.redirect(w, r, back, "You're signed up for "+role.Name+"."+overNote(role, 1))
	}
}

func (s *Server) handleHelperMeDelete(w http.ResponseWriter, r *http.Request) {
	role, e, ok := s.roleAndEvent(w, r)
	if !ok {
		return
	}
	if err := s.store.RemoveUserFromRole(role.ID, currentUser(r).ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/helpers"+roleAnchor(role.ID)), "You're off "+role.Name+".")
}

func (s *Server) helperAndEvent(w http.ResponseWriter, r *http.Request) (*store.Helper, *store.Event, bool) {
	h, err := s.store.GetHelper(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, nil, false
	}
	ev, err := s.store.HelperEvent(h.ID)
	if err != nil {
		s.serverError(w, r, err)
		return nil, nil, false
	}
	e, ok := s.eventByID(w, r, ev)
	return h, e, ok
}

func (s *Server) helperForm(w http.ResponseWriter, r *http.Request, status int, e *store.Event, h store.Helper, errs []string) {
	role, err := s.store.GetHelperRole(h.RoleID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.eventPage(w, r, status, "helpers/helper_form", e, "helpers", map[string]any{
		"Title": "Edit helper · " + e.Title, "Form": h, "Role": role, "Errors": errs,
	})
}

func (s *Server) handleHelperEdit(w http.ResponseWriter, r *http.Request) {
	h, e, ok := s.helperAndEvent(w, r)
	if !ok {
		return
	}
	s.helperForm(w, r, http.StatusOK, e, *h, nil)
}

func (s *Server) handleHelperUpdate(w http.ResponseWriter, r *http.Request) {
	h, e, ok := s.helperAndEvent(w, r)
	if !ok {
		return
	}
	if h.UserID == 0 {
		h.Name = formStr(r, "name")
	}
	h.Phone, h.Notes = formStr(r, "phone"), formStr(r, "notes")
	if h.UserID == 0 && h.Name == "" {
		s.helperForm(w, r, http.StatusUnprocessableEntity, e, *h, []string{"A helper needs a name."})
		return
	}
	if err := s.store.UpdateHelper(h); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/helpers"+roleAnchor(h.RoleID)), "Saved.")
}

func (s *Server) handleHelperDelete(w http.ResponseWriter, r *http.Request) {
	h, e, ok := s.helperAndEvent(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteHelper(h.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/helpers"+roleAnchor(h.RoleID)), h.Name+" removed.")
}
