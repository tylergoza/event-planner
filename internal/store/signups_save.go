package store

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Effective state, saving sign-ups and the calendar outbox for state
// changes (SIGNUPS_PLAN.md steps 2 and 5).

// Effective states of a form.
const (
	SignupDraft  = "draft"
	SignupClosed = "closed"
	SignupFull   = "full"
	SignupOpen   = "open"
)

const clockLayout = "2006-01-02T15:04"

// signupStart is when the event starts as YYYY-MM-DDTHH:MM ("" if not
// scheduled). An all-day event starts at 00:00.
func signupStart(ev *Event) string {
	if ev.StartsOn == "" {
		return ""
	}
	t := ev.StartTime
	if t == "" {
		t = "00:00"
	}
	return ev.StartsOn + "T" + t
}

// signupTimeState is "draft", "closed" or "" (neither), from the form's
// status, the closing time, and the event (cancelled, not scheduled, or
// started). Times are the event planner's local clock: now should be in
// local time.
func signupTimeState(f *SignupForm, ev *Event, now time.Time) string {
	if f.Status == "draft" {
		return SignupDraft
	}
	if f.Status == "closed" || ev.Status == "cancelled" || ev.StartsOn == "" {
		return SignupClosed
	}
	n := now.Format(clockLayout)
	if f.ClosesAt != "" && f.ClosesAt <= n {
		return SignupClosed
	}
	if signupStart(ev) <= n {
		return SignupClosed
	}
	return ""
}

// signupIsFull is true when every choice on every required pick question
// (that is always asked) is full. A form without such a question is never
// full.
func signupIsFull(qs []SignupQuestion) bool {
	any := false
	for _, x := range qs {
		if x.Retired || !x.Required || !IsPickKind(x.Kind) || len(x.When) > 0 {
			continue
		}
		active, full := 0, 0
		for _, c := range x.Choices {
			if c.Retired {
				continue
			}
			active++
			if c.Full() {
				full++
			}
		}
		if active == 0 {
			continue
		}
		any = true
		if full < active {
			return false
		}
	}
	return any
}

func computeSignupState(f *SignupForm, ev *Event, now time.Time, qs []SignupQuestion) string {
	if st := signupTimeState(f, ev, now); st != "" {
		return st
	}
	if signupIsFull(qs) {
		return SignupFull
	}
	return SignupOpen
}

// SignupState is the form's effective state, used by pages and the push:
// "draft"; "closed" (status closed, the closing time or the event's start
// has passed, or the event is cancelled or not scheduled); "full" (every
// choice on every required pick question is full); else "open".
func (s *Store) SignupState(f *SignupForm, ev *Event, now time.Time) (string, error) {
	if st := signupTimeState(f, ev, now); st != "" {
		return st, nil
	}
	qs, err := loadSignupQuestions(s.DB, f.EventID)
	if err != nil {
		return "", err
	}
	return computeSignupState(f, ev, now, qs), nil
}

func queueEventPut(q querier, eventID int64) error {
	_, err := q.Exec(`INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
		VALUES (?, 'put', (SELECT COALESCE(MAX(seq), 0) + 1 FROM calendar_outbox))`, eventID)
	return err
}

// signupStateNow is the effective state read inside a transaction.
func signupStateNow(q querier, eventID int64, now time.Time) (string, error) {
	f, err := loadSignupForm(q, eventID)
	if err != nil {
		return "", err
	}
	ev, err := scanEvent(q.QueryRow(`SELECT `+eventCols+` FROM events WHERE id = ?`, eventID))
	if err != nil {
		return "", notFound(err)
	}
	qs, err := loadSignupQuestions(q, eventID)
	if err != nil {
		return "", err
	}
	return computeSignupState(f, &ev, now, qs), nil
}

// queueIfStateChanged queues the event for the calendar when its effective
// state is no longer before (open to full, or back). A plain answer that
// changes nothing queues nothing.
func queueIfStateChanged(q querier, eventID int64, now time.Time, before string) error {
	after, err := signupStateNow(q, eventID, now)
	if err != nil {
		return err
	}
	if after != before {
		return queueEventPut(q, eventID)
	}
	return nil
}

// ---- saving ----

// PersonInput is one person a sign-up is for. ID is the existing person's
// ID when editing (keeps their per-person answers' place), 0 for new ones.
type PersonInput struct {
	ID                int64
	Name              string
	Allergies         string
	DirectoryPersonID int64
}

