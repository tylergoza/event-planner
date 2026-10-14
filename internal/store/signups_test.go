package store

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

var signupNow = time.Date(2026, 10, 14, 10, 0, 0, 0, time.Local)

func mustQ(t *testing.T, st *Store, ev int64, in SignupQuestionInput) int64 {
	t.Helper()
	id, err := st.CreateSignupQuestion(ev, in)
	if err != nil {
		t.Fatalf("question %q: %v", in.Label, err)
	}
	return id
}

func mustC(t *testing.T, st *Store, q int64, label string, spots int) int64 {
	t.Helper()
	id, err := st.CreateSignupChoice(q, label, spots)
	if err != nil {
		t.Fatalf("choice %q: %v", label, err)
	}
	return id
}

func openForm(t *testing.T, st *Store, ev int64, people string) *SignupForm {
	t.Helper()
	f, err := st.CreateSignupForm(ev)
	if err != nil {
		t.Fatal(err)
	}
	f.Status = "open"
	f.People = people
	f.AskGuests = true
	f.AskAllergies = true
	if err := st.UpdateSignupForm(f); err != nil {
		t.Fatal(err)
	}
	return f
}

// cookoff builds the chili example: question 1 pick any required, 2 a
// follow-up name shown publicly, only when Make chili.
type cookoff struct {
	ev, q1, q2, q3                      int64
	makeChili, makeCorn, judgeChili, jc int64
	mild, hot                           int64
}

func newCookoff(t *testing.T, st *Store, chiliSpots int) cookoff {
	t.Helper()
	var c cookoff
	c.ev = newEvent(t, st, "Cook-Off")
	openForm(t, st, c.ev, "self")
	c.q1 = mustQ(t, st, c.ev, SignupQuestionInput{Kind: "many", Label: "What would you like to do?", Required: true})
	c.makeChili = mustC(t, st, c.q1, "Make chili", chiliSpots)
	c.makeCorn = mustC(t, st, c.q1, "Make cornbread", 0)
	c.judgeChili = mustC(t, st, c.q1, "Judge chili", 0)
	c.jc = mustC(t, st, c.q1, "Judge cornbread", 0)
	c.q2 = mustQ(t, st, c.ev, SignupQuestionInput{Kind: "text", Label: "Name your chili", PublicList: true, Required: true, When: []int64{c.makeChili}})
	c.q3 = mustQ(t, st, c.ev, SignupQuestionInput{Kind: "one", Label: "How hot?", When: []int64{c.makeChili}})
	c.mild = mustC(t, st, c.q3, "Mild", 0)
	c.hot = mustC(t, st, c.q3, "Hot", 0)
	if err := st.SetChoiceExcludes(c.makeChili, []int64{c.judgeChili}); err != nil {
		t.Fatal(err)
	}
	return c
}

func (c cookoff) input(name string, picks ...int64) SignupInput {
	return SignupInput{EventID: c.ev, Name: name, Phone: "555", Answers: []AnswerInput{{QuestionID: c.q1, ChoiceIDs: picks}}}
}

func saveOK(t *testing.T, st *Store, in SignupInput, opts SignupOptions) *SignupResult {
	t.Helper()
	opts.Now = signupNow
	r, err := st.SaveSignup(in, opts)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	return r
}

func saveErr(t *testing.T, st *Store, in SignupInput, opts SignupOptions) *SignupError {
	t.Helper()
	opts.Now = signupNow
	_, err := st.SaveSignup(in, opts)
	var se *SignupError
	if !errors.As(err, &se) {
		t.Fatalf("want a SignupError, got %v", err)
	}
	return se
}

