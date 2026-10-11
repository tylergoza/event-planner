package server

import (
	"io"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tylergoza/event-planner/internal/recur"
	"github.com/tylergoza/event-planner/internal/store"
)

// soon is the first day on or after tomorrow that falls on wd.
func soon(wd time.Weekday) time.Time {
	d := time.Now().AddDate(0, 0, 1)
	for d.Weekday() != wd {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

func day(t time.Time) string { return t.Format(store.DateLayout) }

// newEventForm is a minimal new-event form starting on the given day.
func newEventForm(title, start string, extra url.Values) url.Values {
	v := url.Values{"title": {title}, "status": {"planning"}, "starts_on": {start}, "visibility": {"public"}}
	for k, vals := range extra {
		v[k] = vals
	}
	return v
}

// editFormFor is the edit form for e as it is, so a test changes only what it sets.
func editFormFor(e *store.Event, extra url.Values) url.Values {
	v := url.Values{
		"title": {e.Title}, "kind": {e.Kind}, "status": {e.Status}, "starts_on": {e.StartsOn}, "ends_on": {e.EndsOn},
		"start_time": {e.StartTime}, "end_time": {e.EndTime}, "coordinator": {e.Coordinator},
		"description": {e.Description}, "notes": {e.Notes}, "visibility": {e.Visibility}, "public_description": {e.PublicDescription},
	}
	if e.ExpectedCount > 0 {
		v.Set("expected_count", strconv.Itoa(e.ExpectedCount))
	}
	for k, vals := range extra {
		v[k] = vals
	}
	return v
}

type seriesEnv struct {
	c, pat, ann *client
	st          *store.Store
	sid         int64
	occ         []store.Event
}

// newSeriesEnv has an admin, a planner (pat) and a helper (ann), and a
// weekly Wednesday series with a description on every date.
func newSeriesEnv(t *testing.T) *seriesEnv {
	t.Helper()
	c, st := newTestServer(t)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	st.CreateUser("pat", "Pat", "a long password", store.RolePlanner)
	st.CreateUser("ann", "Ann", "a long password", store.RoleHelper)
	id, err := st.CreateEvent(&store.Event{Title: "Supper", Kind: "Dinner", StartsOn: day(soon(time.Wednesday)), StartTime: "18:00",
		Description: "Soup", Visibility: "public", Coordinator: "Dana", ExpectedCount: 30})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := st.MakeSeries(id, recur.Rule{Freq: recur.Weekly, Every: 1}, day(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	env := &seriesEnv{c: c.as("boss"), pat: c.as("pat"), ann: c.as("ann"), st: st, sid: sid}
	env.reload(t)
	if len(env.occ) < 20 {
		t.Fatalf("only %d occurrences", len(env.occ))
	}
	return env
}

func (e *seriesEnv) reload(t *testing.T) {
	t.Helper()
	occ, err := e.st.Occurrences(e.sid)
	if err != nil {
		t.Fatal(err)
	}
	e.occ = occ
}

func (e *seriesEnv) get(t *testing.T, id int64) *store.Event {
	t.Helper()
	ev, err := e.st.GetEvent(id)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestCreateRepeatingEvents(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("pat", "Pat", "a long password", store.RolePlanner)
	pat := c.as("pat")

	// Weekly, on Mondays and Thursdays.
	start := soon(time.Monday)
	body := pat.post("/events/new", "/events", newEventForm("Choir", day(start), url.Values{
		"repeat_freq": {"weekly"}, "repeat_every": {"1"}, "repeat_weekday": {"1", "4"}, "repeat_end": {"never"},
	}), 200)
	if !strings.Contains(body, "Repeats: Every week on Monday and Thursday.") || !strings.Contains(body, "dates added through") {
		t.Fatalf("flash missing:\n%s", body)
	}
	ev := byTitle(t, st, "Choir")
	occ, _ := st.Occurrences(ev.SeriesID)
	if len(occ) < 40 {
		t.Fatalf("weekly: %d occurrences", len(occ))
	}
	for _, o := range occ {
		if wd := mustDay(t, o.StartsOn).Weekday(); wd != time.Monday && wd != time.Thursday {
			t.Errorf("weekly: %s is a %s", o.StartsOn, wd)
		}
	}

	// Monthly on the 2nd Sunday.
	start = soon(time.Sunday)
	pat.post("/events/new", "/events", newEventForm("Potluck", day(start), url.Values{
		"repeat_freq": {"monthly"}, "repeat_every": {"1"}, "repeat_month_mode": {"weekday"}, "repeat_month_week": {"2"},
		"repeat_month_weekday": {"0"}, "repeat_end": {"never"},
	}), 200)
	pot := byTitle(t, st, "Potluck")
	occ, _ = st.Occurrences(pot.SeriesID)
	if len(occ) < 5 {
		t.Fatalf("monthly weekday: %d occurrences", len(occ))
	}
	for _, o := range occ {
		d := mustDay(t, o.StartsOn)
		if d.Weekday() != time.Sunday || (d.Day()-1)/7 != 1 {
			t.Errorf("monthly weekday: %s", o.StartsOn)
		}
	}

	// Monthly on the 15th, ending after 3 times.
	pat.post("/events/new", "/events", newEventForm("Budget", day(soon(time.Tuesday)), url.Values{
		"repeat_freq": {"monthly"}, "repeat_every": {"1"}, "repeat_month_mode": {"day"}, "repeat_month_day": {"15"},
		"repeat_end": {"after"}, "repeat_count": {"3"},
	}), 200)
	b := byTitle(t, st, "Budget")
	occ, _ = st.Occurrences(b.SeriesID)
	if len(occ) != 3 { // a Tuesday start moves to the first 15th
		t.Fatalf("monthly by date, 3 times: %d occurrences", len(occ))
	}
	for _, o := range occ {
		if mustDay(t, o.StartsOn).Day() != 15 {
			t.Errorf("monthly by date: %s", o.StartsOn)
		}
	}

	// Daily, every 2 days, until a date.
	start = time.Now().AddDate(0, 0, 1)
	until := start.AddDate(0, 0, 10)
	pat.post("/events/new", "/events", newEventForm("Prayer", day(start), url.Values{
		"repeat_freq": {"daily"}, "repeat_every": {"2"}, "repeat_end": {"on"}, "repeat_until": {day(until)},
		"repeat_weekday": {"3"}, "repeat_month_day": {"40"}, // ignored for a daily rule
	}), 200)
	p := byTitle(t, st, "Prayer")
	occ, _ = st.Occurrences(p.SeriesID)
	if len(occ) != 6 {
		t.Fatalf("daily: %d occurrences, want 6", len(occ))
	}
	if got := mustDay(t, occ[1].StartsOn).Sub(mustDay(t, occ[0].StartsOn)); got != 48*time.Hour {
		t.Errorf("daily: gap %s", got)
	}

	// Never: a single event, and no rule fields needed.
	pat.post("/events/new", "/events", newEventForm("One-off", day(start), url.Values{"repeat_freq": {"none"}}), 200)
	one := byTitle(t, st, "One-off")
	if one.SeriesID != 0 {
		t.Error("a one-off became a series")
	}

	// Invalid rules show an error and create nothing.
	for name, form := range map[string]url.Values{
		"bad day":     {"repeat_freq": {"monthly"}, "repeat_every": {"1"}, "repeat_month_mode": {"day"}, "repeat_month_day": {"40"}},
		"zero every":  {"repeat_freq": {"weekly"}, "repeat_every": {"0"}},
		"until early": {"repeat_freq": {"daily"}, "repeat_every": {"1"}, "repeat_end": {"on"}, "repeat_until": {day(start.AddDate(0, 0, -3))}},
		"no until":    {"repeat_freq": {"daily"}, "repeat_every": {"1"}, "repeat_end": {"on"}},
		"bad count":   {"repeat_freq": {"daily"}, "repeat_every": {"1"}, "repeat_end": {"after"}, "repeat_count": {"0"}},
	} {
		body := pat.post("/events/new", "/events", newEventForm("Bad", day(start), form), 422)
		if !strings.Contains(body, "Please fix the following") {
			t.Errorf("%s: no error shown", name)
		}
	}
	var bad int
	st.DB.QueryRow(`SELECT COUNT(*) FROM events WHERE title = 'Bad'`).Scan(&bad)
	if bad != 0 {
		t.Errorf("%d events created from invalid rules", bad)
	}

	// An existing single event can be made to repeat from its edit form.
	body = pat.post("/events/"+itoa(one.ID)+"/edit", "/events/"+itoa(one.ID), editFormFor(one, url.Values{
		"repeat_freq": {"weekly"}, "repeat_every": {"2"}, "repeat_end": {"never"},
	}), 200)
	if !strings.Contains(body, "Repeats: Every 2 weeks on") {
		t.Errorf("edit flash:\n%s", body)
	}
	if o := mustEvent(t, st, one.ID); o.SeriesID == 0 {
		t.Error("event didn't become a series")
	}
	// ... and its edit form no longer offers the repeat fields.
	form := pat.get("/events/"+itoa(one.ID)+"/edit", 200)
	if strings.Contains(form, `name="repeat_freq"`) || !strings.Contains(form, `name="apply"`) || !strings.Contains(form, "/events/"+itoa(one.ID)+"/repeat") {
		t.Error("an occurrence's edit form should offer apply-to and the pattern link instead of repeat fields")
	}
}

func byTitle(t *testing.T, st *store.Store, title string) *store.Event {
	t.Helper()
	var id int64
	if err := st.DB.QueryRow(`SELECT id FROM events WHERE title = ? ORDER BY id LIMIT 1`, title).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return mustEvent(t, st, id)
}

func mustEvent(t *testing.T, st *store.Store, id int64) *store.Event {
	t.Helper()
	e, err := st.GetEvent(id)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func mustDay(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(store.DateLayout, s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSeriesActionsArePlannerOnly(t *testing.T) {
	env := newSeriesEnv(t)
	e := "/events/" + itoa(env.occ[2].ID)
	s := "/series/" + itoa(env.sid)
	ann := env.ann
	ann.get(e+"/match-later", 403)
	ann.post(e, e+"/match-later", url.Values{"section": {"tasks"}}, 403)
	ann.get(e+"/end-series", 403)
	ann.post(e, e+"/end-series", url.Values{}, 403)
	ann.get(e+"/repeat", 403)
	ann.post(e, e+"/repeat", url.Values{"repeat_freq": {"daily"}, "repeat_every": {"1"}}, 403)
	ann.post(e, e+"/delete", url.Values{}, 403)
	ann.get(e+"/edit", 403)
	ann.post(e, e, editFormFor(&env.occ[2], url.Values{"apply": {"later"}}), 403)
	ann.get(s+"/end?from="+env.occ[3].StartsOn, 403)
	ann.post(s, s+"/restore", url.Values{"date": {env.occ[3].StartsOn}}, 403)
	if now, _ := env.st.Occurrences(env.sid); len(now) != len(env.occ) {
		t.Error("a helper changed the series")
	}

	// Helpers can read the series page, but get no planner links or buttons.
	body := ann.get(s, 200)
	if strings.Contains(body, "End the series") || strings.Contains(body, "/restore") {
		t.Error("series page shows planner actions to a helper")
	}
	head := ann.get(e, 200)
	for _, no := range []string{"/match-later", "/end-series", e + "/repeat"} {
		if strings.Contains(head, no) {
			t.Errorf("occurrence page shows %s to a helper", no)
		}
	}
	if !strings.Contains(head, "Repeats: Every week on") || !strings.Contains(head, s) {
		t.Error("helper should still see the repeat line and the series link")
	}
}

func TestEditOccurrences(t *testing.T) {
	env := newSeriesEnv(t)
	pat := env.pat
	url1 := func(i int) string { return "/events/" + itoa(env.occ[i].ID) }

	// Just this date: one title change, and the date moves for this one only.
	moved := day(mustDay(t, env.occ[1].StartsOn).AddDate(0, 0, 1))
	pat.post(url1(1)+"/edit", url1(1), editFormFor(&env.occ[1], url.Values{
		"title": {"Supper (special)"}, "starts_on": {moved}, "apply": {"this"},
	}), 200)
	env.reload(t)
	if env.occ[1].Title != "Supper (special)" || env.occ[1].StartsOn != moved || env.occ[2].Title != "Supper" {
		t.Fatalf("just this: %+v / %+v", env.occ[1], env.occ[2])
	}
	if env.occ[1].OccurrenceDate == moved {
		t.Error("moving an occurrence should keep the date the rule gave it")
	}
	// Without the choice the default is just this date.
	pat.post(url1(0)+"/edit", url1(0), editFormFor(&env.occ[0], url.Values{"title": {"First!"}}), 200)
	env.reload(t)
	if env.occ[0].Title != "First!" || env.occ[1].Title != "Supper (special)" {
		t.Error("default should be just this date")
	}

	// This and later: only the changed fields go forward.
	st := env.st
	st.DB.Exec(`UPDATE events SET description = 'Different' WHERE id = ?`, env.occ[4].ID)
	env.reload(t)
	pat.post(url1(2)+"/edit", url1(2), editFormFor(&env.occ[2], url.Values{
		"title": {"Family Supper"}, "expected_count": {"55"}, "apply": {"later"},
	}), 200)
	env.reload(t)
	for i, o := range env.occ {
		switch {
		case i < 2:
			if o.Title == "Family Supper" || o.ExpectedCount == 55 {
				t.Errorf("date %d (earlier) changed: %+v", i, o)
			}
		default:
			if o.Title != "Family Supper" || o.ExpectedCount != 55 {
				t.Fatalf("date %d (this and later) not changed: %+v", i, o)
			}
			wantDesc := "Soup"
			if i == 4 {
				wantDesc = "Different" // not a changed field, so left alone
			}
			if o.Description != wantDesc || o.Coordinator != "Dana" {
				t.Errorf("date %d: unchanged fields were touched: %+v", i, o)
			}
		}
	}

	// Moving the date with "this and later" is refused and changes nothing.
	body := pat.post(url1(3)+"/edit", url1(3), editFormFor(&env.occ[3], url.Values{
		"title": {"Nope"}, "starts_on": {day(mustDay(t, env.occ[3].StartsOn).AddDate(0, 0, 1))}, "apply": {"later"},
	}), 422)
	if !strings.Contains(body, "To move every later date, change the repeat pattern from here instead.") {
		t.Errorf("expected the refusal:\n%s", body)
	}
	env.reload(t)
	if env.occ[3].Title != "Family Supper" {
		t.Error("a refused edit changed the event")
	}
	pat.post(url1(3)+"/edit", url1(3), editFormFor(&env.occ[3], url.Values{"apply": {"sideways"}}), 422)
}

func TestSkipAndRestore(t *testing.T) {
	env := newSeriesEnv(t)
	pat := env.pat
	victim := env.occ[3]
	e := "/events/" + itoa(victim.ID)
	page := pat.get(e+"/edit", 200)
	if !strings.Contains(page, "Skip this date") || !strings.Contains(page, "Remove only "+fmtDate(victim.OccurrenceDate)+"? Other dates in the series aren&#39;t affected.") {
		t.Errorf("skip button/confirm text missing:\n%s", page)
	}
	body := pat.post(e+"/edit", e+"/delete", url.Values{}, 200)
	if !strings.Contains(body, "Skipped") || !strings.Contains(body, "Restore") {
		t.Errorf("after skipping, the series page should list it:\n%s", body)
	}
	left, _ := env.st.Occurrences(env.sid)
	if len(left) != len(env.occ)-1 {
		t.Fatalf("%d left, want %d", len(left), len(env.occ)-1)
	}
	for _, o := range left {
		if o.ID == victim.ID {
			t.Error("skipped date still there")
		}
	}
	if sk, _ := env.st.SkippedDates(env.sid); len(sk) != 1 || sk[0] != victim.OccurrenceDate {
		t.Errorf("skipped: %v", sk)
	}
	// Topping up doesn't bring it back.
	env.st.ExtendSeries(day(time.Now()))
	if left, _ = env.st.Occurrences(env.sid); len(left) != len(env.occ)-1 {
		t.Error("top-up restored a skipped date")
	}

	s := "/series/" + itoa(env.sid)
	pat.post(s, s+"/restore", url.Values{"date": {victim.OccurrenceDate}}, 200)
	left, _ = env.st.Occurrences(env.sid)
	if len(left) != len(env.occ) {
		t.Errorf("restore: %d occurrences, want %d", len(left), len(env.occ))
	}
	if sk, _ := env.st.SkippedDates(env.sid); len(sk) != 0 {
		t.Errorf("still skipped: %v", sk)
	}
	// A date the rule doesn't give can't be restored.
	if body := pat.post(s, s+"/restore", url.Values{"date": {day(mustDay(t, victim.OccurrenceDate).AddDate(0, 0, 1))}}, 200); !strings.Contains(body, "Couldn") {
		t.Errorf("expected a refusal:\n%s", body)
	}
}

func TestMatchLaterPage(t *testing.T) {
	env := newSeriesEnv(t)
	pat, st := env.pat, env.st
	e := "/events/" + itoa(env.occ[0].ID) + "/match-later"
	// This date has a task; date 2 has finished work and a helper.
	st.CreateTask(&store.Task{EventID: env.occ[0].ID, Title: "Make soup", Status: "todo", UpdatedBy: "x"})
	st.CreateTask(&store.Task{EventID: env.occ[1].ID, Title: "Old task", Status: "done", UpdatedBy: "x"})
	st.CreateTask(&store.Task{EventID: env.occ[1].ID, Title: "Old task 2", Status: "done", UpdatedBy: "x"})
	st.CreateTask(&store.Task{EventID: env.occ[1].ID, Title: "Old task 3", Status: "done", UpdatedBy: "x"})
	roleID, _ := st.CreateHelperRole(&store.HelperRole{EventID: env.occ[1].ID, Name: "Cook", Slots: 3})
	st.SignUpName(roleID, "Riley", "")
	st.SignUpName(roleID, "Sam", "")
	st.AddHelperRoles(env.occ[0].ID, "Greeter x1")

	pat.get(e, 200)
	body := pat.get(e+"?section=tasks&section=helper_jobs", 200)
	want := fmtDate(env.occ[1].OccurrenceDate) + ": 3 tasks done, 2 helpers signed up — these would be replaced"
	if !strings.Contains(body, want) {
		t.Fatalf("expected %q in the preview:\n%s", want, body[strings.Index(body, "<main"):])
	}
	if strings.Contains(body, fmtDate(env.occ[2].OccurrenceDate)) {
		t.Error("dates with no work shouldn't be listed")
	}
	// Nothing chosen: refused.
	pat.post(e, e, url.Values{}, 422)
	// Apply.
	pat.post(e, e, url.Values{"section": {"tasks"}}, 200)
	tasks, _ := st.ListTasks(env.occ[1].ID)
	if len(tasks) != 1 || tasks[0].Title != "Make soup" || tasks[0].Status != "todo" {
		t.Errorf("later tasks after matching: %+v", tasks)
	}
	if roles, _ := st.ListHelperRoles(env.occ[1].ID); len(roles) != 1 || roles[0].Name != "Cook" {
		t.Errorf("helper jobs were not part of the choice: %+v", roles)
	}
	if tasks, _ := st.ListTasks(env.occ[0].ID); len(tasks) != 1 {
		t.Error("this date changed")
	}
}

func TestEndSeriesPage(t *testing.T) {
	env := newSeriesEnv(t)
	pat, st := env.pat, env.st
	from := env.occ[3]
	e := "/events/" + itoa(from.ID) + "/end-series"
	st.CreateTask(&store.Task{EventID: env.occ[5].ID, Title: "Done thing", Status: "done", UpdatedBy: "x"})

	body := pat.get(e, 200)
	if !strings.Contains(body, fmtDate(env.occ[5].OccurrenceDate)+"</li>") && !strings.Contains(body, ": 1 task done") || !strings.Contains(body, "will be lost") {
		t.Errorf("preview should warn about work:\n%s", body)
	}
	if strings.Contains(body, "<li>"+fmtDate(env.occ[2].OccurrenceDate)) {
		t.Error("an earlier date is listed for deletion")
	}
	// From the series page: pick a date, land on the same preview.
	s := "/series/" + itoa(env.sid)
	if body := pat.get(s+"/end?from="+from.OccurrenceDate, 200); !strings.Contains(body, "End the series from "+fmtDate(from.OccurrenceDate)) {
		t.Error("series page date picker should lead to the end-series page")
	}
	pat.get(s+"/end?from=garbage", 200) // back to the series page with a message

	body = pat.post(e, e, url.Values{}, 200)
	if !strings.Contains(body, "Series ended") {
		t.Errorf("expected the flash:\n%s", body)
	}
	left, _ := st.Occurrences(env.sid)
	if len(left) != 3 {
		t.Fatalf("%d dates left, want 3", len(left))
	}
	for i, o := range left {
		if o.ID != env.occ[i].ID {
			t.Errorf("date %d: %+v", i, o)
		}
	}
	if sr, _ := st.GetSeries(env.sid); sr == nil || sr.Rule.Until.IsZero() {
		t.Errorf("the series should now have an end: %+v", sr)
	}
}

func TestChangePatternPage(t *testing.T) {
	env := newSeriesEnv(t)
	pat, st := env.pat, env.st
	from := env.occ[2]
	e := "/events/" + itoa(from.ID) + "/repeat"
	body := pat.get(e, 200)
	if !strings.Contains(body, `value="weekly" checked`) || !strings.Contains(body, "dates will be replaced") {
		t.Errorf("form should be prefilled and show the preview:\n%s", body)
	}
	pat.post(e, e, url.Values{"repeat_freq": {"none"}}, 422)
	pat.post(e, e, url.Values{"repeat_freq": {"daily"}, "repeat_every": {"0"}}, 422)

	body = pat.post(e, e, url.Values{"repeat_freq": {"daily"}, "repeat_every": {"7"}, "repeat_end": {"after"}, "repeat_count": {"4"}}, 200)
	if !strings.Contains(body, "Repeats: Every 7 days, 4 times") {
		t.Errorf("expected the new pattern:\n%s", body)
	}
	old, _ := st.Occurrences(env.sid)
	if len(old) != 2 {
		t.Fatalf("old series has %d dates, want 2", len(old))
	}
	var newSID int64
	st.DB.QueryRow(`SELECT MAX(id) FROM series`).Scan(&newSID)
	if newSID == env.sid {
		t.Fatal("no new series")
	}
	fresh, _ := st.Occurrences(newSID)
	if len(fresh) != 4 || fresh[0].StartsOn != from.OccurrenceDate {
		t.Errorf("new series: %+v", fresh)
	}
}

func TestSeriesPageAndHeader(t *testing.T) {
	env := newSeriesEnv(t)
	pat, boss := env.pat, env.c
	s := "/series/" + itoa(env.sid)

	body := pat.get(s, 200)
	if !strings.Contains(body, "Every week on") || !strings.Contains(body, "Coming up") || !strings.Contains(body, "End the series") {
		t.Errorf("series page:\n%s", body)
	}
	// An occurrence's page says how it repeats and links around.
	e := env.occ[2]
	head := pat.get("/events/"+itoa(e.ID), 200)
	for _, want := range []string{"Repeats: Every week on", s, "3 of " + itoa(int64(len(env.occ))),
		"/events/" + itoa(env.occ[1].ID), "/events/" + itoa(env.occ[3].ID), "/match-later", "/end-series"} {
		if !strings.Contains(head, want) {
			t.Errorf("occurrence page lacks %q", want)
		}
	}
	// The events list and dashboard flag occurrences.
	if !strings.Contains(pat.get("/events", 200), "badge--repeat") || !strings.Contains(pat.get("/", 200), "badge--repeat") {
		t.Error("repeat badge missing on the events list or dashboard")
	}

	// Locked dates are hidden from people who aren't on them.
	env.st.DB.Exec(`UPDATE events SET locked = 1 WHERE id = ?`, env.occ[1].ID)
	hidden := fmtDate(env.occ[1].StartsOn)
	if body := pat.get(s, 200); strings.Contains(body, "/events/"+itoa(env.occ[1].ID)+`"`) {
		t.Errorf("series page lists a locked date (%s) to a non-member", hidden)
	}
	if body := boss.get(s, 200); !strings.Contains(body, "/events/"+itoa(env.occ[1].ID)+`"`) {
		t.Error("admin should see every date")
	}
	if head := pat.get("/events/"+itoa(env.occ[2].ID), 200); strings.Contains(head, "/events/"+itoa(env.occ[1].ID)+`"`) {
		t.Error("previous-date link leaks a locked date")
	}
	// All dates locked: the series isn't there for them.
	env.st.DB.Exec(`UPDATE events SET locked = 1`)
	pat.get(s, 404)
	pat.get(s+"/end?from="+env.occ[0].StartsOn, 404)
	pat.post("/events", s+"/restore", url.Values{"date": {env.occ[0].StartsOn}}, 404)
	boss.get(s, 200)
	pat.get("/series/999", 404)
}

func TestTopUpSeries(t *testing.T) {
	env := newSeriesEnv(t)
	st := env.st
	srv, err := New(Config{}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := srv.TopUpSeries(); err != nil || n != 0 {
		t.Fatalf("nothing is due: %d %v", n, err)
	}
	// Pretend the last two weeks were never created.
	last := env.occ[len(env.occ)-2:]
	st.DB.Exec(`DELETE FROM events WHERE id IN (?, ?)`, last[0].ID, last[1].ID)
	st.DB.Exec(`UPDATE series SET generated_through = ? WHERE id = ?`, env.occ[len(env.occ)-3].OccurrenceDate, env.sid)
	if n, err := srv.TopUpSeries(); err != nil || n != 2 {
		t.Fatalf("top-up created %d (%v), want 2", n, err)
	}
}

// A planner can't change or remove later dates when one of them is locked
// away from them; the admin it's locked for can.
func TestSeriesLockedLaterDates(t *testing.T) {
	env := newSeriesEnv(t)
	first, locked := env.occ[0], env.occ[3]
	if _, err := env.st.DB.Exec(`UPDATE events SET locked = 1 WHERE id = ?`, locked.ID); err != nil {
		t.Fatal(err)
	}
	from := "/events/" + itoa(first.ID)
	for _, p := range []string{"/end-series", "/match-later", "/repeat"} {
		env.pat.get(from+p, 403)
	}
	env.pat.post(from, from+"/end-series", url.Values{}, 403)
	env.pat.post(from, from+"/match-later", url.Values{"section": {"rooms"}}, 403)
	env.pat.post(from+"/edit", from, editFormFor(&first, url.Values{"apply": {"later"}, "coordinator": {"Sam"}}), 403)
	if got := env.get(t, first.ID).Coordinator; got != "Dana" {
		t.Errorf("refused 'this and later' still changed this date (coordinator %q)", got)
	}
	// Just this date is fine, and a date after the locked one is too.
	env.pat.post(from+"/edit", "/events", editFormFor(&first, url.Values{"apply": {"this"}, "coordinator": {"Sam"}}), 200)
	after := "/events/" + itoa(env.occ[4].ID)
	env.pat.get(after+"/end-series", 200)
	// The admin sees every date.
	env.c.get(from+"/end-series", 200)
}
