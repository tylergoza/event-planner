package store

import (
	"strconv"
	"strings"
	"unicode"
)

// Reading sign-ups back: lists, counts, CSV, the allergy list and "who's
// bringing what" (SIGNUPS_PLAN.md steps 2-4).

// Signup is one submission with its people and answers.
type Signup struct {
	ID          int64
	EventID     int64
	UserID      int64 // 0 for a visitor
	Name        string
	Phone       string
	Email       string
	Guests      int
	Status      string // in, withdrawn
	PlannerNote string
	CreatedAt   string
	UpdatedAt   string
	People      []SignupPerson
	Answers     []SignupAnswer
}

// SignupPerson is someone a sign-up is for.
type SignupPerson struct {
	ID                int64
	Position          int
	Name              string
	DirectoryPersonID int64 // 0: typed in
	Allergies         string
}

// SignupAnswer is one row: a picked choice (ChoiceID set) or a value.
// PersonID is 0 for per-sign-up questions.
type SignupAnswer struct {
	PersonID   int64
	QuestionID int64
	ChoiceID   int64
	Value      string
}

const signupCols = `s.id, s.event_id, COALESCE(s.user_id, 0), s.name, s.phone, s.email, s.guests, s.status, s.planner_note, s.created_at, s.updated_at`

func scanSignupRow(sc interface{ Scan(...any) error }) (Signup, error) {
	var x Signup
	err := sc.Scan(&x.ID, &x.EventID, &x.UserID, &x.Name, &x.Phone, &x.Email, &x.Guests, &x.Status, &x.PlannerNote, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}

// loadSignupsOf reads the sign-ups of an event (withdrawn ones too when
// asked), oldest first, with people and answers.
func loadSignupsOf(q querier, eventID int64, withWithdrawn bool) ([]Signup, error) {
	where := ` WHERE s.event_id = ?`
	if !withWithdrawn {
		where += ` AND s.status = 'in'`
	}
	rows, err := q.Query(`SELECT `+signupCols+` FROM signups s`+where+` ORDER BY s.created_at, s.id`, eventID)
	if err != nil {
		return nil, err
	}
	var out []Signup
	idx := map[int64]int{}
	for rows.Next() {
		x, err := scanSignupRow(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		idx[x.ID] = len(out)
		out = append(out, x)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	prows, err := q.Query(`SELECT p.signup_id, p.id, p.position, p.name, COALESCE(p.directory_person_id, 0), p.allergies
		FROM signup_people p JOIN signups s ON s.id = p.signup_id WHERE s.event_id = ? ORDER BY p.position, p.id`, eventID)
	if err != nil {
		return nil, err
	}
	for prows.Next() {
		var sid int64
		var p SignupPerson
		if err := prows.Scan(&sid, &p.ID, &p.Position, &p.Name, &p.DirectoryPersonID, &p.Allergies); err != nil {
			prows.Close()
			return nil, err
		}
		if i, ok := idx[sid]; ok {
			out[i].People = append(out[i].People, p)
		}
	}
	if err := prows.Close(); err != nil {
		return nil, err
	}
	arows, err := q.Query(`SELECT a.signup_id, COALESCE(a.person_id, 0), a.question_id, COALESCE(a.choice_id, 0), a.value
		FROM signup_answers a JOIN signups s ON s.id = a.signup_id JOIN signup_questions x ON x.id = a.question_id
		WHERE s.event_id = ? ORDER BY x.position, a.rowid`, eventID)
	if err != nil {
		return nil, err
	}
	for arows.Next() {
		var sid int64
		var a SignupAnswer
		if err := arows.Scan(&sid, &a.PersonID, &a.QuestionID, &a.ChoiceID, &a.Value); err != nil {
			arows.Close()
			return nil, err
		}
		if i, ok := idx[sid]; ok {
			out[i].Answers = append(out[i].Answers, a)
		}
	}
	return out, arows.Close()
}

func loadSignupByID(q querier, id int64) (*Signup, error) {
	var eventID int64
	if err := q.QueryRow(`SELECT event_id FROM signups WHERE id = ?`, id).Scan(&eventID); err != nil {
		return nil, notFound(err)
	}
	all, err := loadSignupsOf(q, eventID, true)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, ErrNotFound
}

// GetSignup is a sign-up by ID (any status).
func (s *Store) GetSignup(id int64) (*Signup, error) { return loadSignupByID(s.DB, id) }

// GetSignupByCode finds a sign-up by the code from its status link, which
// is looked up by its SHA-256 hash.
func (s *Store) GetSignupByCode(code string) (*Signup, error) {
	var id int64
	if err := s.DB.QueryRow(`SELECT id FROM signups WHERE code_hash = ?`, HashSignupCode(code)).Scan(&id); err != nil {
		return nil, notFound(err)
	}
	return loadSignupByID(s.DB, id)
}

// ListSignups is an event's sign-ups, oldest first, with people and
// answers. Withdrawn ones only when withWithdrawn.
func (s *Store) ListSignups(eventID int64, withWithdrawn bool) ([]Signup, error) {
	return loadSignupsOf(s.DB, eventID, withWithdrawn)
}

// UserSignup is a sign-up with the event it's for, for /my/signups.
type UserSignup struct {
	Signup
	EventTitle string
	StartsOn   string
	StartTime  string
	FormCode   string
}

// SignupsByUser is a signed-in user's sign-ups: upcoming events first
// (soonest first), then events not scheduled yet, then past ones (latest
// first). today is YYYY-MM-DD. People and answers are not loaded.
func (s *Store) SignupsByUser(userID int64, today string) ([]UserSignup, error) {
	rows, err := s.DB.Query(`SELECT `+signupCols+`, e.title, e.starts_on, e.start_time, f.code
		FROM signups s JOIN events e ON e.id = s.event_id JOIN signup_forms f ON f.event_id = s.event_id
		WHERE s.user_id = ? ORDER BY
			CASE WHEN e.starts_on >= ? THEN 0 WHEN e.starts_on = '' THEN 1 ELSE 2 END,
			CASE WHEN e.starts_on >= ? THEN e.starts_on END,
			e.starts_on DESC, s.id`, userID, today, today)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserSignup
	for rows.Next() {
		var u UserSignup
		err := rows.Scan(&u.ID, &u.EventID, &u.UserID, &u.Name, &u.Phone, &u.Email, &u.Guests, &u.Status, &u.PlannerNote,
			&u.CreatedAt, &u.UpdatedAt, &u.EventTitle, &u.StartsOn, &u.StartTime, &u.FormCode)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SignupHeadcount is the people on 'in' sign-ups and their guests; the
// headcount is both added.
func (s *Store) SignupHeadcount(eventID int64) (people, guests int, err error) {
	err = s.DB.QueryRow(`SELECT
		(SELECT COUNT(*) FROM signup_people p JOIN signups s ON s.id = p.signup_id WHERE s.event_id = ? AND s.status = 'in'),
		(SELECT COALESCE(SUM(guests), 0) FROM signups WHERE event_id = ? AND status = 'in')`, eventID, eventID).Scan(&people, &guests)
	return
}

// SignupChoiceCounts is the spots taken per choice ID on 'in' sign-ups.
func (s *Store) SignupChoiceCounts(eventID int64) (map[int64]int, error) {
	qs, err := loadSignupQuestions(s.DB, eventID)
	if err != nil {
		return nil, err
	}
	out := map[int64]int{}
	for _, x := range qs {
		for _, c := range x.Choices {
			out[c.ID] = c.Taken
		}
	}
	return out, nil
}

// SignupPersonRow is one person for the "by person" list and the allergy
// list.
type SignupPersonRow struct {
	PersonID          int64
	Name              string
	Allergies         string
	DirectoryPersonID int64
	SignupID          int64
	SignerName        string
	SignerPhone       string
	SignerEmail       string
	Status            string
}

// SignupPeople is every person on the event's sign-ups, by name.
func (s *Store) SignupPeople(eventID int64, withWithdrawn bool) ([]SignupPersonRow, error) {
	q := `SELECT p.id, p.name, p.allergies, COALESCE(p.directory_person_id, 0), s.id, s.name, s.phone, s.email, s.status
		FROM signup_people p JOIN signups s ON s.id = p.signup_id WHERE s.event_id = ?`
	if !withWithdrawn {
		q += ` AND s.status = 'in'`
	}
	rows, err := s.DB.Query(q+` ORDER BY p.name COLLATE NOCASE, p.id`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SignupPersonRow
	for rows.Next() {
		var r SignupPersonRow
		if err := rows.Scan(&r.PersonID, &r.Name, &r.Allergies, &r.DirectoryPersonID, &r.SignupID, &r.SignerName, &r.SignerPhone, &r.SignerEmail, &r.Status); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SignupAllergies is the typed allergies of people on 'in' sign-ups, with
// who signed them up and that person's phone. For the event team only.
func (s *Store) SignupAllergies(eventID int64) ([]SignupPersonRow, error) {
	rows, err := s.SignupPeople(eventID, false)
	if err != nil {
		return nil, err
	}
	var out []SignupPersonRow
	for _, r := range rows {
		if strings.TrimSpace(r.Allergies) != "" {
			out = append(out, r)
		}
	}
	return out, nil
}

// ---- CSV ----

// SignupCSV builds the rows for the event's CSV (write them with
// encoding/csv): a header, then one row per person. Questions asked once
// per sign-up repeat on each of its people's rows; picked choices are
// joined with "; "; retired questions come last; typed allergies are
// included. Guests are on a sign-up's first row only, so a column sum is
// right. Directory notes never are. Withdrawn sign-ups only when
// withWithdrawn.
func (s *Store) SignupCSV(eventID int64, withWithdrawn bool) ([][]string, error) {
	qs, err := loadSignupQuestions(s.DB, eventID)
	if err != nil {
		return nil, err
	}
	signups, err := loadSignupsOf(s.DB, eventID, withWithdrawn)
	if err != nil {
		return nil, err
	}
	var cols []SignupQuestion
	for _, x := range qs {
		if !x.Retired {
			cols = append(cols, x)
		}
	}
	for _, x := range qs {
		if x.Retired {
			cols = append(cols, x)
		}
	}
	choiceLabel := map[int64]string{}
	for _, x := range qs {
		for _, c := range x.Choices {
			choiceLabel[c.ID] = c.Label
		}
	}
	header := []string{"Signed up by", "Phone", "Email", "Status", "Person", "Allergies", "Guests", "Signed up on"}
	for _, x := range cols {
		h := x.Label
		if x.Retired {
			h += " (retired)"
		}
		header = append(header, h)
	}
	out := [][]string{header}
	for _, su := range signups {
		type key struct{ person, q int64 }
		cells := map[key][]string{}
		for _, a := range su.Answers {
			k := key{a.PersonID, a.QuestionID}
			if a.ChoiceID != 0 {
				cells[k] = append(cells[k], choiceLabel[a.ChoiceID])
			} else if a.Value != "" {
				cells[k] = append(cells[k], a.Value)
			}
		}
		for i, p := range su.People {
			g := ""
			if i == 0 {
				g = strconv.Itoa(su.Guests)
			}
			row := []string{su.Name, su.Phone, su.Email, su.Status, p.Name, p.Allergies, g, su.CreatedAt}
			for _, x := range cols {
				var v []string
				if x.PerPerson {
					v = cells[key{p.ID, x.ID}]
				} else {
					v = cells[key{0, x.ID}]
				}
				row = append(row, strings.Join(v, "; "))
			}
			out = append(out, row)
		}
	}
	return out, nil
}

// ---- who's bringing what ----

// FirstNameInitial is "Tyler Goza" as "Tyler G.": the first name and the
// last name's initial. One name stays as it is.
func FirstNameInitial(name string) string {
	f := strings.Fields(name)
	switch len(f) {
	case 0:
		return ""
	case 1:
		return f[0]
	}
	last := []rune(f[len(f)-1])
	return f[0] + " " + string(unicode.ToUpper(last[0])) + "."
}

// BringingEntry is one signer on a list: "Tyler G." and, for answers to
// "Show on the form" text questions, "Smoky three-bean".
type BringingEntry struct {
	Who    string
	Detail []string
}

// Text is the entry as shown: "Tyler G. · Smoky three-bean".
func (e BringingEntry) Text() string {
	return strings.Join(append([]string{e.Who}, e.Detail...), " · ")
}

// BringingList is one group on the form page: a choice ("Make chili"), or
// a question that has no choice to hang off.
type BringingList struct {
	Title   string
	Entries []BringingEntry
}

// WhosBringingWhat is the public lists from questions marked Show on the
// form, from 'in' sign-ups only, in the order of the form. A pick question
// lists signers under each choice they picked; a text, number or long-text
// question that is only asked when some choices are picked adds its answer
// to those choices' entries (otherwise it gets a list of its own). It
// reads only per-sign-up questions and shows only the signer's first name
// and last initial: never people's names or per-person answers.
func (s *Store) WhosBringingWhat(eventID int64) ([]BringingList, error) {
	qs, err := loadSignupQuestions(s.DB, eventID)
	if err != nil {
		return nil, err
	}
	signups, err := loadSignupsOf(s.DB, eventID, false)
	if err != nil {
		return nil, err
	}
	type listKey struct{ choice, question int64 }
	type list struct {
		title   string
		entries map[int64]*BringingEntry // by sign-up
		order   []int64
	}
	var keys []listKey
	lists := map[listKey]*list{}
	ensure := func(k listKey, title string) {
		if _, ok := lists[k]; !ok {
			lists[k] = &list{title: title, entries: map[int64]*BringingEntry{}}
			keys = append(keys, k)
		}
	}
	entry := func(k listKey, su *Signup) *BringingEntry {
		l := lists[k]
		e, ok := l.entries[su.ID]
		if !ok {
			e = &BringingEntry{Who: FirstNameInitial(su.Name)}
			l.entries[su.ID] = e
			l.order = append(l.order, su.ID)
		}
		return e
	}
	label := map[int64]string{}
	for _, x := range qs {
		for _, c := range x.Choices {
			label[c.ID] = c.Label
		}
	}
	// Create the lists in the order of the form.
	for _, x := range qs {
		if x.Retired || x.PerPerson || !x.PublicList {
			continue
		}
		switch {
		case IsPickKind(x.Kind):
			for _, c := range x.Choices {
				if !c.Retired {
					ensure(listKey{choice: c.ID}, c.Label)
				}
			}
		case len(x.When) > 0:
			for _, cid := range x.When {
				ensure(listKey{choice: cid}, label[cid])
			}
		default:
			ensure(listKey{question: x.ID}, x.Label)
		}
	}
	for i := range signups {
		su := &signups[i]
		picked := map[int64]bool{}
		vals := map[int64][]string{}
		for _, a := range su.Answers {
			if a.PersonID != 0 {
				continue
			}
			if a.ChoiceID != 0 {
				picked[a.ChoiceID] = true
			} else if a.Value != "" {
				vals[a.QuestionID] = append(vals[a.QuestionID], a.Value)
			}
		}
		for _, x := range qs {
			if x.Retired || x.PerPerson || !x.PublicList {
				continue
			}
			if IsPickKind(x.Kind) {
				for _, c := range x.Choices {
					if picked[c.ID] && !c.Retired {
						entry(listKey{choice: c.ID}, su)
					}
				}
				continue
			}
			v := vals[x.ID]
			if len(v) == 0 || x.Kind == "yes_no" {
				continue
			}
			if len(x.When) > 0 {
				for _, cid := range x.When {
					if picked[cid] {
						e := entry(listKey{choice: cid}, su)
						e.Detail = append(e.Detail, v...)
					}
				}
			} else {
				e := entry(listKey{question: x.ID}, su)
				e.Detail = append(e.Detail, v...)
			}
		}
	}
	var out []BringingList
	for _, k := range keys {
		l := lists[k]
		if len(l.order) == 0 {
			continue
		}
		bl := BringingList{Title: l.title}
		for _, id := range l.order {
			bl.Entries = append(bl.Entries, *l.entries[id])
		}
		out = append(out, bl)
	}
	return out, nil
}
