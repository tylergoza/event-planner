package store

import (
	"testing"
	"time"

	"github.com/tylergoza/event-planner/internal/recur"
)

const testToday = "2026-10-11" // a Sunday; the horizon is 2027-04-11

func weekly(every int) recur.Rule {
	return recur.Rule{Freq: recur.Weekly, Every: every}
}

// seriesFixture makes a Sunday dinner on 2026-11-01 with a room, a task in
// it, a schedule item, a need and a helper job, and a weekly series from it.
func seriesFixture(t *testing.T, st *Store) (tpl, sid int64, ann int64) {
	t.Helper()
	ann, _ = st.CreateUser("ann", "Ann", "a long password", RoleHelper)
	tpl, err := st.CreateEvent(&Event{Title: "Supper", Kind: "Fellowship dinner", StartsOn: "2026-11-01", StartTime: "17:00", EndTime: "19:00",
		Coordinator: "Pat", ExpectedCount: 30})
	if err != nil {
		t.Fatal(err)
	}
	hall, _ := st.AddRoom(tpl, 7, "Main › Hall")
	st.CreateTask(&Task{EventID: tpl, Title: "Set tables", Status: "done", AssigneeID: ann, RoomID: hall})
	st.CreateScheduleItem(&ScheduleItem{EventID: tpl, Date: "2026-11-01", Time: "16:00", Label: "Set up", RoomID: hall})
	st.CreateNeed(&Need{EventID: tpl, Name: "Tables", Quantity: 4, Status: "gathered", RoomID: hall})
	role, _ := st.CreateHelperRole(&HelperRole{EventID: tpl, Name: "Greeters", Slots: 2, RoomID: hall})
	st.SignUpUser(role, ann)
	r := weekly(1)
	sid, err = st.MakeSeries(tpl, r, testToday)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func dates(occ []Event) []string {
	var out []string
	for _, e := range occ {
		out = append(out, e.OccurrenceDate)
	}
	return out
}

func mustOcc(t *testing.T, st *Store, sid int64) []Event {
	t.Helper()
	occ, err := st.Occurrences(sid)
	if err != nil {
		t.Fatal(err)
	}
	return occ
}

func TestMakeSeriesGeneratesCopies(t *testing.T) {
	st := openTest(t)
	tpl, sid, ann := seriesFixture(t, st)
	occ := mustOcc(t, st, sid)
	if len(occ) != 24 || occ[0].ID != tpl || occ[0].OccurrenceDate != "2026-11-01" || occ[23].OccurrenceDate != "2027-04-11" {
		t.Fatalf("occurrences %v", dates(occ))
	}
	sr, err := st.GetSeries(sid)
	if err != nil || sr.TemplateEventID != tpl || sr.GeneratedThrough != "2027-04-11" || sr.Rule.Freq != recur.Weekly || sr.Rule.Every != 1 {
		t.Fatalf("series %+v %v", sr, err)
	}
	if got := sr.Rule.Describe(); got != "Every week on Sunday" {
		t.Errorf("describe %q", got)
	}
	e := occ[3]
	if e.StartsOn != "2026-11-22" || e.Status != "planning" || e.Title != "Supper" || e.Coordinator != "Pat" || e.StartTime != "17:00" || e.SeriesID != sid || e.CopiedFrom != 0 {
		t.Fatalf("occurrence %+v", e)
	}
	rooms, _ := st.ListRooms(e.ID)
	tasks, _ := st.ListTasks(e.ID)
	if len(rooms) != 1 || rooms[0].PlaceID != 7 || len(tasks) != 1 {
		t.Fatalf("rooms %+v tasks %+v", rooms, tasks)
	}
	oldRooms, _ := st.ListRooms(tpl)
	if rooms[0].ID == oldRooms[0].ID || tasks[0].RoomID != rooms[0].ID || tasks[0].Status != "todo" || tasks[0].AssigneeID != ann {
		t.Errorf("task %+v rooms %+v", tasks[0], rooms)
	}
	if sch, _ := st.ListSchedule(e.ID); len(sch) != 1 || sch[0].Date != "2026-11-22" || sch[0].RoomID != rooms[0].ID {
		t.Errorf("schedule %+v", sch)
	}
	if needs, _ := st.ListNeeds(e.ID); len(needs) != 1 || needs[0].Status != "needed" {
		t.Errorf("needs %+v", needs)
	}
	if roles, _ := st.ListHelperRoles(e.ID); len(roles) != 1 || len(roles[0].Helpers) != 0 || roles[0].RoomID != rooms[0].ID {
		t.Errorf("roles %+v", roles)
	}
	// The template keeps its own work.
	if tt, _ := st.ListTasks(tpl); tt[0].Status != "done" {
		t.Errorf("template task %+v", tt[0])
	}
}

func TestExtendSeries(t *testing.T) {
	st := openTest(t)
	_, sid, _ := seriesFixture(t, st)
	if n, err := st.ExtendSeries(testToday); err != nil || n != 0 {
		t.Fatalf("again: %d %v", n, err)
	}
	// Two weeks later the horizon moves two weeks.
	n, err := st.ExtendSeries("2026-10-25")
	if err != nil || n != 2 {
		t.Fatalf("top up: %d %v", n, err)
	}
	occ := mustOcc(t, st, sid)
	if len(occ) != 26 || occ[25].OccurrenceDate != "2027-04-25" {
		t.Fatalf("occurrences %v", dates(occ))
	}
	if n, _ := st.ExtendOneSeries(sid, "2026-10-25"); n != 0 {
		t.Errorf("idempotent: %d", n)
	}
}

func TestSkipAndRestore(t *testing.T) {
	st := openTest(t)
	tpl, sid, _ := seriesFixture(t, st)
	occ := mustOcc(t, st, sid)
	third := occ[2]
	t3, _ := st.ListTasks(third.ID)
	st.SetTaskStatus(t3[0].ID, "done", "x")
	if err := st.SkipOccurrence(occ[1].ID); err != nil {
		t.Fatal(err)
	}
	after := mustOcc(t, st, sid)
	if len(after) != 23 || after[1].ID != third.ID {
		t.Fatalf("after skip %v", dates(after))
	}
	if t3b, _ := st.ListTasks(third.ID); t3b[0].Status != "done" {
		t.Error("other occurrence changed")
	}
	if sk, _ := st.SkippedDates(sid); len(sk) != 1 || sk[0] != "2026-11-08" {
		t.Errorf("skips %v", sk)
	}
	st.ExtendSeries("2026-10-25")
	for _, e := range mustOcc(t, st, sid) {
		if e.OccurrenceDate == "2026-11-08" {
			t.Fatal("skipped date came back")
		}
	}
	// Skipping the template moves it to the next occurrence.
	if err := st.SkipOccurrence(tpl); err != nil {
		t.Fatal(err)
	}
	sr, _ := st.GetSeries(sid)
	if sr.TemplateEventID != third.ID {
		t.Fatalf("template %d want %d", sr.TemplateEventID, third.ID)
	}
	// Restore the second date: created from the template.
	if err := st.RestoreSkip(sid, "2026-11-08", "2026-10-25"); err != nil {
		t.Fatal(err)
	}
	occ = mustOcc(t, st, sid)
	if occ[0].OccurrenceDate != "2026-11-08" || occ[0].StartsOn != "2026-11-08" {
		t.Fatalf("restored %v", dates(occ))
	}
	if sk, _ := st.SkippedDates(sid); len(sk) != 1 || sk[0] != "2026-11-01" {
		t.Errorf("skips %v", sk)
	}
	if err := st.RestoreSkip(sid, "2026-11-09", testToday); err == nil {
		t.Error("a date off the pattern should fail")
	}
	// A non-series event is just deleted.
	one := newEvent(t, st, "One")
	if err := st.SkipOccurrence(one); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetEvent(one); err == nil {
		t.Error("should be deleted")
	}
}

func TestEndSeriesFrom(t *testing.T) {
	st := openTest(t)
	_, sid, ann := seriesFixture(t, st)
	occ := mustOcc(t, st, sid)
	t5, _ := st.ListTasks(occ[5].ID)
	st.SetTaskStatus(t5[0].ID, "done", "x")
	role, _ := st.ListHelperRoles(occ[5].ID)
	st.SignUpUser(role[0].ID, ann)

	prev, err := st.EndSeriesPreview(sid, "2026-12-06") // occ[5] and later
	if err != nil || len(prev) != 19 || prev[0].EventID != occ[5].ID || prev[0].TasksDone != 1 || prev[0].Helpers != 1 || !prev[0].HasWork() || prev[1].HasWork() {
		t.Fatalf("preview %d %+v %v", len(prev), prev[0], err)
	}
	n, err := st.EndSeriesFrom(sid, "2026-12-06")
	if err != nil || n != 19 {
		t.Fatalf("end: %d %v", n, err)
	}
	left := mustOcc(t, st, sid)
	if len(left) != 5 || left[4].OccurrenceDate != "2026-11-29" {
		t.Fatalf("left %v", dates(left))
	}
	sr, _ := st.GetSeries(sid)
	if sr.Rule.Until.Format(DateLayout) != "2026-12-05" {
		t.Errorf("until %v", sr.Rule.Until)
	}
	if n, _ := st.ExtendSeries("2027-06-01"); n != 0 {
		t.Errorf("generation should have stopped: %d", n)
	}
	// Ending from the very first date removes the series.
	if n, err := st.EndSeriesFrom(sid, "2026-11-01"); err != nil || n != 5 {
		t.Fatalf("end all: %d %v", n, err)
	}
	if _, err := st.GetSeries(sid); err == nil {
		t.Error("empty series should be gone")
	}
}

func str(s string) *string { return &s }

func TestApplyDetailsForward(t *testing.T) {
	st := openTest(t)
	_, sid, _ := seriesFixture(t, st)
	occ := mustOcc(t, st, sid)
	// A later occurrence has its own coordinator and was moved a day.
	later := occ[6]
	le, _ := st.GetEvent(later.ID)
	le.Coordinator, le.StartsOn = "Sam", "2026-12-14"
	st.UpdateEvent(le)

	n, err := st.ApplyDetailsForward(occ[4].ID, DetailsChange{StartTime: str("18:00"), Title: str("Big supper")}, "x")
	if err != nil || n != 20 {
		t.Fatalf("applied %d %v", n, err)
	}
	e3, _ := st.GetEvent(occ[3].ID)
	if e3.StartTime != "17:00" || e3.Title != "Supper" {
		t.Errorf("earlier changed %+v", e3)
	}
	e4, _ := st.GetEvent(occ[4].ID)
	if e4.StartTime != "18:00" || e4.Title != "Big supper" || e4.Coordinator != "Pat" || e4.StartsOn != "2026-11-29" {
		t.Errorf("this %+v", e4)
	}
	l, _ := st.GetEvent(later.ID)
	if l.StartTime != "18:00" || l.Coordinator != "Sam" || l.StartsOn != "2026-12-14" || l.OccurrenceDate != "2026-12-13" || l.Status != "planning" {
		t.Errorf("later %+v", l)
	}
	sr, _ := st.GetSeries(sid)
	if sr.TemplateEventID != occ[4].ID {
		t.Errorf("template %d", sr.TemplateEventID)
	}
	// New occurrences inherit the new details.
	st.ExtendSeries("2026-10-25")
	last := mustOcc(t, st, sid)
	if g := last[len(last)-1]; g.Title != "Big supper" || g.StartTime != "18:00" {
		t.Errorf("generated %+v", g)
	}
	// The moved occurrence is not duplicated by generation.
	count := 0
	for _, e := range last {
		if e.OccurrenceDate == "2026-12-13" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("moved occurrence appears %d times", count)
	}
	if _, err := st.ApplyDetailsForward(occ[4].ID, DetailsChange{Title: str(" ")}, "x"); err == nil {
		t.Error("blank title should fail")
	}
	if _, err := st.ApplyDetailsForward(newEvent(t, st, "One"), DetailsChange{Notes: str("n")}, "x"); err != ErrNotInSeries {
		t.Errorf("err %v", err)
	}
}

func TestMatchLater(t *testing.T) {
	st := openTest(t)
	tpl, sid, ann := seriesFixture(t, st)
	occ := mustOcc(t, st, sid)
	mid := occ[2]

	// Make the middle occurrence different: a new room and task.
	kitchen, _ := st.AddRoom(mid.ID, 9, "Main › Kitchen")
	st.CreateTask(&Task{EventID: mid.ID, Title: "Cook", AssigneeName: "Team", RoomID: kitchen, DueOn: "2026-11-14"})
	// A later occurrence has work that will be lost, and a room of its own.
	last := occ[4]
	lt, _ := st.ListTasks(last.ID)
	st.SetTaskStatus(lt[0].ID, "in_progress", "x")
	yard, _ := st.AddRoom(last.ID, 0, "Yard")
	st.CreateNeed(&Need{EventID: last.ID, Name: "Rake", RoomID: yard, Status: "gathered"})

	prev, err := st.MatchLaterPreview(mid.ID, []string{SectionRooms, SectionTasks})
	if err != nil || len(prev) != 21 || prev[1].EventID != last.ID || prev[1].TasksInProgress != 1 || prev[1].NeedsGathered != 0 {
		t.Fatalf("preview %+v %v", prev[:2], err)
	}
	n, err := st.MatchLater(mid.ID, []string{SectionRooms, SectionTasks}, "Pat")
	if err != nil || n != 21 {
		t.Fatalf("match %d %v", n, err)
	}
	// Earlier occurrences and the source are untouched.
	if r, _ := st.ListRooms(occ[1].ID); len(r) != 1 {
		t.Errorf("earlier rooms %+v", r)
	}
	if tk, _ := st.ListTasks(tpl); len(tk) != 1 || tk[0].Status != "done" {
		t.Errorf("template tasks %+v", tk)
	}
	if tk, _ := st.ListTasks(mid.ID); len(tk) != 2 {
		t.Errorf("source tasks %+v", tk)
	}
	rooms, _ := st.ListRooms(last.ID)
	tasks, _ := st.ListTasks(last.ID)
	if len(rooms) != 2 || rooms[0].PlaceID != 7 || rooms[1].PlaceID != 9 {
		t.Fatalf("rooms %+v", rooms)
	}
	if len(tasks) != 2 || tasks[0].Status != "todo" || tasks[0].RoomID != rooms[0].ID || tasks[0].AssigneeID != ann ||
		tasks[1].Title != "Cook" || tasks[1].RoomID != rooms[1].ID || tasks[1].DueOn != "2026-11-28" {
		t.Errorf("tasks %+v", tasks)
	}
	// Needs weren't replaced: the hall need follows its room to the new
	// one, the Yard one (no match) lost its room.
	needs, _ := st.ListNeeds(last.ID)
	if len(needs) != 2 {
		t.Fatalf("needs %+v", needs)
	}
	for _, nd := range needs {
		if (nd.Name == "Tables" && nd.RoomID != rooms[0].ID) || (nd.Name == "Rake" && nd.RoomID != 0) {
			t.Errorf("need %+v", nd)
		}
	}
	if sr, _ := st.GetSeries(sid); sr.TemplateEventID != mid.ID {
		t.Errorf("template %d", sr.TemplateEventID)
	}

	// Only schedule: rooms are matched by place, not duplicated.
	if _, err := st.MatchLater(tpl, []string{SectionSchedule}, "Pat"); err != nil {
		t.Fatal(err)
	}
	rooms, _ = st.ListRooms(last.ID)
	if len(rooms) != 2 {
		t.Errorf("rooms %+v", rooms)
	}
	if sch, _ := st.ListSchedule(last.ID); len(sch) != 1 || sch[0].RoomID != rooms[0].ID {
		t.Errorf("schedule %+v", sch)
	}
	// Only helper jobs into an event with no matching room: room is created.
	bare := occ[10]
	st.MatchLater(mid.ID, []string{SectionRooms}, "Pat")
	if _, err := st.MatchLater(mid.ID, []string{SectionHelperJobs, SectionNeeds}, "Pat"); err != nil {
		t.Fatal(err)
	}
	if roles, _ := st.ListHelperRoles(bare.ID); len(roles) != 1 || roles[0].RoomID == 0 || len(roles[0].Helpers) != 0 {
		t.Errorf("roles %+v", roles)
	}
	if _, err := st.MatchLater(mid.ID, []string{"bogus"}, "x"); err == nil {
		t.Error("bad section should fail")
	}
}

func TestChangePatternFrom(t *testing.T) {
	st := openTest(t)
	_, sid, _ := seriesFixture(t, st)
	occ := mustOcc(t, st, sid)
	start, _ := recur.ParseDate("2026-12-06") // occ[5]
	nid, err := st.ChangePatternFrom(occ[5].ID, recur.Rule{Freq: recur.Weekly, Every: 2, Start: start}, testToday)
	if err != nil {
		t.Fatal(err)
	}
	old := mustOcc(t, st, sid)
	if len(old) != 5 || old[4].OccurrenceDate != "2026-11-29" {
		t.Fatalf("old %v", dates(old))
	}
	nw := mustOcc(t, st, nid)
	if len(nw) != 10 || nw[0].OccurrenceDate != "2026-12-06" || nw[1].OccurrenceDate != "2026-12-20" || nw[0].Title != "Supper" {
		t.Fatalf("new %v", dates(nw))
	}
	if r, _ := st.ListRooms(nw[1].ID); len(r) != 1 {
		t.Errorf("rooms %+v", r)
	}
	sr, _ := st.GetSeries(nid)
	if sr.TemplateEventID != nw[0].ID || sr.Rule.Every != 2 {
		t.Errorf("series %+v", sr)
	}
}

func TestSeriesLimits(t *testing.T) {
	st := openTest(t)
	a := newEvent(t, st, "Counted") // 2026-11-01
	sid, err := st.MakeSeries(a, recur.Rule{Freq: recur.Weekly, Every: 1, Count: 3}, testToday)
	if err != nil {
		t.Fatal(err)
	}
	if got := dates(mustOcc(t, st, sid)); len(got) != 3 || got[2] != "2026-11-15" {
		t.Errorf("count: %v", got)
	}
	b := newEvent(t, st, "Until")
	until, _ := recur.ParseDate("2026-12-01")
	sid2, err := st.MakeSeries(b, recur.Rule{Freq: recur.Weekly, Every: 1, Until: until}, testToday)
	if err != nil {
		t.Fatal(err)
	}
	if got := dates(mustOcc(t, st, sid2)); len(got) != 5 || got[4] != "2026-11-29" {
		t.Errorf("until: %v", got)
	}
	// Monthly by weekday round-trips through the table.
	c := newEvent(t, st, "Monthly")
	sid3, err := st.MakeSeries(c, recur.Rule{Freq: recur.Monthly, Every: 1, MonthWeek: -1, MonthWeekday: time.Sunday}, testToday)
	if err != nil {
		t.Fatal(err)
	}
	sr, _ := st.GetSeries(sid3)
	if sr.Rule.MonthWeek != -1 || sr.Rule.MonthWeekday != time.Sunday || sr.Rule.MonthDay != 0 {
		t.Errorf("rule %+v", sr.Rule)
	}
	// An event that doesn't match the rule is still the first occurrence.
	d := newEvent(t, st, "Odd") // Sunday; rule says Wednesday
	sid4, err := st.MakeSeries(d, recur.Rule{Freq: recur.Weekly, Every: 1, Weekdays: []time.Weekday{time.Wednesday, time.Friday}}, testToday)
	if err != nil {
		t.Fatal(err)
	}
	got := dates(mustOcc(t, st, sid4))
	if got[0] != "2026-11-01" || got[1] != "2026-11-04" {
		t.Errorf("odd: %v", got[:3])
	}
	sr4, _ := st.GetSeries(sid4)
	if len(sr4.Rule.Weekdays) != 2 {
		t.Errorf("weekdays %v", sr4.Rule.Weekdays)
	}
	if _, err := st.MakeSeries(d, weekly(1), testToday); err != ErrAlreadyInSeries {
		t.Errorf("err %v", err)
	}
}

func TestCopyOfOccurrenceIsOneOff(t *testing.T) {
	st := openTest(t)
	_, sid, _ := seriesFixture(t, st)
	occ := mustOcc(t, st, sid)
	id, err := st.CopyEvent(occ[2].ID, CopyOptions{StartsOn: "2027-06-01"})
	if err != nil {
		t.Fatal(err)
	}
	e, _ := st.GetEvent(id)
	if e.SeriesID != 0 || e.OccurrenceDate != "" || e.CopiedFrom != occ[2].ID {
		t.Errorf("copy %+v", e)
	}
	if len(mustOcc(t, st, sid)) != 24 {
		t.Error("series changed")
	}
}

func TestDeletedTemplateFallsBack(t *testing.T) {
	st := openTest(t)
	tpl, sid, _ := seriesFixture(t, st)
	occ := mustOcc(t, st, sid)
	// Customise the latest occurrence so we can tell it is the one copied.
	last := occ[23]
	st.ApplyDetailsForward(last.ID, DetailsChange{Notes: str("latest")}, "x") // makes it the template...
	sr, _ := st.GetSeries(sid)
	if sr.TemplateEventID != last.ID {
		t.Fatal("template should be latest")
	}
	if err := st.DeleteEvent(last.ID); err != nil {
		t.Fatal(err)
	}
	sr, _ = st.GetSeries(sid)
	if sr.TemplateEventID != 0 {
		t.Fatalf("template %d", sr.TemplateEventID)
	}
	if err := st.DeleteEvent(tpl); err != nil {
		t.Fatal(err)
	}
	n, err := st.ExtendSeries("2026-11-15")
	if err != nil || n != 4 {
		t.Fatalf("extend %d %v", n, err)
	}
	g := mustOcc(t, st, sid)
	// The deleted latest date isn't recreated; the fallback copy has no notes.
	if e := g[len(g)-1]; e.OccurrenceDate != "2027-05-09" || e.Notes != "" {
		t.Errorf("last %+v", e)
	}
	// No occurrences left: generation stops quietly.
	for _, e := range g {
		st.DB.Exec(`DELETE FROM events WHERE id = ?`, e.ID)
	}
	if n, err := st.ExtendSeries("2027-01-01"); err != nil || n != 0 {
		t.Errorf("empty series: %d %v", n, err)
	}
}
