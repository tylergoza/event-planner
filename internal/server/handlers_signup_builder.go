package server

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/tylergoza/event-planner/internal/store"
)

// The Sign-up section on /events/{id}/edit: the form's settings, its
// questions and their choices. Every action redirects back to the section
// with a message; none of it needs JavaScript.

// suWhenOption is a choice a question can be made to depend on.
type suWhenOption struct {
	ID       int64
	Label    string
	Selected bool
}

type suChoice struct {
	store.SignupChoice
	Excludable []suWhenOption
}

type suQuestion struct {
	store.SignupQuestion
	Number int
	When   []suWhenOption
	Items  []suChoice
	IsPick bool
}

// suData is what the template needs for the section.
type suData struct {
	Form      *store.SignupForm
	StateText string
	Questions []suQuestion
	NewQ      suQuestion // the add-question form
	Kinds     []suKind
}

type suKind struct{ Value, Label string }

var suKinds = []suKind{
	{"one", "Pick one"}, {"many", "Pick any"}, {"text", "Short answer"},
	{"long_text", "Long answer"}, {"number", "Number"}, {"yes_no", "Yes or no"},
}

var suStateText = map[string]string{
	store.SignupDraft:  "Draft: not open yet",
	store.SignupOpen:   "Open",
	store.SignupFull:   "Full",
	store.SignupClosed: "Closed",
}

func suAnchor(questionID int64) string { return "#sq-" + strconv.FormatInt(questionID, 10) }