func TestSignupOnlyAskWhenEarlierQuestionOnly(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Dinner")
	openForm(t, st, ev, "self")
	q1 := mustQ(t, st, ev, SignupQuestionInput{Kind: "one", Label: "First"})
	c1 := mustC(t, st, q1, "A", 0)
	q2 := mustQ(t, st, ev, SignupQuestionInput{Kind: "one", Label: "Second"})
	c2 := mustC(t, st, q2, "B", 0)

	// A later question's choice can't be a condition for an earlier one.
	if err := st.UpdateSignupQuestion(q1, SignupQuestionInput{Kind: "one", Label: "First", When: []int64{c2}}); err == nil {
		t.Fatal("later question's choice accepted")
	}
	// Itself.
	if err := st.UpdateSignupQuestion(q1, SignupQuestionInput{Kind: "one", Label: "First", When: []int64{c1}}); err == nil {
		t.Fatal("own choice accepted")
	}
	// Earlier is fine.
	if err := st.UpdateSignupQuestion(q2, SignupQuestionInput{Kind: "one", Label: "Second", When: []int64{c1}}); err != nil {
		t.Fatal(err)
	}
	// One level only: a third can't hang off the second.
	q3 := mustQ(t, st, ev, SignupQuestionInput{Kind: "text", Label: "Third"})
	if err := st.UpdateSignupQuestion(q3, SignupQuestionInput{Kind: "text", Label: "Third", When: []int64{c2}}); err == nil {
		t.Fatal("two levels accepted")
	}
	// And the first can't now be made conditional, since the second hangs off it.
	if err := st.UpdateSignupQuestion(q1, SignupQuestionInput{Kind: "one", Label: "First", When: []int64{}}); err != nil {
		t.Fatal(err)
	}
	// Moving the second above its condition is refused; the order is kept.
	if err := st.MoveSignupQuestion(q2, -1); err == nil {
		t.Fatal("moved above its condition")
	}
	qs, _ := st.SignupQuestions(ev)
	if qs[0].ID != q1 || qs[1].ID != q2 {
		t.Fatalf("order changed: %v", qs)
	}
	// Moving the third up past the second is fine.
	if err := st.MoveSignupQuestion(q3, -1); err != nil {
		t.Fatal(err)
	}
	qs, _ = st.SignupQuestions(ev)
	if qs[1].ID != q3 {
		t.Fatalf("not moved: %v", qs)
	}
}

func TestSignupExcludesScopeAndPublicList(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Craft night")
	openForm(t, st, ev, "list")
	a := mustQ(t, st, ev, SignupQuestionInput{Kind: "many", Label: "Bring"})
	ac := mustC(t, st, a, "Chairs", 0)
	b := mustQ(t, st, ev, SignupQuestionInput{Kind: "one", Label: "Table", PerPerson: true})
	bc := mustC(t, st, b, "Clay", 0)

	if err := st.SetChoiceExcludes(ac, []int64{bc}); err == nil {
		t.Fatal("per-sign-up x per-person accepted")
	}
	if err := st.SetChoiceExcludes(bc, []int64{ac}); err == nil {
		t.Fatal("per-person x per-sign-up accepted")
	}
	if err := st.SetChoiceExcludes(ac, []int64{ac}); err == nil {
		t.Fatal("self accepted")
	}
	other := newEvent(t, st, "Other")
	openForm(t, st, other, "self")
	oq := mustQ(t, st, other, SignupQuestionInput{Kind: "many", Label: "x"})
	oc := mustC(t, st, oq, "y", 0)
	if err := st.SetChoiceExcludes(ac, []int64{oc}); err == nil {
		t.Fatal("other form accepted")
	}
	// Same scope works, and reads both ways.
	ac2 := mustC(t, st, a, "Tables", 0)
	if err := st.SetChoiceExcludes(ac2, []int64{ac}); err != nil {
		t.Fatal(err)
	}
	qs, _ := st.SignupQuestions(ev)
	if len(qs[0].Choices[0].Excludes) != 1 || len(qs[0].Choices[1].Excludes) != 1 {
		t.Fatalf("excludes %v", qs[0].Choices)
	}

	// per person + public list refused; per person needs a list.
	if _, err := st.CreateSignupQuestion(ev, SignupQuestionInput{Kind: "text", Label: "Z", PerPerson: true, PublicList: true}); err == nil {
		t.Fatal("per-person + public list accepted")
	}
	solo := newEvent(t, st, "Solo")
	openForm(t, st, solo, "self")
	if _, err := st.CreateSignupQuestion(solo, SignupQuestionInput{Kind: "text", Label: "Z", PerPerson: true}); err == nil {
		t.Fatal("per-person on a 'self' form accepted")
	}
	f, _ := st.SignupFormByEvent(ev)
	f.People = "self"
	if err := st.UpdateSignupForm(f); err == nil {
		t.Fatal("switched to self with per-person questions")
	}
	if err := st.UpdateSignupForm(&SignupForm{EventID: ev, Status: "bogus", Audience: "anyone", Contact: "phone", People: "list"}); err == nil {
		t.Fatal("bad status accepted")
	}
}

