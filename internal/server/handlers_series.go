package server

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/tylergoza/event-planner/internal/recur"
	"github.com/tylergoza/event-planner/internal/store"
)

// topUpEvery is how often repeating events are extended to the horizon.
const topUpEvery = 6 * time.Hour

// TopUpSeries creates the occurrences repeating events are due, up to six
// months ahead. Run at startup and every few hours.
func (s *Server) TopUpSeries() (int, error) {
	n, err := s.store.ExtendSeries(s.today())
	if err != nil {
		s.log.Error("extend repeating events", "err", err)
		return n, err
	}
	if n > 0 {
		s.log.Info("extended repeating events", "created", n)
	}
	return n, nil
}

func seriesURL(id int64, rest string) string { return "/series/" + strconv.FormatInt(id, 10) + rest }

// seriesCtx is what an occurrence's page header shows about its series.
type seriesCtx struct {
	ID         int64
	Describe   string
	Pos, Total int
	Prev, Next *store.Event
}

// visibleOccurrences lists a series' occurrences u may see.
func (s *Server) visibleOccurrences(u *store.User, seriesID int64) ([]store.Event, error) {
	all, err := s.store.Occurrences(seriesID)
	if err != nil {
		return nil, err
	}
	var out []store.Event
	for i := range all {
		ok, err := s.store.CanSee(&all[i], u)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, all[i])
		}
	}
	return out, nil
}

func (s *Server) seriesContext(u *store.User, e *store.Event) (*seriesCtx, error) {
	sr, err := s.store.GetSeries(e.SeriesID)
	if err != nil {
		return nil, err
	}
	occ, err := s.visibleOccurrences(u, e.SeriesID)
	if err != nil {
		return nil, err
	}
	c := &seriesCtx{ID: sr.ID, Describe: sr.Rule.Describe(), Total: len(occ)}
	for i := range occ {
		switch {
		case occ[i].ID == e.ID:
			c.Pos = i + 1
			if i > 0 {
				c.Prev = &occ[i-1]
			}
			if i+1 < len(occ) {
				c.Next = &occ[i+1]
			}
		}
	}
	return c, nil
}

// alignToRule moves a new event (and rule.Start) to the first date its
// repeat pattern gives on or after the chosen start, keeping its length.
func alignToRule(e *store.Event, rule *recur.Rule) error {
	start, err := recur.ParseDate(e.StartsOn)
	if err != nil {
		return err
	}
	first, ok := rule.Next(start.AddDate(0, 0, -1))
	if !ok {
		return errors.New("That pattern has no dates on or after the start date.")
	}
	if first.Equal(start) {
		return nil
	}
	if e.EndsOn != "" {
		if end, err := recur.ParseDate(e.EndsOn); err == nil {
			e.EndsOn = recur.FormatDate(end.Add(first.Sub(start)))
		}
	}
	e.StartsOn = recur.FormatDate(first)
	rule.Start = first
	return nil
}

// laterVisible answers 403 unless u can see this occurrence and every later
// one: changing or removing later dates mustn't reach events locked away
// from them.
func (s *Server) laterVisible(w http.ResponseWriter, r *http.Request, e *store.Event) bool {
	all, err := s.store.Occurrences(e.SeriesID)
	if err != nil {
		s.serverError(w, r, err)
		return false
	}
	for i := range all {
		if all[i].OccurrenceDate < e.OccurrenceDate {
			continue
		}
		ok, err := s.store.CanSee(&all[i], currentUser(r))
		if err != nil {
			s.serverError(w, r, err)
			return false
		}
		if !ok {
			s.render(w, r, http.StatusForbidden, "error", map[string]any{
				"Title":   "Some later dates are locked",
				"Message": "One or more later dates in this series are locked to a list of people you aren't on, so they can't be changed from here. Ask an administrator.",
			})
			return false
		}
	}
	return true
}

