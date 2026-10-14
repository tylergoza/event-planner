package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tylergoza/event-planner/internal/store"
)

// The public sign-up form (SIGNUPS_PLAN.md steps 2 and 4).
//
// Field names on the form (also how an edit is pre-filled):
//
//	name, phone, email    the signer, visitors only
//	guests                "how many more guests" when the form asks
//	website               honeypot: hidden by CSS, a person leaves it empty
//	q{qid}                the answer to a per-sign-up question: the value is
//	                      a choice ID (repeated for "pick any"), "yes"/"no",
//	                      or the text/number typed
//	people_n              how many person rows are on the page (list forms)
//	p{n}_id               the existing person's ID when editing, else empty
//	p{n}_name             person n's name (n counts from 0)
//	p{n}_allergies        person n's allergies box (self forms use p0_)
//	p{n}_q{qid}           person n's answer to a per-person question
//	more                  the "Add more people" button (no JavaScript)

// formVals is a submitted (or rebuilt) form, read by the templates too.
type formVals url.Values

func (v formVals) Get(k string) string {
	if l := v[k]; len(l) > 0 {
		return l[0]
	}
	return ""
}

func (v formVals) Has(k, val string) bool {
	for _, x := range v[k] {
		if x == val {
			return true
		}
	}
	return false
}

// suCtx is one sign-up page's context: the form, its event, who's
// looking and, for a status or change page, the sign-up.
type suCtx struct {
	form     *store.SignupForm
	ev       *store.Event
	qs       []store.SignupQuestion
	state    string
	user     *store.User
	existing *store.Signup
	base     string // where an existing sign-up's pages live
	planner  bool   // a planner adding or editing by hand (handlers_signup_responses.go)
}

func (c *suCtx) preview() bool { return c.state == store.SignupDraft }

// asUser: the signer's name comes from an account, so no name or contact is asked.
func (c *suCtx) asUser() bool {
	if c.existing != nil {
		return c.existing.UserID != 0
	}
	return c.user != nil && !c.planner
}

// canSubmitNew: a new sign-up can be sent.
func (c *suCtx) canSubmitNew() bool { return c.state == store.SignupOpen }

// canChange: an existing sign-up can be changed or withdrawn.
func (c *suCtx) canChange() bool {
	return c.existing != nil && c.existing.Status == "in" && (c.state == store.SignupOpen || c.state == store.SignupFull)
}

// inAudience says whether u (nil: a visitor) may use the form. A visitor
// needs an "anyone" form on a public, unlocked event; a signed-in person
// needs to be able to see the event.
func (s *Server) inAudience(f *store.SignupForm, ev *store.Event, u *store.User) (bool, error) {
	if u == nil {
		return f.Audience == "anyone" && ev.Visibility == "public" && !ev.Locked, nil
	}
	return s.store.CanSee(ev, u)
}

func (s *Server) loadCtx(f *store.SignupForm) (*suCtx, error) {
	ev, err := s.store.GetEvent(f.EventID)
	if err != nil {
		return nil, err
	}
	qs, err := s.store.SignupQuestions(f.EventID)
	if err != nil {
		return nil, err
	}
	state, err := s.store.SignupState(f, ev, time.Now())
	if err != nil {
		return nil, err
	}
	return &suCtx{form: f, ev: ev, qs: qs, state: state}, nil
}

// publicCtx loads the form behind /signup/{code} for the viewer, or
// answers 404 (not in the audience, or a draft the viewer can't preview).
func (s *Server) publicCtx(w http.ResponseWriter, r *http.Request) (*suCtx, bool) {
	f, err := s.store.SignupFormByCode(r.PathValue("code"))
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
	c.user = currentUser(r)
	ok, err := s.inAudience(f, c.ev, c.user)
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	if !ok || (c.preview() && (c.user == nil || !c.user.CanPlan())) {
		s.notFoundFor(w, r)
		return nil, false
	}
	return c, true
}