func TestSignupRetireVersusDelete(t *testing.T) {
	st := openTest(t)
	c := newCookoff(t, st, 0)
	// Unanswered: deletable.
	extra := mustQ(t, st, c.ev, SignupQuestionInput{Kind: "text", Label: "Spare"})
	if err := st.DeleteSignupQuestion(extra); err != nil {
		t.Fatal(err)
	}
	saveOK(t, st, SignupInput{EventID: c.ev, Name: "Tyler Goza", Answers: []AnswerInput{
		{QuestionID: c.q1, ChoiceIDs: []int64{c.makeChili}}, {QuestionID: c.q2, Value: "Smoky"}}}, SignupOptions{})
	if err := st.DeleteSignupChoice(c.makeChili); !errors.Is(err, ErrHasAnswers) {
		t.Fatalf("delete answered choice: %v", err)
	}
	if err := st.DeleteSignupQuestion(c.q2); !errors.Is(err, ErrHasAnswers) {
		t.Fatalf("delete answered question: %v", err)
	}
	if err := st.RetireSignupChoice(c.makeChili, true); err != nil {
		t.Fatal(err)
	}
	if err := st.RetireSignupQuestion(c.q2, true); err != nil {
		t.Fatal(err)
	}
	// A retired choice can't be newly picked.
	se := saveErr(t, st, c.input("Jane Doe", c.makeChili), SignupOptions{})
	if !strings.Contains(se.Error(), "isn't available") {
		t.Fatalf("got %v", se)
	}
	if err := st.DeleteSignupForm(c.ev); err == nil {
		t.Fatal("form with sign-ups deleted")
	}
	empty := newEvent(t, st, "Empty")
	if _, err := st.CreateSignupForm(empty); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSignupForm(empty); err != nil {
		t.Fatal(err)
	}
}

func TestSignupCopyForm(t *testing.T) {
	st := openTest(t)
	c := newCookoff(t, st, 12)
	saveOK(t, st, c.input("Tyler Goza", c.makeCorn), SignupOptions{})
	retired := mustQ(t, st, c.ev, SignupQuestionInput{Kind: "text", Label: "Old"})
	st.RetireSignupQuestion(retired, true)
	src, _ := st.SignupFormByEvent(c.ev)
	dst := newEvent(t, st, "Next year")

	tx, _ := st.DB.Begin()
	if err := copySignupFormTx(tx, c.ev, dst); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f, err := st.SignupFormByEvent(dst)
	if err != nil {
		t.Fatal(err)
	}
	if f.Code == src.Code || f.Code == "" || f.Status != "draft" {
		t.Fatalf("code %q vs %q, status %s", f.Code, src.Code, f.Status)
	}
	if su, _ := st.ListSignups(dst, true); len(su) != 0 {
		t.Fatal("sign-ups copied")
	}
	qs, _ := st.SignupQuestions(dst)
	if len(qs) != 3 { // retired one left out
		t.Fatalf("%d questions", len(qs))
	}
	var chili, judge SignupChoice
	for _, c := range qs[0].Choices {
		switch c.Label {
		case "Make chili":
			chili = c
		case "Judge chili":
			judge = c
		}
	}
	if chili.Spots != 12 || chili.ID == c.makeChili || len(chili.Excludes) != 1 || chili.Excludes[0] != judge.ID {
		t.Fatalf("chili %+v judge %+v", chili, judge)
	}
	if len(qs[1].When) != 1 || qs[1].When[0] != chili.ID {
		t.Fatalf("when %v", qs[1].When)
	}
	for _, x := range qs {
		for _, c := range x.Choices {
			if c.Taken != 0 {
				t.Fatalf("taken copied: %+v", c)
			}
		}
	}
	// Copying onto an event that has a form is refused.
	tx, _ = st.DB.Begin()
	defer tx.Rollback()
	if err := copySignupFormTx(tx, c.ev, dst); err == nil {
		t.Fatal("second form accepted")
	}
}

