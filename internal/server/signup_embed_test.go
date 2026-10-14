package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tylergoza/event-planner/internal/store"
)

func hdr(t *testing.T, c *client, path string) (http.Header, string) {
	t.Helper()
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.Header, string(b)
}

func TestSignupEmbedFramePolicy(t *testing.T) {
	boss, st := connAdmin(t, nil)
	_, f := puForm(t, st, "Cook-off", nil)
	page := "/signup/" + f.Code + "?embed=1"

	h, body := hdr(t, boss, page)
	if !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") || h.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("default not locked: %q %q", h.Get("Content-Security-Policy"), h.Get("X-Frame-Options"))
	}
	if strings.Contains(body, `class="topbar"`) || strings.Contains(body, `class="footer`) || !strings.Contains(body, `name="embed" value="1"`) {
		t.Error("embed page has chrome or no hidden embed field")
	}
	if _, plain := hdr(t, boss, "/signup/"+f.Code); !strings.Contains(plain, `class="topbar"`) || strings.Contains(plain, `name="embed"`) {
		t.Error("normal page lost its chrome")
	}

	boss.post("/admin/settings", "/admin/settings", url.Values{"site_name": {"Events"},
		"signup_embed_origins": {"https://Church.example\nhttp://localhost:3000/\nhttps://church.example"}}, 200)
	if got := st.Setting("signup_embed_origins", ""); got != "https://church.example\nhttp://localhost:3000" {
		t.Fatalf("saved %q", got)
	}
	for _, p := range []string{page, "/signup/" + f.Code} {
		h, _ = hdr(t, boss, p)
		csp := h.Get("Content-Security-Policy")
		if !strings.Contains(csp, "frame-ancestors https://church.example http://localhost:3000") || strings.Contains(csp, "'none'") || h.Get("X-Frame-Options") != "" {
			t.Errorf("%s: csp %q xfo %q", p, csp, h.Get("X-Frame-Options"))
		}
	}
	for _, p := range []string{"/events", "/calendar", "/admin/settings"} {
		h, _ = hdr(t, boss, p)
		if !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") || h.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s changed: %q", p, h.Get("Content-Security-Policy"))
		}
	}
}

func TestSignupEmbedKeptThroughPost(t *testing.T) {
	c, st := newTestServer(t)
	_, f := puForm(t, st, "Cook-off", nil)
	page := "/signup/" + f.Code + "?embed=1"
	code, body := puPost(t, c, page, "/signup/"+f.Code, url.Values{"embed": {"1"}, "name": {"Pat"}})
	if code != 422 || !strings.Contains(body, `name="embed" value="1"`) || strings.Contains(body, `class="topbar"`) {
		t.Fatalf("error page lost embed: %d", code)
	}
	code, body = puPost(t, c, page, "/signup/"+f.Code, url.Values{"embed": {"1"}, "name": {"Pat"}, "phone": {"555-1234"}})
	if code != 200 || !strings.Contains(body, "Thank you") || strings.Contains(body, `class="topbar"`) || !strings.Contains(body, "?embed=1") {
		t.Fatalf("thanks page: %d", code)
	}
}

func TestSignupEmbedSettingRejectsBadOrigins(t *testing.T) {
	boss, st := connAdmin(t, nil)
	for _, bad := range []string{"church.example", "https://church.example/path", "ftp://church.example", "https://", "https://a.example?x=1", "https://u@a.example", "*"} {
		code := postStatus(t, boss, url.Values{"site_name": {"Events"}, "signup_embed_origins": {bad}})
		if code != 422 {
			t.Errorf("%q accepted (%d)", bad, code)
		}
	}
	if st.Setting("signup_embed_origins", "") != "" {
		t.Error("a bad value was saved")
	}
}

