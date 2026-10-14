package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/tylergoza/event-planner/internal/store"
)

func suSetup(t *testing.T) (ann *client, st *store.Store, evID int64, edit string) {
	t.Helper()
	c, st := newTestServer(t)
	st.CreateUser("ann", "Ann Lee", "a long password", store.RolePlanner)
	st.CreateUser("hal", "Hal", "a long password", store.RoleHelper)
	ann = c.as("ann")
	evID, err := st.CreateEvent(&store.Event{Title: "Chili Cook-off", StartsOn: "2099-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	return ann, st, evID, "/events/" + itoa(evID) + "/edit"
}

func TestSignupBuilderFormAndSettings(t *testing.T) {
	ann, st, evID, edit := suSetup(t)
	id := itoa(evID)
	if body := ann.get(edit, 200); !strings.Contains(body, "Add a sign-up form") {
		t.Fatal("edit page should offer to add a form")
	}
	ann.get("/events/new", 200)

	ann.post(edit, "/events/"+id+"/signup-form", url.Values{}, 200)
	f, err := st.SignupFormByEvent(evID)
	if err != nil || f.Status != "draft" {
		t.Fatalf("form %+v %v", f, err)
	}
	body := ann.get(edit, 200)
	for _, want := range []string{"Draft: not open yet", "/signup/" + f.Code, "Remove sign-up form", "Add a question"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}

	ann.post(edit, "/events/"+id+"/signup-form/settings", url.Values{
		"label": {"Join us"}, "intro": {"Hello"}, "audience": {"signed_in"}, "contact": {"email"}, "people": {"list"},
		"ask_guests": {"1"}, "ask_allergies": {"1"}, "show_spots": {"1"}, "closes_at": {"2098-12-30T18:00"},
		"thank_you": {"Thanks!"}, "status": {"open"},
	}, 200)
	f, _ = st.SignupFormByEvent(evID)
	if f.Label != "Join us" || f.Audience != "signed_in" || f.Contact != "email" || f.People != "list" || !f.AskGuests ||
		!f.AskAllergies || !f.ShowSpots || f.ClosesAt != "2098-12-30T18:00" || f.Status != "open" || f.ThankYou != "Thanks!" {
		t.Fatalf("settings %+v", f)
	}
	if body := ann.get(edit, 200); !strings.Contains(body, "<strong>Open</strong>") {
		t.Error("status line should say Open")
	}
	// A bad value is refused with a message and nothing changes.
	body = ann.post(edit, "/events/"+id+"/signup-form/settings", url.Values{"status": {"nope"}, "audience": {"anyone"}, "contact": {"phone"}, "people": {"self"}}, 200)
	if !strings.Contains(body, "Pick draft, open or closed") {
		t.Error("expected a friendly message")
	}
	if f, _ = st.SignupFormByEvent(evID); f.Status != "open" {
		t.Error("bad settings were saved")
	}

	// Remove is refused once someone has signed up.
	q, _ := st.CreateSignupQuestion(evID, store.SignupQuestionInput{Kind: "text", Label: "Name of dish"})
	if _, err := st.SaveSignup(store.SignupInput{EventID: evID, Name: "Pat", Phone: "555", People: []store.PersonInput{{Name: "Pat"}}, Answers: []store.AnswerInput{{QuestionID: q, Value: "x"}}}, store.SignupOptions{Planner: true}); err != nil {
		t.Fatal(err)
	}
	if body := ann.post(edit, "/events/"+id+"/signup-form/delete", url.Values{}, 200); !strings.Contains(body, "has sign-ups") {
		t.Error("expected a refusal")
	}
	if _, err := st.SignupFormByEvent(evID); err != nil {
		t.Error("form should still exist")
	}
}

func TestSignupBuilderRemoveEmptyForm(t *testing.T) {
	ann, st, evID, edit := suSetup(t)
	st.CreateSignupForm(evID)
	ann.post(edit, "/events/"+itoa(evID)+"/signup-form/delete", url.Values{}, 200)
	if _, err := st.SignupFormByEvent(evID); err == nil {
		t.Error("form should be gone")
	}
}

func TestSignupBuilderQuestionsAndChoices(t *testing.T) {
	ann, st, evID, edit := suSetup(t)
	id := itoa(evID)
	st.CreateSignupForm(evID)
	post := func(path string, v url.Values) string { return ann.post(edit, path, v, 200) }

	post("/events/"+id+"/signup-questions", url.Values{"kind": {"one"}, "label": {"What will you bring?"}})
	post("/events/"+id+"/signup-questions", url.Values{"kind": {"text"}, "label": {"Which dish?"}})
	qs, _ := st.SignupQuestions(evID)
	if len(qs) != 2 {
		t.Fatalf("%d questions", len(qs))
	}
	q1, q2 := qs[0], qs[1]
	if body := post("/events/"+id+"/signup-questions", url.Values{"kind": {"text"}, "label": {""}}); !strings.Contains(body, "needs some words") {
		t.Error("blank question should be refused with a message")
	}

	// Paste choices, one with spots.
	body := post("/signup-questions/"+itoa(q1.ID)+"/choices/paste", url.Values{"text": {"Make chili | 12\nBring bread\n"}})
	if !strings.Contains(body, "2 choices added") {
		t.Error("expected the paste message")
	}
	post("/signup-questions/"+itoa(q1.ID)+"/choices/paste", url.Values{"text": {"Bad | x"}}) // refused
	post("/signup-questions/"+itoa(q1.ID)+"/choices", url.Values{"label": {"Dessert"}, "spots": {"3"}})
	q1, _ = func() (store.SignupQuestion, error) { q, err := st.SignupQuestion(q1.ID); return *q, err }()
	if len(q1.Choices) != 3 || q1.Choices[0].Spots != 12 || q1.Choices[1].Spots != 0 || q1.Choices[2].Spots != 3 {
		t.Fatalf("choices %+v", q1.Choices)
	}
	chili, bread, dessert := q1.Choices[0], q1.Choices[1], q1.Choices[2]

	// "Only ask when" a choice from the earlier question: fine.
	post("/signup-questions/"+itoa(q2.ID), url.Values{"kind": {"text"}, "label": {"Which dish?"}, "when": {itoa(chili.ID)}})
	if q, _ := st.SignupQuestion(q2.ID); len(q.When) != 1 || q.When[0] != chili.ID {
		t.Fatalf("when %+v", q.When)
	}
	if body := ann.get(edit, 200); !strings.Contains(body, "What will you bring?: Make chili") {
		t.Error("the page should offer earlier choices")
	}
	// Pointing the earlier question at a later question's choice is refused.
	lateChoice, _ := st.CreateSignupQuestion(evID, store.SignupQuestionInput{Kind: "one", Label: "Late"})
	lc, _ := st.CreateSignupChoice(lateChoice, "Later choice", 0)
	body = post("/signup-questions/"+itoa(q1.ID), url.Values{"kind": {"one"}, "label": {"What will you bring?"}, "when": {itoa(lc)}})
	if !strings.Contains(body, "earlier question") {
		t.Errorf("expected a refusal, got no message")
	}
	if q, _ := st.SignupQuestion(q1.ID); len(q.When) != 0 {
		t.Error("the bad condition was saved")
	}

	// Per-person + show on the form is refused; per-person needs a list.
	body = post("/events/"+id+"/signup-questions", url.Values{"kind": {"text"}, "label": {"Class"}, "per_person": {"1"}, "public_list": {"1"}})
	if !strings.Contains(body, "be shown on the form") {
		t.Error("per-person + show on the form should be refused")
	}
	body = post("/events/"+id+"/signup-questions", url.Values{"kind": {"text"}, "label": {"Class"}, "per_person": {"1"}})
	if !strings.Contains(body, "needs") {
		t.Error("per-person with a self form should be refused")
	}

	// Excludes are saved from the choice form, both ways.
	post("/signup-choices/"+itoa(chili.ID), url.Values{"label": {"Make chili"}, "spots": {"12"}, "excludes": {itoa(bread.ID), itoa(dessert.ID)}})
	c, _ := st.SignupQuestion(q1.ID)
	if len(c.Choices[0].Excludes) != 2 || len(c.Choices[1].Excludes) != 1 || c.Choices[1].Excludes[0] != chili.ID {
		t.Fatalf("excludes %+v", c.Choices)
	}
	if body := ann.get(edit, 200); !strings.Contains(body, "Can't be picked with") {
		t.Error("page should show the exclusion list")
	}
	// Bad spots refused, nothing changes.
	body = post("/signup-choices/"+itoa(bread.ID), url.Values{"label": {"Bring bread"}, "spots": {"zero"}})
	if !strings.Contains(body, "whole number") {
		t.Error("expected a spots message")
	}

	// Move question and choice.
	post("/signup-choices/"+itoa(bread.ID)+"/move", url.Values{"dir": {"up"}})
	c, _ = st.SignupQuestion(q1.ID)
	if c.Choices[0].ID != bread.ID {
		t.Errorf("choice did not move up: %+v", c.Choices)
	}
	post("/signup-questions/"+itoa(q2.ID)+"/move", url.Values{"dir": {"down"}})
	post("/signup-questions/"+itoa(q2.ID)+"/move", url.Values{"dir": {"up"}}) // back above its condition: refused, flash only

	// Delete a choice nobody picked.
	post("/signup-choices/"+itoa(dessert.ID)+"/delete", url.Values{})
	if c, _ = st.SignupQuestion(q1.ID); len(c.Choices) != 2 {
		t.Errorf("%d choices after delete", len(c.Choices))
	}
	// Retire and bring back a choice.
	post("/signup-choices/"+itoa(bread.ID)+"/retire", url.Values{})
	if c, _ = st.SignupQuestion(q1.ID); !c.Choices[0].Retired {
		t.Error("choice should be retired")
	}
	post("/signup-choices/"+itoa(bread.ID)+"/retire", url.Values{"undo": {"1"}})
	if c, _ = st.SignupQuestion(q1.ID); c.Choices[0].Retired {
		t.Error("choice should be back")
	}
}

func TestSignupBuilderDeleteAnsweredThenRetire(t *testing.T) {
	ann, st, evID, edit := suSetup(t)
	st.CreateSignupForm(evID)
	q, _ := st.CreateSignupQuestion(evID, store.SignupQuestionInput{Kind: "one", Label: "Bring what?"})
	ch, _ := st.CreateSignupChoice(q, "Chili", 0)
	if _, err := st.SaveSignup(store.SignupInput{EventID: evID, Name: "Pat", Phone: "555", Answers: []store.AnswerInput{{QuestionID: q, ChoiceIDs: []int64{ch}}}}, store.SignupOptions{Planner: true}); err != nil {
		t.Fatal(err)
	}
	qid := itoa(q)
	if body := ann.post(edit, "/signup-questions/"+qid+"/delete", url.Values{}, 200); !strings.Contains(body, "People have answered this") {
		t.Error("expected the answered message")
	}
	if body := ann.post(edit, "/signup-choices/"+itoa(ch)+"/delete", url.Values{}, 200); !strings.Contains(body, "People have answered this") {
		t.Error("expected the answered message for the choice")
	}
	if _, err := st.SignupQuestion(q); err != nil {
		t.Fatal("question should still exist")
	}
	ann.post(edit, "/signup-questions/"+qid+"/retire", url.Values{}, 200)
	if x, _ := st.SignupQuestion(q); !x.Retired {
		t.Fatal("question should be retired")
	}
	if body := ann.get(edit, 200); !strings.Contains(body, "Bring back") {
		t.Error("retired question should offer Bring back")
	}
	ann.post(edit, "/signup-questions/"+qid+"/retire", url.Values{"undo": {"1"}}, 200)
	if x, _ := st.SignupQuestion(q); x.Retired {
		t.Fatal("question should be back")
	}
}

func TestSignupBuilderPlannersOnly(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("hal", "Hal", "a long password", store.RoleHelper)
	evID, _ := st.CreateEvent(&store.Event{Title: "Chili Cook-off", StartsOn: "2099-01-01"})
	st.CreateSignupForm(evID)
	q, _ := st.CreateSignupQuestion(evID, store.SignupQuestionInput{Kind: "one", Label: "Q"})
	ch, _ := st.CreateSignupChoice(q, "A", 0)
	hal := c.as("hal")
	hal.get("/events/"+itoa(evID)+"/edit", 403)
	for _, p := range []string{
		"/events/" + itoa(evID) + "/signup-form", "/events/" + itoa(evID) + "/signup-form/settings", "/events/" + itoa(evID) + "/signup-form/delete",
		"/events/" + itoa(evID) + "/signup-questions", "/signup-questions/" + itoa(q), "/signup-questions/" + itoa(q) + "/delete",
		"/signup-questions/" + itoa(q) + "/retire", "/signup-questions/" + itoa(q) + "/move", "/signup-questions/" + itoa(q) + "/choices",
		"/signup-questions/" + itoa(q) + "/choices/paste", "/signup-choices/" + itoa(ch), "/signup-choices/" + itoa(ch) + "/delete",
		"/signup-choices/" + itoa(ch) + "/retire", "/signup-choices/" + itoa(ch) + "/move",
	} {
		hal.post("/events/"+itoa(evID), p, url.Values{}, 403)
	}
	if _, err := st.SignupFormByEvent(evID); err != nil {
		t.Error("form should be untouched")
	}
}

func TestSignupFormCopiedAsDraft(t *testing.T) {
	_, st, evID, _ := suSetup(t)
	f, _ := st.CreateSignupForm(evID)
	q, _ := st.CreateSignupQuestion(evID, store.SignupQuestionInput{Kind: "one", Label: "Bring what?"})
	st.CreateSignupChoice(q, "Chili", 5)
	f.Status = "open"
	if err := st.UpdateSignupForm(f); err != nil {
		t.Fatal(err)
	}
	newID, err := st.CopyEvent(evID, store.CopyOptions{StartsOn: "2099-02-01"})
	if err != nil {
		t.Fatal(err)
	}
	cf, err := st.SignupFormByEvent(newID)
	if err != nil {
		t.Fatal(err)
	}
	if cf.Status != "draft" || cf.Code == f.Code || cf.Code == "" {
		t.Errorf("copied form %+v (source %+v)", cf, f)
	}
	if cq, _ := st.SignupQuestions(newID); len(cq) != 1 || len(cq[0].Choices) != 1 || cq[0].Choices[0].Spots != 5 {
		t.Errorf("copied questions %+v", cq)
	}
}