func TestSignupLastSpotConcurrent(t *testing.T) {
	st := openTest(t)
	c := newCookoff(t, st, 1)
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in := SignupInput{EventID: c.ev, Name: "Person " + string(rune('A'+i)), Answers: []AnswerInput{
				{QuestionID: c.q1, ChoiceIDs: []int64{c.makeChili}}, {QuestionID: c.q2, Value: "Chili"}}}
			_, results[i] = st.SaveSignup(in, SignupOptions{Now: signupNow})
		}(i)
	}
	wg.Wait()
	wins := 0
	for _, err := range results {
		if err == nil {
			wins++
		} else if !strings.Contains(err.Error(), "Make chili just filled up") {
			t.Fatalf("unexpected: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d winners: %v", wins, results)
	}
}

func TestSignupPerPersonSpots(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Craft night")
	openForm(t, st, ev, "list")
	q := mustQ(t, st, ev, SignupQuestionInput{Kind: "one", Label: "Which table?", PerPerson: true, Required: true})
	clay := mustC(t, st, q, "Clay", 2)
	paint := mustC(t, st, q, "Painting", 0)
	in := SignupInput{EventID: ev, Name: "Jane Doe",
		People: []PersonInput{{Name: "Sam"}, {Name: "Lily"}, {Name: "Max"}}}
	for i := 0; i < 3; i++ {
		in.Answers = append(in.Answers, AnswerInput{QuestionID: q, Person: i, ChoiceIDs: []int64{clay}})
	}
	se := saveErr(t, st, in, SignupOptions{})
	if !strings.Contains(se.Error(), "Clay has only 2 spots left") && !strings.Contains(se.Error(), "Clay") {
		t.Fatalf("got %v", se)
	}
	// Two fit; the third goes to painting.
	in.Answers[2].ChoiceIDs = []int64{paint}
	r := saveOK(t, st, in, SignupOptions{})
	if r.Code == "" {
		t.Fatal("no code")
	}
	qs, _ := st.SignupQuestions(ev)
	if qs[0].Choices[0].Taken != 2 || !qs[0].Choices[0].Full() {
		t.Fatalf("taken %d", qs[0].Choices[0].Taken)
	}
	// Required per person: a missing answer for one is refused.
	in2 := SignupInput{EventID: ev, Name: "Bob", People: []PersonInput{{Name: "A"}, {Name: "B"}},
		Answers: []AnswerInput{{QuestionID: q, Person: 0, ChoiceIDs: []int64{paint}}}}
	se = saveErr(t, st, in2, SignupOptions{})
	if !strings.Contains(se.Error(), "for B") {
		t.Fatalf("got %v", se)
	}
	// The form is full only when every choice is: paint has no limit.
	f, _ := st.SignupFormByEvent(ev)
	e, _ := st.GetEvent(ev)
	if state, _ := st.SignupState(f, e, signupNow); state != "open" {
		t.Fatalf("state %s", state)
	}
}

func TestSignupExclusionsAndPlannerWarnings(t *testing.T) {
	st := openTest(t)
	c := newCookoff(t, st, 1)
	in := c.input("Tyler Goza", c.makeChili, c.judgeChili)
	in.Answers = append(in.Answers, AnswerInput{QuestionID: c.q2, Value: "Smoky"})
	se := saveErr(t, st, in, SignupOptions{})
	if !strings.Contains(se.Error(), "can't pick both Make chili and Judge chili") {
		t.Fatalf("got %v", se)
	}
	r := saveOK(t, st, in, SignupOptions{Planner: true})
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "Judge chili") {
		t.Fatalf("warnings %v", r.Warnings)
	}
	// Over spots: planner warns, others refused.
	in2 := c.input("Jane Doe", c.makeChili)
	in2.Answers = append(in2.Answers, AnswerInput{QuestionID: c.q2, Value: "Hot"})
	se = saveErr(t, st, in2, SignupOptions{})
	if !strings.Contains(se.Error(), "Make chili just filled up") {
		t.Fatalf("got %v", se)
	}
	r = saveOK(t, st, in2, SignupOptions{Planner: true})
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "Make chili: over by 1") {
		t.Fatalf("warnings %v", r.Warnings)
	}
}

