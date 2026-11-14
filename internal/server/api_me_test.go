package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tylergoza/event-planner/internal/store"
	"github.com/tylergoza/event-planner/internal/umtoken"
)

// fakeTokenUM answers User Management's GET /api/v1/token for the tokens
// in its map; anything else (another audience, expired) is 401.
type fakeTokenUM struct {
	srv  *httptest.Server
	hits atomic.Int32
	mu   sync.Mutex
	ok   map[string]umtoken.Answer
	down bool
}

func newFakeTokenUM(t *testing.T) *fakeTokenUM {
	f := &fakeTokenUM{ok: map[string]umtoken.Answer{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/token" {
			http.NotFound(w, r)
			return
		}
		f.hits.Add(1)
		f.mu.Lock()
		ans, ok := f.ok[r.Header.Get("X-Token")]
		down := f.down
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if id, sec, good := r.BasicAuth(); !good || id != "events" || sec != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		switch {
		case down:
			w.WriteHeader(http.StatusBadGateway)
		case !ok:
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid_token"}`))
		default:
			json.NewEncoder(w).Encode(ans)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTokenUM) give(token, sub, username, role string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ok[token] = umtoken.Answer{User: umtoken.User{Sub: sub, Username: username, Name: strings.ToUpper(username[:1]) + username[1:], Role: role}, Via: "mcp"}
}

func (f *fakeTokenUM) setDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

// meServer runs the planner with single sign-on pointing at f (nil for
// none) on st (nil for a new store).
func meServer(t *testing.T, f *fakeTokenUM, st *store.Store) (*Server, *store.Store, *client) {
	t.Helper()
	if st == nil {
		var err error
		if st, err = store.Open(filepath.Join(t.TempDir(), "test.db")); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
	}
	cfg := Config{}
	if f != nil {
		cfg.Env = map[string]string{"SSO_URL": f.srv.URL, "SSO_CLIENT_ID": "events", "SSO_CLIENT_SECRET": "s3cret"}
	}
	srv, err := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return srv, st, &client{t: t, base: ts.URL, http: &http.Client{Jar: jar}}
}

// meGet is apiGet that also checks the /me headers on every answer.
func meGet(t *testing.T, c *client, path, token string, want int) (string, http.Header) {
	t.Helper()
	body, h := apiGet(t, c, path, token, want)
	if h.Get("Set-Cookie") != "" {
		t.Errorf("GET %s set a cookie", path)
	}
	if h.Get("Cache-Control") != "private, no-store" {
		t.Errorf("GET %s: Cache-Control %q", path, h.Get("Cache-Control"))
	}
	if h.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("GET %s: CORS header on /me", path)
	}
	return body, h
}

func isJSONError(body string) bool {
	var e struct{ Error string }
	return json.Unmarshal([]byte(body), &e) == nil && e.Error != ""
}

func TestMeOffWithoutUserManagement(t *testing.T) {
	_, st, c := meServer(t, nil, nil)
	tok, _ := st.CreateToken("site", false, 0)
	for _, p := range []string{"/api/v1/me/events", "/api/v1/me/signups", "/api/v1/me/signups/open", "/api/v1/me/nope"} {
		if body, _ := meGet(t, c, p, "umd_whatever", 404); isJSONError(body) {
			t.Errorf("%s: off should be a plain 404, got %s", p, body)
		}
	}
	// The token API is unchanged.
	apiGet(t, c, "/api/v1/events"+apiRangeQ, tok, 200)
	if body, _ := apiGet(t, c, "/api/v1/nope", tok, 404); !isJSONError(body) {
		t.Error("the /api/ catch-all should still answer JSON")
	}
}

func TestMeTokenChecks(t *testing.T) {
	f := newFakeTokenUM(t)
	_, st, c := meServer(t, f, nil)
	f.give("umd_hal", "s-hal", "hal", "helper")
	epTok, _ := st.CreateToken("site", true, 0)

	// No token, an ep_ token, a token for another audience, an expired
	// one: all 401 invalid_token, and nobody is added.
	for _, tok := range []string{"", epTok, "umd_for_calendar", "umd_expired"} {
		body, h := meGet(t, c, "/api/v1/me/events", tok, 401)
		if !strings.Contains(body, "invalid_token") || h.Get("WWW-Authenticate") == "" {
			t.Errorf("token %q: %s", tok, body)
		}
	}
	if n, _ := st.CountUsers(); n != 0 {
		t.Errorf("refused tokens made %d users", n)
	}
	if f.hits.Load() != 2 {
		t.Errorf("only umd_ tokens should be sent to User Management: %d calls", f.hits.Load())
	}

	// A good token: the user is made like an SSO sign-in, and a second
	// call within 60 s uses the cache.
	f.hits.Store(0)
	meGet(t, c, "/api/v1/me/events", "umd_hal", 200)
	meGet(t, c, "/api/v1/me/signups", "umd_hal", 200)
	if n := f.hits.Load(); n != 1 {
		t.Errorf("second call within 60 s asked User Management again: %d calls", n)
	}
	users, _ := st.ListUsers()
	if len(users) != 1 || users[0].SSOSubject != "s-hal" || users[0].Role != store.RoleHelper || users[0].DisplayName != "Hal" {
		t.Fatalf("users = %+v", users)
	}
	var sessions int
	if err := st.DB.QueryRow(`SELECT count(*) FROM sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Errorf("a /me call made %d sessions (%v)", sessions, err)
	}

	// A umd_ token is no good on the token API.
	apiGet(t, c, "/api/v1/events"+apiRangeQ, "umd_hal", 401)

	// Another delegate is refused; User Management down is 503, not 401.
	f.mu.Lock()
	f.ok["umd_other"] = umtoken.Answer{User: umtoken.User{Sub: "s-x", Username: "x"}, Via: "someapp"}
	f.mu.Unlock()
	meGet(t, c, "/api/v1/me/events", "umd_other", 403)
	f.setDown(true)
	if body, h := meGet(t, c, "/api/v1/me/events", "umd_new", 503); h.Get("Retry-After") == "" || !isJSONError(body) {
		t.Errorf("503 without Retry-After or JSON: %s", body)
	}
	f.setDown(false)

	// Unknown /me paths are a plain 404 ("not built"), unlike /api/'s JSON one.
	for _, p := range []string{"/api/v1/me/nope", "/api/v1/me/signups/9", "/api/v1/me/"} {
		if body, _ := meGet(t, c, p, "umd_hal", 404); isJSONError(body) {
			t.Errorf("unknown /me path %s: %s", p, body)
		}
	}
	// The token API still works with SSO on, CORS and all.
	if _, h := apiGet(t, c, "/api/v1/events"+apiRangeQ, epTok, 200); h.Get("Access-Control-Allow-Origin") != "*" {
		t.Error("the token API lost its CORS header")
	}
	if body, _ := apiGet(t, c, "/api/v1/nope", epTok, 404); !isJSONError(body) {
		t.Error("the /api/ catch-all should still answer JSON")
	}
	// A bad range is a JSON 400.
	if body, _ := meGet(t, c, "/api/v1/me/events?from=nope", "umd_hal", 400); !isJSONError(body) {
		t.Errorf("bad from: %s", body)
	}
}

// meUsers makes hal (helper), ann (planner) and root (admin) as local
// users with passwords, so a server without SSO on the same store can
// show their web pages, and gives each a token for the same username
// (UpsertSSOUser links them).
func meUsers(t *testing.T, f *fakeTokenUM, st *store.Store) map[string]int64 {
	ids := map[string]int64{}
	for _, u := range []struct{ name, role string }{{"hal", store.RoleHelper}, {"ann", store.RolePlanner}, {"root", store.RoleAdmin}} {
		id, err := st.CreateUser(u.name, "", "a long password", u.role)
		if err != nil {
			t.Fatal(err)
		}
		ids[u.name] = id
		f.give("umd_"+u.name, "s-"+u.name, u.name, u.role)
	}
	return ids
}

// TestMeEventsVisibility: each role gets what its calendar page shows.
func TestMeEventsVisibility(t *testing.T) {
	f := newFakeTokenUM(t)
	_, st, c := meServer(t, f, nil)
	ids := meUsers(t, f, st)
	apiEvent(t, st, "Pub Dinner", "public", false, "planning", "2099-11-01", "18:00", "Hall")
	apiEvent(t, st, "Priv Meeting", "private", false, "planning", "2099-11-02", "09:00", "Library")
	apiEvent(t, st, "Locked Retreat", "private", true, "planning", "2099-11-03", "10:00", "Office")
	halsLocked := apiEvent(t, st, "Hal Locked", "private", true, "planning", "2099-11-04", "10:00", "Gym")
	st.SetMembers(halsLocked, []int64{ids["hal"]})
	apiEvent(t, st, "Gone Party", "public", false, "cancelled", "2099-11-05", "10:00")
	apiEvent(t, st, "Undated Idea", "public", false, "planning", "", "")

	_, _, web := meServer(t, nil, st)
	all := []string{"Pub Dinner", "Priv Meeting", "Locked Retreat", "Hal Locked"}
	for _, who := range []string{"hal", "ann", "root"} {
		page := web.as(who).get("/calendar?month=2099-11", 200)
		body, _ := meGet(t, c, "/api/v1/me/events?from=2099-11-01&to=2099-11-30", "umd_"+who, 200)
		for _, secret := range []string{"SECRET NOTE", "SECRET COORD", "Gone Party", "Undated Idea"} {
			if strings.Contains(body, secret) {
				t.Errorf("%s: /me/events has %q", who, secret)
			}
		}
		var out struct{ Events []apiEventJSON }
		json.Unmarshal([]byte(body), &out)
		if len(out.Events) != 4 {
			t.Fatalf("%s: events = %s", who, body)
		}
		busy := 0
		for _, e := range out.Events {
			if e.Busy {
				busy++
				if e.ID != 0 || e.Title != "" || e.URL != "" || e.Description != "" || len(e.Rooms) != 1 {
					t.Errorf("%s: busy entry has details: %+v", who, e)
				}
			}
		}
		for _, title := range all {
			if strings.Contains(page, title) != strings.Contains(body, title) {
				t.Errorf("%s: %q on the web page %v, in /me/events %v", who, title, strings.Contains(page, title), strings.Contains(body, title))
			}
		}
		want := map[string]int{"hal": 1, "ann": 2, "root": 0}[who]
		if busy != want {
			t.Errorf("%s: %d busy entries, want %d", who, busy, want)
		}
	}
	// limit is honoured.
	body, _ := meGet(t, c, "/api/v1/me/events?from=2099-11-01&to=2099-11-30&limit=1", "umd_root", 200)
	var out struct{ Events []apiEventJSON }
	json.Unmarshal([]byte(body), &out)
	if len(out.Events) != 1 {
		t.Errorf("limit=1: %s", body)
	}
	// Empty is [].
	if body, _ := meGet(t, c, "/api/v1/me/events?from=2098-01-01&to=2098-01-02", "umd_hal", 200); !strings.Contains(body, `"events": []`) {
		t.Errorf("empty: %s", body)
	}
}

// TestMeSignups: open sign-ups follow /signup/{code}'s audience, and my
// sign-ups are only the person's own, as on /my/signups, with names only.
func TestMeSignups(t *testing.T) {
	f := newFakeTokenUM(t)
	_, st, c := meServer(t, f, nil)
	ids := meUsers(t, f, st)
	_, _, web := meServer(t, nil, st)

	open := func(title, visibility string, locked bool, mod func(*store.SignupForm)) (int64, *store.SignupForm) {
		ev := apiEvent(t, st, title, visibility, locked, "planning", "2099-11-10", "18:00", "Hall")
		fm, err := st.CreateSignupForm(ev)
		if err != nil {
			t.Fatal(err)
		}
		fm.Status, fm.People, fm.AskAllergies, fm.AskGuests = "open", "list", true, true
		if mod != nil {
			mod(fm)
		}
		if err := st.UpdateSignupForm(fm); err != nil {
			t.Fatal(err)
		}
		return ev, fm
	}
	pubEv, pubF := open("Pub Supper", "public", false, func(f *store.SignupForm) { f.Label = "Supper RSVP"; f.ClosesAt = "2099-11-08T12:00" })
	privEv, privF := open("Priv Lunch", "private", false, func(f *store.SignupForm) { f.Audience = "signed_in" })
	_, lockF := open("Locked Camp", "private", true, nil)
	open("Draft Thing", "public", false, func(f *store.SignupForm) { f.Status = "draft" })
	open("Closed Thing", "public", false, func(f *store.SignupForm) { f.Status = "closed" })
	fullEv, fullF := open("Full Picnic", "public", false, nil)
	q := puQ(t, st, fullEv, store.SignupQuestionInput{Kind: "one", Label: "Bring", Required: true})
	ch := puC(t, st, q, "Pie", 1)

	save := func(ev int64, userID int64, name string, people ...store.PersonInput) int64 {
		t.Helper()
		in := store.SignupInput{EventID: ev, UserID: userID, Name: name, Phone: "555-0199", People: people, Guests: 2}
		if ev == fullEv {
			in.Answers = []store.AnswerInput{{QuestionID: q, ChoiceIDs: []int64{ch}}}
		}
		res, err := st.SaveSignup(in, store.SignupOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return res.ID
	}
	// Kim (a visitor) takes the last spot at the picnic and signs up for
	// supper with her allergy. Hal signs up twice, withdraws one.
	save(fullEv, 0, "Kim Visitor", store.PersonInput{Name: "Kim Visitor", Allergies: "KIMS PEANUTS"})
	save(pubEv, 0, "Kim Visitor", store.PersonInput{Name: "Kim Visitor", Allergies: "KIMS PEANUTS"})
	save(pubEv, ids["hal"], "Hal", store.PersonInput{Name: "Hal Fox", Allergies: "HALS SHELLFISH"}, store.PersonInput{Name: "Sam Fox"})
	gone := save(privEv, ids["hal"], "Hal", store.PersonInput{Name: "Hal Fox"})
	if err := st.WithdrawSignup(gone); err != nil {
		t.Fatal(err)
	}

	// Open sign-ups, per role, against /signup/{code} on the web.
	codes := map[string]*store.SignupForm{"Pub Supper": pubF, "Priv Lunch": privF, "Locked Camp": lockF, "Full Picnic": fullF}
	for _, who := range []string{"hal", "ann", "root"} {
		body, _ := meGet(t, c, "/api/v1/me/signups/open", "umd_"+who, 200)
		for _, secret := range []string{"Kim", "PEANUTS", "SHELLFISH", "SECRET", "Draft Thing", "Closed Thing"} {
			if strings.Contains(body, secret) {
				t.Errorf("%s: open sign-ups have %q: %s", who, secret, body)
			}
		}
		var out struct{ Events []meOpenJSON }
		json.Unmarshal([]byte(body), &out)
		got := map[string]meOpenJSON{}
		for _, e := range out.Events {
			got[e.Title] = e
		}
		wb := web.as(who)
		for title, fm := range codes {
			status := 404
			if _, ok := got[title]; ok {
				status = 200
			}
			wb.get("/signup/"+fm.Code, status) // fails the test if the web differs
		}
		if who == "root" && len(out.Events) != 4 || who != "root" && len(out.Events) != 3 {
			t.Errorf("%s: open = %s", who, body)
		}
		if e := got["Pub Supper"]; e.Form.State != "open" || e.Form.ClosesAt != "2099-11-08" || e.Form.Label != "Supper RSVP" ||
			!strings.HasSuffix(e.Form.URL, "/signup/"+pubF.Code) || e.EventID != pubEv || e.Rooms[0] != "Hall" ||
			e.SignedUp != map[string]int{"hal": 1}[who] {
			t.Errorf("%s: supper = %+v", who, e)
		}
		if e := got["Full Picnic"]; e.Form.State != "full" || e.Form.ClosesAt != "2099-11-10" {
			t.Errorf("%s: picnic = %+v", who, e)
		}
		if e := got["Priv Lunch"]; e.SignedUp != 0 {
			t.Errorf("%s: withdrawn counted as signed up: %+v", who, e)
		}
	}

	// My sign-ups: Hal's two (withdrawn too), names only.
	body, _ := meGet(t, c, "/api/v1/me/signups", "umd_hal", 200)
	for _, secret := range []string{"Kim", "PEANUTS", "SHELLFISH", "555-0199", "allerg"} {
		if strings.Contains(body, secret) {
			t.Errorf("my sign-ups have %q: %s", secret, body)
		}
	}
	var mine struct{ Signups []meSignupJSON }
	json.Unmarshal([]byte(body), &mine)
	if len(mine.Signups) != 2 {
		t.Fatalf("hal's sign-ups = %s", body)
	}
	byTitle := map[string]meSignupJSON{}
	for _, s := range mine.Signups {
		byTitle[s.EventTitle] = s
	}
	sup, lun := byTitle["Pub Supper"], byTitle["Priv Lunch"]
	if sup.Status != "in" || !sup.CanChange || sup.Guests != 2 || strings.Join(sup.People, ",") != "Hal Fox,Sam Fox" ||
		sup.FormLabel != "Supper RSVP" || sup.EventID != pubEv || sup.StartsOn != "2099-11-10" || sup.StartTime != "18:00" {
		t.Errorf("supper = %+v", sup)
	}
	if lun.Status != "withdrawn" || lun.CanChange || lun.FormLabel != "Sign up" {
		t.Errorf("lunch = %+v", lun)
	}
	page := web.as("hal").get("/my/signups", 200)
	for title := range byTitle {
		if !strings.Contains(page, title) {
			t.Errorf("%q is in /me/signups but not on /my/signups", title)
		}
	}
	// Someone else's are not there; nothing yet is [].
	for _, who := range []string{"ann", "root"} {
		if body, _ := meGet(t, c, "/api/v1/me/signups", "umd_"+who, 200); !strings.Contains(body, `"signups": []`) {
			t.Errorf("%s: %s", who, body)
		}
	}
}

// TestMeFieldNames checks the JSON keys against APP_CONTRACTS.md's
// examples for the event planner (the MCP server's tools read these).
func TestMeFieldNames(t *testing.T) {
	f := newFakeTokenUM(t)
	_, st, c := meServer(t, f, nil)
	ids := meUsers(t, f, st)
	ev := apiEvent(t, st, "Supper", "public", false, "planning", "2099-11-10", "18:00", "Hall")
	apiEvent(t, st, "Hidden", "private", true, "planning", "2099-11-10", "09:00", "Office")
	fm, _ := st.CreateSignupForm(ev)
	fm.Status = "open"
	st.UpdateSignupForm(fm)
	if _, err := st.SaveSignup(store.SignupInput{EventID: ev, UserID: ids["hal"], Name: "Hal"}, store.SignupOptions{}); err != nil {
		t.Fatal(err)
	}

	keysOf := func(m map[string]any) string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return strings.Join(ks, ",")
	}
	hasKeys := func(what string, m map[string]any, want string) {
		t.Helper()
		for _, k := range strings.Split(want, ",") {
			if _, ok := m[k]; !ok {
				t.Errorf("%s: no %q in %s", what, k, keysOf(m))
			}
		}
	}
	get := func(path string) map[string]any {
		body, _ := meGet(t, c, path, "umd_hal", 200)
		var m map[string]any
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	events := get("/api/v1/me/events?from=2099-11-10&to=2099-11-10")["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("events = %v", events)
	}
	busy, full := events[0].(map[string]any), events[1].(map[string]any)
	hasKeys("busy event", busy, "busy,starts_on,start_time,ends_on,end_time,rooms")
	for _, k := range []string{"id", "title", "description", "kind", "url"} {
		if _, ok := busy[k]; ok {
			t.Errorf("busy event has %q", k)
		}
	}
	hasKeys("event", full, "id,busy,title,kind,status,starts_on,start_time,ends_on,end_time,tentative,rooms,description,url")

	opens := get("/api/v1/me/signups/open")["events"].([]any)
	if len(opens) != 1 {
		t.Fatalf("open = %v", opens)
	}
	o := opens[0].(map[string]any)
	hasKeys("open sign-up", o, "event_id,title,starts_on,start_time,rooms,public_description,form,signed_up")
	hasKeys("form", o["form"].(map[string]any), "code,label,state,closes_at,url")

	sus := get("/api/v1/me/signups")["signups"].([]any)
	if len(sus) != 1 {
		t.Fatalf("signups = %v", sus)
	}
	s := sus[0].(map[string]any)
	hasKeys("sign-up", s, "id,event_id,event_title,starts_on,start_time,form_label,status,people,guests,can_change")
	if p, ok := s["people"].([]any); !ok || len(p) != 1 || p[0] != "Hal" {
		t.Errorf("people = %v", s["people"])
	}
}
