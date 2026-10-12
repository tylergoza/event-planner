package server

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/tylergoza/event-planner/internal/recur"
	"github.com/tylergoza/event-planner/internal/store"
)

// event loads the event named by the {id} in the path, or shows "not found".
func (s *Server) event(w http.ResponseWriter, r *http.Request) (*store.Event, bool) {
	return s.eventByID(w, r, pathID(r))
}

// eventByID loads an event for the signed-in user. A locked one they
// aren't a member of is "not found", so its title doesn't leak. Every
// page and form for an event or anything in it goes through here.
func (s *Server) eventByID(w http.ResponseWriter, r *http.Request, id int64) (*store.Event, bool) {
	e, err := s.store.GetEvent(id)
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	ok, err := s.store.CanSee(e, currentUser(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	if !ok {
		s.notFound(w, r)
		return nil, false
	}
	return e, true
}

// eventPage renders one of an event's tabs.
func (s *Server) eventPage(w http.ResponseWriter, r *http.Request, status int, page string, e *store.Event, tab string, data map[string]any) {
	data["E"] = e
	data["Tab"] = tab
	if e.SeriesID != 0 {
		if c, err := s.seriesContext(currentUser(r), e); err != nil {
			s.log.Error("series context", "event", e.ID, "err", err)
		} else {
			data["Series"] = c
		}
	}
	if _, ok := data["Title"]; !ok {
		data["Title"] = e.Title
	}
	s.render(w, r, status, page, data)
}

func eventURL(id int64, rest string) string { return "/events/" + strconv.FormatInt(id, 10) + rest }

// Dashboard --------------------------------------------------------------

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListEvents(currentUser(r), true, s.today())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	mine, err := s.store.MyTasks(currentUser(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	jobs, err := s.visibleHelperJobs(currentUser(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "dashboard", map[string]any{"Events": list, "MyTasks": mine, "MyJobs": jobs, "Today": s.today(), "Live": true})
}

// visibleHelperJobs lists the helper jobs u is signed up for, in the
// events u can see (the store's list doesn't check that).
func (s *Server) visibleHelperJobs(u *store.User) ([]store.HelperAssignment, error) {
	all, err := s.store.HelperAssignmentsForUser(u.ID)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	var out []store.HelperAssignment
	for _, a := range all {
		ok, known := seen[a.EventID]
		if !known {
			e, err := s.store.GetEvent(a.EventID)
			if err != nil {
				return nil, err
			}
			if ok, err = s.store.CanSee(e, u); err != nil {
				return nil, err
			}
			seen[a.EventID] = ok
		}
		if ok {
			out = append(out, a)
		}
	}
	return out, nil
}

// Events -----------------------------------------------------------------

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListEvents(currentUser(r), false, s.today())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "events/index", map[string]any{"Title": "Events", "Events": list})
}

func (s *Server) eventForm(w http.ResponseWriter, r *http.Request, status int, e store.Event, members []int64, errs []string) {
	s.eventFormWith(w, r, status, e, members, errs, nil)
}

// eventFormWith is eventForm with extra page data: the repeat fields as
// they were submitted ("Repeat") and the "apply to" choice ("Apply").
func (s *Server) eventFormWith(w http.ResponseWriter, r *http.Request, status int, e store.Event, members []int64, errs []string, extra map[string]any) {
	title := "New event"
	if e.ID != 0 {
		title = "Edit " + e.Title
	}
	data := map[string]any{"Title": title, "Form": e, "Visibilities": store.Visibilities, "Errors": errs, "Statuses": store.EventStatuses, "Apply": "this"}
	for k, v := range extra {
		data[k] = v
	}
	if _, ok := data["Repeat"]; !ok {
		data["Repeat"] = newRepeat(e.StartsOn)
	}
	// Only admins see who's on it and can change that.
	if currentUser(r).IsAdmin() {
		// Current members stay listed even if their access was removed.
		var keep []int64
		if e.ID != 0 {
			var err error
			if keep, err = s.store.Members(e.ID); err != nil {
				s.serverError(w, r, err)
				return
			}
		}
		users, err := s.pickableUsers(r, keep)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		isMember := map[int64]bool{}
		for _, id := range members {
			isMember[id] = true
		}
		data["Users"], data["IsMember"] = users, isMember
	}
	s.render(w, r, status, "events/form", data)
}

func (s *Server) handleEventNew(w http.ResponseWriter, r *http.Request) {
	e := store.Event{Status: "planning", StartsOn: s.today()}
	q := r.URL.Query()
	extra := map[string]any{}
	// "Make this an event" from the church calendar: fill what it sent,
	// ignoring anything that doesn't look right.
	if id, err := strconv.ParseInt(q.Get("from_calendar"), 10, 64); err == nil && id > 0 {
		extra["FromCalendar"] = id
		if t := strings.TrimSpace(q.Get("title")); len(t) <= 200 {
			e.Title = t
		}
		if d := q.Get("starts_on"); validDate(d) {
			e.StartsOn = d
		}
		if d := q.Get("ends_on"); validDate(d) && d >= e.StartsOn {
			e.EndsOn = d
		}
		if t := q.Get("start_time"); validTime(t) {
			e.StartTime = t
		}
		if t := q.Get("end_time"); validTime(t) {
			e.EndTime = t
		}
		if rooms := splitRooms(q.Get("rooms")); len(rooms) > 0 {
			extra["CalendarRooms"] = strings.Join(rooms, ",")
			extra["CalendarRoomList"] = strings.Join(rooms, ", ")
		}
	}
	s.eventFormWith(w, r, http.StatusOK, e, nil, nil, extra)
}

// splitRooms is the room names in a comma-separated list, without blanks
// or repeats.
func splitRooms(v string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range strings.Split(v, ",") {
		n = strings.TrimSpace(n)
		if k := strings.ToLower(n); n != "" && len(n) <= 100 && !seen[k] {
			seen[k] = true
			out = append(out, n)
		}
	}
	return out
}

// readEvent reads the form into e and returns who's on its member list.
// Only admins lock events and pick who's on them; for anyone else e keeps
// the access it had and the list comes back nil.
func readEvent(r *http.Request, e *store.Event) ([]int64, []string) {
	e.Title, e.Kind, e.Status = formStr(r, "title"), formStr(r, "kind"), formStr(r, "status")
	e.StartsOn, e.StartTime = formStr(r, "starts_on"), formStr(r, "start_time")
	e.EndsOn, e.EndTime = formStr(r, "ends_on"), formStr(r, "end_time")
	e.Coordinator, e.Description, e.Notes = formStr(r, "coordinator"), formStr(r, "description"), formStr(r, "notes")
	e.Visibility, e.PublicDescription = store.NormalizeVisibility(formStr(r, "visibility")), formStr(r, "public_description")
	var errs []string
	e.ExpectedCount = 0
	if v := formStr(r, "expected_count"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 0 || n > 1000000 {
			errs = append(errs, "Headcount is a number of people, or leave it empty.")
		} else {
			e.ExpectedCount = n
		}
	}
	var members []int64
	if currentUser(r).IsAdmin() {
		e.Locked = formStr(r, "access") == "locked"
		for _, v := range r.PostForm["member"] {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil {
				members = append(members, id)
			}
		}
	}
	if e.Title == "" {
		errs = append(errs, "Give the event a title.")
	}
	if !slices.Contains(store.EventStatuses, e.Status) {
		errs = append(errs, "Pick a status.")
	}
	if !validDate(e.StartsOn) {
		errs = append(errs, "Pick the day it starts.")
	}
	if e.EndsOn != "" && (!validDate(e.EndsOn) || e.EndsOn < e.StartsOn) {
		errs = append(errs, "It can't end before it starts.")
	}
	for _, t := range []string{e.StartTime, e.EndTime} {
		if t != "" && !validTime(t) {
			errs = append(errs, "Times look like 18:30.")
			break
		}
	}
	return members, errs
}

// adminOnlyAccess answers 403 when someone who isn't an admin tries to
// lock an event; the form doesn't offer them the choice.
func (s *Server) adminOnlyAccess(w http.ResponseWriter, r *http.Request) bool {
	if formStr(r, "access") == "locked" && !currentUser(r).IsAdmin() {
		s.render(w, r, http.StatusForbidden, "error", map[string]any{
			"Title": "Admins only", "Message": "Only administrators can lock an event or choose who can see it.",
		})
		return false
	}
	return true
}

func (s *Server) handleEventCreate(w http.ResponseWriter, r *http.Request) {
	if !s.adminOnlyAccess(w, r) {
		return
	}
	var e store.Event
	members, errs := readEvent(r, &e)
	extra := map[string]any{}
	if id, err := strconv.ParseInt(formStr(r, "from_calendar"), 10, 64); err == nil && id > 0 {
		e.CalendarReplaces = id
		extra["FromCalendar"] = id
	}
	if rooms := splitRooms(formStr(r, "calendar_rooms")); len(rooms) > 0 {
		extra["CalendarRooms"] = strings.Join(rooms, ",")
		extra["CalendarRoomList"] = strings.Join(rooms, ", ")
	}
	rf, rule, repeats, rerrs := readRepeat(r, e.StartsOn)
	if repeats && rerrs == nil {
		// A new series starts on the pattern's first date, so a start of
		// Monday for "every Wednesday" becomes that Wednesday, and "after N
		// times" means N.
		if err := alignToRule(&e, &rule); err != nil {
			rerrs = append(rerrs, err.Error())
		}
	}
	if errs = append(errs, rerrs...); errs != nil {
		extra["Repeat"] = rf
		s.eventFormWith(w, r, http.StatusUnprocessableEntity, e, members, errs, extra)
		return
	}
	id, err := s.store.CreateEvent(&e)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, name := range splitRooms(formStr(r, "calendar_rooms")) {
		if _, err := s.store.AddRoom(id, 0, name); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	if e.Locked && currentUser(r).IsAdmin() {
		if err := s.store.SetMembers(id, members); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	msg := e.Title + " added. Next, add its tasks and schedule."
	if repeats {
		sm, err := s.makeSeries(id, rule)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		msg = e.Title + " added. " + sm
	}
	s.redirect(w, r, eventURL(id, ""), msg)
}

func (s *Server) handleEventShow(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	sum, err := s.store.Summary(e, s.today())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	items, err := s.store.ListSchedule(e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	tasks, err := s.store.ListTasks(e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var upcoming []store.ScheduleItem
	for _, it := range items {
		if it.Date >= s.today() && len(upcoming) < 6 {
			upcoming = append(upcoming, it)
		}
	}
	var attention []store.Task
	for _, t := range tasks {
		if t.Status == "blocked" || t.Overdue(s.today()) {
			attention = append(attention, t)
		}
	}
	rooms, err := s.store.ListRooms(e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	conflicts, err := s.store.RoomConflicts(e.ID, currentUser(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	roles, err := s.store.ListHelperRoles(e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	tally := store.TallyHelperRoles(roles)
	var copiedFrom *store.Event
	if e.CopiedFrom != 0 {
		// Only if it still exists and the viewer may see it.
		if src, err := s.store.GetEvent(e.CopiedFrom); err == nil {
			if ok, err := s.store.CanSee(src, currentUser(r)); err == nil && ok {
				copiedFrom = src
			}
		}
	}
	var calClashes []clash
	if s.Calendar().Configured() {
		calClashes = s.storedClashes(e)
	}
	s.eventPage(w, r, http.StatusOK, "events/show", e, "overview", map[string]any{
		"Sum": sum, "Upcoming": upcoming, "Attention": attention, "Today": s.today(), "Live": true,
		"RoomCount": len(rooms), "RoomConflicts": len(conflicts), "Helpers": tally, "CopiedFrom": copiedFrom,
		"CalendarClashes": calClashes,
	})
}

func (s *Server) handleEventEdit(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	var members []int64
	if currentUser(r).IsAdmin() {
		var err error
		if members, err = s.store.Members(e.ID); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.eventForm(w, r, http.StatusOK, *e, members, nil)
}

func (s *Server) handleEventUpdate(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	if !s.adminOnlyAccess(w, r) {
		return
	}
	old := *e
	members, errs := readEvent(r, e)
	extra := map[string]any{}
	scope := "this"
	var rf repeatForm
	var rule recur.Rule
	repeats := false
	if old.SeriesID != 0 {
		switch formStr(r, "apply") {
		case "", "this":
		case "later":
			scope = "later"
			if e.StartsOn != old.StartsOn || e.EndsOn != old.EndsOn {
				errs = append(errs, "To move every later date, change the repeat pattern from here instead.")
			}
		default:
			errs = append(errs, "Choose whether to change just this date or this and later dates.")
		}
		extra["Apply"] = scope
	} else {
		var rerrs []string
		rf, rule, repeats, rerrs = readRepeat(r, e.StartsOn)
		errs = append(errs, rerrs...)
		extra["Repeat"] = rf
	}
	if errs != nil {
		s.eventFormWith(w, r, http.StatusUnprocessableEntity, *e, members, errs, extra)
		return
	}
	if scope == "later" && !s.laterVisible(w, r, &old) {
		return
	}
	if err := s.store.UpdateEvent(e); err != nil {
		s.serverError(w, r, err)
		return
	}
	msg := "Saved."
	if scope == "later" {
		if c := detailsChange(old, *e); hasChange(c) {
			n, err := s.store.ApplyDetailsForward(e.ID, c, currentUser(r).Name())
			if err != nil {
				s.serverError(w, r, err)
				return
			}
			msg = "Saved. Changes applied to " + pluralize(n, "date") + " (this and later)."
		}
	}
	if repeats {
		sm, err := s.makeSeries(e.ID, rule)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		msg = "Saved. " + sm
	}
	// Unlocking keeps the list, for if it's locked again.
	if e.Locked && currentUser(r).IsAdmin() {
		if err := s.store.SetMembers(e.ID, members); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.redirect(w, r, eventURL(e.ID, ""), msg)
}

func (s *Server) handleEventDelete(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	if e.SeriesID != 0 {
		// Deleting one date of a series skips it; the others stay.
		if err := s.store.SkipOccurrence(e.ID); err != nil {
			s.serverError(w, r, err)
			return
		}
		to := "/events"
		if left, err := s.store.Occurrences(e.SeriesID); err == nil && len(left) > 0 {
			to = seriesURL(e.SeriesID, "")
		}
		s.redirect(w, r, to, "Skipped "+fmtDate(e.OccurrenceDate)+". The other dates aren't affected.")
		return
	}
	if err := s.store.DeleteEvent(e.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/events", e.Title+" deleted.")
}

// handleMove moves a task list up or down its event's lists, then goes
// back to the page it was moved from.
func (s *Server) handleMove(table string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ev, err := s.store.EventOf(table, pathID(r))
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if _, ok := s.eventByID(w, r, ev); !ok {
			return
		}
		if err := s.store.Move(table, pathID(r), r.PostFormValue("dir") == "up"); err != nil {
			s.serverError(w, r, err)
			return
		}
		http.Redirect(w, r, safeRedirect(r.PostFormValue("next")), http.StatusSeeOther)
	}
}