func TestSignupClosesAtEventStart(t *testing.T) {
	st := openTest(t)
	ev, _ := st.CreateEvent(&Event{Title: "Dinner", StartsOn: "2026-11-01", StartTime: "18:00"})
	openForm(t, st, ev, "self")
	f, _ := st.SignupFormByEvent(ev)
	e, _ := st.GetEvent(ev)
	check := func(now time.Time, want string) {
		t.Helper()
		got, err := st.SignupState(f, e, now)
		if err != nil || got != want {
			t.Fatalf("at %v: %s %v, want %s", now, got, err, want)
		}
	}
	check(time.Date(2026, 11, 1, 17, 59, 0, 0, time.Local), "open")
	check(time.Date(2026, 11, 1, 18, 0, 0, 0, time.Local), "closed")
	in := SignupInput{EventID: ev, Name: "Jane"}
	if _, err := st.SaveSignup(in, SignupOptions{Now: time.Date(2026, 11, 1, 18, 1, 0, 0, time.Local)}); err == nil {
		t.Fatal("saved after the start")
	}
	if _, err := st.SaveSignup(in, SignupOptions{Now: signupNow}); err != nil {
		t.Fatal(err)
	}
	// closes_at earlier than the start.
	f.ClosesAt = "2026-10-20T12:00"
	if err := st.UpdateSignupForm(f); err != nil {
		t.Fatal(err)
	}
	check(time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local), "closed")
	check(time.Date(2026, 10, 20, 11, 0, 0, 0, time.Local), "open")
	// Draft, closed, cancelled and unscheduled.
	f.Status = "draft"
	st.UpdateSignupForm(f)
	check(signupNow, "draft")
	f.Status = "closed"
	st.UpdateSignupForm(f)
	check(signupNow, "closed")
	f.Status, f.ClosesAt = "open", ""
	st.UpdateSignupForm(f)
	e.Status = "cancelled"
	check(signupNow, "closed")
	e.Status, e.StartsOn = "planning", ""
	check(signupNow, "closed")
}

func TestSignupWithdrawFreesSpots(t *testing.T) {
	st := openTest(t)
	c := newCookoff(t, st, 1)
	in := c.input("Tyler Goza", c.makeChili)
	in.Answers = append(in.Answers, AnswerInput{QuestionID: c.q2, Value: "Smoky"})
	r := saveOK(t, st, in, SignupOptions{})
	in2 := c.input("Jane Doe", c.makeChili)
	in2.Answers = append(in2.Answers, AnswerInput{QuestionID: c.q2, Value: "Hot"})
	saveErr(t, st, in2, SignupOptions{})
	if err := st.WithdrawSignup(r.ID); err != nil {
		t.Fatal(err)
	}
	saveOK(t, st, in2, SignupOptions{})
	people, guests, _ := st.SignupHeadcount(c.ev)
	if people != 1 || guests != 0 {
		t.Fatalf("headcount %d+%d", people, guests)
	}
	// The withdrawn one can come back only if there's room.
	in.SignupID = r.ID
	saveErr(t, st, in, SignupOptions{})
}

