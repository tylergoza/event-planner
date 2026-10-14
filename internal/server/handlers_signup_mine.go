package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/tylergoza/event-planner/internal/store"
)

// A signer's own pages: the status link (/signup/{code}/s/{scode}, found by
// the code in the link) and, for signed-in people, /my/signups.

// ownCtx finds the sign-up a request is about: by status-link code on
// /signup/ paths, by ID (and owner) on /my/signups/ paths.
func (s *Server) ownCtx(w http.ResponseWriter, r *http.Request) (*suCtx, bool) {
	var su *store.Signup
	var f *store.SignupForm
	var err error
	base := ""
	if code := r.PathValue("code"); code != "" {
		f, err = s.store.SignupFormByCode(code)
		if err == nil {
			su, err = s.store.GetSignupByCode(r.PathValue("scode"))
		}
		if err == nil && su.EventID != f.EventID {
			err = store.ErrNotFound
		}
		base = "/signup/" + code + "/s/" + r.PathValue("scode")
	} else {
		su, err = s.store.GetSignup(pathID(r))
		if err == nil && su.UserID != currentUser(r).ID {
			err = store.ErrNotFound
		}
		if err == nil {
			f, err = s.store.SignupFormByEvent(su.EventID)
		}
		base = "/my/signups/" + strconv.FormatInt(pathID(r), 10)
	}
	if errors.Is(err, store.ErrNotFound) {
		s.notFoundFor(w, r)
		return nil, false
	} else if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	c, err := s.loadCtx(f)
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	c.user, c.existing, c.base = currentUser(r), su, base
	return c, true
}

type suLine struct{ Label, Text string }

type suPersonShow struct {
	Name      string
	Allergies string
	Lines     []suLine
}

// describe lists the answers to the questions asked once (personID 0) or
// to one person, in the order of the form.
func describe(qs []store.SignupQuestion, answers []store.SignupAnswer, personID int64) []suLine {
	var out []suLine
	for _, q := range qs {
		if q.PerPerson != (personID != 0) {
			continue
		}
		var parts []string
		for _, a := range answers {
			if a.QuestionID != q.ID || a.PersonID != personID {
				continue
			}
			if a.ChoiceID != 0 {
				for _, ch := range q.Choices {
					if ch.ID == a.ChoiceID {
						parts = append(parts, ch.Label)
					}
				}
			} else if q.Kind == "yes_no" {
				parts = append(parts, strings.ToUpper(a.Value[:1])+a.Value[1:])
			} else if a.Value != "" {
				parts = append(parts, a.Value)
			}
		}
		if len(parts) > 0 {
			out = append(out, suLine{q.Label, strings.Join(parts, ", ")})
		}
	}
	return out
}

func (s *Server) handleSignupStatus(w http.ResponseWriter, r *http.Request) {
	c, ok := s.ownCtx(w, r)
	if !ok {
		return
	}
	su := c.existing
	var people []suPersonShow
	for _, p := range su.People {
		people = append(people, suPersonShow{Name: p.Name, Allergies: p.Allergies, Lines: describe(c.qs, su.Answers, p.ID)})
	}
	s.renderCalendarPage(w, r, "signup/status", map[string]any{
		"Title": c.ev.Title, "Event": c.ev, "When": c.ev.When(), "Form": c.form, "S": su, "Base": c.base,
		"Lines": describe(c.qs, su.Answers, 0), "People": people, "ListPeople": c.form.People == "list",
		"CanChange": c.canChange(), "StateText": suStateMessage(c.state), "Withdrawn": su.Status == "withdrawn",
		"Mine": strings.HasPrefix(c.base, "/my/"),
	})
}

func (s *Server) handleSignupEdit(w http.ResponseWriter, r *http.Request) {
	c, ok := s.ownCtx(w, r)
	if !ok {
		return
	}
	if !c.canChange() {
		s.redirect(w, r, c.base, "")
		return
	}
	s.renderSignupForm(w, r, c, formVals{}, nil, http.StatusOK)
}

func (s *Server) handleSignupChange(w http.ResponseWriter, r *http.Request) {
	c, ok := s.ownCtx(w, r)
	if !ok {
		return
	}
	if r.PathValue("code") != "" && s.limited(w, r) {
		return
	}
	s.submitSignup(w, r, c)
}

func (s *Server) handleSignupWithdraw(w http.ResponseWriter, r *http.Request) {
	c, ok := s.ownCtx(w, r)
	if !ok {
		return
	}
	if r.PathValue("code") != "" && s.limited(w, r) {
		return
	}
	if !c.canChange() {
		s.redirect(w, r, c.base, "")
		return
	}
	if err := s.store.WithdrawSignup(c.existing.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.countSignup(r)
	s.redirect(w, r, c.base, "You're withdrawn. Your spot is free for someone else.")
}

type suMine struct {
	store.UserSignup
	When string
}

func (s *Server) handleMySignups(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.SignupsByUser(currentUser(r).ID, s.today())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var rows []suMine
	for _, u := range list {
		ev := store.Event{StartsOn: u.StartsOn, StartTime: u.StartTime}
		rows = append(rows, suMine{u, ev.When()})
	}
	s.render(w, r, http.StatusOK, "signup/mine", map[string]any{"Title": "My sign-ups", "Rows": rows})
}