// AnswerInput is the answer to one question. For per-person questions
// there is one per person, Person being the index into SignupInput.People
// (ignored otherwise). ChoiceIDs are for pick questions, Value for the
// rest.
type AnswerInput struct {
	QuestionID int64
	Person     int
	ChoiceIDs  []int64
	Value      string
}

// SignupInput is a submission. SignupID is 0 for a new sign-up. With a
// form whose People is "self" the one person is the signer (Name) and
// People only supplies allergies.
type SignupInput struct {
	EventID  int64
	SignupID int64
	UserID   int64
	Name     string
	Phone    string
	Email    string
	Guests   int
	People   []PersonInput
	Answers  []AnswerInput
}

// SignupOptions: Planner lets spots and "can't be picked with" go over,
// returning warnings instead of errors, and lets a draft or closed form
// take a sign-up. Now is the current local time (zero: time.Now()).
type SignupOptions struct {
	Planner bool
	Now     time.Time
}

// SignupResult is what a save returns. Code is the sign-up's status-link
// code, set only for a new sign-up: it is shown once and only its hash is
// kept.
type SignupResult struct {
	ID       int64
	Code     string
	Warnings []string
}

type answerKey struct {
	person int // index into people, -1 per sign-up
	q      int64
}

// SaveSignup creates or updates a sign-up with its people and answers in
// one BEGIN IMMEDIATE transaction. A *SignupError lists what to fix.
func (s *Store) SaveSignup(in SignupInput, opts SignupOptions) (*SignupResult, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	var res SignupResult
	err := s.immediate(func(q querier) error { return saveSignupTx(q, in, opts, &res) })
	if err != nil {
		return nil, err
	}
	return &res, nil
}