func TestSignupFollowUps(t *testing.T) {
	st := openTest(t)
	c := newCookoff(t, st, 0)
	// Chili name given but chili not picked: dropped, and not required.
	in := c.input("Tyler Goza", c.makeCorn)
	in.Answers = append(in.Answers, AnswerInput{QuestionID: c.q2, Value: "Ghost"}, AnswerInput{QuestionID: c.q3, ChoiceIDs: []int64{c.hot}})
	r := saveOK(t, st, in, SignupOptions{})
	su, _ := st.GetSignup(r.ID)
	if len(su.Answers) != 1 || su.Answers[0].ChoiceID != c.makeCorn {
		t.Fatalf("answers %+v", su.Answers)
	}
	// Picked: the required name is asked.
	in = c.input("Jane Doe", c.makeChili)
	se := saveErr(t, st, in, SignupOptions{})
	if !strings.Contains(se.Error(), "Name your chili") {
		t.Fatalf("got %v", se)
	}
	// Editing from chili to corn drops the follow-ups.
	in.Answers = append(in.Answers, AnswerInput{QuestionID: c.q2, Value: "Mine"}, AnswerInput{QuestionID: c.q3, ChoiceIDs: []int64{c.mild}})
	r2 := saveOK(t, st, in, SignupOptions{})
	edit := c.input("Jane Doe", c.makeCorn)
	edit.SignupID = r2.ID
	edit.Answers = append(edit.Answers, AnswerInput{QuestionID: c.q2, Value: "Mine"})
	saveOK(t, st, edit, SignupOptions{})
	su, _ = st.GetSignup(r2.ID)
	if len(su.Answers) != 1 {
		t.Fatalf("answers %+v", su.Answers)
	}
	// Bad kinds.
	nq := mustQ(t, st, c.ev, SignupQuestionInput{Kind: "number", Label: "How many?"})
	yq := mustQ(t, st, c.ev, SignupQuestionInput{Kind: "yes_no", Label: "Vegan?"})
	bad := c.input("X Y", c.makeCorn)
	bad.Answers = append(bad.Answers, AnswerInput{QuestionID: nq, Value: "lots"}, AnswerInput{QuestionID: yq, Value: "maybe"})
	se = saveErr(t, st, bad, SignupOptions{})
	if len(se.Messages) != 2 {
		t.Fatalf("messages %v", se.Messages)
	}
	// Pick one with two answers.
	two := c.input("X Y", c.makeChili)
	two.Answers = append(two.Answers, AnswerInput{QuestionID: c.q2, Value: "a"}, AnswerInput{QuestionID: c.q3, ChoiceIDs: []int64{c.mild, c.hot}})
	saveErr(t, st, two, SignupOptions{})
}

func TestSignupOutboxOnStateChange(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Dinner")
	openForm(t, st, ev, "self")
	q := mustQ(t, st, ev, SignupQuestionInput{Kind: "one", Label: "Dish", Required: true})
	only := mustC(t, st, q, "Salad", 2)
	pick := func(name string) SignupInput {
		return SignupInput{EventID: ev, Name: name, Answers: []AnswerInput{{QuestionID: q, ChoiceIDs: []int64{only}}}}
	}
	clearOutbox(t, st)
	r1 := saveOK(t, st, pick("Ann Lee"), SignupOptions{})
	if got := outbox(t, st); len(got) != 0 {
		t.Fatalf("plain answer queued: %v", got)
	}
	saveOK(t, st, pick("Bob Ray"), SignupOptions{}) // now full
	if got := outbox(t, st); got[ev] != "put" {
		t.Fatalf("full not queued: %v", got)
	}
	clearOutbox(t, st)
	if err := st.WithdrawSignup(r1.ID); err != nil { // open again
		t.Fatal(err)
	}
	if got := outbox(t, st); got[ev] != "put" {
		t.Fatalf("reopen not queued: %v", got)
	}
	// Form settings write it through the triggers.
	clearOutbox(t, st)
	f, _ := st.SignupFormByEvent(ev)
	f.Intro = "hello"
	st.UpdateSignupForm(f)
	if got := outbox(t, st); len(got) != 0 {
		t.Fatalf("intro queued: %v", got)
	}
	f.Label = "Join us"
	st.UpdateSignupForm(f)
	if got := outbox(t, st); got[ev] != "put" {
		t.Fatalf("label not queued: %v", got)
	}

	// The sweep queues a form that closed by itself, once.
	clearOutbox(t, st)
	f.ClosesAt = "2026-10-15T12:00"
	st.UpdateSignupForm(f)
	clearOutbox(t, st)
	if ids, _ := st.FormsPastClosing(signupNow); len(ids) != 0 {
		t.Fatalf("early: %v", ids)
	}
	later := time.Date(2026, 10, 16, 0, 0, 0, 0, time.Local)
	if ids, _ := st.FormsPastClosing(later); len(ids) != 1 {
		t.Fatalf("past: %v", ids)
	}
	if n, err := st.SweepSignupClosings(later); err != nil || n != 1 {
		t.Fatalf("sweep %d %v", n, err)
	}
	if got := outbox(t, st); got[ev] != "put" {
		t.Fatalf("sweep queued nothing: %v", got)
	}
	if n, _ := st.SweepSignupClosings(later); n != 0 {
		t.Fatalf("swept twice: %d", n)
	}
}