func (s *Server) handleSignupForm(w http.ResponseWriter, r *http.Request) {
	c, ok := s.publicCtx(w, r)
	if !ok {
		return
	}
	s.renderSignupForm(w, r, c, formVals{}, nil, http.StatusOK)
}

// limited refuses a POST when the visitor's address has saved too many
// sign-ups lately. It only checks: countSignup records the ones that count
// (saved, changed, withdrawn, or caught by the honeypot), so validation
// errors and "add more people" don't use up the limit.
func (s *Server) limited(w http.ResponseWriter, r *http.Request) bool {
	if !s.signupLimiter.allow("signup:" + s.clientIP(r)) {
		s.renderCalendarPage(w, r, "error", map[string]any{
			"Title": "Slow down", "Message": "That's a lot of sign-ups from one place. Please wait a few minutes and try again.",
			"Status": http.StatusTooManyRequests,
		})
		return true
	}
	return false
}

// countSignup counts one saved sign-up change against the visitor's address.
func (s *Server) countSignup(r *http.Request) {
	s.signupLimiter.fail("signup:" + s.clientIP(r))
}

func (s *Server) handleSignupSubmit(w http.ResponseWriter, r *http.Request) {
	c, ok := s.publicCtx(w, r)
	if !ok {
		return
	}
	if s.limited(w, r) {
		return
	}
	if c.preview() {
		s.renderCalendarPage(w, r, "error", map[string]any{
			"Title": "Preview only", "Message": "This form is a draft, so it can't take sign-ups yet.",
			"Status": http.StatusForbidden,
		})
		return
	}
	s.submitSignup(w, r, c)
}

// submitSignup handles a POST for a new sign-up (c.existing nil) or a
// change to one.
func (s *Server) submitSignup(w http.ResponseWriter, r *http.Request, c *suCtx) {
	if err := r.ParseForm(); err != nil {
		s.notFoundFor(w, r)
		return
	}
	vals := formVals(r.PostForm)
	if c.existing == nil && vals.Get("website") != "" {
		// A robot filled the hidden box: look like it worked, save nothing.
		s.countSignup(r)
		s.renderSignupThanks(w, r, c, "", "")
		return
	}
	if vals.Get("more") != "" {
		n := len(peopleRows(c, vals, false))
		vals["people_n"] = []string{strconv.Itoa(n + 3)}
		s.renderSignupForm(w, r, c, vals, nil, http.StatusOK)
		return
	}
	if c.existing != nil && !c.canChange() {
		s.renderSignupForm(w, r, c, vals, []string{"This sign-up can't be changed any more"}, http.StatusUnprocessableEntity)
		return
	}
	in, errs := s.readSignup(c, vals)
	if len(errs) == 0 {
		var res *store.SignupResult
		var err error
		res, err = s.store.SaveSignup(in, store.SignupOptions{})
		var se *store.SignupError
		switch {
		case errors.As(err, &se):
			errs = se.Messages
		case err != nil:
			s.serverError(w, r, err)
			return
		default:
			s.countSignup(r)
			if c.existing != nil {
				s.redirect(w, r, c.base, "Saved. Your changes are in.")
				return
			}
			link := ""
			if c.user == nil {
				link = "/signup/" + c.form.Code + "/s/" + res.Code
			} else {
				link = "/my/signups/" + strconv.FormatInt(res.ID, 10)
			}
			s.renderSignupThanks(w, r, c, link, res.Code)
			return
		}
	}
	s.renderSignupForm(w, r, c, vals, errs, http.StatusUnprocessableEntity)
}

