package server

import (
	"encoding/csv"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tylergoza/event-planner/internal/store"
)

// srFixture is a public dinner with an open form: a required "bring" pick
// (Chili has 1 spot), a text question that is later retired, allergies and
// guests asked, and a list of people.
type srFixture struct {
	c             *client
	st            *store.Store
	ev            int64
	f             *store.SignupForm
	qBring, qSize int64
	chili, bread  int64
	pat, sam      int64
	ann, hal, mem *client // planner, helper off the team, helper on the team
	boss          *client
	base          string
}

func newSRFixture(t *testing.T) *srFixture {
	t.Helper()
	c, st := newTestServer(t)
	annID, _ := st.CreateUser("ann", "Ann Lee", "a long password", store.RolePlanner)
	st.CreateUser("hal", "Hal Fox", "a long password", store.RoleHelper)
	memID, _ := st.CreateUser("mem", "Mem Ber", "a long password", store.RoleHelper)
	st.CreateUser("boss", "Boss", "a long password", store.RoleAdmin)
	_ = annID
	ev, f := puForm(t, st, "Chili Cook-off", func(f *store.SignupForm) {
		f.People, f.AskGuests, f.AskAllergies, f.Contact = "list", true, true, "phone"
	})
	if err := st.SetMembers(ev, []int64{memID}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`UPDATE events SET expected_count = 30 WHERE id = ?`, ev); err != nil {
		t.Fatal(err)
	}
	x := &srFixture{c: c, st: st, ev: ev, f: f, base: "/events/" + itoa(ev)}
	x.qBring = puQ(t, st, ev, store.SignupQuestionInput{Kind: "one", Label: "What will you bring?", Required: true})
	x.chili = puC(t, st, x.qBring, "Chili", 1)
	x.bread = puC(t, st, x.qBring, "Bread", 0)
	x.qSize = puQ(t, st, ev, store.SignupQuestionInput{Kind: "text", Label: "Shirt size"})

	r, err := st.SaveSignup(store.SignupInput{EventID: ev, Name: "Pat Jones", Phone: "555-0100", Guests: 1,
		People:  []store.PersonInput{{Name: "Pat Jones"}, {Name: "Kim Jones", Allergies: "Peanuts"}},
		Answers: []store.AnswerInput{{QuestionID: x.qBring, ChoiceIDs: []int64{x.chili}}, {QuestionID: x.qSize, Value: "Large"}}}, store.SignupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	x.pat = r.ID
	r, err = st.SaveSignup(store.SignupInput{EventID: ev, Name: "Sam Smith", Email: "sam@example.com",
		People:  []store.PersonInput{{Name: "Sam Smith"}},
		Answers: []store.AnswerInput{{QuestionID: x.qBring, ChoiceIDs: []int64{x.bread}}}}, store.SignupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	x.sam = r.ID
	if err := st.SetSignupNote(x.pat, "PLANNERSECRET"); err != nil {
		t.Fatal(err)
	}
	x.ann, x.hal, x.mem, x.boss = c.as("ann"), c.as("hal"), c.as("mem"), c.as("boss")
	return x
}

func TestSignupListCountsAndHeadcount(t *testing.T) {
	x := newSRFixture(t)
	body := x.hal.get(x.base+"/signups", 200)
	for _, want := range []string{"Chili: 1 / 1", "Full", "Bread: 1", "4 people signed up", "(3 named + 1 guest)", "30 expected", "Pat Jones", "Sam Smith", "Large"} {
		if !strings.Contains(body, want) {
			t.Errorf("list missing %q", want)
		}
	}
	// A signed-in helper off the team: no contact details, notes or allergies.
	for _, bad := range []string{"555-0100", "sam@example.com", "PLANNERSECRET", "Peanuts", "/signups.csv", "/allergies"} {
		if strings.Contains(body, bad) {
			t.Errorf("non-team list shows %q", bad)
		}
	}
	// The team sees them.
	for _, who := range []*client{x.mem, x.ann, x.boss} {
		b := who.get(x.base+"/signups", 200)
		for _, want := range []string{"555-0100", "sam@example.com", "PLANNERSECRET", "Peanuts", "/signups.csv"} {
			if !strings.Contains(b, want) {
				t.Errorf("team list missing %q", want)
			}
		}
	}
	// Visitors can't read the list.
	if code := x.c.http; code == nil {
		t.Fatal("no client")
	}
	resp, _ := x.c.http.Get(x.c.base + x.base + "/signups")
	resp.Body.Close()
	if resp.Request.URL.Path == x.base+"/signups" && resp.StatusCode == 200 {
		t.Error("a visitor read the sign-up list")
	}
}

func TestSignupListByPersonFilterWithdrawn(t *testing.T) {
	x := newSRFixture(t)
	person := x.ann.get(x.base+"/signups?view=person", 200)
	for _, want := range []string{"Kim Jones", "Pat Jones", "Sam Smith", "By person"} {
		if !strings.Contains(person, want) {
			t.Errorf("by person missing %q", want)
		}
	}
	if strings.Index(person, "Kim Jones") > strings.Index(person, "Pat Jones") {
		t.Error("people should be in name order")
	}
	// Filter on one choice.
	f := x.ann.get(x.base+"/signups?choice="+itoa(x.bread), 200)
	if !strings.Contains(f, "Sam Smith") || strings.Contains(f, "sr-signup\" id=\"signup-"+itoa(x.pat)) || strings.Contains(f, "PLANNERSECRET") {
		t.Errorf("choice filter: %s", f)
	}
	fp := x.ann.get(x.base+"/signups?view=person&choice="+itoa(x.chili), 200)
	if !strings.Contains(fp, "Kim Jones") || strings.Contains(fp, "Sam Smith</strong>") {
		t.Errorf("person filter: %s", fp)
	}
	// Withdrawn hidden unless asked.
	x.ann.post(x.base+"/signups", "/signups/"+itoa(x.sam)+"/withdraw", url.Values{}, 200)
	if b := x.ann.get(x.base+"/signups", 200); strings.Contains(b, "Sam Smith") || !strings.Contains(b, "3 people signed up") {
		t.Errorf("withdrawn should be hidden and uncounted: %s", b)
	}
	if b := x.ann.get(x.base+"/signups?withdrawn=1", 200); !strings.Contains(b, "Sam Smith") || !strings.Contains(b, "Withdrawn") {
		t.Error("withdrawn=1 should show them")
	}
	// The bread spot count drops.
	if b := x.ann.get(x.base+"/signups", 200); strings.Contains(b, "Bread: 1") {
		t.Error("withdrawing should free the choice")
	}
}

func TestSignupCSVDownload(t *testing.T) {
	x := newSRFixture(t)
	if err := x.st.RetireSignupQuestion(x.qSize, true); err != nil {
		t.Fatal(err)
	}
	resp, err := x.ann.http.Get(x.c.base + x.base + "/signups.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="Chili-Cook-off-signups.csv"` {
		t.Errorf("Content-Disposition %q", cd)
	}
	rows, err := csv.NewReader(resp.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	h := rows[0]
	if h[0] != "Signed up by" || h[len(h)-2] != "What will you bring?" || h[len(h)-1] != "Shirt size (retired)" {
		t.Errorf("header %v", h)
	}
	if len(rows) != 4 { // header, Pat, Kim, Sam
		t.Fatalf("rows %v", rows)
	}
	joined := ""
	for _, r := range rows {
		joined += strings.Join(r, "|") + "\n"
	}
	for _, want := range []string{"Peanuts", "Large", "555-0100"} {
		if !strings.Contains(joined, want) {
			t.Errorf("csv missing %q", want)
		}
	}
	// Not for anyone off the team.
	x.hal.get(x.base+"/signups.csv", 403)
	x.mem.get(x.base+"/signups.csv", 200)
	if csvSafe("=cmd()") != "'=cmd()" || csvSafe("+1 555-1234") != "+1 555-1234" || csvSafe("-x") != "'-x" {
		t.Error("csvSafe")
	}
}

func TestSignupPlannerAddsOverSpots(t *testing.T) {
	x := newSRFixture(t)
	page := x.base + "/signups/new"
	if b := x.ann.get(page, 200); !strings.Contains(b, `name="phone"`) || !strings.Contains(b, `name="email"`) {
		t.Errorf("planner form should ask for both contacts: %s", b)
	}
	// Chili has one spot and Pat has it: a planner can still add Lee.
	b := x.ann.post(page, page, url.Values{"name": {"Lee Park"}, "phone": {"555-0199"}, "people_n": {"1"}, "p0_name": {"Lee Park"},
		"q" + itoa(x.qBring): {itoa(x.chili)}}, 200)
	if !strings.Contains(b, "Chili: over by 1") || !strings.Contains(b, "saved") {
		t.Errorf("flash with warning: %s", b)
	}
	list, _ := x.st.ListSignups(x.ev, false)
	var lee *store.Signup
	for i := range list {
		if list[i].Name == "Lee Park" {
			lee = &list[i]
		}
	}
	if lee == nil || lee.Phone != "555-0199" || lee.UserID != 0 {
		t.Fatalf("Lee: %+v", lee)
	}
	// The public form still refuses the same thing.
	// Edit it: change to Bread, no warning now.
	edit := "/signups/" + itoa(lee.ID) + "/edit"
	if b := x.ann.get(edit, 200); !strings.Contains(b, "Lee Park") {
		t.Errorf("edit form: %s", b)
	}
	b = x.ann.post(edit, edit, url.Values{"name": {"Lee Park"}, "phone": {"555-0199"}, "people_n": {"1"}, "p0_id": {itoa(lee.People[0].ID)}, "p0_name": {"Lee Park"},
		"q" + itoa(x.qBring): {itoa(x.bread)}}, 200)
	if strings.Contains(b, "over by") || !strings.Contains(b, "Sign-up saved.") {
		t.Errorf("clean save: %s", b)
	}
	// A missing name is an error on the form.
	if code, body := puPost(t, x.ann, page, page, url.Values{"phone": {"1"}, "people_n": {"1"}, "q" + itoa(x.qBring): {itoa(x.bread)}}); code != 422 || !strings.Contains(body, "Add your name") {
		t.Errorf("no name: %d %s", code, body)
	}
	// Works when the form is a draft, and planners only.
	x.f.Status = "draft"
	x.st.UpdateSignupForm(x.f)
	x.ann.get(page, 200)
	x.hal.get(page, 403)
	x.hal.post(x.base+"/signups", "/signups/"+itoa(lee.ID)+"/withdraw", url.Values{}, 403)
	// Planner note.
	x.ann.post(x.base+"/signups", "/signups/"+itoa(x.sam)+"/note", url.Values{"note": {"Phoned on Sunday"}}, 200)
	if b := x.mem.get(x.base+"/signups", 200); !strings.Contains(b, "Phoned on Sunday") {
		t.Error("team should see the note")
	}
	if b := x.hal.get(x.base+"/signups", 200); strings.Contains(b, "Phoned on Sunday") {
		t.Error("non-team sees the note")
	}
}

func TestSignupPlannerNoteNotOnPublicStatusPage(t *testing.T) {
	x := newSRFixture(t)
	res, err := x.st.SaveSignup(store.SignupInput{EventID: x.ev, Name: "Vi Visitor", Phone: "555-0300",
		People:  []store.PersonInput{{Name: "Vi Visitor"}},
		Answers: []store.AnswerInput{{QuestionID: x.qBring, ChoiceIDs: []int64{x.bread}}}}, store.SignupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	x.st.SetSignupNote(res.ID, "NOTEFORTEAM")
	body := x.c.get("/signup/"+x.f.Code+"/s/"+res.Code, 200)
	if !strings.Contains(body, "Vi Visitor") || strings.Contains(body, "NOTEFORTEAM") {
		t.Errorf("status page: %s", body)
	}
}

func TestAllergyPage(t *testing.T) {
	x := newSRFixture(t)
	page := x.base + "/allergies"
	x.hal.get(page, 403)
	for _, who := range []*client{x.mem, x.ann, x.boss} {
		b := who.get(page, 200)
		for _, want := range []string{"Kim Jones", "Peanuts", "typed for this event", "Pat Jones", "555-0100"} {
			if !strings.Contains(b, want) {
				t.Errorf("allergy page missing %q", want)
			}
		}
		if strings.Contains(b, "Sam Smith") {
			t.Error("people without allergies are not listed")
		}
	}
	// A visitor is sent to sign in, not shown the page.
	resp, _ := x.c.http.Get(x.c.base + page)
	resp.Body.Close()
	if resp.StatusCode == 200 && resp.Request.URL.Path == page {
		t.Error("a visitor read the allergy list")
	}
}

func TestSignupLineOnEventPageAndDashboard(t *testing.T) {
	x := newSRFixture(t)
	b := x.mem.get(x.base, 200)
	if !strings.Contains(b, "Sign-ups: 4 people, open until Nov 1") || !strings.Contains(b, "Allergies (1)") {
		t.Errorf("team event page: %s", b)
	}
	b = x.hal.get(x.base, 200)
	if !strings.Contains(b, "Sign-ups: 4 people") || strings.Contains(b, "Allergies (") {
		t.Errorf("helper event page: %s", b)
	}
	if d := x.hal.get("/", 200); !strings.Contains(d, "Open sign-ups") || !strings.Contains(d, "4 signed up") {
		t.Errorf("dashboard: %s", d)
	}
	x.f.Status = "draft"
	x.st.UpdateSignupForm(x.f)
	if b := x.hal.get(x.base, 200); strings.Contains(b, "Sign-ups:") {
		t.Error("a draft form shows to a helper")
	}
	if b := x.ann.get(x.base, 200); !strings.Contains(b, "draft") {
		t.Error("a draft form should show to a planner")
	}
	if d := x.hal.get("/", 200); strings.Contains(d, "Open sign-ups") {
		t.Error("a draft is not an open sign-up")
	}
	// An event with no form has no line.
	other := apiEvent(t, x.st, "Plain", "public", false, "planning", "2099-11-05", "10:00")
	if b := x.ann.get("/events/"+itoa(other), 200); strings.Contains(b, "Sign-ups:") {
		t.Error("no form, no line")
	}
	x.hal.get("/events/"+itoa(other)+"/signups", 404)
}

func TestSignupAnswersNeverPublic(t *testing.T) {
	x := newSRFixture(t)
	tok, _ := x.st.CreateToken("app", true, 0)
	cal := x.c.get("/calendar/"+itoa(x.ev), 200)
	api, _ := apiGet(t, x.c, "/api/v1/events"+apiRangeQ, tok, 200)
	ics, _ := apiGet(t, x.c, "/calendar.ics", "", 200)
	for name, body := range map[string]string{"calendar page": cal, "api": api, "ics": ics} {
		for _, secret := range []string{"Peanuts", "PLANNERSECRET", "555-0100", "Kim Jones", "Large", "sam@example.com", "Sam Smith"} {
			if strings.Contains(body, secret) {
				t.Errorf("%s shows %q", name, secret)
			}
		}
	}
	// Visitors never get the response pages.
	for _, p := range []string{"/signups", "/signups.csv", "/allergies", "/signups/new"} {
		resp, err := x.c.http.Get(x.c.base + x.base + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && resp.Request.URL.Path == x.base+p {
			t.Errorf("visitor got %s", p)
		}
	}
}