func TestSignupCSV(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Craft night")
	openForm(t, st, ev, "list")
	bring := mustQ(t, st, ev, SignupQuestionInput{Kind: "many", Label: "Bring"})
	chairs := mustC(t, st, bring, "Chairs", 0)
	tables := mustC(t, st, bring, "Tables", 0)
	old := mustQ(t, st, ev, SignupQuestionInput{Kind: "text", Label: "Old question"})
	table := mustQ(t, st, ev, SignupQuestionInput{Kind: "one", Label: "Table", PerPerson: true})
	clay := mustC(t, st, table, "Clay", 0)
	paint := mustC(t, st, table, "Painting", 0)
	saveOK(t, st, SignupInput{EventID: ev, Name: "Jane Doe", Phone: "555", Guests: 1,
		People: []PersonInput{{Name: "Sam", Allergies: "Peanuts"}, {Name: "Lily"}},
		Answers: []AnswerInput{
			{QuestionID: bring, ChoiceIDs: []int64{chairs, tables}},
			{QuestionID: old, Value: "legacy"},
			{QuestionID: table, Person: 0, ChoiceIDs: []int64{clay}},
			{QuestionID: table, Person: 1, ChoiceIDs: []int64{paint}},
		}}, SignupOptions{})
	st.RetireSignupQuestion(old, true)

	rows, err := st.SignupCSV(ev, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows %v", rows)
	}
	want := []string{"Signed up by", "Phone", "Email", "Status", "Person", "Allergies", "Guests", "Signed up on", "Bring", "Table", "Old question (retired)"}
	if strings.Join(rows[0], "|") != strings.Join(want, "|") {
		t.Fatalf("header %v", rows[0])
	}
	r1, r2 := rows[1], rows[2]
	if r1[4] != "Sam" || r1[5] != "Peanuts" || r1[6] != "1" || r1[8] != "Chairs; Tables" || r1[9] != "Clay" || r1[10] != "legacy" {
		t.Fatalf("row 1 %v", r1)
	}
	if r2[4] != "Lily" || r2[6] != "" || r2[8] != "Chairs; Tables" || r2[9] != "Painting" || r2[10] != "legacy" {
		t.Fatalf("row 2 %v", r2)
	}
	al, _ := st.SignupAllergies(ev)
	if len(al) != 1 || al[0].Name != "Sam" || al[0].SignerPhone != "555" {
		t.Fatalf("allergies %+v", al)
	}
	people, guests, _ := st.SignupHeadcount(ev)
	if people != 2 || guests != 1 {
		t.Fatalf("headcount %d %d", people, guests)
	}
}

func TestSignupWhosBringingWhat(t *testing.T) {
	st := openTest(t)
	c := newCookoff(t, st, 0)
	// Make the pick question public too, to check lists merge.
	st.UpdateSignupQuestion(c.q1, SignupQuestionInput{Kind: "many", Label: "What would you like to do?", Required: true, PublicList: true})
	chili := func(name, dish string) *SignupResult {
		in := c.input(name, c.makeChili)
		in.Answers = append(in.Answers, AnswerInput{QuestionID: c.q2, Value: dish})
		return saveOK(t, st, in, SignupOptions{})
	}
	chili("Tyler Goza", "Smoky three-bean")
	gone := chili("Gone Away", "Secret")
	chili("Jane", "Hot Mess")
	saveOK(t, st, c.input("Ann de la Cruz", c.makeCorn), SignupOptions{})
	st.WithdrawSignup(gone.ID)

	lists, err := st.WhosBringingWhat(c.ev)
	if err != nil {
		t.Fatal(err)
	}
	if len(lists) != 2 || lists[0].Title != "Make chili" || lists[1].Title != "Make cornbread" {
		t.Fatalf("lists %+v", lists)
	}
	if len(lists[0].Entries) != 2 || lists[0].Entries[0].Text() != "Tyler G. · Smoky three-bean" || lists[0].Entries[1].Text() != "Jane · Hot Mess" {
		t.Fatalf("entries %+v", lists[0].Entries)
	}
	if lists[1].Entries[0].Who != "Ann C." {
		t.Fatalf("entries %+v", lists[1].Entries)
	}
	if FirstNameInitial("  ") != "" || FirstNameInitial("tyler goza") != "tyler G." {
		t.Fatal("initial helper")
	}
}

