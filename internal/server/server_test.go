package server

import (
	"bufio"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tylergoza/event-planner/internal/store"
)

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func newTestServer(t *testing.T) (*client, *store.Store) {
	t.Helper()
	return newTestServerWith(t, Config{})
}

func newTestServerWith(t *testing.T, cfg Config) (*client, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: ts.URL, http: &http.Client{Jar: jar}}, st
}

func (c *client) get(path string, wantStatus int) string {
	c.t.Helper()
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		c.t.Fatalf("GET %s: status %d, want %d\n%s", path, resp.StatusCode, wantStatus, body)
	}
	return string(body)
}

var csrfRe = regexp.MustCompile(`name="_csrf" value="([^"]+)"`)

// post submits a form, pulling the CSRF token from a page first.
func (c *client) post(tokenPage, path string, form url.Values, wantStatus int) string {
	c.t.Helper()
	m := csrfRe.FindStringSubmatch(c.get(tokenPage, 200))
	if m == nil {
		c.t.Fatalf("no csrf token on %s", tokenPage)
	}
	form.Set("_csrf", m[1])
	resp, err := c.http.PostForm(c.base+path, form)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		c.t.Fatalf("POST %s: status %d, want %d\n%s", path, resp.StatusCode, wantStatus, body)
	}
	return string(body)
}

