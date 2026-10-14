package server

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/tylergoza/event-planner/internal/store"
)

// puForm makes a public, unlocked event with an open form and returns the
// form. mod changes the form's settings before it is saved.
func puForm(t *testing.T, st *store.Store, title string, mod func(*store.SignupForm)) (int64, *store.SignupForm) {
	t.Helper()
	ev := apiEvent(t, st, title, "public", false, "planning", "2099-11-01", "18:00")
	f, err := st.CreateSignupForm(ev)
	if err != nil {
		t.Fatal(err)
	}
	f.Status = "open"
	f.Intro = "Come and join us"
	if mod != nil {
		mod(f)
	}
	if err := st.UpdateSignupForm(f); err != nil {
		t.Fatal(err)
	}
	return ev, f
}

func puQ(t *testing.T, st *store.Store, ev int64, in store.SignupQuestionInput) int64 {
	t.Helper()
	id, err := st.CreateSignupQuestion(ev, in)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func puC(t *testing.T, st *store.Store, q int64, label string, spots int) int64 {
	t.Helper()
	id, err := st.CreateSignupChoice(q, label, spots)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// post sends a POST to a public page without the status check of the
// shared helper, returning the status as well.
func puPost(t *testing.T, c *client, page, path string, v url.Values) (int, string) {
	t.Helper()
	m := csrfRe.FindStringSubmatch(c.get(page, 200))
	if m == nil {
		t.Fatalf("no csrf token on %s", page)
	}
	v.Set("_csrf", m[1])
	resp, err := c.http.PostForm(c.base+path, v)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

var statusLinkRe = regexp.MustCompile(`href="(/signup/[^/"]+/s/[^"]+)"`)

func TestSignupVisitorSignsUp(t *testing.T) {
	c, st := newTestServer(t)
	ev, f := puForm(t, st, "Chili Cook-off", func(f *store.SignupForm) { f.ThankYou = "See you there!" })
	q := puQ(t, st, ev, store.SignupQuestionInput{Kind: "one", Label: "What will you bring?", Required: true})
	chili := puC(t, st, q, "Chili", 2)
	puC(t, st, q, "Bread", 0)
	page := "/signup/" + f.Code

	body := c.get(page, 200)
	for _, want := range []string{"Chili Cook-off", "Come and join us", "2 spots left", "What will you bring?", `name="phone"`, `name="name"`, `name="website"`} {
		if !strings.Contains(body, want) {
			t.Errorf("form missing %q", want)
		}
	}
	if strings.Contains(body, "Preview: this form is a draft") {
		t.Error("open form shows the preview banner")
	}

	// No phone: refused with the typed name kept.
	code, body := puPost(t, c, page, page, url.Values{"name": {"Pat Jones"}, "q" + itoa(q): {itoa(chili)}})
	if code != 422 || !strings.Contains(body, "Add a phone number") || !strings.Contains(body, `value="Pat Jones"`) {
		t.Fatalf("missing phone: %d %s", code, body)
	}
	if list, _ := st.ListSignups(ev, true); len(list) != 0 {
		t.Fatal("a refused sign-up was saved")
	}

	code, body = puPost(t, c, page, page, url.Values{"name": {"Pat Jones"}, "phone": {"555-1234"}, "q" + itoa(q): {itoa(chili)}})
	if code != 200 || !strings.Contains(body, "See you there!") || !strings.Contains(body, "Keep this link to change or withdraw") {
		t.Fatalf("thanks page: %d %s", code, body)
	}
	m := statusLinkRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no status link on the thank-you page")
	}
	status := c.get(m[1], 200)
	if !strings.Contains(status, "Pat Jones") || !strings.Contains(status, "Chili") || !strings.Contains(status, "Change") {
		t.Errorf("status page: %s", status)
	}
	if body := c.get(page, 200); !strings.Contains(body, "1 spot left") {
		t.Error("spots left should drop")
	}
	if list, _ := st.ListSignups(ev, false); len(list) != 1 || list[0].Phone != "555-1234" {
		t.Fatalf("stored: %+v", list)
	}
	// A wrong status code is a 404.
	c.get("/signup/"+f.Code+"/s/nope", 404)
	c.get("/signup/nope", 404)
}

func TestSignupContactRules(t *testing.T) {
	c, st := newTestServer(t)
	_, f := puForm(t, st, "Dinner", func(f *store.SignupForm) { f.Contact = "phone_or_email" })
	page := "/signup/" + f.Code
	code, body := puPost(t, c, page, page, url.Values{"name": {"Pat"}})
	if code != 422 || !strings.Contains(body, "phone number or an email") {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := puPost(t, c, page, page, url.Values{"name": {"Pat"}, "email": {"pat@example.com"}}); code != 200 {
		t.Errorf("email alone should do: %d", code)
	}
	_, f2 := puForm(t, st, "No contact", func(f *store.SignupForm) { f.Contact = "none" })
	body = c.get("/signup/"+f2.Code, 200)
	if strings.Contains(body, `name="phone"`) || strings.Contains(body, `name="email"`) {
		t.Error("contact: none should not ask for contact")
	}
}

func TestSignupAudience(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("ann", "Ann Lee", "a long password", store.RolePlanner)
	st.CreateUser("hal", "Hal Fox", "a long password", store.RoleHelper)

	// Private and locked events refuse visitors.
	privEv := apiEvent(t, st, "Elders", "private", false, "planning", "2099-11-02", "09:00")
	pf, _ := st.CreateSignupForm(privEv)
	pf.Status = "open"
	st.UpdateSignupForm(pf)
	c.get("/signup/"+pf.Code, 404)

	lockedEv := apiEvent(t, st, "Locked", "public", true, "planning", "2099-11-03", "09:00")
	lf, _ := st.CreateSignupForm(lockedEv)
	lf.Status = "open"
	st.UpdateSignupForm(lf)
	c.get("/signup/"+lf.Code, 404)
	hal := c.as("hal")
	hal.get("/signup/"+lf.Code, 404) // not a member
	ann := c.as("ann")
	ann.get("/signup/"+lf.Code, 404) // planner but not a member
	admin := func() *client {
		st.CreateUser("root", "Root", "a long password", store.RoleAdmin)
		return c.as("root")
	}()
	admin.get("/signup/"+lf.Code, 200)

	// A signed-in form refuses visitors, welcomes signed-in people.
	_, sf := puForm(t, st, "Members only", func(f *store.SignupForm) { f.Audience = "signed_in" })
	c.get("/signup/"+sf.Code, 404)
	body := hal.get("/signup/"+sf.Code, 200)
	if !strings.Contains(body, "Hal Fox") || strings.Contains(body, `name="phone"`) {
		t.Errorf("signed in: name from the account, no contact asked: %s", body)
	}
	if code, _ := puPost(t, c, "/login", "/signup/"+sf.Code, url.Values{"name": {"Pat"}, "phone": {"1"}}); code != 404 {
		t.Errorf("visitor POST to a signed-in form: %d", code)
	}
}

func TestSignupDraftPreview(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("ann", "Ann Lee", "a long password", store.RolePlanner)
	st.CreateUser("hal", "Hal Fox", "a long password", store.RoleHelper)
	_, f := puForm(t, st, "Draft dinner", func(f *store.SignupForm) { f.Status = "draft" })
	page := "/signup/" + f.Code
	c.get(page, 404)
	c.as("hal").get(page, 404)
	ann := c.as("ann")
	body := ann.get(page, 200)
	if !strings.Contains(body, "Preview: this form is a draft") || !strings.Contains(body, "disabled>Sign up") {
		t.Errorf("planner preview: %s", body)
	}
	if code, _ := puPost(t, ann, page, page, url.Values{}); code != 403 {
		t.Errorf("a draft takes no sign-ups: %d", code)
	}
}

func TestSignupHoneypot(t *testing.T) {
	c, st := newTestServer(t)
	ev, f := puForm(t, st, "Dinner", nil)
	page := "/signup/" + f.Code
	code, body := puPost(t, c, page, page, url.Values{"name": {"Bot"}, "phone": {"1"}, "website": {"http://spam"}})
	if code != 200 || !strings.Contains(body, "Thank you") {
		t.Fatalf("honeypot should look like it worked: %d", code)
	}
	if strings.Contains(body, "/s/") {
		t.Error("honeypot sign-up got a status link")
	}
	if list, _ := st.ListSignups(ev, true); len(list) != 0 {
		t.Error("honeypot sign-up was saved")
	}
}

func TestSignupRateLimit(t *testing.T) {
	c, st := newTestServer(t)
	_, f := puForm(t, st, "Dinner", nil)
	page := "/signup/" + f.Code
	// Validation errors (no phone) and "add more people" don't use it up.
	for i := 0; i < 40; i++ {
		if code, _ := puPost(t, c, page, page, url.Values{"name": {"x"}}); code != 422 {
			t.Fatalf("validation error %d got %d", i, code)
		}
	}
	// Saved sign-ups do.
	got429 := false
	for i := 0; i < 35; i++ {
		if code, _ := puPost(t, c, page, page, url.Values{"name": {"x"}, "phone": {"1"}}); code == 429 {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Error("expected the sign-up limiter to kick in")
	}
}

func TestSignupPeopleListAndPerPersonSpots(t *testing.T) {
	c, st := newTestServer(t)
	ev, f := puForm(t, st, "Kids' day", func(f *store.SignupForm) { f.People = "list"; f.AskAllergies = true; f.AskGuests = true })
	q := puQ(t, st, ev, store.SignupQuestionInput{Kind: "one", Label: "Which class?", PerPerson: true, Required: true})
	craft := puC(t, st, q, "Craft", 2)
	page := "/signup/" + f.Code

	body := c.get(page, 200)
	for _, want := range []string{`name="p0_name"`, `name="p2_name"`, `name="p0_allergies"`, `name="p1_q` + itoa(q) + `"`, "Add more people", `name="people_n" value="3"`, `name="guests"`} {
		if !strings.Contains(body, want) {
			t.Errorf("list form missing %q", want)
		}
	}
	// Add more people: no JS, nothing saved, typed names kept, 3 more rows.
	code, body := puPost(t, c, page, page, url.Values{"name": {"Mom"}, "phone": {"1"}, "people_n": {"3"}, "p0_name": {"Ava"}, "more": {"1"}})
	if code != 200 || !strings.Contains(body, `name="p5_name"`) || !strings.Contains(body, `value="Ava"`) {
		t.Fatalf("more rows: %d", code)
	}
	if list, _ := st.ListSignups(ev, true); len(list) != 0 {
		t.Fatal("Add more people saved something")
	}

	three := url.Values{"name": {"Mom"}, "phone": {"1"}, "people_n": {"3"}}
	for i, n := range []string{"Ava", "Ben", "Cy"} {
		three.Set("p"+itoa(int64(i))+"_name", n)
		three.Set("p"+itoa(int64(i))+"_q"+itoa(q), itoa(craft))
	}
	code, body = puPost(t, c, page, page, three)
	if code != 422 || !strings.Contains(body, "Craft has only 2 spots left") || !strings.Contains(body, `value="Ava"`) || !strings.Contains(body, `value="Cy"`) {
		t.Fatalf("three in a 2-spot class: %d %s", code, body)
	}
	if list, _ := st.ListSignups(ev, true); len(list) != 0 {
		t.Fatal("refused sign-up was saved")
	}
	// Two fit; a blank middle row doesn't shift answers.
	two := url.Values{"name": {"Mom"}, "phone": {"1"}, "people_n": {"3"}, "p0_name": {"Ava"}, "p2_name": {"Cy"},
		"p0_q" + itoa(q): {itoa(craft)}, "p2_q" + itoa(q): {itoa(craft)}, "p2_allergies": {"nuts"}, "guests": {"1"}}
	code, body = puPost(t, c, page, page, two)
	if code != 200 {
		t.Fatalf("two should fit: %d %s", code, body)
	}
	if list, _ := st.ListSignups(ev, false); len(list) != 1 || len(list[0].People) != 2 || list[0].People[1].Allergies != "nuts" || list[0].Guests != 1 {
		t.Fatalf("stored: %+v", list)
	}
	if body := c.get(page, 200); !strings.Contains(body, "Full") {
		t.Error("the class should show Full")
	}
}

func TestSignupExcludesAndFollowUps(t *testing.T) {
	c, st := newTestServer(t)
	ev, f := puForm(t, st, "Cook-off", nil)
	q1 := puQ(t, st, ev, store.SignupQuestionInput{Kind: "many", Label: "What would you like to do?", Required: true})
	mk := puC(t, st, q1, "Make chili", 0)
	jd := puC(t, st, q1, "Judge chili", 0)
	if err := st.SetChoiceExcludes(mk, []int64{jd}); err != nil {
		t.Fatal(err)
	}
	q2 := puQ(t, st, ev, store.SignupQuestionInput{Kind: "text", Label: "Name of your chili", When: []int64{mk}})
	page := "/signup/" + f.Code

	body := c.get(page, 200)
	if !strings.Contains(body, "(only if you picked Make chili)") || !strings.Contains(body, `data-excludes="`+itoa(jd)+`"`) || !strings.Contains(body, `data-signup-when="`+itoa(mk)+`"`) {
		t.Errorf("follow-up/exclusion markup missing: %s", body)
	}
	both := url.Values{"name": {"Pat"}, "phone": {"1"}, "q" + itoa(q1): {itoa(mk), itoa(jd)}}
	code, body := puPost(t, c, page, page, both)
	if code != 422 || !strings.Contains(body, "You can&#39;t pick both Make chili and Judge chili") {
		t.Fatalf("exclusion: %d %s", code, body)
	}
	if !strings.Contains(body, `value="Pat"`) {
		t.Error("typed name lost")
	}

	// A follow-up answer when its trigger isn't picked is dropped.
	code, _ = puPost(t, c, page, page, url.Values{"name": {"Sam"}, "phone": {"1"}, "q" + itoa(q1): {itoa(jd)}, "q" + itoa(q2): {"Ghost chili"}})
	if code != 200 {
		t.Fatalf("judge only: %d", code)
	}
	list, _ := st.ListSignups(ev, false)
	if len(list) != 1 {
		t.Fatalf("%d sign-ups", len(list))
	}
	for _, a := range list[0].Answers {
		if a.QuestionID == q2 {
			t.Errorf("follow-up kept: %+v", a)
		}
	}
}

// visitor is another browser with no sign-in.
func visitor(c *client) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: c.t, base: c.base, http: &http.Client{Jar: jar}}
}

func TestSignupChangeAndWithdraw(t *testing.T) {
	c, st := newTestServer(t)
	ev, f := puForm(t, st, "Dinner", nil)
	q := puQ(t, st, ev, store.SignupQuestionInput{Kind: "one", Label: "Bring", Required: true})
	chili := puC(t, st, q, "Chili", 1)
	bread := puC(t, st, q, "Bread", 0)
	page := "/signup/" + f.Code
	_, body := puPost(t, c, page, page, url.Values{"name": {"Pat"}, "phone": {"1"}, "q" + itoa(q): {itoa(chili)}})
	link := statusLinkRe.FindStringSubmatch(body)[1]

	// Full now: the choice is disabled for the next person, and a forced post is refused.
	other := visitor(c)
	if body := other.get(page, 200); !strings.Contains(body, "disabled data-full") {
		t.Error("full choice should be disabled")
	}
	if code, body := puPost(t, other, page, page, url.Values{"name": {"Sam"}, "phone": {"2"}, "q" + itoa(q): {itoa(chili)}}); code != 422 || !strings.Contains(body, "Chili") {
		t.Fatalf("full choice taken anyway: %d", code)
	}

	// Change: the form is pre-filled; keep Chili (own spot), change the name.
	edit := c.get(link+"/edit", 200)
	if !strings.Contains(edit, `value="Pat"`) || !strings.Contains(edit, "Save changes") || !strings.Contains(edit, "checked") {
		t.Errorf("edit form not pre-filled: %s", edit)
	}
	if strings.Contains(edit, "disabled data-full") {
		t.Error("the signer's own full choice must stay pickable")
	}
	c.post(link+"/edit", link, url.Values{"name": {"Patricia"}, "phone": {"1"}, "q" + itoa(q): {itoa(chili)}}, 200)
	if body := c.get(link, 200); !strings.Contains(body, "Patricia") || !strings.Contains(body, "Chili") {
		t.Errorf("changed status page: %s", body)
	}
	// Switch to Bread: frees the chili spot.
	c.post(link+"/edit", link, url.Values{"name": {"Patricia"}, "phone": {"1"}, "q" + itoa(q): {itoa(bread)}}, 200)
	if body := other.get(page, 200); strings.Contains(body, "disabled data-full") {
		t.Error("chili should be free again")
	}
	c.post(link+"/edit", link, url.Values{"name": {"Patricia"}, "phone": {"1"}, "q" + itoa(q): {itoa(chili)}}, 200)

	// Withdraw frees the spot and the page says so.
	body = c.post(link, link+"/withdraw", url.Values{}, 200)
	if !strings.Contains(body, "You withdrew this sign-up") {
		t.Errorf("withdrawn page: %s", body)
	}
	if body := other.get(page, 200); strings.Contains(body, "disabled data-full") || !strings.Contains(body, "1 spot left") {
		t.Error("withdrawing should free the spot")
	}
	if list, _ := st.ListSignups(ev, false); len(list) != 0 {
		t.Error("withdrawn sign-up still counts")
	}
}

func TestSignupClosedFormRefuses(t *testing.T) {
	c, st := newTestServer(t)
	ev, f := puForm(t, st, "Dinner", func(f *store.SignupForm) { f.ClosesAt = "2000-01-01T10:00" })
	page := "/signup/" + f.Code
	body := c.get(page, 200)
	if !strings.Contains(body, "Sign-ups are closed") || strings.Contains(body, `name="name"`) {
		t.Errorf("closed form: %s", body)
	}
	if code, _ := puPost(t, c, "/login", page, url.Values{"name": {"Pat"}, "phone": {"1"}}); code != 422 {
		t.Errorf("closed form took a sign-up: %d", code)
	}
	if list, _ := st.ListSignups(ev, true); len(list) != 0 {
		t.Error("saved")
	}
}

func TestMySignups(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("hal", "Hal Fox", "a long password", store.RoleHelper)
	st.CreateUser("ann", "Ann Lee", "a long password", store.RolePlanner)
	ev, f := puForm(t, st, "Harvest Dinner", nil)
	page := "/signup/" + f.Code
	hal := c.as("hal")

	code, body := puPost(t, hal, page, page, url.Values{})
	if code != 200 || !strings.Contains(body, "/my/signups/") || strings.Contains(body, "Keep this link") {
		t.Fatalf("signed-in sign-up: %d %s", code, body)
	}
	list, _ := st.ListSignups(ev, false)
	if len(list) != 1 || list[0].Name != "Hal Fox" || list[0].UserID == 0 {
		t.Fatalf("stored %+v", list)
	}
	mine := hal.get("/my/signups", 200)
	if !strings.Contains(mine, "Harvest Dinner") {
		t.Errorf("my sign-ups: %s", mine)
	}
	one := "/my/signups/" + itoa(list[0].ID)
	if body := hal.get(one, 200); !strings.Contains(body, "Hal Fox") || !strings.Contains(body, "Withdraw") {
		t.Errorf("my sign-up: %s", body)
	}
	hal.get(one+"/edit", 200)
	hal.post(one+"/edit", one, url.Values{}, 200)
	// Not someone else's.
	c.as("ann").get(one, 404)
	visitor(c).get("/my/signups", 200) // signed-out: lands on the sign-in page
	hal.post(one, one+"/withdraw", url.Values{}, 200)
	if list, _ := st.ListSignups(ev, false); len(list) != 0 {
		t.Error("withdraw from My sign-ups failed")
	}
	if body := hal.get("/my/signups", 200); !strings.Contains(body, "Withdrawn") {
		t.Error("withdrawn mark missing")
	}
}

func TestCalendarSignupButton(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("hal", "Hal Fox", "a long password", store.RoleHelper)
	ev, f := puForm(t, st, "Dinner", func(f *store.SignupForm) { f.Label = "Join in" })
	cal := "/calendar/" + itoa(ev)
	if body := c.get(cal, 200); !strings.Contains(body, `href="/signup/`+f.Code+`"`) || !strings.Contains(body, "Join in") {
		t.Errorf("open form: %s", body)
	}
	// A signed-in-only form: visitors are asked to sign in.
	f.Audience = "signed_in"
	st.UpdateSignupForm(f)
	if body := c.get(cal, 200); !strings.Contains(body, "Sign in to sign up") || strings.Contains(body, `href="/signup/`) {
		t.Errorf("signed_in form for a visitor: %s", body)
	}
	if body := c.as("hal").get(cal, 200); !strings.Contains(body, "Join in") {
		t.Error("signed-in person should see the button")
	}
	// Full.
	f.Audience = "anyone"
	st.UpdateSignupForm(f)
	q := puQ(t, st, ev, store.SignupQuestionInput{Kind: "one", Label: "Bring", Required: true})
	ch := puC(t, st, q, "Chili", 1)
	st.SaveSignup(store.SignupInput{EventID: ev, Name: "Pat", People: []store.PersonInput{{}}, Phone: "1",
		Answers: []store.AnswerInput{{QuestionID: q, ChoiceIDs: []int64{ch}}}}, store.SignupOptions{})
	if body := c.get(cal, 200); !strings.Contains(body, "<strong>Full</strong>") || strings.Contains(body, `href="/signup/`) {
		t.Errorf("full form: %s", body)
	}
	// Closed.
	f.Status = "closed"
	st.UpdateSignupForm(f)
	if body := c.get(cal, 200); !strings.Contains(body, "Sign-ups closed") {
		t.Errorf("closed form: %s", body)
	}
	// Draft: nothing.
	f.Status = "draft"
	st.UpdateSignupForm(f)
	if body := c.get(cal, 200); strings.Contains(body, "Sign-ups closed") || strings.Contains(body, "Full") || strings.Contains(body, "/signup/") {
		t.Errorf("draft form shows on the calendar: %s", body)
	}
	// No form at all.
	other := apiEvent(t, st, "Plain", "public", false, "planning", "2099-11-05", "10:00")
	if body := c.get("/calendar/"+itoa(other), 200); strings.Contains(body, "/signup/") {
		t.Error("no form, no button")
	}
}

func TestWhosBringingWhatOnTheForm(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("hal", "Hal Fox", "a long password", store.RoleHelper)
	ev, f := puForm(t, st, "Cook-off", func(f *store.SignupForm) { f.People = "list" })
	q := puQ(t, st, ev, store.SignupQuestionInput{Kind: "one", Label: "What will you bring?", PublicList: true})
	chili := puC(t, st, q, "Make chili", 0)
	judge := puC(t, st, q, "Judge chili", 0)
	dish := puQ(t, st, ev, store.SignupQuestionInput{Kind: "text", Label: "Name of the chili", PublicList: true, When: []int64{chili}})
	cls := puQ(t, st, ev, store.SignupQuestionInput{Kind: "text", Label: "Favourite colour", PerPerson: true})
	page := "/signup/" + f.Code

	if body := c.get(page, 200); strings.Contains(body, "Who's bringing what") || !strings.Contains(body, "Your first name, last initial and this answer show on this page.") {
		t.Error("empty list hidden, notice shown")
	}
	post := func(who, qid string, choice int64, extra url.Values) string {
		v := url.Values{"name": {who}, "phone": {"1"}, "people_n": {"3"}, "p0_name": {"Kiddo " + qid}, "p0_q" + itoa(cls): {"Purple"}, "q" + itoa(q): {itoa(choice)}}
		for k, vv := range extra {
			v[k] = vv
		}
		code, body := puPost(t, visitor(c), page, page, v)
		if code != 200 {
			t.Fatalf("%s: %d %s", who, code, body)
		}
		return statusLinkRe.FindStringSubmatch(body)[1]
	}
	link := post("Tyler Goza", "A", chili, url.Values{"q" + itoa(dish): {"Smoky three-bean"}})
	post("Jane Doe", "B", judge, nil)
	gone := post("Gone Person", "C", chili, nil)

	body := c.get(page, 200)
	for _, want := range []string{"Who's bringing what", "Make chili", "Tyler G.", "Smoky three-bean", "Judge chili", "Jane D."} {
		if !strings.Contains(body, want) {
			t.Errorf("list missing %q", want)
		}
	}
	for _, bad := range []string{"Kiddo", "Purple", "Goza", "Doe"} {
		if strings.Contains(body, bad) {
			t.Errorf("form page leaks %q", bad)
		}
	}
	if !strings.Contains(body, "Gone P.") {
		t.Error("in sign-up should be listed")
	}
	visitor(c).post(page, gone+"/withdraw", url.Values{}, 200)
	if body := c.get(page, 200); strings.Contains(body, "Gone P.") || !strings.Contains(body, "Tyler G.") {
		t.Error("a withdrawn sign-up should leave the list")
	}

	// A signed-in-only form hides its lists from visitors (they get a 404).
	f.Audience = "signed_in"
	st.UpdateSignupForm(f)
	c.get(page, 404)
	if body := c.as("hal").get(page, 200); !strings.Contains(body, "Tyler G.") {
		t.Error("signed-in people see the list")
	}

	// Answers, names and phones never reach the public calendar, API or feed.
	f.Audience = "anyone"
	st.UpdateSignupForm(f)
	tok, _ := st.CreateToken("app", true, 0)
	api, _ := apiGet(t, c, "/api/v1/events"+apiRangeQ, tok, 200)
	ics, _ := apiGet(t, c, "/calendar.ics"+apiRangeQ, "", 200)
	cal := c.get("/calendar/"+itoa(ev), 200)
	for name, text := range map[string]string{"api": api, "ics": ics, "calendar page": cal} {
		for _, bad := range []string{"Smoky three-bean", "Tyler", "Goza", "Kiddo", "Purple", "Jane"} {
			if strings.Contains(text, bad) {
				t.Errorf("%s leaks %q", name, bad)
			}
		}
	}
	_ = link
}