func TestSignupCodeIsHashed(t *testing.T) {
	st := openTest(t)
	c := newCookoff(t, st, 0)
	r := saveOK(t, st, c.input("Tyler Goza", c.makeCorn), SignupOptions{})
	if r.Code == "" {
		t.Fatal("no code")
	}
	var hash string
	st.DB.QueryRow(`SELECT code_hash FROM signups WHERE id = ?`, r.ID).Scan(&hash)
	if hash == r.Code || hash != HashSignupCode(r.Code) || len(hash) != 64 {
		t.Fatalf("hash %q code %q", hash, r.Code)
	}
	got, err := st.GetSignupByCode(r.Code)
	if err != nil || got.ID != r.ID {
		t.Fatalf("lookup %v %v", got, err)
	}
	if _, err := st.GetSignupByCode(hash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("hash worked as a code: %v", err)
	}
	// Editing keeps the code, and doesn't return a new one.
	edit := c.input("Tyler Goza", c.makeCorn, c.jc)
	edit.SignupID = r.ID
	if r2 := saveOK(t, st, edit, SignupOptions{}); r2.Code != "" || r2.ID != r.ID {
		t.Fatalf("edit result %+v", r2)
	}
	// Per-user list, upcoming first.
	uid, err := st.CreateUser("tyler", "Tyler", "pw-pw-pw-pw", "helper")
	if err != nil {
		t.Fatal(err)
	}
	in := c.input("Tyler Goza", c.makeCorn)
	in.UserID = uid
	saveOK(t, st, in, SignupOptions{})
	mine, err := st.SignupsByUser(uid, "2026-10-14")
	if err != nil || len(mine) != 1 || mine[0].EventTitle != "Cook-Off" || mine[0].FormCode == "" {
		t.Fatalf("mine %+v %v", mine, err)
	}
}

func TestSignupParseChoiceLines(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Dinner")
	openForm(t, st, ev, "self")
	q := mustQ(t, st, ev, SignupQuestionInput{Kind: "one", Label: "Dish"})
	n, err := st.AddSignupChoices(q, "Make chili | 12\n\n Judge chili\nBake | 4 ")
	if err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	qs, _ := st.SignupQuestions(ev)
	ch := qs[0].Choices
	if ch[0].Label != "Make chili" || ch[0].Spots != 12 || ch[1].Spots != 0 || ch[2].Spots != 4 {
		t.Fatalf("%+v", ch)
	}
	if _, err := st.AddSignupChoices(q, "Fine\nBad | x"); err == nil {
		t.Fatal("bad line accepted")
	}
	qs, _ = st.SignupQuestions(ev)
	if len(qs[0].Choices) != 3 {
		t.Fatal("partial paste added")
	}
	if err := st.MoveSignupChoice(ch[2].ID, -1); err != nil {
		t.Fatal(err)
	}
	qs, _ = st.SignupQuestions(ev)
	if qs[0].Choices[1].Label != "Bake" {
		t.Fatalf("%+v", qs[0].Choices)
	}
}

func TestSignupDuplicatePersonIDs(t *testing.T) {
	st := openTest(t)
	ev := newEvent(t, st, "Kids day")
	openForm(t, st, ev, "list")
	first := saveOK(t, st, SignupInput{EventID: ev, Name: "Pat", Phone: "1",
		People: []PersonInput{{Name: "A"}, {Name: "B"}}}, SignupOptions{})
	got, err := st.GetSignup(first.ID)
	if err != nil || len(got.People) != 2 {
		t.Fatalf("people %+v %v", got, err)
	}
	id := got.People[0].ID
	in := SignupInput{EventID: ev, SignupID: first.ID, Name: "Pat", Phone: "1",
		People: []PersonInput{{ID: id, Name: "A2"}, {ID: id, Name: "B2"}, {Name: "C"}}}
	saveOK(t, st, in, SignupOptions{})
	got, _ = st.GetSignup(first.ID)
	if len(got.People) != 3 {
		t.Fatalf("want 3 people, got %+v", got.People)
	}
}
