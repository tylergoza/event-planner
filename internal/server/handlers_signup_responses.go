package server

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tylergoza/event-planner/internal/store"
)

// Responses to a sign-up form (SIGNUPS_PLAN.md step 3): the list, the CSV,
// the allergy list, and planners adding, editing and withdrawing sign-ups.
// Anyone who can see the event reads the list and counts; the event team
// (planners, admins and event_members) also sees contact details, the
// planner's notes, the CSV and the allergy list.

// signupEvent loads the event for the path ID and its form. A viewer who
// can't see the event, or an event with no form, gets "not found". team is
// whether the viewer is on the event's team.
func (s *Server) signupEvent(w http.ResponseWriter, r *http.Request, id int64) (e *store.Event, f *store.SignupForm, team, ok bool) {
	e, ok = s.eventByID(w, r, id)
	if !ok {
		return
	}
	var err error
	if f, err = s.store.SignupFormByEvent(e.ID); err != nil {
		s.serverError(w, r, err)
		return nil, nil, false, false
	}
	if team, err = s.canEditAgenda(e, currentUser(r)); err != nil {
		s.serverError(w, r, err)
		return nil, nil, false, false
	}
	return e, f, team, true
}

// signupTeam is signupEvent for pages only the team may use.
func (s *Server) signupTeam(w http.ResponseWriter, r *http.Request) (*store.Event, *store.SignupForm, bool) {
	e, f, team, ok := s.signupEvent(w, r, pathID(r))
	if !ok {
		return nil, nil, false
	}
	if !team {
		s.render(w, r, http.StatusForbidden, "error", map[string]any{
			"Title": "Event team only", "Message": "Only planners and the people on this event can see that.",
		})
		return nil, nil, false
	}
	return e, f, true
}

func signupsURL(eventID int64) string { return eventURL(eventID, "/signups") }

type srChoice struct {
	Label    string
	Taken    int
	Spots    int
	Full     bool
	Retired  bool
	Selected bool
	Href     string
}

type srCount struct {
	Question string
	Retired  bool
	Choices  []srChoice
}

type srSignup struct {
	S      store.Signup
	Lines  []suLine
	People []suPersonShow
}

type srPerson struct {
	Name, Allergies, Signer, Phone, Email string
	SignupID                              int64
	Withdrawn                             bool
	Lines                                 []suLine
}