func saveSignupTx(q querier, in SignupInput, opts SignupOptions, res *SignupResult) error {
	form, err := loadSignupForm(q, in.EventID)
	if err != nil {
		return err
	}
	ev, err := scanEvent(q.QueryRow(`SELECT `+eventCols+` FROM events WHERE id = ?`, in.EventID))
	if err != nil {
		return notFound(err)
	}
	qs, err := loadSignupQuestions(q, in.EventID)
	if err != nil {
		return err
	}
	var prev *Signup
	if in.SignupID != 0 {
		prev, err = loadSignupByID(q, in.SignupID)
		if err != nil {
			return err
		}
		if prev.EventID != in.EventID {
			return ErrNotFound
		}
	}
	before := computeSignupState(form, &ev, opts.Now, qs)
	if !opts.Planner && (before == SignupDraft || before == SignupClosed) {
		if before == SignupDraft {
			return signupErr("Sign-ups aren't open yet")
		}
		return signupErr("Sign-ups are closed")
	}

	var errs, warns []string
	fail := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	warn := func(format string, a ...any) { warns = append(warns, fmt.Sprintf(format, a...)) }

	// The signer and the people.
	name := strings.TrimSpace(in.Name)
	if name == "" {
		fail("Add your name")
	}
	guests := in.Guests
	if !form.AskGuests {
		guests = 0
	}
	if guests < 0 {
		fail("Guests can't be below 0")
		guests = 0
	}
	var people []PersonInput
	if form.People == "self" {
		p := PersonInput{Name: name}
		if len(in.People) > 0 {
			p.Allergies = in.People[0].Allergies
		}
		if prev != nil && len(prev.People) > 0 {
			p.ID = prev.People[0].ID
			p.DirectoryPersonID = prev.People[0].DirectoryPersonID
		}
		people = []PersonInput{p}
	} else {
		for _, p := range in.People {
			p.Name = strings.TrimSpace(p.Name)
			if p.Name != "" {
				people = append(people, p)
			}
		}
		if len(people) == 0 {
			fail("Add at least one person")
		}
	}
	for i := range people {
		if form.AskAllergies {
			people[i].Allergies = strings.TrimSpace(people[i].Allergies)
		} else {
			people[i].Allergies = ""
		}
	}
	prevPerson := map[int64]bool{}
	prevPicked := map[int64]bool{} // choices already picked on this sign-up
	prevOwn := map[int64]int{}     // spots this sign-up holds now, by choice
	if prev != nil {
		for _, p := range prev.People {
			prevPerson[p.ID] = true
		}
		for _, a := range prev.Answers {
			if a.ChoiceID != 0 {
				prevPicked[a.ChoiceID] = true
				if prev.Status == "in" {
					prevOwn[a.ChoiceID]++
				}
			}
		}
	}
	usedID := map[int64]bool{}
	for i := range people {
		if people[i].ID != 0 && (!prevPerson[people[i].ID] || usedID[people[i].ID]) {
			people[i].ID = 0
		}
		if people[i].ID != 0 {
			usedID[people[i].ID] = true
		}
	}

	// Index the form.
	qmap := map[int64]*SignupQuestion{}
	cmap := map[int64]*SignupChoice{}
	cq := map[int64]*SignupQuestion{}
	for i := range qs {
		qmap[qs[i].ID] = &qs[i]
		for j := range qs[i].Choices {
			c := &qs[i].Choices[j]
			cmap[c.ID] = c
			cq[c.ID] = &qs[i]
		}
	}

	// Gather what was answered, per sign-up or per person.
	picks := map[answerKey][]int64{}
	vals := map[answerKey]string{}
	for _, a := range in.Answers {
		x := qmap[a.QuestionID]
		if x == nil || x.Retired {
			continue
		}
		k := answerKey{-1, x.ID}
		if x.PerPerson {
			if a.Person < 0 || a.Person >= len(people) {
				continue
			}
			k.person = a.Person
		}
		for _, cid := range a.ChoiceIDs {
			dup := false
			for _, e := range picks[k] {
				dup = dup || e == cid
			}
			if !dup {
				picks[k] = append(picks[k], cid)
			}
		}
		if v := strings.TrimSpace(a.Value); v != "" {
			vals[k] = v
		}
	}

	pickedBy := func(person int, choice int64) bool {
		if cq[choice] == nil {
			return false
		}
		for _, c := range picks[answerKey{person, cq[choice].ID}] {
			if c == choice {
				return true
			}
		}
		return false
	}
	// applies: is question x asked (for person, if per-person)?
	applies := func(x *SignupQuestion, person int) bool {
		if len(x.When) == 0 {
			return true
		}
		for _, cid := range x.When {
			if cq[cid] == nil {
				continue
			}
			switch {
			case !cq[cid].PerPerson:
				if pickedBy(-1, cid) {
					return true
				}
			case x.PerPerson:
				if pickedBy(person, cid) {
					return true
				}
			default: // asked once, depends on a per-person choice: anyone picking it
				for p := range people {
					if pickedBy(p, cid) {
						return true
					}
				}
			}
		}
		return false
	}

	who := func(x *SignupQuestion, person int) string {
		if x.PerPerson {
			return " for " + people[person].Name
		}
		return ""
	}
	scopes := func(x *SignupQuestion) []int {
		if !x.PerPerson {
			return []int{-1}
		}
		out := make([]int, len(people))
		for i := range people {
			out[i] = i
		}
		return out
	}
	// Decide what applies first (conditions are on questions that are
	// always asked), dropping what doesn't, then check what's left.
	type dropKey = answerKey
	var drop []dropKey
	for i := range qs {
		x := &qs[i]
		if x.Retired {
			continue
		}
		for _, p := range scopes(x) {
			if !applies(x, p) {
				drop = append(drop, dropKey{p, x.ID})
			}
		}
	}
	for _, k := range drop {
		delete(picks, k)
		delete(vals, k)
	}
	dropped := map[dropKey]bool{}
	for _, k := range drop {
		dropped[k] = true
	}

	for i := range qs {
		x := &qs[i]
		if x.Retired {
			continue
		}
		for _, p := range scopes(x) {
			k := answerKey{p, x.ID}
			if dropped[k] {
				continue
			}
			switch x.Kind {
			case "one", "many":
				ids := picks[k]
				if x.Kind == "one" && len(ids) > 1 {
					fail("Pick just one for %q%s", x.Label, who(x, p))
				}
				for _, cid := range ids {
					c := cmap[cid]
					if c == nil || cq[cid].ID != x.ID {
						fail("That isn't a choice for %q", x.Label)
						continue
					}
					if c.Retired && !prevPicked[cid] {
						fail("%s isn't available any more", c.Label)
					}
				}
				if x.Required && len(ids) == 0 {
					fail("Answer %q%s", x.Label, who(x, p))
				}
			default:
				v := vals[k]
				if v == "" {
					if x.Required {
						fail("Answer %q%s", x.Label, who(x, p))
					}
					break
				}
				switch x.Kind {
				case "number":
					if _, err := strconv.ParseFloat(v, 64); err != nil {
						fail("%q%s should be a number", x.Label, who(x, p))
					}
				case "yes_no":
					if v != "yes" && v != "no" {
						fail("%q%s should be yes or no", x.Label, who(x, p))
					}
				}
			}
		}
	}

	// Can't be picked with: within the sign-up, or within one person.
	picked := func(person int) map[int64]bool {
		out := map[int64]bool{}
		for k, ids := range picks {
			if k.person == person {
				for _, id := range ids {
					out[id] = true
				}
			}
		}
		return out
	}
	checkPairs := func(person int, prefix string) {
		set := picked(person)
		var ids []int64
		for id := range set {
			ids = append(ids, id)
		}
		sortIDs(ids)
		for _, a := range ids {
			ca := cmap[a]
			if ca == nil {
				continue
			}
			for _, b := range ca.Excludes {
				if b <= a || !set[b] {
					continue
				}
				if opts.Planner {
					warn("%sAlso picked %s (with %s)", prefix, cmap[b].Label, ca.Label)
				} else {
					fail("%sYou can't pick both %s and %s", prefix, ca.Label, cmap[b].Label)
				}
			}
		}
	}
	checkPairs(-1, "")
	for i := range people {
		checkPairs(i, people[i].Name+": ")
	}

	// Spots: what others hold plus what this sign-up now picks.
	want := map[int64]int{}
	for k, ids := range picks {
		_ = k
		for _, id := range ids {
			want[id]++
		}
	}
	var wantIDs []int64
	for id := range want {
		wantIDs = append(wantIDs, id)
	}
	sortIDs(wantIDs)
	for _, id := range wantIDs {
		c := cmap[id]
		if c == nil || c.Spots == 0 {
			continue
		}
		others := c.Taken - prevOwn[id]
		total := others + want[id]
		if total <= c.Spots || want[id] <= prevOwn[id] {
			continue // fits, or this edit takes no more than it already held
		}
		switch {
		case opts.Planner:
			warn("%s: over by %d", c.Label, total-c.Spots)
		case others >= c.Spots:
			fail("%s just filled up", c.Label)
		case c.Spots-others == 1:
			fail("%s has only 1 spot left", c.Label)
		default:
			fail("%s has only %d spots left", c.Label, c.Spots-others)
		}
	}

	if len(errs) > 0 {
		return &SignupError{Messages: errs}
	}

	// Write.
	if prev == nil {
		code, err := randomCode(16)
		if err != nil {
			return err
		}
		r, err := q.Exec(`INSERT INTO signups (event_id, code_hash, user_id, name, phone, email, guests) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			in.EventID, HashSignupCode(code), nullID(in.UserID), name, strings.TrimSpace(in.Phone), strings.TrimSpace(in.Email), guests)
		if err != nil {
			return err
		}
		if res.ID, err = r.LastInsertId(); err != nil {
			return err
		}
		res.Code = code
	} else {
		res.ID = prev.ID
		if _, err := q.Exec(`UPDATE signups SET name = ?, phone = ?, email = ?, guests = ?, status = 'in', updated_at = datetime('now') WHERE id = ?`,
			name, strings.TrimSpace(in.Phone), strings.TrimSpace(in.Email), guests, prev.ID); err != nil {
			return err
		}
		if in.UserID != 0 && prev.UserID == 0 {
			if _, err := q.Exec(`UPDATE signups SET user_id = ? WHERE id = ?`, in.UserID, prev.ID); err != nil {
				return err
			}
		}
		keep := map[int64]bool{}
		for _, p := range people {
			if p.ID != 0 {
				keep[p.ID] = true
			}
		}
		for _, p := range prev.People {
			if !keep[p.ID] {
				if _, err := q.Exec(`DELETE FROM signup_people WHERE id = ?`, p.ID); err != nil {
					return err
				}
			}
		}
		if _, err := q.Exec(`DELETE FROM signup_answers WHERE signup_id = ? AND question_id IN
			(SELECT id FROM signup_questions WHERE event_id = ? AND retired = 0)`, prev.ID, in.EventID); err != nil {
			return err
		}
	}
	personIDs := make([]int64, len(people))
	for i, p := range people {
		if p.ID != 0 {
			if _, err := q.Exec(`UPDATE signup_people SET position = ?, name = ?, directory_person_id = ?, allergies = ? WHERE id = ?`,
				i+1, p.Name, nullID(p.DirectoryPersonID), p.Allergies, p.ID); err != nil {
				return err
			}
			personIDs[i] = p.ID
			continue
		}
		r, err := q.Exec(`INSERT INTO signup_people (signup_id, position, name, directory_person_id, allergies) VALUES (?, ?, ?, ?, ?)`,
			res.ID, i+1, p.Name, nullID(p.DirectoryPersonID), p.Allergies)
		if err != nil {
			return err
		}
		if personIDs[i], err = r.LastInsertId(); err != nil {
			return err
		}
	}
	for i := range qs {
		x := &qs[i]
		if x.Retired {
			continue
		}
		for _, p := range scopes(x) {
			k := answerKey{p, x.ID}
			var pid any
			if p >= 0 {
				pid = personIDs[p]
			}
			for _, cid := range picks[k] {
				if _, err := q.Exec(`INSERT INTO signup_answers (signup_id, person_id, question_id, choice_id) VALUES (?, ?, ?, ?)`,
					res.ID, pid, x.ID, cid); err != nil {
					return err
				}
			}
			if v := vals[k]; v != "" {
				if _, err := q.Exec(`INSERT INTO signup_answers (signup_id, person_id, question_id, value) VALUES (?, ?, ?, ?)`,
					res.ID, pid, x.ID, v); err != nil {
					return err
				}
			}
		}
	}
	res.Warnings = warns
	return queueIfStateChanged(q, in.EventID, opts.Now, before)
}

func sortIDs(ids []int64) {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}

// WithdrawSignup marks a sign-up withdrawn, freeing its spots. Saving it
// again with SaveSignup brings it back.
func (s *Store) WithdrawSignup(id int64) error {
	return s.immediate(func(q querier) error {
		var eventID int64
		var status string
		if err := q.QueryRow(`SELECT event_id, status FROM signups WHERE id = ?`, id).Scan(&eventID, &status); err != nil {
			return notFound(err)
		}
		if status == "withdrawn" {
			return nil
		}
		now := time.Now()
		before, err := signupStateNow(q, eventID, now)
		if err != nil {
			return err
		}
		if _, err := q.Exec(`UPDATE signups SET status = 'withdrawn', updated_at = datetime('now') WHERE id = ?`, id); err != nil {
			return err
		}
		return queueIfStateChanged(q, eventID, now, before)
	})
}

// SetSignupNote saves the planner's internal note on a sign-up.
func (s *Store) SetSignupNote(id int64, note string) error {
	res, err := s.DB.Exec(`UPDATE signups SET planner_note = ?, updated_at = datetime('now') WHERE id = ?`, strings.TrimSpace(note), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- closing sweep ----

// FormsPastClosing is the open forms whose closing time has passed and
// whose closing hasn't been pushed to the calendar yet.
func (s *Store) FormsPastClosing(now time.Time) ([]int64, error) {
	rows, err := s.DB.Query(`SELECT event_id FROM signup_forms WHERE status = 'open' AND closes_at <> '' AND closes_at <= ? AND closed_pushed = 0
		ORDER BY event_id`, now.Format(clockLayout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SweepSignupClosings queues the calendar push for every open form that
// has closed by itself (its closing time or the event's start passed) and
// hasn't been pushed since. Run it with the series top-up. It returns how
// many forms were queued.
func (s *Store) SweepSignupClosings(now time.Time) (int, error) {
	n := 0
	err := s.immediate(func(q querier) error {
		rows, err := q.Query(`SELECT event_id FROM signup_forms WHERE status = 'open' AND closed_pushed = 0`)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, id := range ids {
			f, err := loadSignupForm(q, id)
			if err != nil {
				return err
			}
			ev, err := scanEvent(q.QueryRow(`SELECT `+eventCols+` FROM events WHERE id = ?`, id))
			if err != nil {
				return notFound(err)
			}
			if signupTimeState(f, &ev, now) != SignupClosed {
				continue
			}
			if err := queueEventPut(q, id); err != nil {
				return err
			}
			if _, err := q.Exec(`UPDATE signup_forms SET closed_pushed = 1 WHERE event_id = ?`, id); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}
