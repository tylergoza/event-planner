package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tylergoza/event-planner/internal/store"
)

func apiEvent(t *testing.T, st *store.Store, title, visibility string, locked bool, status, startsOn, startTime string, rooms ...string) int64 {
	t.Helper()
	res, err := st.DB.Exec(`INSERT INTO events (title, kind, status, starts_on, start_time, visibility, public_description, locked, notes, coordinator)
		VALUES (?, 'Dinner', ?, ?, ?, ?, 'Come, hungry; bring a dish', ?, 'SECRET NOTE', 'SECRET COORD')`,
		title, status, startsOn, startTime, visibility, locked)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	for i, r := range rooms {
		if _, err := st.DB.Exec(`INSERT INTO event_rooms (event_id, position, place_name) VALUES (?, ?, ?)`, id, i+1, r); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// apiGet requests path with an optional bearer token, no cookies.
func apiGet(t *testing.T, c *client, path, token string, want int) (string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest("GET", c.base+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("GET %s: status %d, want %d\n%s", path, resp.StatusCode, want, body)
	}
	return string(body), resp.Header
}

type apiList struct {
	Events []map[string]any `json:"events"`
}

func apiEvents(t *testing.T, c *client, path, token string) []map[string]any {
	t.Helper()
	body, _ := apiGet(t, c, path, token, 200)
	var l apiList
	if err := json.Unmarshal([]byte(body), &l); err != nil {
		t.Fatal(err, body)
	}
	return l.Events
}

const apiRangeQ = "?from=2099-01-01&to=2099-12-31"

func TestAPIEvents(t *testing.T) {
	c, st := newTestServer(t)
	pub := apiEvent(t, st, "Harvest Dinner", "public", false, "planning", "2099-11-01", "18:00", "Lifecenter", "Kitchen")
	priv := apiEvent(t, st, "Elders Meeting", "private", false, "planning", "2099-11-02", "09:00", "Library")
	apiEvent(t, st, "Locked Thing", "private", true, "planning", "2099-11-03", "10:00", "Office")
	apiEvent(t, st, "Cancelled", "public", false, "cancelled", "2099-11-04", "10:00")
	pubTok, _ := st.CreateToken("site", false, 0)
	privTok, _ := st.CreateToken("app", true, 0)

	apiGet(t, c, "/api/v1/events"+apiRangeQ, "", 401)
	body, hdr := apiGet(t, c, "/api/v1/events"+apiRangeQ, "ep_bogus", 401)
	if !strings.Contains(body, `"error"`) || hdr.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("401 body/CORS: %s %v", body, hdr)
	}
	apiGet(t, c, "/api/v1/events"+apiRangeQ+"&token="+pubTok, "", 401) // ?token= is only for .ics

	// Public token.
	list := apiEvents(t, c, "/api/v1/events"+apiRangeQ, pubTok)
	if len(list) != 3 {
		t.Fatalf("public token: %v", list)
	}
	if list[0]["title"] != "Harvest Dinner" || list[0]["busy"] != false || list[0]["public"] != true ||
		list[0]["description"] != "Come, hungry; bring a dish" || !strings.HasSuffix(list[0]["url"].(string), "/calendar/1") {
		t.Errorf("full entry: %v", list[0])
	}
	for _, i := range []int{1, 2} {
		e := list[i]
		if e["busy"] != true || e["title"] != nil || e["id"] != nil || e["url"] != nil || e["description"] != nil {
			t.Errorf("busy entry leaks: %v", e)
		}
		if len(e["rooms"].([]any)) != 1 {
			t.Errorf("busy entry keeps rooms: %v", e)
		}
	}
	raw, _ := apiGet(t, c, "/api/v1/events"+apiRangeQ, pubTok, 200)
	for _, secret := range []string{"Elders", "Locked Thing", "SECRET", "Cancelled"} {
		if strings.Contains(raw, secret) {
			t.Errorf("public token response contains %q", secret)
		}
	}
	_, hdr = apiGet(t, c, "/api/v1/events"+apiRangeQ, pubTok, 200)
	if hdr.Get("Cache-Control") != "private, no-store" || hdr.Get("Set-Cookie") != "" {
		t.Errorf("headers: %v", hdr)
	}

	// One event.
	body, _ = apiGet(t, c, "/api/v1/events/"+itoa(pub), pubTok, 200)
	if !strings.Contains(body, "Harvest Dinner") || strings.Contains(body, "SECRET") {
		t.Error(body)
	}
	apiGet(t, c, "/api/v1/events/"+itoa(priv), pubTok, 404)
	apiGet(t, c, "/api/v1/events/999", pubTok, 404)
	apiGet(t, c, "/api/v1/events/"+itoa(priv), "", 401)

	// Private token: private in full, locked still busy.
	list = apiEvents(t, c, "/api/v1/events"+apiRangeQ, privTok)
	if len(list) != 3 || list[1]["title"] != "Elders Meeting" || list[1]["public"] != false || list[2]["busy"] != true || list[2]["title"] != nil {
		t.Errorf("private token: %v", list)
	}
	apiGet(t, c, "/api/v1/events/"+itoa(priv), privTok, 200)
	apiGet(t, c, "/api/v1/events/3", privTok, 404)
	raw, _ = apiGet(t, c, "/api/v1/events"+apiRangeQ, privTok, 200)
	if strings.Contains(raw, "Locked Thing") || strings.Contains(raw, "SECRET") {
		t.Error("private token sees locked or internal details")
	}

	// last_used_at is recorded.
	var used string
	st.DB.QueryRow(`SELECT COALESCE(last_used_at, '') FROM api_tokens WHERE id = 1`).Scan(&used)
	if used == "" {
		t.Error("last_used_at not set")
	}

	// Unknown endpoints are JSON 404s.
	body, _ = apiGet(t, c, "/api/v1/nope", pubTok, 404)
	if !strings.Contains(body, `"error"`) {
		t.Error(body)
	}
}

func TestAPIRange(t *testing.T) {
	c, st := newTestServer(t)
	tok, _ := st.CreateToken("site", false, 0)
	for _, q := range []string{
		"?from=soon", "?from=2099-01-01&to=nope", "?from=2099-02-01&to=2099-01-01",
		"?from=2099-01-01&to=2100-03-01", // over 400 days
	} {
		body, _ := apiGet(t, c, "/api/v1/events"+q, tok, 400)
		if !strings.Contains(body, `"error"`) {
			t.Errorf("%s: %s", q, body)
		}
	}
	apiGet(t, c, "/api/v1/events?from=2099-01-01&to=2100-02-05", tok, 200) // exactly 400 days

	// Defaults: today to 90 days on.
	today := time.Now().Format(store.DateLayout)
	in := time.Now().AddDate(0, 0, 89).Format(store.DateLayout)
	out := time.Now().AddDate(0, 0, 95).Format(store.DateLayout)
	apiEvent(t, st, "Soon", "public", false, "planning", today, "")
	apiEvent(t, st, "Later", "public", false, "planning", in, "")
	apiEvent(t, st, "TooFar", "public", false, "planning", out, "")
	if got := apiEvents(t, c, "/api/v1/events", tok); len(got) != 2 {
		t.Errorf("default range: %v", got)
	}
	// Multi-day overlap.
	st.DB.Exec(`INSERT INTO events (title, status, starts_on, ends_on, visibility) VALUES ('Camp', 'planning', '2098-05-01', '2098-05-10', 'public')`)
	if got := apiEvents(t, c, "/api/v1/events?from=2098-05-05&to=2098-05-06", tok); len(got) != 1 || got[0]["ends_on"] != "2098-05-10" {
		t.Errorf("overlap: %v", got)
	}
}

func TestAPICORS(t *testing.T) {
	c, _ := newTestServer(t)
	for _, p := range []string{"/api/v1/events", "/calendar.ics"} {
		req, _ := http.NewRequest("OPTIONS", c.base+p, nil)
		req.Header.Set("Origin", "https://example.org")
		req.Header.Set("Access-Control-Request-Method", "GET")
		req.Header.Set("Access-Control-Request-Headers", "authorization")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		h := resp.Header
		if resp.StatusCode != 204 || h.Get("Access-Control-Allow-Origin") != "*" ||
			!strings.Contains(h.Get("Access-Control-Allow-Headers"), "Authorization") || !strings.Contains(h.Get("Access-Control-Allow-Methods"), "GET") {
			t.Errorf("OPTIONS %s: %d %v", p, resp.StatusCode, h)
		}
	}
	_, hdr := apiGet(t, c, "/calendar.ics", "", 200)
	if hdr.Get("Access-Control-Allow-Origin") != "*" || hdr.Get("Set-Cookie") != "" {
		t.Errorf("calendar.ics headers: %v", hdr)
	}
}

func unfold(s string) string { return strings.ReplaceAll(s, "\r\n ", "") }

func TestICS(t *testing.T) {
	c, st := newTestServer(t)
	long := strings.Repeat("Long name é ", 12)
	apiEvent(t, st, long, "public", false, "planning", "2099-11-01", "18:00", "Lifecenter", "Kitchen")
	st.DB.Exec(`UPDATE events SET end_time = '20:00' WHERE id = 1`)
	apiEvent(t, st, "Work day", "public", false, "planning", "2099-11-02", "")
	apiEvent(t, st, "Elders", "private", false, "planning", "2099-11-03", "09:00", "Library")
	apiEvent(t, st, "Mystery", "private", false, "planning", "2099-11-04", "")
	tok, _ := st.CreateToken("cal", true, 0)

	// Anonymous feed: no session, no private titles.
	body, hdr := apiGet(t, c, "/calendar.ics"+apiRangeQ, "", 200)
	if !strings.HasPrefix(hdr.Get("Content-Type"), "text/calendar") {
		t.Errorf("content type %q", hdr.Get("Content-Type"))
	}
	for _, secret := range []string{"Elders", "Mystery", "SECRET"} {
		if strings.Contains(body, secret) {
			t.Errorf("public feed shows %q", secret)
		}
	}
	if !strings.HasPrefix(body, "BEGIN:VCALENDAR\r\n") || !strings.HasSuffix(body, "END:VCALENDAR\r\n") ||
		strings.Count(body, "BEGIN:VEVENT") != 4 || strings.Count(body, "END:VEVENT") != 4 {
		t.Fatalf("structure:\n%s", body)
	}
	for _, l := range strings.Split(strings.TrimSuffix(body, "\r\n"), "\r\n") {
		if len(l) > 75 {
			t.Errorf("line over 75 octets (%d): %q", len(l), l)
		}
	}
	text := unfold(body)
	for _, want := range []string{
		"SUMMARY:" + strings.TrimSpace(long), // folded line rejoined
		`LOCATION:Lifecenter\, Kitchen`,
		`DESCRIPTION:Come\, hungry\; bring a dish`,
		"UID:event-1@",
		"URL:http://",
		"SUMMARY:Busy: Library", "TRANSP:OPAQUE", "SUMMARY:Private event",
		"DTSTART;VALUE=DATE:20991102", "DTEND;VALUE=DATE:20991103", // all-day, exclusive end
	} {
		if !strings.Contains(text, want) {
			t.Errorf("feed missing %q", want)
		}
	}
	// Timed events are UTC.
	local, _ := time.ParseInLocation("2006-01-02 15:04", "2099-11-01 18:00", time.Local)
	if want := "DTSTART:" + local.UTC().Format("20060102T150405Z"); !strings.Contains(text, want) {
		t.Errorf("missing %q", want)
	}
	if !strings.Contains(text, "DTEND:") {
		t.Error("timed event with end time needs DTEND")
	}
	// A busy VEVENT has no description.
	for _, ev := range strings.Split(text, "BEGIN:VEVENT")[1:] {
		if strings.Contains(ev, "TRANSP:OPAQUE") && (strings.Contains(ev, "DESCRIPTION") || strings.Contains(ev, "LOCATION")) {
			t.Errorf("busy event has details:\n%s", ev)
		}
	}

	// Token feed: Bearer or ?token=, private in full.
	for _, withQuery := range []bool{false, true} {
		path, bearer := "/api/v1/events.ics"+apiRangeQ, tok
		if withQuery {
			path, bearer = path+"&token="+tok, ""
		}
		body, hdr := apiGet(t, c, path, bearer, 200)
		if !strings.Contains(unfold(body), "SUMMARY:Elders") || hdr.Get("Cache-Control") != "private, no-store" {
			t.Errorf("token feed (query=%v): %v\n%s", withQuery, hdr, body)
		}
	}
	apiGet(t, c, "/api/v1/events.ics"+apiRangeQ, "", 401)
	apiGet(t, c, "/api/v1/events.ics"+apiRangeQ+"&token=ep_bad", "", 401)
	apiGet(t, c, "/calendar.ics?from=bad", "", 400)
}

func TestFoldAndEscapeICS(t *testing.T) {
	if got := escapeICS("a,b;c\\d\r\ne\nf"); got != `a\,b\;c\\d\ne\nf` {
		t.Errorf("escape: %q", got)
	}
	s := "SUMMARY:" + strings.Repeat("é", 100)
	folded := foldICS(s)
	for _, l := range strings.Split(strings.TrimSuffix(folded, "\r\n"), "\r\n") {
		if len(l) > 75 {
			t.Errorf("line of %d octets", len(l))
		}
	}
	if unfold(strings.TrimSuffix(folded, "\r\n")) != s {
		t.Error("folding changed the text")
	}
}

func TestTokenPages(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	st.CreateUser("ann", "", "a long password", store.RoleHelper)
	st.CreateUser("pat", "", "a long password", store.RolePlanner)
	boss := c.as("boss")

	c.as("ann").get("/admin/tokens", 403)
	c.as("pat").get("/admin/tokens", 403)
	if body := boss.get("/admin/tokens", 200); !strings.Contains(body, "Can see private events") || !strings.Contains(body, "never locked events") {
		t.Error("tokens page")
	}

	body := boss.post("/admin/tokens", "/admin/tokens", url.Values{"name": {"Church website"}}, 200)
	m := regexpToken.FindString(body)
	if m == "" || !strings.Contains(body, "won't be shown again") {
		t.Fatalf("new token not shown:\n%s", body)
	}
	if tok, err := st.AuthenticateToken(m); err != nil || tok.IncludePrivate {
		t.Fatalf("%+v %v", tok, err)
	}
	// Not shown again.
	if body := boss.get("/admin/tokens", 200); strings.Contains(body, m) || !strings.Contains(body, m[:9]) {
		t.Error("token should be shown once, prefix after")
	}
	boss.post("/admin/tokens", "/admin/tokens", url.Values{"name": {"App"}, "include_private": {"1"}}, 200)
	if list, _ := st.ListTokens(); len(list) != 2 || !list[0].IncludePrivate && !list[1].IncludePrivate {
		t.Fatalf("%+v", list)
	}
	boss.post("/admin/tokens", "/admin/tokens", url.Values{"name": {""}}, 422)

	list, _ := st.ListTokens()
	id := list[0].ID // App
	boss.post("/admin/tokens", "/admin/tokens/"+itoa(id)+"/private", url.Values{"private": {"0"}}, 200)
	if l, _ := st.ListTokens(); l[0].IncludePrivate {
		t.Error("toggle did not turn private off")
	}
	boss.post("/admin/tokens", "/admin/tokens/"+itoa(id)+"/private", url.Values{"private": {"1"}}, 200)
	if l, _ := st.ListTokens(); !l[0].IncludePrivate {
		t.Error("toggle did not turn private on")
	}
	c.as("ann").post("/", "/admin/tokens/"+itoa(id)+"/delete", url.Values{}, 403)
	boss.post("/admin/tokens", "/admin/tokens/"+itoa(id)+"/delete", url.Values{}, 200)
	if l, _ := st.ListTokens(); len(l) != 1 {
		t.Errorf("after delete: %+v", l)
	}
}

var regexpToken = regexp.MustCompile(`ep_[A-Za-z0-9_-]+`)