// occurrence loads the event in the path and makes sure it is part of a series.
func (s *Server) occurrence(w http.ResponseWriter, r *http.Request) (*store.Event, bool) {
	e, ok := s.event(w, r)
	if !ok {
		return nil, false
	}
	if e.SeriesID == 0 {
		s.notFound(w, r)
		return nil, false
	}
	return e, true
}

// series loads the series in the path for someone who can see at least one
// of its occurrences, returning the ones they can see.
func (s *Server) series(w http.ResponseWriter, r *http.Request) (*store.Series, []store.Event, bool) {
	sr, err := s.store.GetSeries(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, nil, false
	}
	occ, err := s.visibleOccurrences(currentUser(r), sr.ID)
	if err != nil {
		s.serverError(w, r, err)
		return nil, nil, false
	}
	if len(occ) == 0 {
		s.notFound(w, r)
		return nil, nil, false
	}
	return sr, occ, true
}

// Series page ----------------------------------------------------------------

type occurrenceRow struct {
	store.Event
	Tasks store.Tally
}

func (s *Server) handleSeriesShow(w http.ResponseWriter, r *http.Request) {
	sr, occ, ok := s.series(w, r)
	if !ok {
		return
	}
	skipped, err := s.store.SkippedDates(sr.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	today := s.today()
	var upcoming []occurrenceRow
	var past []store.Event
	var tpl *store.Event
	for i := range occ {
		e := occ[i]
		if e.ID == sr.TemplateEventID {
			tpl = &occ[i]
		}
		if e.LastDate() < today {
			past = append(past, e)
			continue
		}
		sum, err := s.store.Summary(&e, today)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		upcoming = append(upcoming, occurrenceRow{Event: e, Tasks: sum.Tasks})
	}
	slices.Reverse(past)
	s.render(w, r, http.StatusOK, "series/show", map[string]any{
		"Title": "Repeating event", "S": sr, "Describe": sr.Rule.Describe(), "Upcoming": upcoming, "Past": past,
		"Skipped": skipped, "Template": tpl, "First": occ[0], "Today": today,
	})
}

func (s *Server) handleSeriesRestore(w http.ResponseWriter, r *http.Request) {
	sr, _, ok := s.series(w, r)
	if !ok {
		return
	}
	date := formStr(r, "date")
	if !validDate(date) {
		s.setFlash(w, r, "error", "Pick a skipped date to restore.")
		http.Redirect(w, r, seriesURL(sr.ID, ""), http.StatusSeeOther)
		return
	}
	if err := s.store.RestoreSkip(sr.ID, date, s.today()); err != nil {
		s.setFlash(w, r, "error", "Couldn't restore "+fmtDate(date)+": "+err.Error()+".")
		http.Redirect(w, r, seriesURL(sr.ID, ""), http.StatusSeeOther)
		return
	}
	s.redirect(w, r, seriesURL(sr.ID, ""), fmtDate(date)+" is back.")
}

// handleSeriesEnd sends "end the series from this date" on to the first
// occurrence on or after the date.
func (s *Server) handleSeriesEnd(w http.ResponseWriter, r *http.Request) {
	sr, occ, ok := s.series(w, r)
	if !ok {
		return
	}
	from := r.URL.Query().Get("from")
	if !validDate(from) {
		s.setFlash(w, r, "error", "Pick the date to end the series from.")
		http.Redirect(w, r, seriesURL(sr.ID, ""), http.StatusSeeOther)
		return
	}
	for _, e := range occ {
		if e.OccurrenceDate >= from {
			http.Redirect(w, r, eventURL(e.ID, "/end-series"), http.StatusSeeOther)
			return
		}
	}
	s.setFlash(w, r, "error", "There are no dates on or after "+fmtDate(from)+".")
	http.Redirect(w, r, seriesURL(sr.ID, ""), http.StatusSeeOther)
}

// End the series ------------------------------------------------------------

func (s *Server) handleEndSeriesForm(w http.ResponseWriter, r *http.Request) {
	e, ok := s.occurrence(w, r)
	if !ok || !s.laterVisible(w, r, e) {
		return
	}
	impacts, err := s.store.EndSeriesPreview(e.SeriesID, e.OccurrenceDate)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.eventPage(w, r, http.StatusOK, "events/end_series", e, "", map[string]any{
		"Title": "End the series", "Impacts": impacts, "Warn": anyWork(impacts),
	})
}

func anyWork(list []store.OccurrenceImpact) bool {
	for _, i := range list {
		if i.HasWork() {
			return true
		}
	}
	return false
}

func (s *Server) handleEndSeries(w http.ResponseWriter, r *http.Request) {
	e, ok := s.occurrence(w, r)
	if !ok || !s.laterVisible(w, r, e) {
		return
	}
	n, err := s.store.EndSeriesFrom(e.SeriesID, e.OccurrenceDate)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	msg := fmt.Sprintf("Series ended. %s removed from %s on.", pluralize(n, "date"), fmtDate(e.OccurrenceDate))
	if _, err := s.store.GetSeries(e.SeriesID); err != nil {
		s.redirect(w, r, "/events", msg)
		return
	}
	s.redirect(w, r, seriesURL(e.SeriesID, ""), msg)
}

// Make later dates like this one ---------------------------------------------

type sectionOpt struct {
	Key, Label string
	Checked    bool
}

var matchSections = []sectionOpt{
	{Key: store.SectionRooms, Label: "Rooms"},
	{Key: store.SectionSchedule, Label: "Timeline"},
	{Key: store.SectionTasks, Label: "Tasks (lists and tasks)"},
	{Key: store.SectionHelperJobs, Label: "Helper jobs"},
	{Key: store.SectionNeeds, Label: "Needs"},
}

func sectionOpts(chosen []string) ([]sectionOpt, []string) {
	opts := slices.Clone(matchSections)
	var keep []string
	for i := range opts {
		if slices.Contains(chosen, opts[i].Key) {
			opts[i].Checked = true
			keep = append(keep, opts[i].Key)
		}
	}
	return opts, keep
}

func (s *Server) matchLaterPage(w http.ResponseWriter, r *http.Request, status int, e *store.Event, chosen []string, errs []string) {
	opts, keep := sectionOpts(chosen)
	data := map[string]any{"Title": "Make later dates like this one", "Sections": opts, "Errors": errs}
	if len(keep) > 0 && errs == nil {
		impacts, err := s.store.MatchLaterPreview(e.ID, keep)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		data["Previewed"], data["Impacts"], data["Warn"] = true, impacts, anyWork(impacts)
	}
	s.eventPage(w, r, status, "events/match_later", e, "", data)
}

func (s *Server) handleMatchLaterForm(w http.ResponseWriter, r *http.Request) {
	e, ok := s.occurrence(w, r)
	if !ok || !s.laterVisible(w, r, e) {
		return
	}
	s.matchLaterPage(w, r, http.StatusOK, e, r.URL.Query()["section"], nil)
}

func (s *Server) handleMatchLater(w http.ResponseWriter, r *http.Request) {
	e, ok := s.occurrence(w, r)
	if !ok || !s.laterVisible(w, r, e) {
		return
	}
	_ = r.ParseForm()
	_, keep := sectionOpts(r.PostForm["section"])
	if len(keep) == 0 {
		s.matchLaterPage(w, r, http.StatusUnprocessableEntity, e, nil, []string{"Choose what the later dates should copy from this one."})
		return
	}
	n, err := s.store.MatchLater(e.ID, keep, currentUser(r).Name())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, seriesURL(e.SeriesID, ""), fmt.Sprintf("%s now match %s.", pluralize(n, "later date"), fmtDate(e.OccurrenceDate)))
}