// readSignup turns the posted form into a store input, checking the
// signer's contact. Everything else is checked by the store.
func (s *Server) readSignup(c *suCtx, v formVals) (store.SignupInput, []string) {
	in := store.SignupInput{EventID: c.form.EventID}
	var errs []string
	switch {
	case c.existing != nil:
		in.SignupID = c.existing.ID
		in.UserID = c.existing.UserID
		if c.asUser() {
			in.Name, in.Phone, in.Email = c.existing.Name, c.existing.Phone, c.existing.Email
		}
	case c.user != nil && !c.planner:
		in.UserID = c.user.ID
		in.Name = c.user.Name()
	}
	if !c.asUser() {
		in.Name = strings.TrimSpace(v.Get("name"))
		in.Phone = strings.TrimSpace(v.Get("phone"))
		in.Email = strings.TrimSpace(v.Get("email"))
		if in.Name == "" {
			errs = append(errs, "Add your name")
		}
		contact := c.form.Contact
		if c.planner {
			contact = "keep" // phoned in: keep whatever they gave
		}
		switch contact {
		case "keep":
		case "phone":
			if in.Phone == "" {
				errs = append(errs, "Add a phone number")
			}
		case "email":
			if in.Email == "" {
				errs = append(errs, "Add an email address")
			}
		case "phone_or_email":
			if in.Phone == "" && in.Email == "" {
				errs = append(errs, "Add a phone number or an email address")
			}
		default:
			in.Phone, in.Email = "", ""
		}
		if in.Email != "" && !strings.Contains(in.Email, "@") {
			errs = append(errs, "That email address doesn't look right")
		}
	}
	if c.form.AskGuests {
		if g := strings.TrimSpace(v.Get("guests")); g != "" {
			n, err := strconv.Atoi(g)
			if err != nil || n < 0 || n > 500 {
				errs = append(errs, "Guests should be a number, 0 or more")
			} else {
				in.Guests = n
			}
		}
	}

	// People. The store counts a person by their place in this list, so
	// blank rows are dropped here and answers use the new place.
	var rows []int
	if c.form.People == "list" {
		for _, n := range peopleRows(c, v, true) {
			in.People = append(in.People, store.PersonInput{
				ID:        atoi64(v.Get(fmt.Sprintf("p%d_id", n))),
				Name:      strings.TrimSpace(v.Get(fmt.Sprintf("p%d_name", n))),
				Allergies: v.Get(fmt.Sprintf("p%d_allergies", n)),
			})
			rows = append(rows, n)
		}
	} else {
		in.People = []store.PersonInput{{Allergies: v.Get("p0_allergies")}}
	}

	for _, q := range c.qs {
		if q.Retired {
			continue
		}
		if !q.PerPerson {
			in.Answers = append(in.Answers, readAnswer(q, -1, v, fmt.Sprintf("q%d", q.ID)))
			continue
		}
		for i, n := range rows {
			in.Answers = append(in.Answers, readAnswer(q, i, v, fmt.Sprintf("p%d_q%d", n, q.ID)))
		}
	}
	return in, errs
}