// fakeTracker answers like the maintenance tracker's API: a product of
// lapel mics, folding chairs, and a supply of batteries.
func fakeTracker(t *testing.T) *httptest.Server {
	t.Helper()
	routes := map[string]string{
		"/api/v1/products?q=mic":    `{"products":[{"id":5,"name":"Lapel mics","category":"Audio","total":2,"item_count":1,"place_count":1}]}`,
		"/api/v1/products?q=chairs": `{"products":[{"id":9,"name":"Folding chairs","category":"Furniture","counted":true,"total":100,"item_count":2,"place_count":2}]}`,
		"/api/v1/products": `{"products":[{"id":5,"name":"Lapel mics","total":2,"item_count":1,"place_count":1},
			{"id":9,"name":"Folding chairs","counted":true,"total":100,"item_count":2,"place_count":2}]}`,
		"/api/v1/products/5":        `{"id":5,"name":"Lapel mics","total":2,"items":[{"id":7,"product_id":5,"name":"Lapel mics","quantity":2,"place":{"id":3,"path":"Sanctuary › Sound booth"}}]}`,
		"/api/v1/items/7":           `{"id":7,"product_id":5,"name":"Lapel mics","quantity":2,"place":{"id":3,"path":"Sanctuary › Sound booth"},"units":[{"number":1,"id":"L1"},{"number":2,"id":"L2"}]}`,
		"/api/v1/supplies?q=chairs": `{"supplies":[]}`,
		"/api/v1/supplies":          `{"supplies":[{"id":4,"name":"AA batteries","unit":"batteries","place":{"id":3,"path":"Sanctuary › Sound booth"},"on_hand":6,"total":6,"reorder_at":12,"stock":"low","requested":null}]}`,
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer mt_test" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"bad token"}`)
			return
		}
		body, ok := routes[r.URL.RequestURI()]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"not found"}`)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestEndToEnd(t *testing.T) {
	c, st := newTestServer(t)
	tracker := fakeTracker(t)

	// No users yet: everything leads to setup.
	if body := c.get("/login", 200); !strings.Contains(body, "Create the first administrator") {
		t.Fatal("expected setup page")
	}
	c.post("/setup", "/setup", url.Values{
		"site_name": {"Grace Church Events"}, "username": {"coordinator"}, "display_name": {"Pat"},
		"password": {"a long password"}, "password_confirm": {"a long password"},
	}, 200)

	// Connect the tracker; saving checks it answers.
	if body := c.post("/admin/connections", "/admin/connections/tracker", url.Values{
		"tracker_url": {tracker.URL + "/"}, "tracker_token": {"mt_test"},
	}, 200); !strings.Contains(body, "Connected to the maintenance tracker") {
		t.Fatal("expected the tracker check to pass")
	}
	if body := c.post("/admin/connections", "/admin/connections/tracker", url.Values{"tracker_token": {"nope"}}, 422); !strings.Contains(body, "start with mt_") {
		t.Error("expected token format error")
	}

	// An event, with a schedule.
	c.post("/events/new", "/events", url.Values{"title": {"Fall Dinner"}, "status": {"planning"}, "starts_on": {"2099-11-01"}, "start_time": {"17:30"}, "coordinator": {"Pat"}, "expected_count": {"80"}}, 200)
	if body := c.post("/events/new", "/events", url.Values{"title": {""}, "status": {"planning"}, "starts_on": {"2099-11-01"}}, 422); !strings.Contains(body, "Give the event a title") {
		t.Error("expected title error")
	}
	if body := c.post("/events/new", "/events", url.Values{"title": {"X"}, "status": {"planning"}, "starts_on": {"2099-11-02"}, "ends_on": {"2099-11-01"}}, 422); !strings.Contains(body, "can&#39;t end before it starts") {
		t.Error("expected end date error")
	}
	if ev, _ := st.GetEvent(1); ev.ExpectedCount != 80 || ev.Coordinator != "Pat" {
		t.Fatalf("event: %+v", ev)
	}
	if body := c.get("/events/1/schedule", 200); !strings.Contains(body, `value="2099-11-01"`) {
		t.Error("the schedule form should default to the event's first day")
	}
	c.post("/events/1/schedule", "/events/1/schedule", url.Values{"label": {"Doors open"}, "date": {"2099-11-01"}, "time": {"17:30"}}, 200)
	c.post("/events/1/schedule", "/events/1/schedule", url.Values{"label": {"Set up"}, "date": {"2099-11-01"}, "time": {"15:00"}, "end_time": {"17:00"}}, 200)
	if body := c.post("/events/1/schedule", "/events/1/schedule", url.Values{"label": {"Bad"}, "date": {"soon"}}, 422); !strings.Contains(body, "Pick a date") {
		t.Error("expected date error")
	}
	if body := c.post("/events/1/schedule", "/events/1/schedule", url.Values{"label": {"Bad"}, "date": {"2099-11-01"}, "room_id": {"77"}}, 422); !strings.Contains(body, "isn&#39;t part of this event") {
		t.Error("expected room error")
	}
	if items, _ := st.ListSchedule(1); len(items) != 2 || items[0].Label != "Set up" {
		t.Fatalf("schedule: %+v", items)
	}
	c.post("/schedule/1/edit", "/schedule/1", url.Values{"label": {"Doors open!"}, "date": {"2099-11-01"}}, 200)
	if it, _ := st.GetScheduleItem(1); it.Label != "Doors open!" || it.Time != "" {
		t.Errorf("schedule edit: %+v", it)
	}

	// Task lists and tasks.
	c.post("/events/1/tasks", "/events/1/task-lists", url.Values{"name": {"Before"}}, 200)
	c.post("/events/1/tasks", "/events/1/task-lists", url.Values{"name": {"Kitchen"}}, 200)
	c.post("/events/1/tasks", "/task-lists/2/move", url.Values{"dir": {"up"}, "next": {"/events/1/tasks"}}, 200)
	if lists, _ := st.ListTaskLists(1); lists[0].Name != "Kitchen" {
		t.Error("list should have moved up")
	}
	c.post("/events/1/tasks", "/task-lists/2", url.Values{"name": {"The Kitchen"}}, 200)
	c.post("/events/1/tasks", "/events/1/tasks", url.Values{"list_id": {"1"}, "title": {"Book the hall"}}, 200)
	c.post("/events/1/tasks", "/events/1/tasks/paste", url.Values{"list_id": {"2"}, "text": {"Order food\n\nBorrow urns\n"}}, 200)
	c.post("/events/1/tasks", "/events/1/tasks", url.Values{"title": {"Thank-you notes"}}, 200)
	tasks, _ := st.ListTasks(1)
	if len(tasks) != 4 || tasks[2].Title != "Borrow urns" || tasks[2].ListID != 2 || tasks[3].ListID != 0 {
		t.Fatalf("tasks: %+v", tasks)
	}
	body := c.get("/events/1/tasks", 200)
	for _, want := range []string{"Before", "The Kitchen", "Other", "Paste many", "Book the hall"} {
		if !strings.Contains(body, want) {
			t.Errorf("tasks page is missing %q", want)
		}
	}
	// Status from the list: done gets a time, and the user is recorded.
	c.post("/events/1/tasks", "/tasks/1/status", url.Values{"status": {"done"}, "next": {"/events/1/tasks"}}, 200)
	if task, _ := st.GetTask(1); task.Status != "done" || task.DoneAt == "" || task.UpdatedBy != "Pat" {
		t.Errorf("task status: %+v", task)
	}
	c.post("/events/1/tasks", "/tasks/1/status", url.Values{"status": {"nonsense"}}, 200)
	if task, _ := st.GetTask(1); task.Status != "done" {
		t.Error("a bad status should be refused")
	}
	// Edit: a person or a name, never both; overdue shows.
	user, _ := st.GetUser(1)
	if body := c.post("/tasks/2/edit", "/tasks/2", url.Values{"title": {"Order food"}, "status": {"todo"}, "assignee_id": {itoa(user.ID)}, "assignee_name": {"Youth group"}}, 422); !strings.Contains(body, "not both") {
		t.Error("expected the assignee refusal")
	}
	c.post("/tasks/2/edit", "/tasks/2", url.Values{"title": {"Order food"}, "status": {"blocked"}, "assignee_id": {itoa(user.ID)}, "due_on": {"2000-01-02"}, "list_id": {"2"}}, 200)
	c.post("/tasks/3/edit", "/tasks/3", url.Values{"title": {"Borrow urns"}, "status": {"todo"}, "assignee_name": {"Youth group"}, "list_id": {"2"}}, 200)
	if task, _ := st.GetTask(2); task.AssigneeID != user.ID || task.AssigneeName != "" || task.Status != "blocked" {
		t.Errorf("task edit: %+v", task)
	}
	if task, _ := st.GetTask(3); task.AssigneeID != 0 || task.AssigneeName != "Youth group" {
		t.Errorf("task edit by name: %+v", task)
	}
	if body := c.post("/tasks/2/edit", "/tasks/2", url.Values{"title": {"Order food"}, "status": {"todo"}, "list_id": {"99"}}, 422); !strings.Contains(body, "That list isn&#39;t part of this event") {
		t.Error("expected the list refusal")
	}
	if body := c.get("/", 200); !strings.Contains(body, "My tasks") || !strings.Contains(body, "Order food") || !strings.Contains(body, "overdue") {
		t.Error("the dashboard should list my overdue task")
	}
	if body := c.get("/events/1", 200); !strings.Contains(body, "Needs attention") || !strings.Contains(body, "was due") {
		t.Error("overview should flag overdue and blocked tasks")
	}
	// Filters.
	if body := c.get("/events/1/tasks?filter=mine", 200); !strings.Contains(body, "Order food") || strings.Contains(body, "Borrow urns") {
		t.Error("the Mine filter should show only my tasks")
	}
	if body := c.get("/events/1/tasks?filter=open", 200); strings.Contains(body, "Book the hall") || !strings.Contains(body, "Borrow urns") {
		t.Error("the Open filter should hide done tasks")
	}
	if body := c.get("/events/1/tasks?print=1", 200); !strings.Contains(body, "checklist") || !strings.Contains(body, "Borrow urns") {
		t.Error("expected the printable checklist")
	}
	// Removing a list keeps its tasks.
	c.post("/events/1/tasks", "/task-lists/1/delete", url.Values{}, 200)
	if task, _ := st.GetTask(1); task == nil || task.ListID != 0 {
		t.Errorf("task after its list was removed: %+v", task)
	}

	// Needs: search the tracker and add from it, plus something to buy.
	if body := c.get("/events/1/needs?q=chairs", 200); !strings.Contains(body, "Folding chairs") {
		t.Fatal("expected tracker search results")
	}
	c.post("/events/1/needs", "/events/1/needs", url.Values{"tracker_kind": {"product"}, "tracker_id": {"9"}, "name": {"Folding chairs"}, "quantity": {"150"}}, 200)
	c.post("/events/1/needs", "/events/1/needs", url.Values{"tracker_kind": {"supply"}, "tracker_id": {"4"}, "name": {"AA batteries"}, "quantity": {"4"}}, 200)
	c.post("/events/1/needs", "/events/1/needs", url.Values{"name": {"Paper plates"}, "quantity": {"3"}}, 200)
	body = c.get("/events/1/needs", 200)
	if !strings.Contains(body, "Short by 50") || !strings.Contains(body, "6 batteries on hand") || !strings.Contains(body, "buy or borrow") {
		t.Fatalf("needs should show tracker availability:\n%s", body)
	}
	c.post("/events/1/needs", "/needs/3/status", url.Values{"status": {"gathered"}}, 200)
	if n, _ := st.GetNeed(3); n.Status != "gathered" || n.UpdatedBy != "Pat" {
		t.Errorf("need status: %+v", n)
	}
	if body := c.post("/events/1/needs", "/events/1/needs", url.Values{"name": {"X"}, "quantity": {"1"}, "room_id": {"5"}}, 422); !strings.Contains(body, "isn&#39;t part of this event") {
		t.Error("expected the room refusal")
	}

	// A room, so room selects have something to offer.
	st.DB.Exec(`INSERT INTO event_rooms (event_id, position, place_name, purpose) VALUES (1, 1, 'Main › Fellowship Hall', 'Dinner')`)
	c.post("/events/1/needs", "/events/1/needs", url.Values{"name": {"Tables"}, "quantity": {"8"}, "room_id": {"1"}}, 200)
	c.post("/events/1/schedule", "/events/1/schedule", url.Values{"label": {"Dinner"}, "date": {"2099-11-01"}, "room_id": {"1"}}, 200)
	if body := c.get("/events/1/needs", 200); !strings.Contains(body, "to Main › Fellowship Hall") {
		t.Error("a need should show where it goes")
	}

	// A second event, for pages with nothing in them.
	c.post("/events/new", "/events", url.Values{"title": {"Work Day"}, "status": {"planning"}, "starts_on": {"2099-12-01"}}, 200)

	// A repeating event (events 3, 4, ...), for the series pages.
	c.post("/events/new", "/events", url.Values{
		"title": {"Wednesday Supper"}, "status": {"planning"}, "starts_on": {day(soon(time.Wednesday))},
		"repeat_freq": {"weekly"}, "repeat_every": {"1"}, "repeat_end": {"never"},
	}, 200)

	// Every signed-in page renders.
	for _, p := range []string{
		"/", "/events", "/events/new", "/events/1", "/events/1/edit", "/events/1/schedule", "/schedule/1/edit",
		"/events/1/tasks", "/events/1/tasks?print=1", "/events/1/tasks?filter=mine", "/tasks/2/edit",
		"/events/1/needs", "/events/1/needs?q=chairs", "/needs/1/edit", "/events/1/rooms", "/events/1/rooms?print=1", "/events/1/helpers",
		"/events/1/people", "/events/1/people?print=1", "/events/1/copy", "/events/2/people", "/events/2/people?print=1", "/events/2/copy",
		"/events/2", "/events/2/schedule", "/events/2/tasks", "/events/2/tasks?print=1", "/events/2/needs", "/events/2/rooms", "/events/2/helpers",
		"/account", "/admin/users", "/admin/users/new", "/admin/users/1/edit", "/admin/settings", "/admin/connections", "/offline",
		"/calendar", "/calendar?month=2099-11", "/calendar?month=2099-12&room=Nowhere", "/calendar?month=bogus",
		"/series/1", "/events/3", "/events/4", "/events/4/edit", "/events/4/schedule", "/events/4/match-later",
		"/events/4/match-later?section=tasks&section=rooms", "/events/4/end-series", "/events/4/repeat",
		"/series/1/end?from=" + day(time.Now().AddDate(0, 0, 20)),
	} {
		c.get(p, 200)
	}
	c.get("/events/99", 404)
	c.get("/calendar/99", 404)
	c.get("/tasks/99/edit", 404)
	if body := c.get("/manifest.webmanifest", 200); !strings.Contains(body, "Grace Church Events") {
		t.Error("manifest should carry the site name")
	}

	// Deleting an event takes everything in it.
	c.post("/events/1/edit", "/events/1/delete", url.Values{}, 200)
	if n, _ := st.ListNeeds(1); len(n) != 0 {
		t.Error("needs should go with the event")
	}
	if n, _ := st.ListTasks(1); len(n) != 0 {
		t.Error("tasks should go with the event")
	}
}

func TestTrackerDown(t *testing.T) {
	c, st := newTestServer(t)
	c.post("/setup", "/setup", url.Values{
		"site_name": {"P"}, "username": {"coordinator"}, "password": {"a long password"}, "password_confirm": {"a long password"},
	}, 200)
	if body := c.post("/admin/connections", "/admin/connections/tracker", url.Values{
		"tracker_url": {"http://127.0.0.1:1"}, "tracker_token": {"mt_x"},
	}, 200); !strings.Contains(body, "Can&#39;t reach http://127.0.0.1:1") {
		t.Fatal("expected a warning that the tracker couldn't be reached")
	}
	c.post("/events/new", "/events", url.Values{"title": {"Dinner"}, "status": {"planning"}, "starts_on": {"2099-11-01"}}, 200)
	st.CreateNeed(&store.Need{EventID: 1, Name: "Chairs", Quantity: 5, TrackerKind: "product", TrackerID: 9})
	// Pages still work, saying the tracker couldn't be checked.
	if body := c.get("/events/1/needs", 200); !strings.Contains(body, "Couldn't check the maintenance tracker") {
		t.Error("needs page should say the tracker couldn't be reached")
	}
	c.get("/events/1/needs?q=chairs", 200)
	for _, p := range []string{"/", "/events", "/events/1", "/events/1/schedule", "/events/1/tasks", "/events/1/tasks?print=1", "/events/1/rooms", "/events/1/helpers", "/events/1/people", "/events/1/people?print=1", "/events/1/copy", "/calendar", "/calendar?month=2099-11"} {
		c.get(p, 200)
	}
	// And the public calendar, signed out, with the tracker down.
	anon := &client{t: t, base: c.base, http: &http.Client{}}
	for _, p := range []string{"/", "/calendar", "/calendar?month=2099-11"} {
		anon.get(p, 200)
	}
}

func TestSignInRequired(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("someone", "", "a long password", store.RoleAdmin)
	for _, p := range []string{"/events", "/events/1/tasks", "/events/1/rooms"} {
		if body := c.get(p, 200); !strings.Contains(body, "Sign in to plan") {
			t.Errorf("%s should ask to sign in", p)
		}
	}
}

func TestLiveEvents(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("coordinator", "", "a long password", store.RoleAdmin)

	// The stream is for signed-in people only, and says so with a plain
	// 401 (no redirect to /login for the EventSource to follow).
	c.get("/live", http.StatusUnauthorized)
	c.post("/login", "/login", url.Values{"username": {"coordinator"}, "password": {"a long password"}}, 200)

	resp, err := c.http.Get(c.base + "/live")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data:") {
				lines <- sc.Text()
			}
		}
	}()
	next := func(wait time.Duration) string {
		select {
		case l := <-lines:
			return l
		case <-time.After(wait):
			return ""
		}
	}
	if l := next(2 * time.Second); l != "data: ping" {
		t.Fatalf("first message %q, want a ping", l)
	}

	// Signing in changes nothing anyone else sees.
	c.post("/login", "/login", url.Values{"username": {"coordinator"}, "password": {"a long password"}}, 200)
	if l := next(200 * time.Millisecond); l != "" {
		t.Fatalf("signing in sent %q", l)
	}
	// A failed save doesn't either.
	c.post("/events/new", "/events", url.Values{"title": {""}, "status": {"planning"}, "starts_on": {"2099-11-01"}}, 422)
	if l := next(200 * time.Millisecond); l != "" {
		t.Fatalf("a rejected save sent %q", l)
	}
	c.post("/events/new", "/events", url.Values{"title": {"Easter Breakfast"}, "status": {"planning"}, "starts_on": {"2099-04-01"}}, 200)
	if l := next(2 * time.Second); l != "data: change" {
		t.Fatalf("after a save got %q, want a change", l)
	}

	for _, p := range []string{"/", "/events/1", "/events/1/schedule", "/events/1/tasks", "/events/1/needs", "/events/1/rooms", "/events/1/people"} {
		if !strings.Contains(c.get(p, 200), `data-controller="live"`) {
			t.Errorf("%s should update live", p)
		}
	}
	for _, p := range []string{"/events", "/events/1/edit", "/events/1/tasks?print=1", "/events/1/rooms?print=1", "/events/1/people?print=1", "/events/1/copy"} {
		if strings.Contains(c.get(p, 200), `data-controller="live"`) {
			t.Errorf("%s shouldn't update live", p)
		}
	}
	// A form sent back with errors would lose what was typed on a refresh.
	if body := c.post("/events/1/schedule", "/events/1/schedule", url.Values{"label": {""}}, 422); strings.Contains(body, `data-controller="live"`) {
		t.Error("a form with errors shouldn't update live")
	}
	c.get("/static/js/controllers/live_controller.js", 200)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// as signs in as someone else in a browser of their own.
func (c *client) as(username string) *client {
	jar, _ := cookiejar.New(nil)
	other := &client{t: c.t, base: c.base, http: &http.Client{Jar: jar}}
	other.post("/login", "/login", url.Values{"username": {username}, "password": {"a long password"}}, 200)
	return other
}

func TestLockedEvents(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	annID, _ := st.CreateUser("ann", "", "a long password", store.RolePlanner)
	bobID, _ := st.CreateUser("bob", "", "a long password", store.RolePlanner)
	ann, bob, boss := c.as("ann"), c.as("bob"), c.as("boss")

	form := func(title, access string, members ...string) url.Values {
		v := url.Values{"title": {title}, "status": {"planning"}, "starts_on": {"2099-01-01"}, "access": {access}}
		if len(members) > 0 {
			v["member"] = members
		}
		return v
	}

	// Only admins lock: Ann asking for it is refused.
	ann.post("/events/new", "/events", form("Open Concert", "locked"), 403)
	if evs, _ := st.ListEvents(&store.User{Role: store.RoleAdmin}, false, "2099-01-01"); len(evs) != 0 {
		t.Fatal("a refused lock shouldn't create an event")
	}
	ann.post("/events/new", "/events", form("Open Concert", "open"), 200)
	if e, _ := st.GetEvent(1); e.Locked {
		t.Fatal("a non-admin shouldn't be able to lock an event")
	}
	if strings.Contains(ann.get("/events/1/edit", 200), "Who can see it") {
		t.Error("non-admins shouldn't get the access controls")
	}

	boss.post("/events/new", "/events", form("Youth Retreat", "locked", itoa(annID), "999"), 200)
	ann.post("/events/2/schedule", "/events/2/schedule", url.Values{"label": {"Leave"}, "date": {"2099-01-01"}}, 200)
	ann.post("/events/2/tasks", "/events/2/tasks", url.Values{"title": {"Pack"}}, 200)
	if m, _ := st.Members(2); len(m) != 1 || m[0] != annID {
		t.Fatalf("members %v, want just ann", m)
	}

	// Bob doesn't see it anywhere, and can't reach it or anything in it.
	for _, p := range []string{"/", "/events"} {
		body := bob.get(p, 200)
		if strings.Contains(body, "Youth Retreat") || !strings.Contains(body, "Open Concert") {
			t.Errorf("%s: bob should see only the open event", p)
		}
	}
	for _, p := range []string{"/events/2", "/events/2/edit", "/events/2/tasks", "/events/2/rooms", "/events/2/helpers", "/schedule/1/edit", "/tasks/1/edit"} {
		bob.get(p, 404)
	}
	bob.post("/events", "/events/2/schedule", url.Values{"label": {"x"}, "date": {"2099-01-02"}}, 404)
	bob.post("/events", "/events/2/tasks", url.Values{"title": {"x"}}, 404)
	bob.post("/events", "/tasks/1/status", url.Values{"status": {"done"}}, 404)
	bob.post("/events", "/task-lists/1/move", url.Values{"dir": {"up"}}, 404)
	bob.post("/events", "/events/2/delete", url.Values{}, 404)

	// A member saving the details can't open it up or change who's on it.
	ann.post("/events/2/edit", "/events/2", form("Youth Retreat", "open", itoa(bobID)), 200)
	if e, _ := st.GetEvent(2); !e.Locked {
		t.Fatal("a non-admin shouldn't be able to unlock an event")
	}
	if m, _ := st.Members(2); len(m) != 1 || m[0] != annID {
		t.Fatalf("members %v, want just ann", m)
	}

	// Admins see everything, add Bob, then open it up, which keeps the list.
	if body := boss.get("/events/2", 200); !strings.Contains(body, "Locked") {
		t.Error("admin should see the locked event, marked locked")
	}
	boss.post("/events/2/edit", "/events/2", form("Youth Retreat", "locked", itoa(annID), itoa(bobID)), 200)
	bob.get("/events/2", 200)
	boss.post("/events/2/edit", "/events/2", form("Youth Retreat", "open"), 200)
	if m, _ := st.Members(2); len(m) != 2 {
		t.Errorf("members %v, want ann and bob kept", m)
	}
	if body := bob.get("/events", 200); !strings.Contains(body, "Youth Retreat") {
		t.Error("an open event is for everyone")
	}
}