func (s *Server) handleSignups(w http.ResponseWriter, r *http.Request) {
	e, f, team, ok := s.signupEvent(w, r, pathID(r))
	if !ok {
		return
	}
	qs, err := s.store.SignupQuestions(e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	q := r.URL.Query()
	person := q.Get("view") == "person"
	withdrawn := q.Get("withdrawn") == "1"
	choice := queryInt(r, "choice")
	href := func(view string, choice int64, withdrawn bool) string {
		v := url.Values{}
		if view == "person" {
			v.Set("view", "person")
		}
		if choice != 0 {
			v.Set("choice", strconv.FormatInt(choice, 10))
		}
		if withdrawn {
			v.Set("withdrawn", "1")
		}
		if len(v) == 0 {
			return signupsURL(e.ID)
		}
		return signupsURL(e.ID) + "?" + v.Encode()
	}
	view := ""
	if person {
		view = "person"
	}

	var counts []srCount
	var choiceName string
	for _, x := range qs {
		if !store.IsPickKind(x.Kind) {
			continue
		}
		c := srCount{Question: x.Label, Retired: x.Retired}
		for _, ch := range x.Choices {
			if ch.Retired && ch.Taken == 0 {
				continue
			}
			c.Choices = append(c.Choices, srChoice{Label: ch.Label, Taken: ch.Taken, Spots: ch.Spots, Full: ch.Full(), Retired: ch.Retired,
				Selected: ch.ID == choice, Href: href(view, ch.ID, withdrawn)})
			if ch.ID == choice {
				choiceName = ch.Label
			}
		}
		if len(c.Choices) > 0 {
			counts = append(counts, c)
		}
	}

	all, err := s.store.ListSignups(e.ID, withdrawn)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	people, guests, err := s.store.SignupHeadcount(e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	picked := func(su store.Signup, personID int64) bool {
		if choice == 0 {
			return true
		}
		for _, a := range su.Answers {
			if a.ChoiceID == choice && (personID == 0 || a.PersonID == 0 || a.PersonID == personID) {
				return true
			}
		}
		return false
	}
	// Contact details, notes and allergies are for the team only.
	hide := func(su store.Signup) store.Signup {
		if !team {
			su.Phone, su.Email, su.PlannerNote = "", "", ""
			ps := make([]store.SignupPerson, len(su.People))
			copy(ps, su.People)
			for i := range ps {
				ps[i].Allergies = ""
			}
			su.People = ps
		}
		return su
	}
	var bySignup []srSignup
	var byPerson []srPerson
	for _, raw := range all {
		su := hide(raw)
		if !person {
			if !picked(su, 0) {
				continue
			}
			row := srSignup{S: su, Lines: describe(qs, su.Answers, 0)}
			for _, p := range su.People {
				row.People = append(row.People, suPersonShow{Name: p.Name, Allergies: p.Allergies, Lines: describe(qs, su.Answers, p.ID)})
			}
			bySignup = append(bySignup, row)
			continue
		}
		for _, p := range su.People {
			if !picked(su, p.ID) {
				continue
			}
			lines := append(describe(qs, su.Answers, p.ID), describe(qs, su.Answers, 0)...)
			byPerson = append(byPerson, srPerson{Name: p.Name, Allergies: p.Allergies, Signer: su.Name, Phone: su.Phone, Email: su.Email,
				SignupID: su.ID, Withdrawn: su.Status == "withdrawn", Lines: lines})
		}
	}
	sortPeople(byPerson)

	state, err := s.store.SignupState(f, e, time.Now())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.eventPage(w, r, http.StatusOK, "signups/index", e, "signups", map[string]any{
		"Title": "Sign-ups · " + e.Title, "Form": f, "State": state, "StateText": signupStateWords(state),
		"Team": team, "Counts": counts, "People": people, "Guests": guests, "Headcount": people + guests,
		"Person": person, "Withdrawn": withdrawn, "Choice": choice, "ChoiceName": choiceName,
		"BySignup": bySignup, "ByPerson": byPerson, "Live": true,
		"SignupHref": href("", choice, withdrawn), "PersonHref": href("person", choice, withdrawn),
		"ClearHref": href(view, 0, withdrawn), "WithdrawnHref": href(view, choice, !withdrawn),
		"CSVHref": signupsURL(e.ID) + ".csv" + csvQuery(withdrawn),
	})
}

func csvQuery(withdrawn bool) string {
	if withdrawn {
		return "?withdrawn=1"
	}
	return ""
}

func sortPeople(l []srPerson) {
	for i := 1; i < len(l); i++ {
		for j := i; j > 0 && strings.ToLower(l[j].Name) < strings.ToLower(l[j-1].Name); j-- {
			l[j], l[j-1] = l[j-1], l[j]
		}
	}
}

func signupStateWords(state string) string {
	switch state {
	case store.SignupDraft:
		return "Draft: not open to anyone yet"
	case store.SignupOpen:
		return "Open"
	case store.SignupFull:
		return "Full"
	}
	return "Closed"
}

var safeNameRe = regexp.MustCompile(`[^A-Za-z0-9]+`)

// csvFileName is "<event-title>-signups.csv" with anything odd turned into dashes.
func csvFileName(title string) string {
	n := strings.Trim(safeNameRe.ReplaceAllString(title, "-"), "-")
	if n == "" {
		n = "event"
	}
	if len(n) > 60 {
		n = strings.Trim(n[:60], "-")
	}
	return n + "-signups.csv"
}

var plainNumberRe = regexp.MustCompile(`^[+-]?[0-9][0-9\s().-]*$`)

// csvSafe stops a spreadsheet from running typed text as a formula.
func csvSafe(v string) string {
	if v == "" {
		return v
	}
	switch v[0] {
	case '=', '@', '\t', '\r':
		return "'" + v
	case '+', '-':
		if !plainNumberRe.MatchString(v) {
			return "'" + v
		}
	}
	return v
}

func (s *Server) handleSignupsCSV(w http.ResponseWriter, r *http.Request) {
	e, _, ok := s.signupTeam(w, r)
	if !ok {
		return
	}
	rows, err := s.store.SignupCSV(e.ID, r.URL.Query().Get("withdrawn") == "1")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+csvFileName(e.Title)+`"`)
	w.Header().Set("Cache-Control", "private, no-store")
	cw := csv.NewWriter(w)
	for i, row := range rows {
		if i > 0 {
			out := make([]string, len(row))
			for j, c := range row {
				out[j] = csvSafe(c)
			}
			row = out
		}
		cw.Write(row)
	}
	cw.Flush()
}

func (s *Server) handleAllergies(w http.ResponseWriter, r *http.Request) {
	e, f, ok := s.signupTeam(w, r)
	if !ok {
		return
	}
	rows, err := s.store.SignupAllergies(e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.eventPage(w, r, http.StatusOK, "signups/allergies", e, "signups", map[string]any{
		"Title": "Allergies · " + e.Title, "Form": f, "Rows": rows,
	})
}

// ---- planners add, edit, withdraw ----

// plannerCtx builds the form context for a planner working on a sign-up.
func (s *Server) plannerCtx(w http.ResponseWriter, r *http.Request, e *store.Event, f *store.SignupForm, su *store.Signup) (*suCtx, bool) {
	c, err := s.loadCtx(f)
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	c.user, c.planner, c.existing = currentUser(r), true, su
	return c, true
}

// plannerFormData adjusts the public form's page for a planner: every
// contact box, the form shown even as a draft or closed, and the planner's
// own action and way back.
func (s *Server) plannerFormData(c *suCtx, data map[string]any) {
	f := *c.form
	f.Contact = "phone_or_email"
	data["Form"] = &f
	data["Preview"], data["StateText"], data["ShowForm"], data["CanSubmit"] = false, "", true, true
	data["Lists"] = nil
	data["Cancel"] = signupsURL(c.form.EventID)
	data["BackLabel"] = "Back to the sign-ups"
	data["Submit"] = "Save sign-up"
	if c.existing != nil {
		data["Action"] = fmt.Sprintf("/signups/%d/edit", c.existing.ID)
		data["Title"] = "Edit sign-up · " + c.ev.Title
	} else {
		data["Action"] = eventURL(c.form.EventID, "/signups/new")
		data["Title"] = "Add a sign-up · " + c.ev.Title
	}
}

func (s *Server) handleSignupNew(w http.ResponseWriter, r *http.Request) {
	e, f, _, ok := s.signupEvent(w, r, pathID(r))
	if !ok {
		return
	}
	c, ok := s.plannerCtx(w, r, e, f, nil)
	if !ok {
		return
	}
	s.renderSignupForm(w, r, c, formVals{}, nil, http.StatusOK)
}

func (s *Server) handleSignupNewSave(w http.ResponseWriter, r *http.Request) {
	e, f, _, ok := s.signupEvent(w, r, pathID(r))
	if !ok {
		return
	}
	c, ok := s.plannerCtx(w, r, e, f, nil)
	if !ok {
		return
	}
	s.plannerSave(w, r, c)
}

// plannerSignup loads the sign-up named in the path with its event and form.
func (s *Server) plannerSignup(w http.ResponseWriter, r *http.Request) (*store.Signup, *store.Event, *store.SignupForm, bool) {
	su, err := s.store.GetSignup(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, nil, nil, false
	}
	e, f, _, ok := s.signupEvent(w, r, su.EventID)
	if !ok {
		return nil, nil, nil, false
	}
	return su, e, f, true
}

func (s *Server) handleSignupPlannerEdit(w http.ResponseWriter, r *http.Request) {
	su, e, f, ok := s.plannerSignup(w, r)
	if !ok {
		return
	}
	c, ok := s.plannerCtx(w, r, e, f, su)
	if !ok {
		return
	}
	s.renderSignupForm(w, r, c, formVals{}, nil, http.StatusOK)
}

func (s *Server) handleSignupPlannerSave(w http.ResponseWriter, r *http.Request) {
	su, e, f, ok := s.plannerSignup(w, r)
	if !ok {
		return
	}
	c, ok := s.plannerCtx(w, r, e, f, su)
	if !ok {
		return
	}
	s.plannerSave(w, r, c)
}

// plannerSave saves a hand-entered sign-up. Going over spots or "can't be
// picked with" saves anyway and says so in a flash.
func (s *Server) plannerSave(w http.ResponseWriter, r *http.Request, c *suCtx) {
	if err := r.ParseForm(); err != nil {
		s.notFoundFor(w, r)
		return
	}
	vals := formVals(r.PostForm)
	if vals.Get("more") != "" {
		n := len(peopleRows(c, vals, false))
		vals["people_n"] = []string{strconv.Itoa(n + 3)}
		s.renderSignupForm(w, r, c, vals, nil, http.StatusOK)
		return
	}
	in, errs := s.readSignup(c, vals)
	if len(errs) == 0 {
		res, err := s.store.SaveSignup(in, store.SignupOptions{Planner: true})
		var se *store.SignupError
		switch {
		case errors.As(err, &se):
			errs = se.Messages
		case err != nil:
			s.serverError(w, r, err)
			return
		default:
			msg, kind := "Sign-up saved.", "success"
			if len(res.Warnings) > 0 {
				msg, kind = "Sign-up saved, with warnings: "+strings.Join(res.Warnings, "; ")+".", "error"
			}
			s.setFlash(w, r, kind, msg)
			http.Redirect(w, r, signupsURL(c.form.EventID), http.StatusSeeOther)
			return
		}
	}
	s.renderSignupForm(w, r, c, vals, errs, http.StatusUnprocessableEntity)
}

func (s *Server) handleSignupPlannerWithdraw(w http.ResponseWriter, r *http.Request) {
	su, e, _, ok := s.plannerSignup(w, r)
	if !ok {
		return
	}
	if err := s.store.WithdrawSignup(su.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, signupsURL(e.ID), su.Name+"'s sign-up is withdrawn. Their spots are free.")
}

func (s *Server) handleSignupNote(w http.ResponseWriter, r *http.Request) {
	su, e, _, ok := s.plannerSignup(w, r)
	if !ok {
		return
	}
	note := formStr(r, "note")
	if len(note) > 2000 {
		note = note[:2000]
	}
	if err := s.store.SetSignupNote(su.ID, note); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, signupsURL(e.ID)+fmt.Sprintf("#signup-%d", su.ID), "Note saved.")
}

// ---- the event page and the dashboard ----

type signupSummary struct {
	Text      string
	Allergies int // count for the team; -1: don't show the link
	Team      bool
}

func peopleText(n int) string {
	if n == 1 {
		return "1 person"
	}
	return strconv.Itoa(n) + " people"
}

// signupSummaryFor is the line under an event: nil when the event has no
// form, or the form is a draft and the viewer isn't a planner.
func (s *Server) signupSummaryFor(e *store.Event, u *store.User, team bool) (*signupSummary, error) {
	f, err := s.store.SignupFormByEvent(e.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	state, err := s.store.SignupState(f, e, time.Now())
	if err != nil {
		return nil, err
	}
	if state == store.SignupDraft && !u.CanPlan() {
		return nil, nil
	}
	people, guests, err := s.store.SignupHeadcount(e.ID)
	if err != nil {
		return nil, err
	}
	out := &signupSummary{Allergies: -1, Team: team}
	n := peopleText(people + guests)
	switch state {
	case store.SignupDraft:
		out.Text = "Sign-ups: the form is a draft, not open yet"
	case store.SignupFull:
		out.Text = "Sign-ups: " + n + ", full"
	case store.SignupClosed:
		out.Text = "Sign-ups: " + n + ", closed"
	default:
		until := f.ClosesAt
		if len(until) >= 10 {
			until = until[:10]
		} else {
			until = e.StartsOn
		}
		out.Text = "Sign-ups: " + n + ", open until " + shortDate(until)
	}
	if team && f.AskAllergies {
		list, err := s.store.SignupAllergies(e.ID)
		if err != nil {
			return nil, err
		}
		out.Allergies = len(list)
	}
	return out, nil
}

func shortDate(d string) string {
	t, err := time.Parse(store.DateLayout, d)
	if err != nil {
		return d
	}
	return t.Format("Jan 2")
}

type openSignup struct {
	ID    int64
	Title string
	Text  string
}

// openSignups lists the dashboard's events whose form is taking sign-ups.
func (s *Server) openSignups(list []store.EventSummary) ([]openSignup, error) {
	var out []openSignup
	for _, ev := range list {
		e := ev.Event
		f, err := s.store.SignupFormByEvent(e.ID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			return nil, err
		}
		state, err := s.store.SignupState(f, &e, time.Now())
		if err != nil {
			return nil, err
		}
		if state != store.SignupOpen && state != store.SignupFull {
			continue
		}
		people, guests, err := s.store.SignupHeadcount(e.ID)
		if err != nil {
			return nil, err
		}
		t := strconv.Itoa(people+guests) + " signed up"
		if state == store.SignupFull {
			t += ", full"
		}
		out = append(out, openSignup{ID: e.ID, Title: e.Title, Text: t})
	}
	return out, nil
}