// signupSection builds the section for an existing event; nil when the
// event has no form.
func (s *Server) signupSection(e *store.Event) (*suData, error) {
	f, err := s.store.SignupFormByEvent(e.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	state, err := s.store.SignupState(f, e, time.Now())
	if err != nil {
		return nil, err
	}
	qs, err := s.store.SignupQuestions(e.ID)
	if err != nil {
		return nil, err
	}
	d := &suData{Form: f, StateText: suStateText[state], Kinds: suKinds}
	labelOf := map[int64]string{}
	for _, q := range qs {
		for _, c := range q.Choices {
			labelOf[c.ID] = q.Label + ": " + c.Label
		}
	}
	n := 0
	for i, q := range qs {
		v := suQuestion{SignupQuestion: q, IsPick: store.IsPickKind(q.Kind)}
		if !q.Retired {
			n++
			v.Number = n
		}
		picked := map[int64]bool{}
		for _, id := range q.When {
			picked[id] = true
		}
		// Choices of earlier questions that aren't conditional themselves.
		for _, p := range qs[:i] {
			if len(p.When) > 0 {
				continue
			}
			for _, c := range p.Choices {
				if (p.Retired || c.Retired) && !picked[c.ID] {
					continue
				}
				v.When = append(v.When, suWhenOption{ID: c.ID, Label: labelOf[c.ID], Selected: picked[c.ID]})
			}
		}
		for _, c := range q.Choices {
			sc := suChoice{SignupChoice: c}
			has := map[int64]bool{}
			for _, id := range c.Excludes {
				has[id] = true
			}
			for _, o := range qs {
				if o.PerPerson != q.PerPerson {
					continue
				}
				for _, oc := range o.Choices {
					if oc.ID == c.ID || ((o.Retired || oc.Retired) && !has[oc.ID]) {
						continue
					}
					sc.Excludable = append(sc.Excludable, suWhenOption{ID: oc.ID, Label: labelOf[oc.ID], Selected: has[oc.ID]})
				}
			}
			v.Items = append(v.Items, sc)
		}
		d.Questions = append(d.Questions, v)
	}
	d.NewQ = suQuestion{SignupQuestion: store.SignupQuestion{Kind: "one"}}
	for _, q := range d.Questions {
		if q.Retired || len(q.When) > 0 {
			continue
		}
		for _, c := range q.Choices {
			if !c.Retired {
				d.NewQ.When = append(d.NewQ.When, suWhenOption{ID: c.ID, Label: labelOf[c.ID]})
			}
		}
	}
	return d, nil
}

// suFail sends the planner back with the store's message, or shows 404.
func (s *Server) suFail(w http.ResponseWriter, r *http.Request, back string, err error) {
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	s.log.Info("sign-up builder", "path", r.URL.Path, "err", err)
	s.setFlash(w, r, "error", err.Error())
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) suQuestionEvent(w http.ResponseWriter, r *http.Request) (*store.SignupQuestion, *store.Event, bool) {
	q, err := s.store.SignupQuestion(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, nil, false
	}
	e, ok := s.eventByID(w, r, q.EventID)
	return q, e, ok
}

func (s *Server) suChoiceEvent(w http.ResponseWriter, r *http.Request) (qid int64, e *store.Event, ok bool) {
	qid, eid, err := s.store.SignupChoiceOf(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return 0, nil, false
	}
	e, ok = s.eventByID(w, r, eid)
	return qid, e, ok
}

func (s *Server) handleSignupFormCreate(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	back := eventURL(e.ID, "/edit#signup")
	if _, err := s.store.CreateSignupForm(e.ID); err != nil {
		s.suFail(w, r, back, err)
		return
	}
	s.redirect(w, r, back, "Sign-up form added. It's a draft until you open it.")
}

func (s *Server) handleSignupFormSettings(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	back := eventURL(e.ID, "/edit#signup")
	f, err := s.store.SignupFormByEvent(e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f.Label, f.Intro, f.ThankYou = formStr(r, "label"), formStr(r, "intro"), formStr(r, "thank_you")
	f.Status, f.Audience, f.Contact, f.People = formStr(r, "status"), formStr(r, "audience"), formStr(r, "contact"), formStr(r, "people")
	f.AskGuests, f.AskAllergies, f.ShowSpots = formStr(r, "ask_guests") != "", formStr(r, "ask_allergies") != "", formStr(r, "show_spots") != ""
	f.ClosesAt = formStr(r, "closes_at")
	if err := s.store.UpdateSignupForm(f); err != nil {
		s.suFail(w, r, back, err)
		return
	}
	s.redirect(w, r, back, "Sign-up settings saved.")
}

func (s *Server) handleSignupFormDelete(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	back := eventURL(e.ID, "/edit#signup")
	if err := s.store.DeleteSignupForm(e.ID); err != nil {
		s.suFail(w, r, back, err)
		return
	}
	s.redirect(w, r, back, "Sign-up form removed.")
}

// readSignupQuestion reads the question fields (add and edit share them).
func readSignupQuestion(r *http.Request) store.SignupQuestionInput {
	r.ParseForm()
	in := store.SignupQuestionInput{
		Kind: formStr(r, "kind"), Label: formStr(r, "label"), Help: formStr(r, "help"),
		Required: formStr(r, "required") != "", PerPerson: formStr(r, "per_person") != "", PublicList: formStr(r, "public_list") != "",
	}
	in.When = formIDs(r, "when")
	return in
}

// formIDs reads every positive whole number sent under key.
func formIDs(r *http.Request, key string) []int64 {
	var out []int64
	for _, v := range r.PostForm[key] {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out
}

func (s *Server) handleSignupQuestionCreate(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	back := eventURL(e.ID, "/edit#signup-add")
	id, err := s.store.CreateSignupQuestion(e.ID, readSignupQuestion(r))
	if err != nil {
		s.suFail(w, r, back, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/edit"+suAnchor(id)), "Question added.")
}

func (s *Server) handleSignupQuestionUpdate(w http.ResponseWriter, r *http.Request) {
	q, e, ok := s.suQuestionEvent(w, r)
	if !ok {
		return
	}
	back := eventURL(e.ID, "/edit"+suAnchor(q.ID))
	if err := s.store.UpdateSignupQuestion(q.ID, readSignupQuestion(r)); err != nil {
		s.suFail(w, r, back, err)
		return
	}
	s.redirect(w, r, back, "Question saved.")
}

func (s *Server) handleSignupQuestionDelete(w http.ResponseWriter, r *http.Request) {
	q, e, ok := s.suQuestionEvent(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteSignupQuestion(q.ID); err != nil {
		s.suFail(w, r, eventURL(e.ID, "/edit"+suAnchor(q.ID)), err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/edit#signup"), "Question deleted.")
}

func (s *Server) handleSignupQuestionRetire(w http.ResponseWriter, r *http.Request) {
	q, e, ok := s.suQuestionEvent(w, r)
	if !ok {
		return
	}
	back := eventURL(e.ID, "/edit"+suAnchor(q.ID))
	retire := formStr(r, "undo") == ""
	if err := s.store.RetireSignupQuestion(q.ID, retire); err != nil {
		s.suFail(w, r, back, err)
		return
	}
	msg := "Question retired. It's off the form and its answers are kept."
	if !retire {
		msg = "Question is back on the form."
	}
	s.redirect(w, r, back, msg)
}

func (s *Server) handleSignupQuestionMove(w http.ResponseWriter, r *http.Request) {
	q, e, ok := s.suQuestionEvent(w, r)
	if !ok {
		return
	}
	back := eventURL(e.ID, "/edit"+suAnchor(q.ID))
	dir := 1
	if formStr(r, "dir") == "up" {
		dir = -1
	}
	if err := s.store.MoveSignupQuestion(q.ID, dir); err != nil {
		s.suFail(w, r, back, err)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) handleSignupChoiceCreate(w http.ResponseWriter, r *http.Request) {
	q, e, ok := s.suQuestionEvent(w, r)
	if !ok {
		return
	}
	back := eventURL(e.ID, "/edit"+suAnchor(q.ID))
	spots, err := parseSpots(formStr(r, "spots"))
	if err == nil {
		_, err = s.store.CreateSignupChoice(q.ID, formStr(r, "label"), spots)
	}
	if err != nil {
		s.suFail(w, r, back, err)
		return
	}
	s.redirect(w, r, back, "Choice added.")
}

func (s *Server) handleSignupChoicePaste(w http.ResponseWriter, r *http.Request) {
	q, e, ok := s.suQuestionEvent(w, r)
	if !ok {
		return
	}
	back := eventURL(e.ID, "/edit"+suAnchor(q.ID))
	n, err := s.store.AddSignupChoices(q.ID, r.PostFormValue("text"))
	if err != nil {
		s.suFail(w, r, back, err)
		return
	}
	if n == 0 {
		s.setFlash(w, r, "error", "Nothing to add: put one choice on each line.")
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	s.redirect(w, r, back, pluralize(n, "choice")+" added.")
}

// parseSpots reads the spots box: blank means no limit.
func parseSpots(v string) (int, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 9999 {
		return 0, errors.New("Spots should be a whole number above 0, or blank for no limit.")
	}
	return n, nil
}

func (s *Server) handleSignupChoiceUpdate(w http.ResponseWriter, r *http.Request) {
	qid, e, ok := s.suChoiceEvent(w, r)
	if !ok {
		return
	}
	back := eventURL(e.ID, "/edit"+suAnchor(qid))
	r.ParseForm()
	spots, err := parseSpots(formStr(r, "spots"))
	if err == nil {
		err = s.store.UpdateSignupChoice(pathID(r), formStr(r, "label"), spots)
	}
	if err == nil {
		err = s.store.SetChoiceExcludes(pathID(r), formIDs(r, "excludes"))
	}
	if err != nil {
		s.suFail(w, r, back, err)
		return
	}
	s.redirect(w, r, back, "Choice saved.")
}

func (s *Server) handleSignupChoiceDelete(w http.ResponseWriter, r *http.Request) {
	qid, e, ok := s.suChoiceEvent(w, r)
	if !ok {
		return
	}
	back := eventURL(e.ID, "/edit"+suAnchor(qid))
	if err := s.store.DeleteSignupChoice(pathID(r)); err != nil {
		s.suFail(w, r, back, err)
		return
	}
	s.redirect(w, r, back, "Choice deleted.")
}

func (s *Server) handleSignupChoiceRetire(w http.ResponseWriter, r *http.Request) {
	qid, e, ok := s.suChoiceEvent(w, r)
	if !ok {
		return
	}
	back := eventURL(e.ID, "/edit"+suAnchor(qid))
	retire := formStr(r, "undo") == ""
	if err := s.store.RetireSignupChoice(pathID(r), retire); err != nil {
		s.suFail(w, r, back, err)
		return
	}
	msg := "Choice retired. Nobody new can pick it."
	if !retire {
		msg = "Choice is back."
	}
	s.redirect(w, r, back, msg)
}

func (s *Server) handleSignupChoiceMove(w http.ResponseWriter, r *http.Request) {
	qid, e, ok := s.suChoiceEvent(w, r)
	if !ok {
		return
	}
	back := eventURL(e.ID, "/edit"+suAnchor(qid))
	dir := 1
	if formStr(r, "dir") == "up" {
		dir = -1
	}
	if err := s.store.MoveSignupChoice(pathID(r), dir); err != nil {
		s.suFail(w, r, back, err)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