func postStatus(t *testing.T, c *client, v url.Values) int {
	t.Helper()
	m := csrfRe.FindStringSubmatch(c.get("/admin/settings", 200))
	v.Set("_csrf", m[1])
	resp, err := c.http.PostForm(c.base+"/admin/settings", v)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// bookingFor builds the push payload for an event on a server that knows its address.
func bookingJSON(t *testing.T, st *store.Store, id int64) map[string]any {
	t.Helper()
	srv, err := New(Config{Env: map[string]string{"BASE_URL": "https://events.example.org"}}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(srv.calendarBooking(mustEvent(t, st, id), nil))
	var m map[string]any
	json.Unmarshal(raw, &m)
	return m
}

func TestBookingCarriesSignup(t *testing.T) {
	_, st := newTestServer(t)

	// Draft: left out.
	ev, f := puForm(t, st, "Cook-off", func(f *store.SignupForm) {
		f.Status = "draft"
		f.ClosesAt = "2099-10-30T12:00"
		f.Label = "Join the cook-off"
	})
	if _, ok := bookingJSON(t, st, ev)["signup"]; ok {
		t.Error("draft form is sent")
	}
	f.Status = "open"
	st.UpdateSignupForm(f)
	m := bookingJSON(t, st, ev)
	want := map[string]any{"label": "Join the cook-off", "url": "https://events.example.org/signup/" + f.Code, "state": "open", "closes_at": "2099-10-30T12:00"}
	got, _ := m["signup"].(map[string]any)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("signup.%s = %v, want %v", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("extra keys: %v", got)
	}

	// Closed after closes_at.
	f.ClosesAt = "2000-01-01T00:00"
	st.UpdateSignupForm(f)
	if got, _ := bookingJSON(t, st, ev)["signup"].(map[string]any); got["state"] != "closed" {
		t.Errorf("past closes_at: %v", got)
	}
	f.ClosesAt = ""
	f.Status = "closed"
	st.UpdateSignupForm(f)
	if got, _ := bookingJSON(t, st, ev)["signup"].(map[string]any); got["state"] != "closed" || got["closes_at"] != nil {
		t.Errorf("closed form: %v", got)
	}

	// Locked: left out.
	f.Status = "open"
	st.UpdateSignupForm(f)
	st.DB.Exec(`UPDATE events SET locked = 1 WHERE id = ?`, ev)
	if _, ok := bookingJSON(t, st, ev)["signup"]; ok {
		t.Error("locked event sends the sign-up")
	}

	// No form: left out.
	other := apiEvent(t, st, "Dinner", "public", false, "planning", "2099-11-01", "18:00")
	if _, ok := bookingJSON(t, st, other)["signup"]; ok {
		t.Error("event without a form sends one")
	}
}

func TestBookingSignupNeedsAddress(t *testing.T) {
	_, st := newTestServer(t)
	ev, _ := puForm(t, st, "Cook-off", nil)
	srv, _ := newConnServer(t, nil)
	if b := srv.calendarBooking(mustEvent(t, st, ev), nil); b.Signup != nil {
		t.Error("sent a link with no public address")
	}
}

func epOutboxRows(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM calendar_outbox`).Scan(&n)
	return n
}

func TestSignupOutboxOnlyOnStateChange(t *testing.T) {
	c, st := newTestServer(t)
	ev, f := puForm(t, st, "Cook-off", nil)
	q := puQ(t, st, ev, store.SignupQuestionInput{Kind: "one", Label: "Bring", Required: true})
	chili := puC(t, st, q, "Chili", 2)
	page := "/signup/" + f.Code
	clear := func() { st.DB.Exec(`DELETE FROM calendar_outbox`) }
	sign := func(name string) {
		t.Helper()
		if code, body := puPost(t, c, page, page, url.Values{"name": {name}, "phone": {"555-1234"}, "q" + itoa(q): {itoa(chili)}}); code != 200 {
			t.Fatalf("sign-up %d: %s", code, body)
		}
	}

	clear()
	f.Intro = "New words"
	st.UpdateSignupForm(f)
	if epOutboxRows(t, st) != 0 {
		t.Error("an intro change queued the event")
	}
	f.ClosesAt = "2099-10-30T12:00"
	st.UpdateSignupForm(f)
	if epOutboxRows(t, st) != 1 {
		t.Error("closing time change did not queue the event")
	}
	clear()
	f.Status = "closed"
	st.UpdateSignupForm(f)
	if epOutboxRows(t, st) != 1 {
		t.Error("status change did not queue the event")
	}
	f.Status = "open"
	st.UpdateSignupForm(f)

	clear()
	sign("Pat")
	if epOutboxRows(t, st) != 0 {
		t.Error("a plain sign-up queued the event")
	}
	sign("Sam") // takes the last spot: open -> full
	if epOutboxRows(t, st) != 1 {
		t.Fatal("open -> full did not queue the event")
	}
	if got, _ := bookingJSON(t, st, ev)["signup"].(map[string]any); got["state"] != "full" {
		t.Errorf("payload after full: %v", got)
	}
}