func readAnswer(q store.SignupQuestion, person int, v formVals, key string) store.AnswerInput {
	a := store.AnswerInput{QuestionID: q.ID, Person: person}
	if store.IsPickKind(q.Kind) {
		for _, s := range v[key] {
			if id := atoi64(s); id != 0 {
				a.ChoiceIDs = append(a.ChoiceIDs, id)
			}
		}
	} else {
		a.Value = v.Get(key)
	}
	return a
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

// peopleRows is the row numbers of the person rows on a posted list form:
// all of them, or only those with a name when named is set.
func peopleRows(c *suCtx, v formVals, named bool) []int {
	n := int(atoi64(v.Get("people_n")))
	if n > 60 {
		n = 60
	}
	var out []int
	for i := 0; i < n; i++ {
		if !named || strings.TrimSpace(v.Get(fmt.Sprintf("p%d_name", i))) != "" {
			out = append(out, i)
		}
	}
	return out
}

// ---- view ----

type suOpt struct {
	Value    string
	ID       int64
	Label    string
	Note     string
	Excludes string
	Checked  bool
	Disabled bool
}

type suAsk struct {
	ID       int64
	Kind     string
	Label    string
	Help     string
	Name     string
	Value    string
	Input    string // radio, checkbox
	Required bool
	Notice   bool
	When     string // choice IDs, space-separated
	OnlyIf   string
	Opts     []suOpt
}

type suRow struct {
	N         int
	ID        string
	Name      string
	Allergies string
	Ask       []suAsk
}

func spotsNote(c store.SignupChoice, showSpots bool) string {
	switch {
	case c.Full():
		return "Full"
	case c.Spots > 0 && showSpots:
		if l := c.Left(); l == 1 {
			return "1 spot left"
		} else {
			return fmt.Sprintf("%d spots left", l)
		}
	}
	return ""
}

func (s *Server) buildAsk(c *suCtx, q store.SignupQuestion, name string, v formVals, label map[int64]string) suAsk {
	a := suAsk{ID: q.ID, Kind: q.Kind, Label: q.Label, Help: q.Help, Name: name, Required: q.Required, Notice: q.PublicList}
	if len(q.When) > 0 {
		var ids, names []string
		for _, id := range q.When {
			ids = append(ids, strconv.FormatInt(id, 10))
			names = append(names, label[id])
		}
		a.When = strings.Join(ids, " ")
		a.OnlyIf = "(only if you picked " + strings.Join(names, " or ") + ")"
	}
	switch q.Kind {
	case "one", "many":
		a.Input = "radio"
		if q.Kind == "many" {
			a.Input = "checkbox"
		}
		for _, ch := range q.Choices {
			id := strconv.FormatInt(ch.ID, 10)
			checked := v.Has(name, id)
			if ch.Retired && !checked {
				continue
			}
			var ex []string
			for _, e := range ch.Excludes {
				ex = append(ex, strconv.FormatInt(e, 10))
			}
			a.Opts = append(a.Opts, suOpt{Value: id, ID: ch.ID, Label: ch.Label, Note: spotsNote(ch, c.form.ShowSpots),
				Excludes: strings.Join(ex, " "), Checked: checked, Disabled: ch.Full() && !checked})
		}
	case "yes_no":
		a.Input = "radio"
		a.Opts = []suOpt{
			{Value: "yes", Label: "Yes", Checked: v.Get(name) == "yes"},
			{Value: "no", Label: "No", Checked: v.Get(name) == "no"},
		}
	default:
		a.Value = v.Get(name)
	}
	return a
}

// signupToVals rebuilds the form fields from a saved sign-up, to pre-fill
// the change page.
func signupToVals(c *suCtx, su *store.Signup) formVals {
	v := formVals{}
	set := func(k, val string) { v[k] = append(v[k], val) }
	set("name", su.Name)
	set("phone", su.Phone)
	set("email", su.Email)
	set("guests", strconv.Itoa(su.Guests))
	set("people_n", strconv.Itoa(len(su.People)))
	idx := map[int64]int{}
	for i, p := range su.People {
		idx[p.ID] = i
		set(fmt.Sprintf("p%d_id", i), strconv.FormatInt(p.ID, 10))
		set(fmt.Sprintf("p%d_name", i), p.Name)
		set(fmt.Sprintf("p%d_allergies", i), p.Allergies)
	}
	for _, a := range su.Answers {
		key := fmt.Sprintf("q%d", a.QuestionID)
		if a.PersonID != 0 {
			i, ok := idx[a.PersonID]
			if !ok {
				continue
			}
			key = fmt.Sprintf("p%d_q%d", i, a.QuestionID)
		}
		if a.ChoiceID != 0 {
			set(key, strconv.FormatInt(a.ChoiceID, 10))
		} else {
			set(key, a.Value)
		}
	}
	return v
}

func (s *Server) renderSignupForm(w http.ResponseWriter, r *http.Request, c *suCtx, v formVals, errs []string, status int) {
	if c.existing != nil && len(v) == 0 {
		v = signupToVals(c, c.existing)
	}
	label := map[int64]string{}
	for _, q := range c.qs {
		for _, ch := range q.Choices {
			label[ch.ID] = ch.Label
		}
	}
	var ask []suAsk
	for _, q := range c.qs {
		if !q.Retired && !q.PerPerson {
			ask = append(ask, s.buildAsk(c, q, fmt.Sprintf("q%d", q.ID), v, label))
		}
	}
	var rows []suRow
	if c.form.People == "list" {
		n := int(atoi64(v.Get("people_n")))
		if n < 1 {
			n = 3
		}
		if n > 60 {
			n = 60
		}
		for i := 0; i < n; i++ {
			row := suRow{N: i, ID: v.Get(fmt.Sprintf("p%d_id", i)), Name: v.Get(fmt.Sprintf("p%d_name", i)),
				Allergies: v.Get(fmt.Sprintf("p%d_allergies", i))}
			for _, q := range c.qs {
				if !q.Retired && q.PerPerson {
					row.Ask = append(row.Ask, s.buildAsk(c, q, fmt.Sprintf("p%d_q%d", i, q.ID), v, label))
				}
			}
			rows = append(rows, row)
		}
	}

	data := map[string]any{
		"Title": c.ev.Title, "Form": c.form, "Event": c.ev, "When": c.ev.When(), "State": c.state,
		"StateText": suStateMessage(c.state), "Preview": c.preview(), "Errors": errs, "Vals": v,
		"Ask": ask, "Rows": rows, "AsUser": c.asUser(), "Editing": c.existing != nil,
		"Status": status,
	}
	if c.user != nil && c.existing == nil {
		data["SignerName"] = c.user.Name()
	} else if c.existing != nil {
		data["SignerName"] = c.existing.Name
	}
	if c.existing != nil {
		data["Action"] = c.base
		data["CanSubmit"] = c.canChange()
		data["Cancel"] = c.base
		data["Submit"] = "Save changes"
	} else {
		data["Action"] = "/signup/" + c.form.Code
		data["CanSubmit"] = c.canSubmitNew()
		data["Submit"] = c.form.ButtonLabel()
	}
	data["ShowForm"] = c.state == store.SignupOpen || c.preview() || (c.existing != nil && c.canChange())
	if c.existing == nil {
		lists, err := s.bringingLists(c)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		data["Lists"] = lists
	}
	data["Embed"] = isEmbed(r)
	if c.planner {
		s.plannerFormData(c, data)
	}
	s.renderCalendarPage(w, r, "signup/form", data)
}

func suStateMessage(state string) string {
	switch state {
	case store.SignupFull:
		return "Full: all the spots are taken."
	case store.SignupClosed:
		return "Sign-ups are closed."
	}
	return ""
}

// bringingLists is "who's bringing what", only when a question shows on the form.
func (s *Server) bringingLists(c *suCtx) ([]store.BringingList, error) {
	for _, q := range c.qs {
		if q.PublicList && !q.Retired && !q.PerPerson {
			return s.store.WhosBringingWhat(c.form.EventID)
		}
	}
	return nil, nil
}

func (s *Server) renderSignupThanks(w http.ResponseWriter, r *http.Request, c *suCtx, link, code string) {
	data := map[string]any{
		"Title": "Thank you", "Event": c.ev, "When": c.ev.When(), "ThankYou": c.form.ThankYou,
		"Link": link, "Code": code, "SignedIn": c.user != nil, "FormCode": c.form.Code,
	}
	if isEmbed(r) {
		data["Embed"] = true
	}
	if code != "" && c.user == nil {
		data["Full"] = s.baseURL(r) + link
	}
	s.renderCalendarPage(w, r, "signup/thanks", data)
}

// isEmbed: the form is shown inside another site's page (?embed=1, kept as
// a hidden field through the POST), so the page has no navigation.
func isEmbed(r *http.Request) bool {
	return r.URL.Query().Get("embed") == "1" || r.PostFormValue("embed") == "1"
}