// Change the repeat pattern ------------------------------------------------------

func (s *Server) repeatPage(w http.ResponseWriter, r *http.Request, status int, e *store.Event, f repeatForm, errs []string) {
	f.NoNever = true
	impacts, err := s.store.EndSeriesPreview(e.SeriesID, e.OccurrenceDate)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	sr, err := s.store.GetSeries(e.SeriesID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.eventPage(w, r, status, "events/repeat", e, "", map[string]any{
		"Title": "Change the repeat pattern", "Repeat": f, "Errors": errs, "Impacts": impacts, "Warn": anyWork(impacts),
		"Current": sr.Rule.Describe(),
	})
}

func (s *Server) handleRepeatForm(w http.ResponseWriter, r *http.Request) {
	e, ok := s.occurrence(w, r)
	if !ok || !s.laterVisible(w, r, e) {
		return
	}
	sr, err := s.store.GetSeries(e.SeriesID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.repeatPage(w, r, http.StatusOK, e, repeatFromRule(sr.Rule, e.OccurrenceDate), nil)
}

func (s *Server) handleRepeatUpdate(w http.ResponseWriter, r *http.Request) {
	e, ok := s.occurrence(w, r)
	if !ok || !s.laterVisible(w, r, e) {
		return
	}
	f, rule, set, errs := readRepeat(r, e.OccurrenceDate)
	if !set && errs == nil {
		errs = []string{"Pick how often it repeats. To stop repeating, end the series instead."}
	}
	if errs != nil {
		s.repeatPage(w, r, http.StatusUnprocessableEntity, e, f, errs)
		return
	}
	id, err := s.store.ChangePatternFrom(e.ID, rule, s.today())
	if err != nil {
		if errors.Is(err, store.ErrNotInSeries) {
			s.notFound(w, r)
			return
		}
		s.repeatPage(w, r, http.StatusUnprocessableEntity, e, f, []string{err.Error()})
		return
	}
	s.redirect(w, r, seriesURL(id, ""), repeatSummary(rule)+" from "+fmtDate(e.OccurrenceDate)+" on.")
}

// Helpers for the event handlers --------------------------------------------------

// makeSeries turns event id into a series and returns the flash message.
func (s *Server) makeSeries(id int64, rule recur.Rule) (string, error) {
	sid, err := s.store.MakeSeries(id, rule, s.today())
	if err != nil {
		return "", err
	}
	sr, err := s.store.GetSeries(sid)
	if err != nil {
		return "", err
	}
	occ, err := s.store.Occurrences(sid)
	if err != nil {
		return "", err
	}
	rule.Start = sr.Rule.Start
	return fmt.Sprintf("%s. %s added through %s.", repeatSummary(rule), pluralize(len(occ)-1, "date"), fmtDate(sr.GeneratedThrough)), nil
}

// detailsChange is the detail fields that differ between old and now.
func detailsChange(old, now store.Event) store.DetailsChange {
	str := func(a, b string) *string {
		if a == b {
			return nil
		}
		return &b
	}
	c := store.DetailsChange{
		Title: str(old.Title, now.Title), Kind: str(old.Kind, now.Kind),
		StartTime: str(old.StartTime, now.StartTime), EndTime: str(old.EndTime, now.EndTime),
		Coordinator: str(old.Coordinator, now.Coordinator), Description: str(old.Description, now.Description),
		PublicDescription: str(old.PublicDescription, now.PublicDescription),
		Visibility:        str(old.Visibility, now.Visibility), Notes: str(old.Notes, now.Notes),
	}
	if old.ExpectedCount != now.ExpectedCount {
		n := now.ExpectedCount
		c.ExpectedCount = &n
	}
	return c
}

func hasChange(c store.DetailsChange) bool {
	return c.Title != nil || c.Kind != nil || c.StartTime != nil || c.EndTime != nil || c.Coordinator != nil ||
		c.Description != nil || c.PublicDescription != nil || c.Visibility != nil || c.Notes != nil || c.ExpectedCount != nil
}
