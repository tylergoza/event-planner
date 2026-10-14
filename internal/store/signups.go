package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Sign-up forms on events (SIGNUPS_PLAN.md). This file is the form, its
// questions and choices; signups_save.go saves sign-ups, signups_read.go
// reads them back.

// Allowed values for the form's settings.
var (
	SignupStatuses  = []string{"draft", "open", "closed"}
	SignupAudiences = []string{"anyone", "signed_in"}
	SignupContacts  = []string{"phone", "email", "phone_or_email", "none"}
	SignupPeople    = []string{"self", "list"}
	// SignupKinds are the question kinds.
	SignupKinds = []string{"one", "many", "text", "long_text", "number", "yes_no"}
)

func oneOf(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// SignupForm is the one form an event can have.
type SignupForm struct {
	EventID      int64
	Code         string // random, in the public URL
	Label        string // button text, "" = "Sign up"
	Intro        string
	ThankYou     string
	Status       string // draft, open, closed
	Audience     string // anyone, signed_in
	Contact      string // phone, email, phone_or_email, none
	People       string // self, list
	AskGuests    bool
	AskAllergies bool
	ClosesAt     string // YYYY-MM-DDTHH:MM, "" = when the event starts
	ShowSpots    bool
	CreatedAt    string
	UpdatedAt    string
}

// ButtonLabel is the text on the Sign up button.
func (f SignupForm) ButtonLabel() string {
	if f.Label == "" {
		return "Sign up"
	}
	return f.Label
}

// IsPick reports whether the question kind is a pick from choices.
func IsPickKind(kind string) bool { return kind == "one" || kind == "many" }

// SignupQuestion is one question on a form. When holds the choice IDs of
// earlier questions that must be picked for this one to be asked (empty:
// always asked).
type SignupQuestion struct {
	ID         int64
	EventID    int64
	Position   int
	Kind       string
	Label      string
	Help       string
	Required   bool
	PerPerson  bool
	PublicList bool
	Retired    bool
	When       []int64
	Choices    []SignupChoice
}

// SignupChoice is one option of a pick question. Spots 0 means no limit.
// Taken is worked out when read. Excludes holds the IDs of choices that
// can't be picked with this one (read both ways).
type SignupChoice struct {
	ID         int64
	QuestionID int64
	Position   int
	Label      string
	Spots      int
	Taken      int
	Retired    bool
	Excludes   []int64
}

// Full reports whether a limited choice has no spots left.
func (c SignupChoice) Full() bool { return c.Spots > 0 && c.Taken >= c.Spots }

// Left is the spots left, or -1 for no limit. Never below 0.
func (c SignupChoice) Left() int {
	if c.Spots == 0 {
		return -1
	}
	if c.Taken >= c.Spots {
		return 0
	}
	return c.Spots - c.Taken
}

// SignupError lists what's wrong with a submission, so the form can be
// shown again with what they typed.
type SignupError struct{ Messages []string }

func (e *SignupError) Error() string { return strings.Join(e.Messages, "; ") }

func signupErr(format string, a ...any) error {
	return &SignupError{Messages: []string{fmt.Sprintf(format, a...)}}
}

// ---- transactions ----

// connQ lets a dedicated connection with its own BEGIN IMMEDIATE be used
// where a querier is wanted.
type connQ struct {
	ctx  context.Context
	conn *sql.Conn
}

func (c connQ) Exec(q string, args ...any) (sql.Result, error) {
	return c.conn.ExecContext(c.ctx, q, args...)
}
func (c connQ) Query(q string, args ...any) (*sql.Rows, error) {
	return c.conn.QueryContext(c.ctx, q, args...)
}
func (c connQ) QueryRow(q string, args ...any) *sql.Row {
	return c.conn.QueryRowContext(c.ctx, q, args...)
}

// immediate runs fn in a BEGIN IMMEDIATE transaction: the write lock is
// taken up front, so counts read inside can't change before the commit.
// fn must use only the querier it is given (the pool has one connection).
func (s *Store) immediate(fn func(q querier) error) error {
	ctx := context.Background()
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	if err := fn(connQ{ctx, conn}); err != nil {
		conn.ExecContext(ctx, `ROLLBACK`)
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		conn.ExecContext(ctx, `ROLLBACK`)
		return err
	}
	return nil
}

func randomCode(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashSignupCode is how a sign-up's status-link code is stored.
func HashSignupCode(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func nullSpots(n int) any {
	if n <= 0 {
		return nil
	}
	return n
}

// ---- the form ----

const signupFormCols = `event_id, code, label, intro, thank_you, status, audience, contact, people, ask_guests, ask_allergies,
	closes_at, show_spots, created_at, updated_at`

func scanSignupForm(sc interface{ Scan(...any) error }) (*SignupForm, error) {
	var f SignupForm
	err := sc.Scan(&f.EventID, &f.Code, &f.Label, &f.Intro, &f.ThankYou, &f.Status, &f.Audience, &f.Contact, &f.People,
		&f.AskGuests, &f.AskAllergies, &f.ClosesAt, &f.ShowSpots, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	return &f, nil
}

func loadSignupForm(q querier, eventID int64) (*SignupForm, error) {
	return scanSignupForm(q.QueryRow(`SELECT `+signupFormCols+` FROM signup_forms WHERE event_id = ?`, eventID))
}

// SignupFormByEvent is the event's form, ErrNotFound if it has none.
func (s *Store) SignupFormByEvent(eventID int64) (*SignupForm, error) {
	return loadSignupForm(s.DB, eventID)
}

// SignupFormByCode is the form behind a public URL code.
func (s *Store) SignupFormByCode(code string) (*SignupForm, error) {
	return scanSignupForm(s.DB.QueryRow(`SELECT `+signupFormCols+` FROM signup_forms WHERE code = ?`, code))
}

// CreateSignupForm adds a draft form with a new random code to an event
// that has none.
func (s *Store) CreateSignupForm(eventID int64) (*SignupForm, error) {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM events WHERE id = ?`, eventID).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	if err := insertSignupForm(s.DB, eventID, nil); err != nil {
		return nil, err
	}
	return s.SignupFormByEvent(eventID)
}

// insertSignupForm inserts a draft form; from, if set, supplies the
// settings to copy.
func insertSignupForm(q querier, eventID int64, from *SignupForm) error {
	if _, err := loadSignupForm(q, eventID); err == nil {
		return errors.New("this event already has a sign-up form")
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	code, err := randomCode(12)
	if err != nil {
		return err
	}
	f := SignupForm{Audience: "anyone", Contact: "phone", People: "self", ShowSpots: true}
	if from != nil {
		f = *from
	}
	_, err = q.Exec(`INSERT INTO signup_forms (event_id, code, label, intro, thank_you, status, audience, contact, people,
		ask_guests, ask_allergies, closes_at, show_spots) VALUES (?, ?, ?, ?, ?, 'draft', ?, ?, ?, ?, ?, ?, ?)`,
		eventID, code, f.Label, f.Intro, f.ThankYou, f.Audience, f.Contact, f.People, f.AskGuests, f.AskAllergies, f.ClosesAt, f.ShowSpots)
	return err
}

// NormalizeClosesAt turns a closing time from a form (a datetime-local
// value, "YYYY-MM-DD HH:MM", or just a date meaning the end of that day)
// into YYYY-MM-DDTHH:MM. "" stays "".
func NormalizeClosesAt(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.Format("2006-01-02T15:04"), nil
		}
	}
	if t, err := time.Parse(DateLayout, v); err == nil {
		return t.Format("2006-01-02") + "T23:59", nil
	}
	return "", errors.New("The closing time isn't a date and time")
}

// UpdateSignupForm saves the form's settings (not its code or questions).
func (s *Store) UpdateSignupForm(f *SignupForm) error {
	closes, err := NormalizeClosesAt(f.ClosesAt)
	if err != nil {
		return err
	}
	switch {
	case !oneOf(SignupStatuses, f.Status):
		return errors.New("Pick draft, open or closed")
	case !oneOf(SignupAudiences, f.Audience):
		return errors.New("Pick who can sign up")
	case !oneOf(SignupContacts, f.Contact):
		return errors.New("Pick what contact to ask for")
	case !oneOf(SignupPeople, f.People):
		return errors.New("Pick who's coming")
	}
	if f.People == "self" {
		var n int
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM signup_questions WHERE event_id = ? AND per_person = 1 AND retired = 0`, f.EventID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return errors.New("Questions asked once per person need \"Who's coming\" to be a list")
		}
	}
	res, err := s.DB.Exec(`UPDATE signup_forms SET label = ?, intro = ?, thank_you = ?, status = ?, audience = ?, contact = ?, people = ?,
		ask_guests = ?, ask_allergies = ?, closes_at = ?, show_spots = ?, closed_pushed = 0, updated_at = datetime('now') WHERE event_id = ?`,
		strings.TrimSpace(f.Label), strings.TrimSpace(f.Intro), strings.TrimSpace(f.ThankYou), f.Status, f.Audience, f.Contact, f.People,
		f.AskGuests, f.AskAllergies, closes, f.ShowSpots, f.EventID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSignupForm removes a form with its questions. It is refused when
// anyone has signed up (withdraw or keep the form instead).
func (s *Store) DeleteSignupForm(eventID int64) error {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM signups WHERE event_id = ?`, eventID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return errors.New("This form has sign-ups, so it can't be deleted. Close it instead.")
	}
	res, err := s.DB.Exec(`DELETE FROM signup_forms WHERE event_id = ?`, eventID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- reading questions ----

// SignupQuestions is the form's questions in order, with choices (spots
// taken, exclusions) and "only ask when" choice IDs. Retired ones are
// included, flagged.
func (s *Store) SignupQuestions(eventID int64) ([]SignupQuestion, error) {
	return loadSignupQuestions(s.DB, eventID)
}

func loadSignupQuestions(q querier, eventID int64) ([]SignupQuestion, error) {
	rows, err := q.Query(`SELECT id, event_id, position, kind, label, help, required, per_person, public_list, retired
		FROM signup_questions WHERE event_id = ? ORDER BY position, id`, eventID)
	if err != nil {
		return nil, err
	}
	var qs []SignupQuestion
	idx := map[int64]int{}
	for rows.Next() {
		var x SignupQuestion
		if err := rows.Scan(&x.ID, &x.EventID, &x.Position, &x.Kind, &x.Label, &x.Help, &x.Required, &x.PerPerson, &x.PublicList, &x.Retired); err != nil {
			rows.Close()
			return nil, err
		}
		idx[x.ID] = len(qs)
		qs = append(qs, x)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(qs) == 0 {
		return nil, nil
	}

	// Spots taken: answers on 'in' sign-ups, one per picking person.
	taken := map[int64]int{}
	trows, err := q.Query(`SELECT a.choice_id, COUNT(*) FROM signup_answers a JOIN signups s ON s.id = a.signup_id
		WHERE s.event_id = ? AND s.status = 'in' AND a.choice_id IS NOT NULL GROUP BY a.choice_id`, eventID)
	if err != nil {
		return nil, err
	}
	for trows.Next() {
		var id int64
		var n int
		if err := trows.Scan(&id, &n); err != nil {
			trows.Close()
			return nil, err
		}
		taken[id] = n
	}
	if err := trows.Close(); err != nil {
		return nil, err
	}

	crows, err := q.Query(`SELECT c.id, c.question_id, c.position, c.label, COALESCE(c.spots, 0), c.retired
		FROM signup_choices c JOIN signup_questions x ON x.id = c.question_id
		WHERE x.event_id = ? ORDER BY c.position, c.id`, eventID)
	if err != nil {
		return nil, err
	}
	cidx := map[int64][2]int{} // choice ID → question index, choice index
	for crows.Next() {
		var c SignupChoice
		if err := crows.Scan(&c.ID, &c.QuestionID, &c.Position, &c.Label, &c.Spots, &c.Retired); err != nil {
			crows.Close()
			return nil, err
		}
		c.Taken = taken[c.ID]
		qi := idx[c.QuestionID]
		cidx[c.ID] = [2]int{qi, len(qs[qi].Choices)}
		qs[qi].Choices = append(qs[qi].Choices, c)
	}
	if err := crows.Close(); err != nil {
		return nil, err
	}

	xrows, err := q.Query(`SELECT e.choice_id, e.other_id FROM signup_choice_excludes e
		JOIN signup_choices c ON c.id = e.choice_id JOIN signup_questions x ON x.id = c.question_id
		WHERE x.event_id = ? ORDER BY e.choice_id, e.other_id`, eventID)
	if err != nil {
		return nil, err
	}
	for xrows.Next() {
		var a, b int64
		if err := xrows.Scan(&a, &b); err != nil {
			xrows.Close()
			return nil, err
		}
		ia, oka := cidx[a]
		ib, okb := cidx[b]
		if oka && okb {
			qs[ia[0]].Choices[ia[1]].Excludes = append(qs[ia[0]].Choices[ia[1]].Excludes, b)
			qs[ib[0]].Choices[ib[1]].Excludes = append(qs[ib[0]].Choices[ib[1]].Excludes, a)
		}
	}
	if err := xrows.Close(); err != nil {
		return nil, err
	}

	wrows, err := q.Query(`SELECT w.question_id, w.choice_id FROM signup_question_when w
		JOIN signup_questions x ON x.id = w.question_id WHERE x.event_id = ? ORDER BY w.question_id, w.choice_id`, eventID)
	if err != nil {
		return nil, err
	}
	for wrows.Next() {
		var qid, cid int64
		if err := wrows.Scan(&qid, &cid); err != nil {
			wrows.Close()
			return nil, err
		}
		if i, ok := idx[qid]; ok {
			qs[i].When = append(qs[i].When, cid)
		}
	}
	return qs, wrows.Close()
}

// SignupQuestion is one question with its choices.
func (s *Store) SignupQuestion(id int64) (*SignupQuestion, error) {
	var eventID int64
	if err := s.DB.QueryRow(`SELECT event_id FROM signup_questions WHERE id = ?`, id).Scan(&eventID); err != nil {
		return nil, notFound(err)
	}
	qs, err := loadSignupQuestions(s.DB, eventID)
	if err != nil {
		return nil, err
	}
	for i := range qs {
		if qs[i].ID == id {
			return &qs[i], nil
		}
	}
	return nil, ErrNotFound
}

// ---- writing questions ----

// SignupQuestionInput is what a planner types for a question.
type SignupQuestionInput struct {
	Kind       string
	Label      string
	Help       string
	Required   bool
	PerPerson  bool
	PublicList bool
	When       []int64 // choices of earlier questions; empty: always asked
}

// checkQuestion validates an input for question id (0 for a new one, which
// goes last) on the form of eventID, against the questions as they are.
func checkQuestion(q querier, eventID, id int64, in *SignupQuestionInput, qs []SignupQuestion) error {
	in.Label = strings.TrimSpace(in.Label)
	in.Help = strings.TrimSpace(in.Help)
	if in.Label == "" {
		return errors.New("The question needs some words")
	}
	if !oneOf(SignupKinds, in.Kind) {
		return errors.New("Pick what kind of question this is")
	}
	if in.PerPerson && in.PublicList {
		return errors.New("A question asked for each person can't be shown on the form: it would list children's names")
	}
	if in.PerPerson {
		f, err := loadSignupForm(q, eventID)
		if err != nil {
			return err
		}
		if f.People != "list" {
			return errors.New("Asking once per person needs \"Who's coming\" to be a list")
		}
	}
	// Position of this question: its own, or last for a new one.
	pos := 0
	byQ := map[int64]*SignupQuestion{}
	byChoice := map[int64]*SignupQuestion{}
	for i := range qs {
		x := &qs[i]
		byQ[x.ID] = x
		for _, c := range x.Choices {
			byChoice[c.ID] = x
		}
		if x.ID == id {
			pos = x.Position
		} else if id == 0 && x.Position >= pos {
			pos = x.Position + 1
		}
	}
	seen := map[int64]bool{}
	for _, cid := range in.When {
		if seen[cid] {
			continue
		}
		seen[cid] = true
		cq := byChoice[cid]
		switch {
		case cq == nil:
			return errors.New("\"Only ask when\" picks a choice that isn't on this form")
		case cq.ID == id:
			return errors.New("A question can't depend on its own choices")
		case cq.Position >= pos:
			return fmt.Errorf("\"Only ask when\" needs a choice from an earlier question, and %q comes later", cq.Label)
		case len(cq.When) > 0:
			return fmt.Errorf("\"Only ask when\" goes one level only: %q is itself only asked sometimes", cq.Label)
		}
	}
	if len(in.When) > 0 && id != 0 {
		// This question can't be the target of another question's condition.
		for _, x := range qs {
			for _, cid := range x.When {
				if byChoice[cid] != nil && byChoice[cid].ID == id {
					return fmt.Errorf("%q is only asked after this question's choices, so this one can't be conditional too", x.Label)
				}
			}
		}
	}
	if id != 0 {
		old := byQ[id]
		if old == nil {
			return ErrNotFound
		}
		var answered int
		if err := q.QueryRow(`SELECT COUNT(*) FROM signup_answers WHERE question_id = ?`, id).Scan(&answered); err != nil {
			return err
		}
		if answered > 0 && (old.Kind != in.Kind || old.PerPerson != in.PerPerson) {
			return errors.New("This question has answers, so its kind and per-person setting can't change. Retire it and add a new one.")
		}
		if !IsPickKind(in.Kind) && len(old.Choices) > 0 {
			return errors.New("Remove the choices before making this a question without choices")
		}
		if old.PerPerson != in.PerPerson {
			for _, c := range old.Choices {
				if len(c.Excludes) > 0 {
					return errors.New("Clear the \"can't be picked with\" lists of its choices before changing per-person")
				}
			}
		}
	}
	return nil
}

func setWhen(q querier, id int64, when []int64) error {
	if _, err := q.Exec(`DELETE FROM signup_question_when WHERE question_id = ?`, id); err != nil {
		return err
	}
	for _, cid := range when {
		if _, err := q.Exec(`INSERT OR IGNORE INTO signup_question_when (question_id, choice_id) VALUES (?, ?)`, id, cid); err != nil {
			return err
		}
	}
	return nil
}

// CreateSignupQuestion adds a question last on the event's form.
func (s *Store) CreateSignupQuestion(eventID int64, in SignupQuestionInput) (int64, error) {
	var id int64
	err := s.immediate(func(q querier) error {
		if _, err := loadSignupForm(q, eventID); err != nil {
			return err
		}
		qs, err := loadSignupQuestions(q, eventID)
		if err != nil {
			return err
		}
		if err := checkQuestion(q, eventID, 0, &in, qs); err != nil {
			return err
		}
		var pos int
		if err := q.QueryRow(`SELECT COALESCE(MAX(position), 0) + 1 FROM signup_questions WHERE event_id = ?`, eventID).Scan(&pos); err != nil {
			return err
		}
		res, err := q.Exec(`INSERT INTO signup_questions (event_id, position, kind, label, help, required, per_person, public_list)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, eventID, pos, in.Kind, in.Label, in.Help, in.Required, in.PerPerson, in.PublicList)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		return setWhen(q, id, in.When)
	})
	return id, err
}

// UpdateSignupQuestion saves a question's settings and its "only ask when".
func (s *Store) UpdateSignupQuestion(id int64, in SignupQuestionInput) error {
	return s.immediate(func(q querier) error {
		var eventID int64
		if err := q.QueryRow(`SELECT event_id FROM signup_questions WHERE id = ?`, id).Scan(&eventID); err != nil {
			return notFound(err)
		}
		qs, err := loadSignupQuestions(q, eventID)
		if err != nil {
			return err
		}
		if err := checkQuestion(q, eventID, id, &in, qs); err != nil {
			return err
		}
		if _, err := q.Exec(`UPDATE signup_questions SET kind = ?, label = ?, help = ?, required = ?, per_person = ?, public_list = ? WHERE id = ?`,
			in.Kind, in.Label, in.Help, in.Required, in.PerPerson, in.PublicList, id); err != nil {
			return err
		}
		return setWhen(q, id, in.When)
	})
}

// ErrHasAnswers is returned when deleting something people have answered;
// the caller offers Retire instead.
var ErrHasAnswers = errors.New("People have answered this, so it can't be deleted. Retire it instead.")

// DeleteSignupQuestion removes a question with its choices, refused with
// ErrHasAnswers if it was answered.
func (s *Store) DeleteSignupQuestion(id int64) error {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM signup_answers WHERE question_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrHasAnswers
	}
	res, err := s.DB.Exec(`DELETE FROM signup_questions WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RetireSignupQuestion hides a question from the form (or brings it back);
// its answers stay.
func (s *Store) RetireSignupQuestion(id int64, retired bool) error {
	res, err := s.DB.Exec(`UPDATE signup_questions SET retired = ? WHERE id = ?`, retired, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MoveSignupQuestion swaps a question with its neighbour (dir -1 up, +1
// down). Moving a question above the choices it depends on, or one that
// depends on it below it, is refused.
func (s *Store) MoveSignupQuestion(id int64, dir int) error {
	return s.immediate(func(q querier) error {
		var eventID int64
		if err := q.QueryRow(`SELECT event_id FROM signup_questions WHERE id = ?`, id).Scan(&eventID); err != nil {
			return notFound(err)
		}
		qs, err := loadSignupQuestions(q, eventID)
		if err != nil {
			return err
		}
		i := -1
		for k := range qs {
			if qs[k].ID == id {
				i = k
			}
		}
		j := i + dir
		if i < 0 || j < 0 || j >= len(qs) || dir == 0 {
			return nil // already at the end
		}
		// Renumber so positions are distinct, with the two swapped.
		qs[i], qs[j] = qs[j], qs[i]
		pos := map[int64]int{}
		for k := range qs {
			pos[qs[k].ID] = k + 1
		}
		owner := map[int64]int64{}
		for _, x := range qs {
			for _, c := range x.Choices {
				owner[c.ID] = x.ID
			}
		}
		for _, x := range qs {
			for _, cid := range x.When {
				if pos[owner[cid]] >= pos[x.ID] {
					for _, o := range qs {
						if o.ID == owner[cid] {
							return fmt.Errorf("%q has to stay after %q, which it depends on", x.Label, o.Label)
						}
					}
				}
			}
		}
		for k := range qs {
			if _, err := q.Exec(`UPDATE signup_questions SET position = ? WHERE id = ?`, k+1, qs[k].ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---- choices ----

// ParseChoiceLines reads pasted lines like "Make chili | 12" (label, then
// optional spots). Blank lines are skipped.
func ParseChoiceLines(text string) (labels []string, spots []int, err error) {
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		label, sp, has := strings.Cut(line, "|")
		label = strings.TrimSpace(label)
		if label == "" {
			return nil, nil, fmt.Errorf("Line %d has no name for the choice", n+1)
		}
		count := 0
		if has && strings.TrimSpace(sp) != "" {
			count, err = strconv.Atoi(strings.TrimSpace(sp))
			if err != nil || count < 1 {
				return nil, nil, fmt.Errorf("Line %d: spots should be a whole number above 0", n+1)
			}
		}
		labels = append(labels, label)
		spots = append(spots, count)
	}
	return labels, spots, nil
}

func addChoice(q querier, questionID int64, label string, spots int) (int64, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return 0, errors.New("The choice needs a name")
	}
	if spots < 0 {
		return 0, errors.New("Spots can't be negative")
	}
	var kind string
	if err := q.QueryRow(`SELECT kind FROM signup_questions WHERE id = ?`, questionID).Scan(&kind); err != nil {
		return 0, notFound(err)
	}
	if !IsPickKind(kind) {
		return 0, errors.New("Only questions that pick from choices have choices")
	}
	var pos int
	if err := q.QueryRow(`SELECT COALESCE(MAX(position), 0) + 1 FROM signup_choices WHERE question_id = ?`, questionID).Scan(&pos); err != nil {
		return 0, err
	}
	res, err := q.Exec(`INSERT INTO signup_choices (question_id, position, label, spots) VALUES (?, ?, ?, ?)`, questionID, pos, label, nullSpots(spots))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CreateSignupChoice adds a choice last on a question. spots 0 = no limit.
func (s *Store) CreateSignupChoice(questionID int64, label string, spots int) (int64, error) {
	return addChoice(s.DB, questionID, label, spots)
}

// AddSignupChoices adds one choice per pasted line ("Make chili | 12").
// Nothing is added if a line is wrong. Returns how many were added.
func (s *Store) AddSignupChoices(questionID int64, text string) (int, error) {
	labels, spots, err := ParseChoiceLines(text)
	if err != nil {
		return 0, err
	}
	err = s.immediate(func(q querier) error {
		for i := range labels {
			if _, err := addChoice(q, questionID, labels[i], spots[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(labels), nil
}

// UpdateSignupChoice changes a choice's label and spots (0 = no limit).
// Lowering spots below what's taken is allowed; nobody is dropped.
func (s *Store) UpdateSignupChoice(id int64, label string, spots int) error {
	label = strings.TrimSpace(label)
	if label == "" {
		return errors.New("The choice needs a name")
	}
	if spots < 0 {
		return errors.New("Spots can't be negative")
	}
	res, err := s.DB.Exec(`UPDATE signup_choices SET label = ?, spots = ? WHERE id = ?`, label, nullSpots(spots), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSignupChoice removes a choice, refused with ErrHasAnswers if anyone
// picked it.
func (s *Store) DeleteSignupChoice(id int64) error {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM signup_answers WHERE choice_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrHasAnswers
	}
	res, err := s.DB.Exec(`DELETE FROM signup_choices WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RetireSignupChoice stops a choice being picked (or brings it back). Its
// exclusions stay for the answers already given.
func (s *Store) RetireSignupChoice(id int64, retired bool) error {
	res, err := s.DB.Exec(`UPDATE signup_choices SET retired = ? WHERE id = ?`, retired, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MoveSignupChoice swaps a choice with its neighbour on the question.
func (s *Store) MoveSignupChoice(id int64, dir int) error {
	return s.immediate(func(q querier) error {
		var qid int64
		if err := q.QueryRow(`SELECT question_id FROM signup_choices WHERE id = ?`, id).Scan(&qid); err != nil {
			return notFound(err)
		}
		rows, err := q.Query(`SELECT id FROM signup_choices WHERE question_id = ? ORDER BY position, id`, qid)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var c int64
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, c)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		i := -1
		for k, c := range ids {
			if c == id {
				i = k
			}
		}
		j := i + dir
		if i < 0 || j < 0 || j >= len(ids) || dir == 0 {
			return nil
		}
		ids[i], ids[j] = ids[j], ids[i]
		for k, c := range ids {
			if _, err := q.Exec(`UPDATE signup_choices SET position = ? WHERE id = ?`, k+1, c); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetChoiceExcludes sets the list of choices that can't be picked with
// this one, replacing the old list (pairs are stored once, lower ID first,
// and read both ways). The others must be on the same form, not this
// choice, and on questions with the same per-person setting as this one's.
func (s *Store) SetChoiceExcludes(choiceID int64, others []int64) error {
	return s.immediate(func(q querier) error {
		var eventID int64
		var perPerson bool
		err := q.QueryRow(`SELECT x.event_id, x.per_person FROM signup_choices c JOIN signup_questions x ON x.id = c.question_id
			WHERE c.id = ?`, choiceID).Scan(&eventID, &perPerson)
		if err != nil {
			return notFound(err)
		}
		seen := map[int64]bool{}
		for _, o := range others {
			if o == choiceID {
				return errors.New("A choice can't exclude itself")
			}
			if seen[o] {
				continue
			}
			seen[o] = true
			var oEvent int64
			var oPer bool
			err := q.QueryRow(`SELECT x.event_id, x.per_person FROM signup_choices c JOIN signup_questions x ON x.id = c.question_id
				WHERE c.id = ?`, o).Scan(&oEvent, &oPer)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return errors.New("That choice isn't on this form")
				}
				return err
			}
			if oEvent != eventID {
				return errors.New("That choice isn't on this form")
			}
			if oPer != perPerson {
				return errors.New("A choice asked for each person can't be linked with one asked once per sign-up")
			}
		}
		if _, err := q.Exec(`DELETE FROM signup_choice_excludes WHERE choice_id = ? OR other_id = ?`, choiceID, choiceID); err != nil {
			return err
		}
		for o := range seen {
			a, b := choiceID, o
			if a > b {
				a, b = b, a
			}
			if _, err := q.Exec(`INSERT INTO signup_choice_excludes (choice_id, other_id) VALUES (?, ?)`, a, b); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---- copy ----

// copySignupFormTx copies src's form to dst (an event that has none): a new
// random code, status draft, the same settings, the questions and choices
// that aren't retired, and their "only ask when" and "can't be picked with"
// links where both ends were copied. No sign-ups or answers. Does nothing
// when src has no form.
func copySignupFormTx(tx *sql.Tx, src, dst int64) error {
	form, err := loadSignupForm(tx, src)
	if errors.Is(err, ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if err := insertSignupForm(tx, dst, form); err != nil {
		return err
	}
	qs, err := loadSignupQuestions(tx, src)
	if err != nil {
		return err
	}
	choiceMap := map[int64]int64{}
	questionMap := map[int64]int64{}
	pos := 0
	for _, x := range qs {
		if x.Retired {
			continue
		}
		pos++
		res, err := tx.Exec(`INSERT INTO signup_questions (event_id, position, kind, label, help, required, per_person, public_list)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, dst, pos, x.Kind, x.Label, x.Help, x.Required, x.PerPerson, x.PublicList)
		if err != nil {
			return err
		}
		nq, _ := res.LastInsertId()
		questionMap[x.ID] = nq
		cpos := 0
		for _, c := range x.Choices {
			if c.Retired {
				continue
			}
			cpos++
			res, err := tx.Exec(`INSERT INTO signup_choices (question_id, position, label, spots) VALUES (?, ?, ?, ?)`,
				nq, cpos, c.Label, nullSpots(c.Spots))
			if err != nil {
				return err
			}
			nc, _ := res.LastInsertId()
			choiceMap[c.ID] = nc
		}
	}
	for _, x := range qs {
		nq, ok := questionMap[x.ID]
		if !ok {
			continue
		}
		for _, cid := range x.When {
			if nc, ok := choiceMap[cid]; ok {
				if _, err := tx.Exec(`INSERT INTO signup_question_when (question_id, choice_id) VALUES (?, ?)`, nq, nc); err != nil {
					return err
				}
			}
		}
		for _, c := range x.Choices {
			for _, o := range c.Excludes {
				a, aok := choiceMap[c.ID]
				b, bok := choiceMap[o]
				if !aok || !bok || c.ID > o { // each pair once
					continue
				}
				if a > b {
					a, b = b, a
				}
				if _, err := tx.Exec(`INSERT INTO signup_choice_excludes (choice_id, other_id) VALUES (?, ?)`, a, b); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// SignupChoiceOf is the question and event a choice belongs to.
func (s *Store) SignupChoiceOf(id int64) (questionID, eventID int64, err error) {
	err = s.DB.QueryRow(`SELECT c.question_id, x.event_id FROM signup_choices c JOIN signup_questions x ON x.id = c.question_id
		WHERE c.id = ?`, id).Scan(&questionID, &eventID)
	return questionID, eventID, notFound(err)
}
